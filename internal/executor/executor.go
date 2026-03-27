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
	"strings"
)

// Executor is a stateless command runner. Debug controls whether command output
// is streamed to the terminal in addition to being captured.
type Executor struct {
	Debug bool
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

	// Stdin: use /dev/null unless the caller supplies a reader.
	// This prevents package managers from blocking on interactive input.
	if opts.Stdin != nil {
		cmd.Stdin = opts.Stdin
	} else {
		devNull, err := os.Open(os.DevNull)
		if err == nil {
			cmd.Stdin = devNull
			defer devNull.Close()
		}
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	if e.Debug || opts.Stream {
		cmd.Stdout = io.MultiWriter(os.Stdout, &stdoutBuf)
		cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)
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
	return result, err
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
