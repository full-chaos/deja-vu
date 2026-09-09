package instructions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstructionSuffixRoundTripsQuotedStore(t *testing.T) {
	for _, name := range []string{
		"Deja's approved registry.json",
		"approved --instructions-store registry's copy.json",
	} {
		store := filepath.Join(t.TempDir(), name)
		cmd, err := WithInstructionSuffix("deja hook-context", store, "claude")
		if err != nil {
			t.Fatal(err)
		}
		base, gotStore, agent, ok := StripInstructionSuffix(cmd)
		if !ok || base != "deja hook-context" || gotStore != store || agent != "claude" {
			t.Fatalf("StripInstructionSuffix(%q) = %q, %q, %q, %v", cmd, base, gotStore, agent, ok)
		}
	}
	for _, malformed := range []string{
		"deja hook-context --instructions-store /tmp/plain --instructions-agent claude",
		"deja hook-context --instructions-store '/tmp/a' --instructions-agent claude extra",
		"deja hook-context --instructions-store '/tmp/a' --instructions-agent other",
	} {
		if _, _, _, ok := StripInstructionSuffix(malformed); ok {
			t.Fatalf("accepted non-canonical suffix %q", malformed)
		}
	}
}

func TestSharedHookKindRecognizesBareDejaAndLauncherOnly(t *testing.T) {
	for _, command := range []string{
		"deja hook-context",
		"/home/me/.config/deja/bin/deja-hook hook-context",
		"'/tmp/Deja App/deja-hook' hook-context",
		"'/tmp/the exact binary' hook-context",
	} {
		binary := "/tmp/the exact binary"
		kind, _ := sharedHookKindOf(command, binary, "hook-context")
		if kind != sharedHookOwned {
			t.Fatalf("%q kind = %v, want owned", command, kind)
		}
	}
	kind, _ := sharedHookKindOf("sh -c 'deja hook-context'", "/tmp/deja", "hook-context")
	if kind != sharedHookWrapper {
		t.Fatalf("wrapper kind = %v, want wrapper", kind)
	}
}

func TestInstallSharesExistingHooksAndMigratesStandaloneEntries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("installer explicitly supports macOS/Linux only")
	}
	d := isolated(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(d, "approved registry's copy.json")
	config := filepath.Join(d, "settings.json")
	// This is the exact line a prior install wrote from an older binary. A
	// current install must remove it even though its executable differs.
	old := shellQuote("/opt/old/deja") + " instructions hook --agent " + shellQuote("claude") + " --store " + shellQuote(store)
	seed := map[string]any{"hooks": map[string]any{
		"SessionStart": []any{map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": "deja hook-context"},
			map[string]any{"type": "command", "command": old, "statusMessage": hookMarker},
		}}},
		"UserPromptSubmit": []any{map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": "deja hook-prompt"},
		}}},
		"PreToolUse": []any{map[string]any{"matcher": "Bash|Write", "hooks": []any{
			map[string]any{"type": "command", "command": "deja hook-tool"},
			map[string]any{"type": "command", "command": "theirs"},
		}}},
	}}
	seedJSON, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	write(t, config, seedJSON)
	if _, err := Install("claude", config, binary, store); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	hooks := root["hooks"].(map[string]any)
	for event, sub := range map[string]string{
		"SessionStart": "hook-context", "UserPromptSubmit": "hook-prompt", "PreToolUse": "hook-tool",
	} {
		cmds := eventCommands(t, hooks, event)
		var integrated []string
		for _, command := range cmds {
			if strings.Contains(command, sub) {
				integrated = append(integrated, command)
			}
		}
		if len(integrated) != 1 {
			t.Fatalf("%s commands = %#v", event, cmds)
		}
		if _, gotStore, gotAgent, ok := StripInstructionSuffix(integrated[0]); !ok || gotStore != store || gotAgent != "claude" {
			t.Fatalf("%s lost canonical suffix: %q", event, integrated[0])
		}
		if !strings.HasPrefix(integrated[0], shellQuote(binary)+" "+sub) {
			t.Fatalf("%s kept the old executable instead of requested binary: %q", event, integrated[0])
		}
	}
	if strings.Contains(string(data), old) {
		t.Fatalf("old standalone command survived:\n%s", data)
	}
	subagent := eventCommands(t, hooks, "SubagentStart")
	if len(subagent) != 1 || !strings.Contains(subagent[0], " instructions hook --agent ") {
		t.Fatalf("SubagentStart commands = %#v", subagent)
	}
	pre := hooks["PreToolUse"].([]any)
	if len(pre) != 2 {
		t.Fatalf("PreToolUse groups = %#v", pre)
	}
	var unfiltered bool
	for _, groupAny := range pre {
		group := groupAny.(map[string]any)
		cmds := groupCommands(group)
		if len(cmds) == 1 && strings.Contains(cmds[0], "hook-tool") {
			_, hasMatcher := group["matcher"]
			unfiltered = !hasMatcher
		}
	}
	if !unfiltered || !strings.Contains(string(data), `"command": "theirs"`) {
		t.Fatalf("PreToolUse did not split the foreign matcher group:\n%s", data)
	}
	first := string(data)
	if _, err := Install("claude", config, binary, store); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(config)
	if err != nil || string(second) != first {
		t.Fatalf("non-idempotent integration: %v\n%s", err, second)
	}
}

func TestInstallPreservesStandaloneInstructionWrapper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("installer explicitly supports macOS/Linux only")
	}
	d := isolated(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(d, "approved.json")
	old := shellQuote("/opt/old/deja") + " instructions hook --agent " + shellQuote("claude") + " --store " + shellQuote(store)
	wrapped := old + "; reader-command"
	if markedStandaloneInstructionHook(wrapped, binary) {
		t.Fatal("a wrapped old command was claimed as installer-owned")
	}
	config := filepath.Join(d, "settings.json")
	seed := map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{
		map[string]any{"type": "command", "command": wrapped, "statusMessage": hookMarker},
	}}}}}
	b, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	write(t, config, b)
	if _, err := Install("claude", config, binary, store); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(config)
	if err != nil || !strings.Contains(string(got), wrapped) {
		t.Fatalf("wrapped reader command was changed: %v\n%s", err, got)
	}
}

func TestInstallRejectsWrappedSharedHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("installer explicitly supports macOS/Linux only")
	}
	d := isolated(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(d, "settings.json")
	original := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"sh -c 'deja hook-context'"}]}]}}`
	write(t, config, []byte(original))
	if _, err := Install("claude", config, binary, filepath.Join(d, "approved.json")); err == nil {
		t.Fatal("wrapped hook was silently duplicated")
	}
	got, err := os.ReadFile(config)
	if err != nil || string(got) != original {
		t.Fatalf("wrapper was changed: %v\n%s", err, got)
	}
}

func eventCommands(t *testing.T, hooks map[string]any, event string) []string {
	t.Helper()
	var out []string
	for _, groupAny := range hooks[event].([]any) {
		out = append(out, groupCommands(groupAny.(map[string]any))...)
	}
	return out
}

func groupCommands(group map[string]any) []string {
	var out []string
	for _, handlerAny := range group["hooks"].([]any) {
		if handler, _ := handlerAny.(map[string]any); handler != nil {
			if command, _ := handler["command"].(string); command != "" {
				out = append(out, command)
			}
		}
	}
	return out
}

func TestBareInstructionHookRejectsShellExpansionsAndLines(t *testing.T) {
	for _, prefix := range []string{"deja*", "deja\nother", "deja\r", "~/deja", "deja\\-old", "(deja)", "NAME=value", "'deja' 'extra'"} {
		if IsBareHookCommand(prefix+" hook-context", "hook-context") {
			t.Fatalf("accepted shell syntax as owned executable: %q", prefix)
		}
	}
}
