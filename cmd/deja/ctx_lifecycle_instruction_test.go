package main

import (
	"os"
	"strings"
	"testing"
)

// The lifecycle promise only works when an agent actually receives it. Exercise
// the rendered MCP initialize field and files that install/warmup write, rather
// than testing the generator constants that could drift from those surfaces.
func TestRenderedAgentInstructionsPutCtxResumeBeforeRecall(t *testing.T) {
	modes := advertisedCtxModes(t)
	initialized, code, message := handleMCP(t.TempDir(), rpcRequest{Method: "initialize"})
	if code != 0 {
		t.Fatalf("initialize: %d %s", code, message)
	}
	result, ok := initialized.(map[string]any)
	if !ok {
		t.Fatalf("initialize result = %T", initialized)
	}
	handshake, _ := result["instructions"].(string)
	assertMCPResumeContract(t, modes, "MCP initialize", handshake)

	hermeticEnv(t)
	if err := writeCLISkill(); err != nil {
		t.Fatal(err)
	}
	cli, err := os.ReadFile(cliSkillPath())
	if err != nil {
		t.Fatal(err)
	}
	assertCLIResumeContract(t, cliSkillPath(), string(cli))

	seen := map[string]bool{}
	for _, target := range installTargetNames() {
		harness := guidanceHarness(target)
		path := guidancePath(harness)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if _, err := guidanceResult(target, false); err != nil {
			t.Fatalf("install guidance for %s: %v", target, err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read installed guidance for %s at %s: %v", target, path, err)
		}
		assertMCPResumeContract(t, modes, path, string(body))
	}
}

// Marketplace/plugin copies bypass the installer, so assert the actual shipped
// files carry the same lifecycle rather than relying only on their equality
// check against guidanceText.
func TestBundledAgentSkillsCarryTheCtxLifecycle(t *testing.T) {
	modes := advertisedCtxModes(t)
	for _, rel := range []string{
		"skills/deja-search/SKILL.md",
		"claude-plugin/skills/deja-history/SKILL.md",
		"codex-plugin/skills/deja-history/SKILL.md",
		"extensions/grok/skills/deja-history/SKILL.md",
		"extensions/kimi/skills/deja-history/SKILL.md",
	} {
		body := string(repoFile(t, rel))
		if strings.Contains(rel, "deja-search") {
			assertCLIResumeContract(t, rel, body)
			continue
		}
		assertMCPResumeContract(t, modes, rel, body)
	}
}

// advertisedCtxModes reads the server's real tools/list schema. The lifecycle
// text must name the only listed tool and modes rather than a backend alias
// that remains callable only for compatibility.
func advertisedCtxModes(t *testing.T) map[string]bool {
	t.Helper()
	response, code, message := handleMCP(t.TempDir(), rpcRequest{Method: "tools/list"})
	if code != 0 {
		t.Fatalf("tools/list: %d %s", code, message)
	}
	payload, ok := response.(map[string]any)
	if !ok {
		t.Fatalf("tools/list payload = %T", response)
	}
	tools, ok := payload["tools"].([]map[string]any)
	if !ok {
		t.Fatalf("tools/list tools = %T", payload["tools"])
	}
	for _, tool := range tools {
		if tool["name"] != "deja" {
			continue
		}
		schema, _ := tool["inputSchema"].(map[string]any)
		properties, _ := schema["properties"].(map[string]any)
		mode, _ := properties["mode"].(map[string]any)
		values, _ := mode["enum"].([]string)
		out := make(map[string]bool, len(values))
		for _, value := range values {
			out[value] = true
		}
		return out
	}
	t.Fatal("tools/list does not advertise the deja tool")
	return nil
}

func assertMCPResumeContract(t *testing.T, modes map[string]bool, surface, body string) {
	t.Helper()
	for _, mode := range []string{"ctx_resume", "ctx_checkpoint"} {
		if !modes[mode] {
			t.Fatalf("tools/list does not advertise required lifecycle mode %q: %v", mode, modes)
		}
	}
	for _, want := range []string{
		"listed `deja` tool with `mode: \"ctx_resume\"`", "absolute", "required", "before independently reconstructing",
		"required gap", "stale or invalid", "source-of-truth verification",
		"listed `deja` tool with `mode: \"ctx_checkpoint\"`", "confirmed findings", "test results", "private reasoning",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("%s omits lifecycle contract term %q", surface, want)
		}
	}
	if recall := strings.Index(body, "recall"); recall >= 0 && strings.Index(body, `mode: "ctx_resume"`) > recall {
		t.Errorf("%s puts recall before ctx resume", surface)
	}
}

func assertCLIResumeContract(t *testing.T, surface, body string) {
	t.Helper()
	for _, want := range []string{
		"deja ctx resume", "--task <active-task-id>", "absolute", "required",
		"before independently reconstructing", "required gap", "stale or invalid",
		"source-of-truth verification", "deja ctx checkpoint", "confirmed findings",
		"tests", "private reasoning",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("%s omits lifecycle contract term %q", surface, want)
		}
	}
	if recall := strings.Index(body, "recall"); recall >= 0 && strings.Index(body, "deja ctx resume") > recall {
		t.Errorf("%s puts recall before ctx resume", surface)
	}
}

func TestCtxLifecycleDocsMatchThePublicContracts(t *testing.T) {
	for _, rel := range []string{"README.md", "docs/ctx-cache.md", "docs/guide/agents.html"} {
		body := string(repoFile(t, rel))
		for _, want := range []string{"deja", "ctx_resume", "ctx_checkpoint", "absolute", "required", "private reasoning"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s omits public contract term %q", rel, want)
			}
		}
	}
	ctxDocs := string(repoFile(t, "docs/ctx-cache.md"))
	for _, want := range []string{
		"DEJA_INSTRUCTIONS_FILE", "instructions.json", "ctx_history", "ctx_promote",
		"conservative rendered-byte bound", "irreducible packet", "--source ALIAS",
	} {
		if !strings.Contains(ctxDocs, want) {
			t.Errorf("docs/ctx-cache.md omits %q", want)
		}
	}
}
