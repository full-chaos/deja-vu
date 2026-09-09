package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vshulcz/deja-vu/internal/index"
	"github.com/vshulcz/deja-vu/internal/instructions"
)

func TestSharedOnceRetriesSessionStartRecallAfterBudgetReject(t *testing.T) {
	tmp := hermeticEnv(t)
	work := filepath.Join(tmp, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	workJSON, err := json.Marshal(work)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-72 * time.Hour).UTC().Format(time.RFC3339)
	writeClaudeFixture(t, filepath.Join(os.Getenv("DEJA_CLAUDE_ROOT"), encodedProjectDir(work), "prior.jsonl"), "prior", []string{
		`{"type":"user","sessionId":"prior","cwd":` + string(workJSON) + `,"timestamp":"` + at + `","message":{"role":"user","content":"budget-retry-marker records the session-start decision"}}`,
	})
	if err := index.Ensure(index.DefaultDir(), "", true, nil); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"hook_event_name": "SessionStart",
		"session_id":      "once-budget",
		"cwd":             work,
	})
	if err != nil {
		t.Fatal(err)
	}

	blocked := &sharedRecallOutput{budget: 0}
	if err := runHookContextIO(index.DefaultDir(), false, true, strings.NewReader(string(payload)), blocked); err != nil {
		t.Fatal(err)
	}
	if !blocked.omitted || blocked.Len() != 0 {
		t.Fatalf("rejected recall = %#v", blocked)
	}
	if sessionHadDigest(index.DefaultDir(), "once-budget") {
		t.Fatal("an omitted shared recall consumed the once-per-session delivery")
	}

	delivered := &sharedRecallOutput{budget: instructions.DefaultBudget}
	if err := runHookContextIO(index.DefaultDir(), false, true, strings.NewReader(string(payload)), delivered); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(delivered.String(), "budget-retry-marker") {
		t.Fatalf("same session did not retry the admitted recall: %s", delivered.String())
	}
	if !sessionHadDigest(index.DefaultDir(), "once-budget") {
		t.Fatal("an admitted recall did not consume the once-per-session delivery")
	}
}
