package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCtxCacheCLIIsLocalAndPreservesLegacyCtx(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DEJA_CTX_DIR", filepath.Join(root, "ctx"))
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	state := `{"objective":"finish cache","confirmed":[{"id":"fact","text":"resume is local","source":"file://spec"}]}`
	var out strings.Builder
	if err := runCtxCache(filepath.Join(root, "index.db"), []string{"checkpoint", "--workspace", repo, "--task", "T-1", "--state", state}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runCtxCache(filepath.Join(root, "index.db"), []string{"resume", "--workspace", repo, "--task", "T-1", "--budget", "1000"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	var result struct {
		CacheStatus string `json:"cache_status"`
	}
	if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
		t.Fatal(err)
	}
	if result.CacheStatus != "hit" {
		t.Fatalf("resume=%s", out.String())
	}
	if isCtxCacheCommand("pool") {
		t.Fatal("ordinary historical query became a cache subcommand")
	}
}

func TestCtxCacheMCPModesAndAliases(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DEJA_CTX_DIR", filepath.Join(root, "ctx"))
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	checkpoint := `{"workspace":` + quoteJSON(repo) + `,"task_id":"T-1","state":{"objective":"ship","failing":[{"id":"test","text":"fails","source":"file://test"}]}}`
	if got, err := callMCPTool(filepath.Join(root, "index.db"), "deja_ctx_checkpoint", json.RawMessage(checkpoint)); err != nil || !strings.Contains(got, "snapshot_id") {
		t.Fatalf("checkpoint=%q %v", got, err)
	}
	args := json.RawMessage(`{"workspace":` + quoteJSON(repo) + `,"task_id":"T-1"}`)
	for _, name := range []string{"ctx_resume", "deja_ctx_status", "ctx_refresh", "ctx_explain", "ctx_invalidate"} {
		callArgs := args
		if strings.Contains(name, "explain") {
			callArgs = json.RawMessage(`{"workspace":` + quoteJSON(repo) + `,"task_id":"T-1","item_id":"test"}`)
		}
		if _, err := callMCPTool(filepath.Join(root, "index.db"), name, callArgs); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
