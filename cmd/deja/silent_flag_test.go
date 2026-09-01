package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every command in this family refuses a flag it does not take. friction and
// restore dropped one on the floor and answered as though nothing was wrong, so
// a script asking either for --json got prose on stdout and exit 0 (#2253).
//
// friction ANSWERS --json now (#1932), so it has moved to the accepted list
// below. The property this test exists for is unchanged: a flag the command
// cannot honour must be refused, never ignored. --limt keeps that covered, and
// restore still refuses --json because it does not implement one.
func TestFrictionAndRestoreRefuseAFlagTheyDoNotTake(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DEJA_CLAUDE_ROOT", filepath.Join(tmp, "claude"))
	t.Setenv("DEJA_CODEX_ROOT", filepath.Join(tmp, "codex"))
	t.Setenv("DEJA_OPENCODE_DB", filepath.Join(tmp, "none.db"))
	t.Setenv("DEJA_NOTES_FILE", filepath.Join(tmp, "notes.jsonl"))
	dir := filepath.Join(tmp, "index.db")

	var out bytes.Buffer
	for _, args := range [][]string{
		{"--limt", "3"},
		{"--limit"},
		{"--jsonn"},
	} {
		out.Reset()
		err := runFriction(dir, args, &out)
		if err == nil {
			t.Errorf("friction %v was accepted, and printed %q", args, out.String())
			continue
		}
		if !strings.Contains(err.Error(), args[0]) {
			t.Errorf("friction %v: %v — the message should name the flag", args, err)
		}
	}
	// What it does take still works.
	out.Reset()
	if err := runFriction(dir, []string{"--limit", "3"}, &out); err != nil {
		t.Errorf("friction --limit 3: %v", err)
	}
	out.Reset()
	if err := runFriction(dir, []string{"--json"}, &out); err != nil {
		t.Errorf("friction --json: %v", err)
	}

	for _, args := range [][]string{
		{"api/upload.go", "--json"},
		{"api/upload.go", "-o"},
		{"api/upload.go", "--span"},
	} {
		out.Reset()
		err := runRestore(dir, args, &out)
		if err == nil {
			t.Errorf("restore %v was accepted, and printed %q", args, out.String())
			continue
		}
		if !strings.Contains(err.Error(), args[1]) {
			t.Errorf("restore %v: %v — the message should name the flag", args, err)
		}
	}
}
