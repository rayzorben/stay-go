// Package executor provides a thin, testable wrapper around os/exec with
// support for capturing stdout/stderr, optional real-time streaming (--debug),
// sudo escalation, and context cancellation.
package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// Executor is a stateless command runner. Debug controls whether command output
// is streamed to the terminal in addition to being captured.
type Executor struct {
	Debug bool
	// Verbose, when true alongside Debug, streams all command output without
	// truncation. Without Verbose, Debug mode shows only the first and last line
	// of outputs longer than 5 lines.
	Verbose bool
	// ProgressFn, when set, is called with each non-empty output line as the
	// command runs. Used by the engine to show a live last-line indicator in the
	// progress row. Not called in Debug or Stream mode (output is already visible).
	// Safe to set/clear from the engine's sequential execute loop.
	ProgressFn func(string)
}

// Options configures a single command invocation.
type Options struct {
	// Sudo prepends "sudo" to the command, requesting privilege escalation.
	Sudo bool
	// Env is a list of "KEY=VALUE" pairs appended to the process environment.
	Env []string
	// Stdin is an optional reader attached to the process's standard input.
	// Leave nil to use /dev/null (fully non-interactive).
	Stdin io.Reader
	// Stream pipes stdout/stderr to the terminal in addition to capturing,
	// even when the executor is not in Debug mode. Use for long-running
	// commands where the user needs to see live progress (e.g. in-box apply).
	Stream bool
	// AllowInteractive streams output in real time and detects when no newline
	// has been written for 15 seconds. The user is then prompted to cancel or
	// enter interactive mode (connecting os.Stdin to the process). Falls back
	// to normal non-streaming behaviour when stdin is not a terminal.
	AllowInteractive bool
}

// Result holds the captured output of a completed command.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Run executes name with args under the given options. If Debug is true, output
// is both streamed to the terminal and captured in Result. Returns a non-nil
// error if the process fails to start or exits with a non-zero code.
func (e *Executor) Run(ctx context.Context, opts Options, name string, args ...string) (*Result, error) {
	if opts.Sudo {
		args = append([]string{name}, args...)
		name = "sudo"
	}

	cmd := exec.CommandContext(ctx, name, args...)

	// Inherit the base environment and append caller-supplied vars.
	if len(opts.Env) > 0 {
		cmd.Env = append(os.Environ(), opts.Env...)
	}

	if e.Debug {
		fmt.Fprintf(os.Stderr, "  $ %s\n", strings.Join(cmd.Args, " "))
	}

	// Interactive mode: streams output via PTY so the process sees a real
	// terminal. Not used when sudo is involved — the PTY creates a new TTY
	// device which breaks sudo's per-tty credential cache, causing a second
	// password prompt even after preSudo has already authenticated.
	if opts.AllowInteractive && isTerminalStdin() && !opts.Sudo {
		return e.runInteractive(ctx, cmd)
	}

	// Stdin: sudo commands get os.Stdin so that the process runs on the same
	// controlling terminal that preSudo used, preventing a second prompt.
	// All other commands use /dev/null to stay non-interactive.
	if opts.Sudo {
		cmd.Stdin = os.Stdin
	} else if opts.Stdin != nil {
		cmd.Stdin = opts.Stdin
	} else {
		devNull, err := os.Open(os.DevNull)
		if err == nil {
			cmd.Stdin = devNull
			defer devNull.Close()
		}
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	if (e.Debug && e.Verbose) || opts.Stream {
		cmd.Stdout = io.MultiWriter(os.Stdout, &stdoutBuf)
		cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)
	} else if e.Debug {
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = &stderrBuf
	} else if e.ProgressFn != nil {
		llw := &lastLineWriter{fn: e.ProgressFn}
		cmd.Stdout = io.MultiWriter(llw, &stdoutBuf)
		cmd.Stderr = &stderrBuf
	} else {
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = &stderrBuf
	}

	err := cmd.Run()
	result := &Result{
		Stdout: stdoutBuf.String(),
		Stderr: stderrBuf.String(),
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exitErr.ExitCode()
	}

	// In Debug (non-Verbose) mode, print captured output now, truncated if long.
	if e.Debug && !e.Verbose {
		if stdoutBuf.Len() > 0 {
			fmt.Fprint(os.Stdout, truncateDebugOutput(result.Stdout))
		}
		if stderrBuf.Len() > 0 {
			fmt.Fprint(os.Stderr, truncateDebugOutput(result.Stderr))
		}
	}

	return result, err
}

// truncateDebugOutput truncates command output longer than 5 lines to just the
// first and last line, separated by "...". Used in --debug mode to reduce noise.
func truncateDebugOutput(s string) string {
	s = strings.TrimRight(s, "\n")
	lines := strings.Split(s, "\n")
	if len(lines) <= 5 {
		return s + "\n"
	}
	return lines[0] + "\n...\n" + lines[len(lines)-1] + "\n"
}

// isTerminalStdin reports whether os.Stdin is connected to a terminal.
func isTerminalStdin() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// runInteractive executes cmd under a PTY so the process sees a real terminal
// (preserving ANSI colours, box-drawing, and correct isatty detection). The
// terminal is put in raw mode and stdin is forwarded directly to the PTY so
// the user can interact with the process in real time. Ctrl+C is forwarded to
// the process's terminal line discipline, which sends SIGINT to the child.
func (e *Executor) runInteractive(ctx context.Context, cmd *exec.Cmd) (*Result, error) {
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("pty start: %w", err)
	}
	defer ptmx.Close()

	// Match PTY dimensions to our terminal so TUI layouts render correctly.
	if cols, rows, sizeErr := term.GetSize(int(os.Stdout.Fd())); sizeErr == nil {
		pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}) //nolint:errcheck
	}

	// Forward terminal resize events into the PTY.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer func() { signal.Stop(winch); close(winch) }()
	go func() {
		for range winch {
			if cols, rows, sizeErr := term.GetSize(int(os.Stdout.Fd())); sizeErr == nil {
				pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}) //nolint:errcheck
			}
		}
	}()

	// Raw mode: pass all bytes (including Ctrl+C) straight through to the PTY.
	if oldState, rawErr := term.MakeRaw(int(os.Stdin.Fd())); rawErr == nil {
		defer term.Restore(int(os.Stdin.Fd()), oldState) //nolint:errcheck
	}
	go io.Copy(ptmx, os.Stdin) //nolint:errcheck

	// Stream PTY output to the terminal and capture it.
	var outBuf bytes.Buffer
	doneCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				os.Stdout.Write(buf[:n]) //nolint:errcheck
				outBuf.Write(buf[:n])
			}
			if readErr != nil {
				break
			}
		}
		doneCh <- cmd.Wait()
	}()

	buildResult := func() *Result {
		r := &Result{Stdout: outBuf.String()}
		if cmd.ProcessState != nil {
			r.ExitCode = cmd.ProcessState.ExitCode()
		}
		return r
	}

	select {
	case <-ctx.Done():
		cmd.Process.Kill() //nolint:errcheck
		<-doneCh
		return buildResult(), ctx.Err()
	case err := <-doneCh:
		r := buildResult()
		if exitErr, ok := err.(*exec.ExitError); ok {
			r.ExitCode = exitErr.ExitCode()
		}
		return r, err
	}
}

// lastLineWriter captures output line by line and calls fn with each non-empty line.
// Partial lines (no trailing newline yet) are held in buf until the next Write.
type lastLineWriter struct {
	fn  func(string)
	buf []byte
}

func (w *lastLineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		idx := bytes.IndexByte(w.buf, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimSpace(string(w.buf[:idx]))
		w.buf = w.buf[idx+1:]
		if line != "" {
			w.fn(line)
		}
	}
	return len(p), nil
}

// Output runs name with args (no sudo, no extra env) and returns trimmed stdout.
// Stderr is discarded. Convenience wrapper for knowledge-gathering commands.
func (e *Executor) Output(ctx context.Context, name string, args ...string) (string, error) {
	r, err := e.Run(ctx, Options{}, name, args...)
	if err != nil {
		return "", fmt.Errorf("%s %s: %w\nstderr: %s", name, strings.Join(args, " "), err, r.Stderr)
	}
	return strings.TrimSpace(r.Stdout), nil
}

// Lines runs name with args and returns stdout split into trimmed, non-empty lines.
func (e *Executor) Lines(ctx context.Context, name string, args ...string) ([]string, error) {
	out, err := e.Output(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// Exists reports whether binary is available in PATH.
func Exists(binary string) bool {
	_, err := exec.LookPath(binary)
	return err == nil
}
