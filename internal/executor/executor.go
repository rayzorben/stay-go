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
	"regexp"
	"strings"
	"syscall"
	"time"

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
	// AllowInteractive lets the user answer prompts the process may raise.
	// It requires os.Stdin to be a terminal; otherwise the normal captured,
	// non-interactive behaviour applies. The mode depends on Sudo:
	//
	//   - Without Sudo the process runs under a PTY in a new session with the
	//     terminal in raw mode: output streams live and keystrokes are
	//     forwarded (runInteractive).
	//   - With Sudo a new session would give the process a different
	//     controlling terminal, defeating sudo's per-terminal credential cache
	//     and prompting for a password again. So the process keeps the real
	//     terminal as stdin and controlling terminal, and only its stdout and
	//     stderr go through a PTY. Output is captured quietly; if none arrives
	//     for stallTimeout the tail (typically a prompt) is shown and output
	//     streams live from then on. Keystrokes already reach the process via
	//     the inherited terminal (runStallAware).
	AllowInteractive bool
}

// Result holds the captured output of a completed command.
type Result struct {
	// Stdout holds the process's standard output. In the PTY modes (see
	// Options.AllowInteractive) stdout and stderr share one PTY, so Stdout
	// holds the merged transcript with terminal escape sequences removed.
	Stdout string
	// Stderr holds the process's standard error. In the PTY modes it holds the
	// merged transcript when that was never shown on the terminal, and is
	// empty when the output was streamed — so callers that append Stderr to an
	// error surface exactly the output the user has not seen.
	Stderr   string
	ExitCode int
}

// stallTimeout is how long a stall-aware process may stay silent before its
// output is surfaced. A variable so tests can shorten it.
var stallTimeout = 10 * time.Second

// stallTailLines is how many trailing lines of captured output are shown when
// a stall is detected — enough to include a multi-line prompt.
const stallTailLines = 12

const (
	ansiDim   = "\033[2m"
	ansiReset = "\033[0m"
)

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

	// Interactive modes need a terminal to hand keystrokes to. See
	// Options.AllowInteractive for why sudo gets a different mode.
	if opts.AllowInteractive && isTerminalStdin() {
		if opts.Sudo {
			return e.runStallAware(ctx, cmd, opts)
		}
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
	} else if err != nil {
		// Start failure (binary not found, permission denied, context cancel):
		// there is no real exit code. Use -1 so that ExitCode == 0 always means
		// "the process ran and succeeded" — callers that only inspect ExitCode
		// (e.g. services isEnabled, containers isRunning) must never mistake a
		// missing binary for success.
		result.ExitCode = -1
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

// mirrorWinsize sizes ptmx to match our terminal (so TUI layouts render
// correctly) and keeps it in sync on resize until the returned stop function
// is called.
func mirrorWinsize(ptmx *os.File) (stop func()) {
	resize := func() {
		if cols, rows, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
			pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}) //nolint:errcheck
		}
	}
	resize()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			resize()
		}
	}()
	return func() { signal.Stop(winch); close(winch) }
}

// runInteractive executes cmd under a PTY so the process sees a real terminal
// (preserving ANSI colours, box-drawing, and correct isatty detection). The
// terminal is put in raw mode and stdin is forwarded directly to the PTY so
// the user can interact with the process in real time. Ctrl+C is forwarded to
// the process's terminal line discipline, which sends SIGINT to the child.
func (e *Executor) runInteractive(ctx context.Context, cmd *exec.Cmd) (*Result, error) {
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return &Result{ExitCode: -1}, fmt.Errorf("pty start: %w", err)
	}
	defer ptmx.Close()
	defer mirrorWinsize(ptmx)()

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
		r := &Result{Stdout: cleanPTYText(outBuf.String())}
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

// runStallAware executes cmd with its stdout and stderr on a PTY that we read,
// while stdin stays on the real terminal and no new session is created. The
// controlling terminal — and with it sudo's credential cache, which is keyed
// on it — is therefore unchanged. sudo (use_pty enabled, the default since
// 1.9.14) passes through an output fd that is a tty other than the user's, so
// the PTY still receives everything; verified against sudo 1.9.17.
//
// Output is captured quietly, feeding ProgressFn if set. If the process stays
// silent for stallTimeout while still running it is most likely waiting on a
// prompt the user cannot see: the tail of the captured output is printed and
// everything after it streams live. No input forwarding is needed — the
// process reads the inherited terminal directly, in cooked mode — and stay-go
// never reads stdin itself here, so it cannot race the process for keystrokes.
func (e *Executor) runStallAware(ctx context.Context, cmd *exec.Cmd, opts Options) (*Result, error) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		return &Result{ExitCode: -1}, fmt.Errorf("pty open: %w", err)
	}
	defer ptmx.Close()
	defer mirrorWinsize(ptmx)()

	cmd.Stdin = os.Stdin
	cmd.Stdout = tty
	cmd.Stderr = tty
	if err := cmd.Start(); err != nil {
		tty.Close()
		return &Result{ExitCode: -1}, err
	}
	// The child holds its own copy of the slave; once every holder is gone,
	// reads on ptmx fail with EIO, which ends the reader below.
	tty.Close()

	streaming := opts.Stream || (e.Debug && e.Verbose)
	var hint *lastLineWriter
	if !streaming && e.ProgressFn != nil {
		hint = &lastLineWriter{fn: e.ProgressFn}
	}

	chunks := make(chan []byte)
	go func() {
		defer close(chunks)
		buf := make([]byte, 4096)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				c := make([]byte, n)
				copy(c, buf[:n])
				chunks <- c
			}
			if readErr != nil {
				return
			}
		}
	}()

	var timer *time.Timer
	var timerC <-chan time.Time
	if !streaming {
		timer = time.NewTimer(stallTimeout)
		defer timer.Stop()
		timerC = timer.C
	}

	var transcript bytes.Buffer
	cancelled := false
loop:
	for {
		select {
		case c, ok := <-chunks:
			if !ok {
				break loop
			}
			transcript.Write(c)
			switch {
			case streaming:
				os.Stdout.Write(c) //nolint:errcheck
			case hint != nil:
				hint.Write(c) //nolint:errcheck
			}
			if timer != nil {
				timer.Reset(stallTimeout)
			}
		case <-timerC:
			streaming = true
			timer, timerC = nil, nil
			announceStall(os.Stdout, transcript.Bytes(), hint != nil)
		case <-ctx.Done():
			cancelled = true
			cmd.Process.Kill() //nolint:errcheck
			for range chunks { // drain until the reader hits EIO
			}
			break loop
		}
	}
	waitErr := cmd.Wait()

	text := cleanPTYText(transcript.String())
	r := &Result{Stdout: text}
	if !streaming {
		r.Stderr = text
	}
	if cmd.ProcessState != nil {
		r.ExitCode = cmd.ProcessState.ExitCode()
	}
	if e.Debug && !e.Verbose && !streaming && text != "" {
		fmt.Fprint(os.Stdout, truncateDebugOutput(text))
	}
	if cancelled {
		return r, ctx.Err()
	}
	return r, waitErr
}

// announceStall reports that a stall-aware process has gone quiet. It commits
// the caller's progress line if one is being drawn, prints a dim notice, and
// echoes the tail of the output captured so far — usually the prompt the
// process is waiting on — so the user can answer it.
func announceStall(w io.Writer, transcript []byte, progressLineOpen bool) {
	if progressLineOpen {
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "%s     ┆ no output for %s: showing live output (type to answer prompts, Ctrl+C to cancel)%s\n",
		ansiDim, stallTimeout, ansiReset)
	if tail := lastLines(transcript, stallTailLines); len(tail) > 0 {
		w.Write(tail) //nolint:errcheck
	}
}

// lastLines returns the final n lines of b. A trailing partial line counts as
// a line (that is where a prompt lives); a trailing newline does not start one.
func lastLines(b []byte, n int) []byte {
	if n <= 0 {
		return nil
	}
	i := len(b)
	if i > 0 && b[i-1] == '\n' {
		i--
	}
	for count := 0; ; {
		j := bytes.LastIndexByte(b[:i], '\n')
		if j < 0 {
			return b
		}
		if count++; count == n {
			return b[j+1:]
		}
		i = j
	}
}

// ansiRE matches CSI sequences (colours, cursor movement), OSC sequences
// (titles, sudo's session markers) and any other two-byte ESC sequence.
var ansiRE = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-_]`)

// stripANSI removes terminal escape sequences from s.
func stripANSI(s string) string {
	return ansiRE.ReplaceAllString(s, "")
}

// cleanPTYText strips escape sequences and normalises the CRLF line endings a
// PTY produces, so captured output reads like ordinary pipe output.
func cleanPTYText(s string) string {
	s = stripANSI(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// lastLineWriter captures output line by line and calls fn with each non-empty
// line. A bare carriage return also ends a line so that progress bars redrawn
// with \r (common once a program sees a tty) update the hint. Partial lines are
// held in buf until the next Write. Escape sequences are stripped from lines.
type lastLineWriter struct {
	fn  func(string)
	buf []byte
}

func (w *lastLineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		idx := bytes.IndexAny(w.buf, "\r\n")
		if idx < 0 {
			break
		}
		line := strings.TrimSpace(stripANSI(string(w.buf[:idx])))
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
