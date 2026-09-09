package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vshulcz/deja-vu/internal/instructions"
)

func TestAutomaticHooksKeepInstructionStoreInEitherInstallOrder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("instruction hook installer supports macOS/Linux only")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, agent string
		path        func(string) string
		install     func(string, bool) (installResult, error)
	}{
		{"claude", "claude", func(home string) string { return filepath.Join(home, ".claude", "settings.json") }, installClaudeHook},
		{"codex", "codex", func(home string) string { return filepath.Join(home, ".codex", "hooks.json") }, installCodexHooks},
	} {
		for _, instructionsFirst := range []bool{false, true} {
			order := "auto-then-instructions"
			if instructionsFirst {
				order = "instructions-then-auto"
			}
			t.Run(tc.name+"/"+order, func(t *testing.T) {
				home := sharedHookTestHome(t)
				path := tc.path(home)
				store := filepath.Join(home, "approved registry's copy.json")
				installAuto := func() {
					if _, err := tc.install(exe, false); err != nil {
						t.Fatalf("automatic install: %v", err)
					}
				}
				installInstructions := func() {
					if _, err := instructions.Install(tc.agent, path, exe, store); err != nil {
						t.Fatalf("instruction install: %v", err)
					}
				}
				if instructionsFirst {
					installInstructions()
					installAuto()
				} else {
					installAuto()
					installInstructions()
				}
				// A later ordinary `deja install <agent>-auto` must adopt its old
				// command instead of dropping the explicit opt-in registry.
				installAuto()
				root := readSharedHookConfig(t, path)
				for event, sub := range map[string]string{
					"SessionStart": "hook-context", "UserPromptSubmit": "hook-prompt", "PreToolUse": "hook-tool",
				} {
					handlers := matchingSharedHandlers(root, event, sub)
					if len(handlers) != 1 {
						t.Fatalf("%s %s handlers = %#v", tc.name, event, handlers)
					}
					if _, gotStore, gotAgent, ok := instructions.StripInstructionSuffix(handlers[0]["command"].(string)); !ok || gotStore != store || gotAgent != tc.agent {
						t.Fatalf("%s %s lost the instruction store: %#v", tc.name, event, handlers[0])
					}
				}
				if subagent := matchingSharedHandlers(root, "SubagentStart", "instructions hook"); len(subagent) != 1 {
					t.Fatalf("%s SubagentStart handlers = %#v", tc.name, subagent)
				}
				if tc.agent == "codex" {
					for _, handler := range matchingSharedHandlers(root, "SessionStart", "hook-context") {
						if limit, ok := handler["additionalContextLimit"].(float64); !ok || limit != 0 {
							t.Fatalf("codex instruction context limit = %#v", handler["additionalContextLimit"])
						}
					}
				}
			})
		}
	}
}

func TestInstructionInstallSplitsThirdPartyPreToolMatcher(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("instruction hook installer supports macOS/Linux only")
	}
	home := sharedHookTestHome(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"deja hook-tool"},{"type":"command","command":"third-party"}]}]}}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := instructions.Install("claude", path, exe, filepath.Join(home, "approved.json")); err != nil {
		t.Fatal(err)
	}
	root := readSharedHookConfig(t, path)
	pre := root["hooks"].(map[string]any)["PreToolUse"].([]any)
	var foreignMatcher, unfiltered bool
	for _, groupAny := range pre {
		group := groupAny.(map[string]any)
		commands := groupHookCommands(group)
		if strings.Contains(strings.Join(commands, "\n"), "third-party") && group["matcher"] == "Bash" {
			foreignMatcher = true
		}
		if strings.Contains(strings.Join(commands, "\n"), "hook-tool") {
			_, hasMatcher := group["matcher"]
			unfiltered = !hasMatcher
		}
	}
	if !foreignMatcher || !unfiltered {
		t.Fatalf("PreToolUse did not preserve foreign matcher and widen ours: %#v", pre)
	}
}

func TestAutomaticPreToolMatcherWidensOnlyForIntegratedInstructions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("instruction hook installer supports macOS/Linux only")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, agent, wantMatcher string
		path                     func(string) string
		install                  func(string, bool) (installResult, error)
	}{
		{"claude", "claude", "Bash|Edit|Write|MultiEdit|NotebookEdit|Task|Agent", func(home string) string { return filepath.Join(home, ".claude", "settings.json") }, installClaudeHook},
		{"codex", "codex", "Bash|apply_patch", func(home string) string { return filepath.Join(home, ".codex", "hooks.json") }, installCodexHooks},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := sharedHookTestHome(t)
			path := tc.path(home)
			if _, err := tc.install(exe, false); err != nil {
				t.Fatal(err)
			}
			if matcher := preToolMatcherFor(t, readSharedHookConfig(t, path), false); matcher != tc.wantMatcher {
				t.Fatalf("default PreToolUse matcher = %q, want %q", matcher, tc.wantMatcher)
			}
			if _, err := instructions.Install(tc.agent, path, exe, filepath.Join(home, "approved.json")); err != nil {
				t.Fatal(err)
			}
			if _, err := tc.install(exe, false); err != nil {
				t.Fatal(err)
			}
			if matcher := preToolMatcherFor(t, readSharedHookConfig(t, path), true); matcher != "" {
				t.Fatalf("integrated PreToolUse matcher = %q, want unfiltered", matcher)
			}
		})
	}
}

func sharedHookTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("DEJA_CLAUDE_ROOT", "")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("DEJA_CODEX_ROOT", filepath.Join(home, ".codex"))
	return home
}

func readSharedHookConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func matchingSharedHandlers(root map[string]any, event, contains string) []map[string]any {
	var out []map[string]any
	hooks, _ := root["hooks"].(map[string]any)
	for _, groupAny := range hooks[event].([]any) {
		for _, handlerAny := range groupAny.(map[string]any)["hooks"].([]any) {
			handler, _ := handlerAny.(map[string]any)
			command, _ := handler["command"].(string)
			if strings.Contains(command, contains) {
				out = append(out, handler)
			}
		}
	}
	return out
}

func groupHookCommands(group map[string]any) []string {
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

func preToolMatcherFor(t *testing.T, root map[string]any, integrated bool) string {
	t.Helper()
	hooks := root["hooks"].(map[string]any)
	for _, groupAny := range hooks["PreToolUse"].([]any) {
		group := groupAny.(map[string]any)
		for _, command := range groupHookCommands(group) {
			if !strings.Contains(command, "hook-tool") {
				continue
			}
			_, _, _, hasSuffix := instructions.StripInstructionSuffix(command)
			if hasSuffix == integrated {
				matcher, _ := group["matcher"].(string)
				return matcher
			}
		}
	}
	t.Fatalf("no PreToolUse hook with integrated=%t", integrated)
	return ""
}
