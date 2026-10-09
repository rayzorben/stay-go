package executor

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A binary that fails to START must not report ExitCode 0: callers that only
// inspect ExitCode (services isEnabled, containers isRunning) would mistake a
// missing binary for success — e.g. every service looked "enabled" on systems
// without systemctl and got silently adopted into state. Regression for that.
func TestRun_missingBinaryHasNonZeroExitCode(t *testing.T) {
	e := &Executor{}
	result, err := e.Run(context.Background(), Options{}, "sgtest-definitely-no-such-binary-xyz")
	if err == nil {
		t.Fatal("expected an error for a missing binary")
	}
	if result.ExitCode == 0 {
		t.Fatal("ExitCode must be non-zero when the process never ran")
	}
}

// A process that runs and exits non-zero reports its real exit code.
func TestRun_realExitCodePreserved(t *testing.T) {
	e := &Executor{}
	result, err := e.Run(context.Background(), Options{}, "sh", "-c", "exit 3")
	if err == nil {
		t.Fatal("expected an error for exit 3")
	}
	if result.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want 3", result.ExitCode)
	}
}

// A successful run reports ExitCode 0 and no error.
func TestRun_successIsZero(t *testing.T) {
	e := &Executor{}
	result, err := e.Run(context.Background(), Options{}, "true")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", result.ExitCode)
	}
}

// ─── Stall-aware (sudo + AllowInteractive) mode ──────────────────────────────
//
// These drive runStallAware directly: Run only selects it when os.Stdin is a
// terminal, which it never is under go test.

func setStallTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := stallTimeout
	stallTimeout = d
	t.Cleanup(func() { stallTimeout = old })
}

// captureStdout runs fn with os.Stdout redirected and returns what was written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		io.Copy(&buf, r) //nolint:errcheck
		close(done)
	}()
	fn()
	w.Close()
	os.Stdout = orig
	<-done
	return buf.String()
}

// While the process keeps producing output nothing is shown: both streams are
// captured (merged, CRLF normalised, escapes stripped), ProgressFn sees every
// line, and Stderr carries the unseen transcript for error messages.
func TestRunStallAware_quietCapture(t *testing.T) {
	setStallTimeout(t, time.Hour)
	var lines []string
	e := &Executor{ProgressFn: func(l string) { lines = append(lines, l) }}
	cmd := exec.CommandContext(context.Background(), "bash", "-c",
		`echo hi; echo there >&2; printf '\033[32mgreen\033[0m\n'`)

	var res *Result
	var err error
	shown := captureStdout(t, func() { res, err = e.runStallAware(context.Background(), cmd, Options{}) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if shown != "" {
		t.Fatalf("nothing should reach the terminal before a stall, got %q", shown)
	}
	if want := "hi\nthere\ngreen\n"; res.Stdout != want {
		t.Fatalf("Stdout = %q, want %q", res.Stdout, want)
	}
	if res.Stderr != res.Stdout {
		t.Fatalf("Stderr should carry the unseen transcript, got %q", res.Stderr)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if got := strings.Join(lines, ","); got != "hi,there,green" {
		t.Fatalf("ProgressFn lines = %q", got)
	}
}

// A silent process is assumed to be waiting on a prompt: after stallTimeout the
// captured tail (the prompt) is echoed and later output streams live. Because
// the user has now seen everything, Stderr is empty.
func TestRunStallAware_stallShowsTailThenStreams(t *testing.T) {
	setStallTimeout(t, 150*time.Millisecond)
	e := &Executor{}
	cmd := exec.CommandContext(context.Background(), "bash", "-c",
		`echo starting; printf 'Continue? [Y/n]: '; sleep 1; echo answered`)

	var res *Result
	var err error
	shown := captureStdout(t, func() { res, err = e.runStallAware(context.Background(), cmd, Options{}) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"no output for 150ms", "starting", "Continue? [Y/n]: ", "answered"} {
		if !strings.Contains(shown, want) {
			t.Errorf("terminal output missing %q:\n%s", want, shown)
		}
	}
	// The prompt must be printed once by the stall tail, not again by streaming.
	if n := strings.Count(shown, "Continue? [Y/n]: "); n != 1 {
		t.Errorf("prompt shown %d times, want 1:\n%s", n, shown)
	}
	if !strings.Contains(res.Stdout, "Continue? [Y/n]: answered") {
		t.Errorf("Stdout should hold the full transcript, got %q", res.Stdout)
	}
	if res.Stderr != "" {
		t.Errorf("Stderr should be empty once output was streamed, got %q", res.Stderr)
	}
}

func TestRunStallAware_exitCodes(t *testing.T) {
	setStallTimeout(t, time.Hour)
	e := &Executor{}

	cmd := exec.CommandContext(context.Background(), "bash", "-c", "echo boom >&2; exit 3")
	var res *Result
	var err error
	captureStdout(t, func() { res, err = e.runStallAware(context.Background(), cmd, Options{}) })
	if err == nil || res.ExitCode != 3 {
		t.Fatalf("exit 3: err=%v ExitCode=%d", err, res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "boom") {
		t.Fatalf("unseen failure output should be in Stderr, got %q", res.Stderr)
	}

	cmd = exec.CommandContext(context.Background(), "/sgtest-definitely-no-such-binary-xyz")
	res, err = e.runStallAware(context.Background(), cmd, Options{})
	if err == nil || res == nil || res.ExitCode != -1 {
		t.Fatalf("start failure: err=%v res=%+v", err, res)
	}
}

// Cancelling the context kills the process and returns promptly rather than
// waiting for the PTY to drain on its own.
func TestRunStallAware_contextCancel(t *testing.T) {
	setStallTimeout(t, time.Hour)
	e := &Executor{}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", "exec sleep 10")

	start := time.Now()
	var err error
	captureStdout(t, func() { _, err = e.runStallAware(ctx, cmd, Options{}) })
	if err != context.DeadlineExceeded {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("took %v to return after cancel", el)
	}
}

func TestLastLines(t *testing.T) {
	cases := []struct {
		in, want string
		n        int
	}{
		{"a\nb\nc", "b\nc", 2},
		{"a\nb\nc\n", "b\nc\n", 2},
		{"a\nb\nc\n", "c\n", 1},
		{"a\nb", "a\nb", 5},
		{"prompt: ", "prompt: ", 3},
		{"", "", 3},
	}
	for _, c := range cases {
		if got := string(lastLines([]byte(c.in), c.n)); got != c.want {
			t.Errorf("lastLines(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

// Lines end at \r as well as \n so \r-redrawn progress bars update the hint,
// and escape sequences never leak into it.
func TestLastLineWriter_carriageReturnAndANSI(t *testing.T) {
	var lines []string
	w := &lastLineWriter{fn: func(l string) { lines = append(lines, l) }}
	w.Write([]byte("\x1b[32mok\x1b[0m\r\n10%\r20%")) //nolint:errcheck
	w.Write([]byte("\n\x1b]0;title\x07partial"))     //nolint:errcheck
	if got := strings.Join(lines, ","); got != "ok,10%,20%" {
		t.Fatalf("lines = %q", got)
	}
}
