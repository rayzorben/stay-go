// Package runlog writes a persistent, plain-text log of a single stay-go run:
// the invocation, every command executed (argv, exit code, duration, captured
// output), the plan, each node's execution result, and how the run ended.
// One file per run lives in the log directory (by default next to the state
// file, ~/.local/share/stay-go/logs/); latest.log points at the newest and only
// the most recent files are kept.
//
// Writes go straight to the file (no buffering) so a log survives a crash or a
// kill mid-run. Every method is safe on a nil *Log, which makes logging
// optional for callers (tests, --show, internal sub-modes) without nil checks.
package runlog

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultKeep is how many run logs Open retains in the log directory.
const DefaultKeep = 100

// maxStream caps how much of a single stdout/stderr stream is logged. Longer
// output keeps its head and (larger) tail — the end is where errors usually are.
const (
	maxStream  = 64 << 10
	streamHead = 8 << 10
)

const (
	timeFormat = "2006-01-02 15:04:05.000"
	latestName = "latest.log"
	redacted   = "«redacted»"
)

// ansiRE matches terminal escape sequences (colours in plan notes, coloured
// command output); the log is plain text.
var ansiRE = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-_]`)

// logNameRE matches the files Open creates; pruning never touches anything else.
var logNameRE = regexp.MustCompile(`^\d{8}-\d{6}\.\d{3}-\d+\.log$`)

// Log is one run's log file. Safe for concurrent use.
type Log struct {
	mu      sync.Mutex
	f       *os.File
	path    string
	secrets []string
	seq     int
	start   time.Time
}

// Open creates a new log file in dir (created 0700 if needed; the file is
// 0600 since command lines and output may be sensitive), points latest.log at
// it, and prunes all but the newest keep logs. keep <= 0 disables pruning.
func Open(dir string, keep int) (*Log, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating log dir: %w", err)
	}
	now := time.Now()
	name := fmt.Sprintf("%s.%03d-%d.log", now.Format("20060102-150405"), now.Nanosecond()/1e6, os.Getpid())
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("creating log file: %w", err)
	}

	// Best-effort conveniences: a failure here must not stop the run.
	latest := filepath.Join(dir, latestName)
	os.Remove(latest)        //nolint:errcheck
	os.Symlink(name, latest) //nolint:errcheck
	prune(dir, keep)

	return &Log{f: f, path: path, start: now}, nil
}

// prune removes the oldest run logs in dir so that at most keep remain.
// Names sort chronologically, so lexical order is age order.
func prune(dir string, keep int) {
	if keep <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var logs []string
	for _, e := range entries {
		if e.Type().IsRegular() && logNameRE.MatchString(e.Name()) {
			logs = append(logs, e.Name())
		}
	}
	if len(logs) <= keep {
		return
	}
	sort.Strings(logs)
	for _, name := range logs[:len(logs)-keep] {
		os.Remove(filepath.Join(dir, name)) //nolint:errcheck
	}
}

// Path returns the log file's path ("" for a nil Log).
func (l *Log) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Redact registers a secret value; it is replaced with «redacted» in
// everything written from then on. Called as secrets are decrypted, before any
// command that could carry the plaintext runs.
func (l *Log) Redact(secret string) {
	if l == nil || secret == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.secrets {
		if s == secret {
			return
		}
	}
	l.secrets = append(l.secrets, secret)
	// Longest first, so a secret containing another is replaced whole.
	sort.Slice(l.secrets, func(i, j int) bool { return len(l.secrets[i]) > len(l.secrets[j]) })
}

// Printf writes one timestamped event line.
func (l *Log) Printf(format string, args ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.writeLocked(stamp() + "  " + fmt.Sprintf(format, args...) + "\n")
}

// CommandStart records that a command is about to run and returns its id,
// which pairs the start line with CommandEnd's result (knowledge gathering
// runs commands in parallel, so their lines interleave).
func (l *Log) CommandStart(argv []string, env []string) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	var b strings.Builder
	fmt.Fprintf(&b, "%s  cmd#%d $ %s\n", stamp(), l.seq, quoteArgs(argv))
	if len(env) > 0 {
		fmt.Fprintf(&b, "%s  cmd#%d   env: %s\n", stamp(), l.seq, strings.Join(env, " "))
	}
	l.writeLocked(b.String())
	return l.seq
}

// CommandEnd records a command's outcome and its captured output.
func (l *Log) CommandEnd(id, exitCode int, dur time.Duration, stdout, stderr string, err error) {
	if l == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s  cmd#%d exit=%d %s", stamp(), id, exitCode, dur.Round(time.Millisecond))
	if err != nil {
		fmt.Fprintf(&b, " err=%q", err.Error())
	}
	b.WriteByte('\n')
	writeStream(&b, "stdout", stdout)
	writeStream(&b, "stderr", stderr)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.writeLocked(b.String())
}

// Close writes the closing line (how the run ended and how long it took) and
// closes the file. exitCode is the process exit status about to be returned.
func (l *Log) Close(exitCode int) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	l.writeLocked(fmt.Sprintf("%s  run finished: exit=%d after %s\n",
		stamp(), exitCode, time.Since(l.start).Round(time.Millisecond)))
	l.f.Close()
	l.f = nil
}

// writeLocked strips escape sequences from s, redacts it, and appends it to the
// file. Write errors are ignored:
// logging must never break a run. Caller holds l.mu.
func (l *Log) writeLocked(s string) {
	if l.f == nil {
		return
	}
	s = ansiRE.ReplaceAllString(s, "")
	for _, secret := range l.secrets {
		s = strings.ReplaceAll(s, secret, redacted)
	}
	l.f.WriteString(s) //nolint:errcheck
}

// writeStream appends a captured output stream as an indented block, capped at
// maxStream bytes (head + tail kept).
func writeStream(b *strings.Builder, label, s string) {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return
	}
	if len(s) > maxStream {
		tail := maxStream - streamHead
		s = s[:streamHead] +
			fmt.Sprintf("\n… %d bytes omitted …\n", len(s)-maxStream) +
			s[len(s)-tail:]
	}
	fmt.Fprintf(b, "    %s:\n", label)
	for _, line := range strings.Split(s, "\n") {
		b.WriteString("    │ ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
}

// quoteArgs renders argv as a copy-pasteable shell command line.
func quoteArgs(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && !strings.ContainsAny(a, " \t\n'\"\\$`;&|<>()*?[]{}!#~") {
			out[i] = a
		} else {
			out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(out, " ")
}

func stamp() string { return time.Now().Format(timeFormat) }
