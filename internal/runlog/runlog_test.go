package runlog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readLog(t *testing.T, l *Log) string {
	t.Helper()
	data, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	return string(data)
}

func TestOpenCreatesPrivateFileAndLatestLink(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	l, err := Open(dir, 0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close(0)

	fi, err := os.Stat(l.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("log perm = %o, want 600", perm)
	}
	target, err := os.Readlink(filepath.Join(dir, latestName))
	if err != nil {
		t.Fatalf("latest.log: %v", err)
	}
	if target != filepath.Base(l.Path()) {
		t.Errorf("latest.log -> %q, want %q", target, filepath.Base(l.Path()))
	}
}

func TestOpenPrunesOldestLogs(t *testing.T) {
	dir := t.TempDir()
	old := []string{
		"20250101-000000.000-1.log",
		"20250102-000000.000-1.log",
		"20250103-000000.000-1.log",
	}
	for _, name := range append(old, "notes.txt") {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	l, err := Open(dir, 2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close(0)

	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	if exists(old[0]) || exists(old[1]) {
		t.Errorf("oldest logs not pruned")
	}
	if !exists(old[2]) || !exists(filepath.Base(l.Path())) {
		t.Errorf("newest logs pruned")
	}
	if !exists("notes.txt") {
		t.Errorf("pruned a file stay-go did not create")
	}
}

func TestCommandRecordAndRedaction(t *testing.T) {
	l, err := Open(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	l.Redact("hunter2")
	id := l.CommandStart([]string{"sudo", "echo", "pw=hunter2", "two words"}, []string{"TOKEN=hunter2"})
	l.CommandEnd(id, 3, 1500*time.Millisecond, "out \x1b[32mline\x1b[0m\n", "boom hunter2\n", errors.New("exit status 3"))
	l.Close(1)

	got := readLog(t, l)
	if strings.Contains(got, "hunter2") {
		t.Errorf("secret leaked into log:\n%s", got)
	}
	for _, want := range []string{
		"cmd#1 $ sudo echo pw=«redacted» 'two words'",
		"cmd#1   env: TOKEN=«redacted»",
		`cmd#1 exit=3 1.5s err="exit status 3"`,
		"    stdout:\n    │ out line\n",
		"    stderr:\n    │ boom «redacted»\n",
		"run finished: exit=1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log missing %q:\n%s", want, got)
		}
	}
}

func TestLongOutputKeepsHeadAndTail(t *testing.T) {
	var b strings.Builder
	out := "HEAD" + strings.Repeat("x", 2*maxStream) + "TAIL"
	writeStream(&b, "stdout", out)
	got := b.String()
	if !strings.Contains(got, "HEAD") || !strings.Contains(got, "TAIL") {
		t.Errorf("head or tail dropped")
	}
	if !strings.Contains(got, "bytes omitted") {
		t.Errorf("no truncation marker")
	}
	if len(got) > maxStream+1024 {
		t.Errorf("logged %d bytes, want about %d", len(got), maxStream)
	}
}

func TestNilLogIsNoop(t *testing.T) {
	var l *Log
	l.Redact("x")
	l.Printf("x")
	l.CommandEnd(l.CommandStart([]string{"true"}, nil), 0, 0, "", "", nil)
	l.Close(0)
	if l.Path() != "" {
		t.Errorf("nil Path = %q", l.Path())
	}
}
