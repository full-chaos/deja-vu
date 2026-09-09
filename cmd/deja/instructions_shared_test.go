package main

import (
	"encoding/json"
	"fmt"
	"github.com/vshulcz/deja-vu/internal/index"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vshulcz/deja-vu/internal/instructions"
)

func sharedInstructionFixture(t *testing.T) (string, string) {
	t.Helper()
	root := hermeticEnv(t)
	t.Setenv("DEJA_TASK_ID", "")
	t.Setenv("DEJA_ENVIRONMENT", "")
	t.Setenv("DEJA_RECALL", "off")
	// A missing history index must not hide independently approved instructions.
	store := filepath.Join(root, "approved.json")
	_, err := (instructions.FileStore{Path: store}).Replace(0, instructions.Snapshot{Version: instructions.Version, Rules: []instructions.Rule{{ID: "shared.required", Revision: 1, Kind: "constraint", Text: "Preserve the approved synthetic fixture.", Authority: "owner", Status: "active", Strength: "must", Lifetime: "persistent", Source: "synthetic test approval"}}})
	if err != nil {
		t.Fatal(err)
	}
	return root, store
}

func TestSharedInstructionsReachExistingHookCommands(t *testing.T) {
	for command, event := range map[string]string{"hook-context": "SessionStart", "hook-prompt": "UserPromptSubmit", "hook-tool": "PreToolUse"} {
		t.Run(command, func(t *testing.T) {
			root, store := sharedInstructionFixture(t)
			payload, err := json.Marshal(map[string]any{"hook_event_name": event, "cwd": root, "session_id": "shared-session", "prompt": "ok", "tool_name": "Read", "tool_input": map[string]string{"file_path": "new.go"}})
			if err != nil {
				t.Fatal(err)
			}
			withHookStdin(t, string(payload))
			out, err := captureRun(t, command, "--instructions-store", store, "--instructions-agent", "claude")
			if err != nil {
				t.Fatal(err)
			}
			var response sessionStartHookResponse
			if err := json.Unmarshal([]byte(out), &response); err != nil {
				t.Fatalf("one JSON response required: %v: %s", err, out)
			}
			if response.HookSpecificOutput.HookEventName != event || strings.Count(response.HookSpecificOutput.AdditionalContext, "shared.required@1") != 1 {
				t.Fatalf("missing or repeated instructions: %s", out)
			}
			if _, err := os.Stat(store); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSharedInstructionContextPreservesRecallAndToolUpdates(t *testing.T) {
	recall := []byte(`{"systemMessage":"recall receipt","hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":"ranked memory","updatedInput":{"prompt":"original child task plus recall","large":9007199254740993},"permissionDecision":"allow"}}`)
	approved := []byte(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":"approved required rule"}}`)
	var out strings.Builder
	if err := emitSharedInstructionContext("PreToolUse", recall, approved, &out, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "9007199254740993") || !strings.Contains(out.String(), "original child task plus recall") || !strings.Contains(out.String(), `"permissionDecision":"allow"`) {
		t.Fatalf("changed the existing hook's tool update: %s", out.String())
	}
	var response sessionStartHookResponse
	if err := json.Unmarshal([]byte(out.String()), &response); err != nil {
		t.Fatal(err)
	}
	if response.HookSpecificOutput.AdditionalContext != "approved required rule\nranked memory" || response.SystemMessage != "recall receipt" {
		t.Fatal(out.String())
	}
}

func TestSharedInstructionContextBudgetKeepsRequiredRulesWhole(t *testing.T) {
	required := strings.Repeat("界", 2500)
	approved, err := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{"additionalContext": required}})
	if err != nil {
		t.Fatal(err)
	}
	recall, err := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{"additionalContext": strings.Repeat("history", 200)}})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := emitSharedInstructionContext("SessionStart", recall, approved, &out, false); err != nil {
		t.Fatal(err)
	}
	var response sessionStartHookResponse
	if err := json.Unmarshal([]byte(out.String()), &response); err != nil {
		t.Fatal(err)
	}
	if response.HookSpecificOutput.AdditionalContext != required || len(response.HookSpecificOutput.AdditionalContext) > instructions.DefaultBudget || !strings.Contains(response.SystemMessage, "omitted recalled history") {
		t.Fatal("required rules truncated or budget exceeded", out.String())
	}
}

func TestSharedInstructionsFailOpenAndRemainOptIn(t *testing.T) {
	root, store := sharedInstructionFixture(t)
	var out strings.Builder
	handled, err := runSharedInstructionHook("unused", "PreToolUse", nil, strings.NewReader("not json"), &out)
	if handled || err != nil || out.Len() != 0 {
		t.Fatal("ordinary hook opted in", handled, err, out.String())
	}
	for _, payload := range []string{"not json", `{"hook_event_name":"PreToolUse","cwd":"missing"}`} {
		out.Reset()
		handled, err = runSharedInstructionHook("unused", "PreToolUse", []string{"--instructions-store", store, "--instructions-agent", "codex"}, strings.NewReader(payload), &out)
		if !handled || err != nil || !strings.Contains(out.String(), "INCOMPLETE") {
			t.Fatal(handled, err, out.String())
		}
		if strings.Contains(out.String(), "permissionDecision") {
			t.Fatal("instruction warning made a permission decision", out.String())
		}
	}
	// A missing approved registry must remain visible even when recall is off.
	if err := os.Remove(store); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"hook_event_name": "PreToolUse", "cwd": root})
	out.Reset()
	_, err = runSharedInstructionHook("unused", "PreToolUse", []string{"--instructions-store", store, "--instructions-agent", "claude"}, strings.NewReader(string(payload)), &out)
	if err != nil || !strings.Contains(out.String(), "cannot load approved registry") {
		t.Fatal(err, out.String())
	}
}

func TestSharedInstructionsAndRealRecallArriveTogether(t *testing.T) {
	root, store := sharedInstructionFixture(t)
	t.Setenv("DEJA_RECALL", "")
	// The sessions and command are synthetic; both the real history producer and
	// real instruction resolver must contribute to the same response.
	for _, id := range []string{"shared-a", "shared-b"} {
		cwdJSON, err := json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		writeClaudeFixture(t, filepath.Join(os.Getenv("DEJA_CLAUDE_ROOT"), strings.ReplaceAll(root, string(filepath.Separator), "-"), id+".jsonl"), id, []string{
			fmt.Sprintf(`{"type":"user","sessionId":%q,"cwd":%s,"timestamp":"2026-01-02T03:04:05Z","message":{"role":"user","content":"the suite keeps failing on the shared fixture"}}`, id, cwdJSON),
			fmt.Sprintf(`{"type":"assistant","sessionId":%q,"cwd":%s,"timestamp":"2026-01-02T03:04:06Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test ./... -count=1"}}]}}`, id, cwdJSON),
			fmt.Sprintf(`{"type":"assistant","sessionId":%q,"cwd":%s,"timestamp":"2026-01-02T03:06:00Z","message":{"role":"assistant","content":"the suite has to run with -p 1: the shared fixture cannot take parallel packages."}}`, id, cwdJSON),
		})
	}
	if err := index.Ensure(index.DefaultDir(), "", true, nil); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "cwd": root, "session_id": "both", "tool_name": "Bash", "tool_input": map[string]string{"command": "go test ./... -count=1"}})
	if err != nil {
		t.Fatal(err)
	}

	// Exhaust the shared budget first. This must not mark the history as served:
	// the same session should receive it once a subsequent approval frees space.
	registry := instructions.FileStore{Path: store}
	snapshot, err := registry.Load()
	if err != nil {
		t.Fatal(err)
	}
	original := snapshot.Rules[0].Text
	snapshot.Rules[0].Revision++
	snapshot.Rules[0].Text = strings.Repeat("r", 7700)
	saved, err := registry.Replace(snapshot.Revision, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var omitted strings.Builder
	handled, err := runSharedInstructionHook(index.DefaultDir(), "PreToolUse", []string{"--instructions-store", store, "--instructions-agent", "claude"}, strings.NewReader(string(payload)), &omitted)
	if !handled || err != nil || !strings.Contains(omitted.String(), "omitted recalled history") || strings.Contains(omitted.String(), "2 sessions") {
		t.Fatal(handled, err, omitted.String())
	}
	saved.Rules[0].Revision++
	saved.Rules[0].Text = original
	if _, err := registry.Replace(saved.Revision, saved); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	handled, err = runSharedInstructionHook(index.DefaultDir(), "PreToolUse", []string{"--instructions-store", store, "--instructions-agent", "claude"}, strings.NewReader(string(payload)), &out)
	if !handled || err != nil {
		t.Fatal(handled, err)
	}
	var response sessionStartHookResponse
	if err := json.Unmarshal([]byte(out.String()), &response); err != nil {
		t.Fatal(err)
	}
	context := response.HookSpecificOutput.AdditionalContext
	if !strings.Contains(context, "shared.required@3") || !strings.Contains(context, "2 sessions") {
		t.Fatalf("one producer disappeared: %s", out.String())
	}
}

func TestSharedInstructionBudgetDoesNotSpendOmittedNudge(t *testing.T) {
	root, _ := sharedInstructionFixture(t)
	t.Setenv("DEJA_RECALL", "")
	saved := spawnWarmup
	spawnWarmup = func(_, _ string) error { return nil }
	t.Cleanup(func() { spawnWarmup = saved })
	dir := filepath.Join(root, "index.db")
	payload, err := json.Marshal(map[string]string{"hook_event_name": "UserPromptSubmit", "cwd": root, "session_id": "nudge", "prompt": "ok we rolled back that change"})
	if err != nil {
		t.Fatal(err)
	}
	omitted := sharedRecallOutput{budget: 0}
	if err := runHookPromptMode(dir, strings.NewReader(string(payload)), &omitted, false); err != nil {
		t.Fatal(err)
	}
	if !omitted.omitted {
		t.Fatal("fixture did not try to deliver a nudge")
	}
	if _, err := os.Stat(dir + ".nudge"); !os.IsNotExist(err) {
		t.Fatalf("undelivered nudge consumed timer: %v", err)
	}
	delivered := sharedRecallOutput{budget: instructions.DefaultBudget}
	if err := runHookPromptMode(dir, strings.NewReader(string(payload)), &delivered, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(delivered.String(), "deja remember") {
		t.Fatal("omitted nudge was lost", delivered.String())
	}
	if _, err := os.Stat(dir + ".nudge"); err != nil {
		t.Fatalf("delivered nudge did not consume timer: %v", err)
	}
}
