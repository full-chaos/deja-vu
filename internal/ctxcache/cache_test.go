package ctxcache

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testIdentity(head string) Identity {
	return Identity{WorkspaceID: "workspace", Repository: "repo", RepositoryRoot: "/repo", Worktree: "/repo", Branch: "main", GitHead: head, TaskID: "TASK-1", ProjectID: "repo"}
}

func TestCheckpointResumeRefreshAndInvalidate(t *testing.T) {
	root := t.TempDir()
	id := testIdentity("aaa")
	miss, err := Resume(root, id, 1000)
	if err != nil || miss.CacheStatus != "miss" {
		t.Fatalf("miss = %#v, %v", miss, err)
	}
	state := State{Objective: "ship it", Confirmed: []Item{{ID: "finding", Text: "cache is local", Source: "git://repo/a.go#L1"}}, Gaps: []Gap{{Subject: "acceptance criteria", Severity: "required"}}}
	s, err := Checkpoint(root, id, state)
	if err != nil {
		t.Fatal(err)
	}
	if s.Freshness.SnapshotVersion != 1 {
		t.Fatalf("version=%d", s.Freshness.SnapshotVersion)
	}
	hit, err := Resume(root, id, 1000)
	if err != nil || hit.CacheStatus != "hit" {
		t.Fatalf("hit = %#v, %v", hit, err)
	}
	changed := id
	changed.GitHead = "bbb"
	st, err := Inspect(root, changed)
	if err != nil || st.Status != "stale" || len(st.Changed) != 1 || st.Changed[0] != "git" {
		t.Fatalf("status=%#v, %v", st, err)
	}
	updated, layers, err := Refresh(root, changed)
	if err != nil || len(layers) != 1 || layers[0] != "git" {
		t.Fatalf("refresh=%#v %v %v", updated, layers, err)
	}
	if err := Invalidate(root, changed, "task"); err != nil {
		t.Fatal(err)
	}
	st, _ = Inspect(root, changed)
	if st.Status != "stale" {
		t.Fatalf("invalid status=%#v", st)
	}
	history, err := History(root, changed)
	if err != nil || len(history) != 3 {
		t.Fatalf("invalidation was not recorded in history: %d %v", len(history), err)
	}
}

func TestBranchSwitchReusesSnapshotAndCreatesValidationGap(t *testing.T) {
	root := t.TempDir()
	main := testIdentity("aaa")
	if _, err := Checkpoint(root, main, State{Objective: "ship"}); err != nil {
		t.Fatal(err)
	}
	feature := main
	feature.Branch = "feature"
	feature.GitHead = "bbb"
	feature.WorktreeState = "dirty"
	r, err := Resume(root, feature, 1000)
	if err != nil || r.CacheStatus != "stale" {
		t.Fatalf("branch resume=%#v %v", r, err)
	}
	updated, changed, err := Refresh(root, feature)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) < 2 || len(updated.State.Gaps) != 1 || updated.State.Gaps[0].Severity != "required" {
		t.Fatalf("refresh=%#v changes=%v", updated, changed)
	}
}

func TestHistoryDiffExplainAndBudget(t *testing.T) {
	root := t.TempDir()
	id := testIdentity("aaa")
	first, _ := Checkpoint(root, id, State{Confirmed: []Item{{ID: "fact", Text: "old"}}})
	second, _ := Checkpoint(root, id, State{Confirmed: []Item{{ID: "fact", Text: "new"}}, Failing: []Item{{ID: "test", Text: "go test fails"}}})
	h, err := History(root, id)
	if err != nil || len(h) != 2 || h[0].ID != second.ID {
		t.Fatalf("history=%#v %v", h, err)
	}
	d := Diff(first, second)
	if len(d) != 2 {
		t.Fatalf("diff=%#v", d)
	}
	item, section, ok := FindItem(second, "test")
	if !ok || section != "failing" || item.Source != "checkpoint://local" {
		t.Fatalf("item=%#v %q %v", item, section, ok)
	}
	if _, err := Resume(root, id, 1); err == nil {
		t.Fatal("impossibly small budget was silently exceeded")
	}
	long := State{Objective: "bounded", Unknown: []Item{{ID: "detail", Text: strings.Repeat("x", 2000)}}}
	if _, err := Checkpoint(root, id, long); err != nil {
		t.Fatal(err)
	}
	r, err := Resume(root, id, 200)
	if err != nil || !r.Truncated || len(r.Snapshot.State.Unknown) != 0 {
		t.Fatalf("bounded=%#v %v", r, err)
	}
	b, _ := json.Marshal(r)
	if len(b) > 800 {
		t.Fatalf("bounded packet is %d bytes", len(b))
	}
}

func TestResolveIdentityUsesRepositoryNotAgent(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		p := append([]string{"-C", repo}, args...)
		if out, err := execGit(p...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "a"), []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "a")
	run("commit", "-m", "initial")
	a, err := ResolveIdentity(repo, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ResolveIdentity(repo, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	if a.WorkspaceID == "" || a.WorkspaceID != b.WorkspaceID || a.GitHead == "" {
		t.Fatalf("identities %#v %#v", a, b)
	}
	if err := os.WriteFile(filepath.Join(repo, "a"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	dirty, err := ResolveIdentity(repo, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	if dirty.WorktreeState == a.WorktreeState {
		t.Fatal("worktree edits did not change the freshness marker")
	}
}

func TestCheckpointRejectsAmbiguousStructuredState(t *testing.T) {
	id := testIdentity("aaa")
	_, err := Checkpoint(t.TempDir(), id, State{Confirmed: []Item{{ID: "same", Text: "one"}}, Decisions: []Item{{ID: "same", Text: "two"}}})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate ids: %v", err)
	}
	_, err = Checkpoint(t.TempDir(), id, State{Gaps: []Gap{{Subject: "missing authority"}}})
	if err == nil || !strings.Contains(err.Error(), "severity") {
		t.Fatalf("incomplete gap: %v", err)
	}
}

func TestConcurrentCheckpointsHaveUniqueVersions(t *testing.T) {
	root := t.TempDir()
	id := testIdentity("aaa")
	const count = 8
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := Checkpoint(root, id, State{Objective: "shared"}); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	h, err := History(root, id)
	if err != nil || len(h) != count {
		t.Fatalf("history=%d %v", len(h), err)
	}
	seen := map[uint64]bool{}
	for _, s := range h {
		if seen[s.Freshness.SnapshotVersion] {
			t.Fatalf("duplicate version %d", s.Freshness.SnapshotVersion)
		}
		seen[s.Freshness.SnapshotVersion] = true
	}
}

func execGit(args ...string) (string, error) {
	p := exec.Command("git", args...)
	b, e := p.CombinedOutput()
	return string(b), e
}
