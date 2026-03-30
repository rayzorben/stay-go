package files

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rayben/stay-go/internal/executor"
)

func TestFileContainsSnippet(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")

	if fileContainsSnippet(p, "hello") {
		t.Fatal("missing file should not contain")
	}
	if err := os.WriteFile(p, []byte("alpha\nhello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !fileContainsSnippet(p, "hello") {
		t.Fatal("expected substring match")
	}
	if fileContainsSnippet(p, "zzz") {
		t.Fatal("should not match absent substring")
	}
}

func TestAppendSnippetIfMissing(t *testing.T) {
	ctx := context.Background()
	ex := &executor.Executor{}
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg")

	if err := appendSnippetIfMissing(ctx, ex, p, "block\n", 0o644, false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "block\n" {
		t.Fatalf("got %q", b)
	}
	// idempotent
	if err := appendSnippetIfMissing(ctx, ex, p, "block\n", 0o644, false); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(p)
	if string(b2) != "block\n" {
		t.Fatalf("after second apply got %q", b2)
	}
	// append second snippet
	if err := appendSnippetIfMissing(ctx, ex, p, "tail", 0o644, false); err != nil {
		t.Fatal(err)
	}
	b3, _ := os.ReadFile(p)
	if string(b3) != "block\ntail" {
		t.Fatalf("append got %q", b3)
	}
}

func TestRemoveSnippetFromFile(t *testing.T) {
	ctx := context.Background()
	ex := &executor.Executor{}
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("keep\nSNIP\nend\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeSnippetFromFile(ctx, ex, p, "SNIP\n", false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "keep\nend\n" {
		t.Fatalf("got %q", b)
	}
	// strip all leaving empty -> file removed
	if err := os.WriteFile(p, []byte("only"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeSnippetFromFile(ctx, ex, p, "only", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("expected file removed, stat err=%v", err)
	}
}
