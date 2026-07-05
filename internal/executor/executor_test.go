package executor

import (
	"context"
	"testing"
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
