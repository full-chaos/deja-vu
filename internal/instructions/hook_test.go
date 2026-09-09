package instructions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunbookAndBudget(t *testing.T) {
	d := isolated(t)
	p := filepath.Join(d, "RUNBOOK.md")
	body := []byte("Step 1: run synthetic checks.\nStep 2: inspect results.")
	write(t, p, body)
	h := sha256.Sum256(body)
	r := rule("runbook")
	r.Kind = "procedure"
	r.Runbook = &Runbook{Path: p, SHA256: hex.EncodeToString(h[:])}
	resolved, e := Resolve(snapshot(r), ctx())
	if e != nil {
		t.Fatal(e)
	}
	text, e := Render(resolved, DefaultBudget)
	if e != nil || !strings.Contains(text, string(body)) {
		t.Fatal(text, e)
	}
	write(t, p, []byte("unapproved edit"))
	_, e = Render(resolved, DefaultBudget)
	bad(t, e)
	r.Scope.Tools = []string{"Bash"}
	resolved, e = Resolve(snapshot(r), ctx())
	if e != nil {
		t.Fatal(e)
	}
	text, e = Render(resolved, DefaultBudget)
	if e != nil || !strings.Contains(text, "CONDITIONAL") || strings.Contains(text, "unapproved edit") {
		t.Fatal(text, e)
	}
	for _, budget := range []int{0, 1, DefaultBudget + 1} {
		_, e = Render(resolved, budget)
		bad(t, e)
	}
	resolved.Conflicts = []Conflict{{Key: "x", RuleIDs: []string{"a", "b"}}}
	_, e = Render(resolved, DefaultBudget)
	bad(t, e)
	if text, e = Render(Result{}, DefaultBudget); e != nil || text != "" {
		t.Fatal(text, e)
	}
	r = rule("large")
	r.Text = strings.Repeat("x", DefaultBudget)
	resolved, _ = Resolve(snapshot(r), ctx())
	text, e = Render(resolved, DefaultBudget)
	if e == nil || text != "" {
		t.Fatal("mandatory truncation")
	}
	r.Strength = "should"
	resolved, _ = Resolve(snapshot(r, rule("must")), ctx())
	text, e = Render(resolved, 1000)
	if e != nil || !strings.Contains(text, "Optional preferences omitted: 1") || !strings.Contains(text, "must@1") {
		t.Fatal(text, e)
	}
	r.Text = "A small preference."
	resolved, _ = Resolve(snapshot(r), ctx())
	text, e = Render(resolved, 1000)
	if e != nil || !strings.Contains(text, r.Text) {
		t.Fatal(text, e)
	}
	r.Kind = "procedure"
	r.Strength = "must"
	r.Runbook = &Runbook{Path: p, SHA256: strings.Repeat("0", 64)}
	if e = os.Remove(p); e != nil {
		t.Fatal(e)
	}
	resolved, _ = Resolve(snapshot(r), ctx())
	_, e = Render(resolved, DefaultBudget)
	bad(t, e)
	invalid := []byte{0xff, 0xfe}
	write(t, p, invalid)
	h = sha256.Sum256(invalid)
	r.Runbook.SHA256 = hex.EncodeToString(h[:])
	resolved, _ = Resolve(snapshot(r), ctx())
	_, e = Render(resolved, DefaultBudget)
	bad(t, e)
}
func TestWorkspace(t *testing.T) {
	d := isolated(t)
	root := filepath.Join(d, "repo")
	git := filepath.Join(root, ".git")
	sub := filepath.Join(root, "src")
	if e := os.MkdirAll(git, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(sub, 0700); e != nil {
		t.Fatal(e)
	}
	repo, wt, e := Workspace(sub)
	if e != nil || repo != root || wt != root {
		t.Fatal(repo, wt, e)
	}
	plain := filepath.Join(d, "plain")
	if e = os.Mkdir(plain, 0700); e != nil {
		t.Fatal(e)
	}
	repo, wt, e = Workspace(plain)
	if e != nil || repo != plain || wt != plain {
		t.Fatal(repo, wt, e)
	}
	worktree := filepath.Join(d, "checkout")
	meta := filepath.Join(git, "worktrees", "checkout")
	if e = os.MkdirAll(meta, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(worktree, 0700); e != nil {
		t.Fatal(e)
	}
	write(t, filepath.Join(worktree, ".git"), []byte("gitdir: "+meta+"\n"))
	write(t, filepath.Join(meta, "commondir"), []byte("../..\n"))
	repo, wt, e = Workspace(worktree)
	if e != nil || repo != root || wt != worktree {
		t.Fatal(repo, wt, e)
	}
	for _, invalid := range []string{"relative", filepath.Join(d, "missing"), filepath.Join(worktree, ".git")} {
		_, _, e = Workspace(invalid)
		bad(t, e)
	}
	for _, text := range []string{"invalid", "gitdir: "} {
		write(t, filepath.Join(worktree, ".git"), []byte(text))
		_, _, e = Workspace(worktree)
		bad(t, e)
	}
	write(t, filepath.Join(worktree, ".git"), []byte("gitdir: ../repo/.git/worktrees/checkout\n"))
	write(t, filepath.Join(meta, "commondir"), []byte(""))
	_, _, e = Workspace(worktree)
	bad(t, e)
	write(t, filepath.Join(meta, "commondir"), []byte(d))
	_, _, e = Workspace(worktree)
	bad(t, e)
}
func TestNewFileSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires symlink privilege")
	}
	d := isolated(t)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if e := os.Symlink(outside, filepath.Join(d, "link")); e != nil {
		t.Fatal(e)
	}
	got, e := canonicalTarget(filepath.Join(d, "link", "new", "file.go"))
	if e != nil || got != filepath.Join(outside, "new", "file.go") {
		t.Fatal(got, e)
	}
	if e = os.Symlink(filepath.Join(d, "missing"), filepath.Join(d, "dangling")); e != nil {
		t.Fatal(e)
	}
	_, e = canonicalTarget(filepath.Join(d, "dangling", "file.go"))
	bad(t, e)
}

type memoryStore struct {
	s   Snapshot
	err error
}

func (m memoryStore) Load() (Snapshot, error) { return m.s, m.err }
func (m memoryStore) Replace(uint64, Snapshot) (Snapshot, error) {
	return Snapshot{}, errors.New("read only")
}
func TestHookAdapters(t *testing.T) {
	d := isolated(t)
	r := rule("absolute")
	r.Absolute = true
	for _, agent := range []string{"claude", "codex"} {
		for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "SubagentStart"} {
			input := HookInput{Event: event, CWD: d, Session: "synthetic", Tool: "Edit", Input: json.RawMessage(`{"file_path":"src/new.go"}`)}
			var out bytes.Buffer
			if e := Hook(memoryStore{s: snapshot(r)}, agent, "T", "local", bytes.NewReader(encoded(t, input)), &out, instant); e != nil {
				t.Fatal(e)
			}
			var result map[string]any
			if e := json.Unmarshal(out.Bytes(), &result); e != nil {
				t.Fatal(e)
			}
			specific, ok := result["hookSpecificOutput"].(map[string]any)
			if !ok || specific["hookEventName"] != event || !strings.Contains(specific["additionalContext"].(string), "absolute@1") {
				t.Fatal(out.String())
			}
			if _, ok := specific["permissionDecision"]; ok {
				t.Fatal("advisory hook granted permission")
			}
		}
	}
	for _, data := range []string{`not json`, `{"hook_event_name":"Unsupported","cwd":"x"}`, `{"hook_event_name":"SessionStart","cwd":"relative"}`} {
		var out bytes.Buffer
		if e := Hook(memoryStore{s: snapshot(r)}, "codex", "", "", strings.NewReader(data), &out, instant); e != nil || !strings.Contains(out.String(), "INCOMPLETE") {
			t.Fatal(out.String(), e)
		}
	}
	input := HookInput{Event: "PreToolUse", CWD: d, Tool: "Read", Input: json.RawMessage(`{"path":"src/a.go"}`)}
	var out bytes.Buffer
	for _, store := range []memoryStore{{err: errors.New("broken store")}, {s: Snapshot{}}, {s: snapshot(func() Rule { x := rule("oversize"); x.Text = strings.Repeat("x", 9000); return x }())}} {
		out.Reset()
		if e := Hook(store, "claude", "", "", bytes.NewReader(encoded(t, input)), &out, instant); e != nil || !strings.Contains(out.String(), "INCOMPLETE") || !strings.Contains(out.String(), "additionalContext") {
			t.Fatal(out.String(), e)
		}
	}
	out.Reset()
	input.Input = json.RawMessage(`"wrong shape"`)
	if e := Hook(memoryStore{s: snapshot(r)}, "claude", "", "", bytes.NewReader(encoded(t, input)), &out, instant); e != nil || !strings.Contains(out.String(), "INCOMPLETE") {
		t.Fatal(out.String(), e)
	}
	bad(t, Hook(memoryStore{}, "other", "", "", strings.NewReader(`{}`), &out, instant))
	input.Input = nil
	out.Reset()
	if e := Hook(memoryStore{s: snapshot()}, "codex", "", "", bytes.NewReader(encoded(t, input)), &out, instant); e != nil || out.Len() != 0 {
		t.Fatal(out.String(), e)
	}
	bad(t, Hook(memoryStore{s: snapshot(r)}, "codex", "", "", bytes.NewReader(encoded(t, input)), failingWriter{}, instant))
	bad(t, hookWarning(failingWriter{}, "", "test"))
}
