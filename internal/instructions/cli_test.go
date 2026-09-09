package instructions

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCLI(t *testing.T) {
	d := isolated(t)
	p := filepath.Join(d, "deja", "instructions.json")
	got, e := DefaultPath()
	if e != nil || got != p {
		t.Fatal(got, e)
	}
	var out bytes.Buffer
	if e = Run([]string{"example"}, nil, &out); e != nil {
		t.Fatal(e)
	}
	draft := filepath.Join(d, "draft.json")
	write(t, draft, out.Bytes())
	out.Reset()
	if e = Run([]string{"apply", "--file", draft, "--expect", "0", "--approve"}, nil, &out); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{{"export"}, {"resolve", "--repo", d}, {"resolve", "--repo", d, "--json"}, {"resolve"}, {"hook", "--agent", "codex"}, {"help"}, {}, {"resolve", "--help"}} {
		out.Reset()
		input := bytes.NewReader(encoded(t, HookInput{Event: "SessionStart", CWD: d}))
		if e = Run(args, input, &out); e != nil || out.Len() == 0 {
			t.Fatalf("%v: %s %v", args, out.String(), e)
		}
	}
	for _, args := range [][]string{{"unknown"}, {"export", "extra"}, {"export", "--unknown"}, {"apply"}, {"apply", "--file", draft, "--expect", "bad", "--approve"}, {"apply", "--file", draft + "missing", "--expect", "1", "--approve"}, {"resolve", "--repo", "relative"}, {"resolve", "--repo", d, "--budget", "1"}, {"export", "--store", "relative"}, {"install", "--agent", "unsupported"}} {
		bad(t, Run(args, nil, io.Discard))
	}
	write(t, draft, []byte("broken"))
	bad(t, Run([]string{"apply", "--file", draft, "--expect", "1", "--approve"}, nil, io.Discard))
	t.Setenv("DEJA_INSTRUCTIONS_FILE", "relative")
	_, e = DefaultPath()
	bad(t, e)
	bad(t, Run([]string{"export"}, nil, io.Discard))
	t.Setenv("DEJA_INSTRUCTIONS_FILE", p)
	if got, e = DefaultPath(); e != nil || got != p {
		t.Fatal(got, e)
	}
	t.Setenv("DEJA_INSTRUCTIONS_FILE", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	if got, e = DefaultPath(); e != nil || got != filepath.Join(d, ".config", "deja", "instructions.json") {
		t.Fatal(got, e)
	}
	bad(t, Run(nil, nil, failingWriter{}))
	bad(t, Run([]string{"install", "--agent", "codex", "--store", filepath.Join(d, "absent.json")}, nil, io.Discard))
	if runtime.GOOS != "windows" {
		if e = Run([]string{"install", "--agent", "codex", "--store", p}, nil, io.Discard); e != nil {
			t.Fatal(e)
		}
	}
}
func TestInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("installer explicitly supports macOS/Linux only")
	}
	d := isolated(t)
	store := filepath.Join(d, "registry.json")
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	for _, agent := range []string{"claude", "codex"} {
		config := filepath.Join(d, agent+".json")
		original := []byte(`{"other":{"keep":true,"large":9007199254740993},"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"deja hook-tool"}]}]}}`)
		write(t, config, original)
		p, e := Install(agent, config, binary, store)
		if e != nil || p != config {
			t.Fatal(p, e)
		}
		first, e := os.ReadFile(config)
		if e != nil {
			t.Fatal(e)
		}
		for _, needle := range []string{"hook-tool", "keep", "9007199254740993"} {
			if !bytes.Contains(first, []byte(needle)) {
				t.Fatalf("clobbered %s", needle)
			}
		}
		if bytes.Contains(first, []byte("additionalContextLimit")) != (agent == "codex") {
			t.Fatal("wrong agent's context-limit field")
		}
		if _, e = Install(agent, config, binary, store); e != nil {
			t.Fatal(e)
		}
		second, e := os.ReadFile(config)
		if e != nil || !bytes.Equal(first, second) {
			t.Fatal("non-idempotent install", e)
		}
		backups, e := filepath.Glob(config + ".deja-instructions-backup-*")
		if e != nil || len(backups) != 1 {
			t.Fatal(backups, e)
		}
		b, e := os.ReadFile(backups[0])
		if e != nil || !bytes.Equal(b, original) {
			t.Fatal("backup changed", e)
		}
		if _, e = Install(agent, "", "", store); e != nil {
			t.Fatal(e)
		}
	}
	for _, tc := range []struct{ agent, config, binary, store string }{{"other", "", binary, store}, {"claude", "relative", binary, store}, {"codex", "", "relative", store}, {"codex", "", binary, "relative"}, {"claude", "", d, store}, {"claude", "", filepath.Join(d, "absent"), store}} {
		_, e = Install(tc.agent, tc.config, tc.binary, tc.store)
		bad(t, e)
	}
	for _, invalid := range []string{`not json`, `null`, `{"hooks":null}`, `{"hooks":{"SessionStart":1}}`, `{"hooks":{"SessionStart":[1]}}`, `{"hooks":{"SessionStart":[{}]}}`, `{"hooks":{"SessionStart":[{"hooks":[1]}]}}`} {
		config := filepath.Join(d, "broken.json")
		write(t, config, []byte(invalid))
		_, e = Install("claude", config, binary, store)
		bad(t, e)
		b, err := os.ReadFile(config)
		if err != nil || string(b) != invalid {
			t.Fatal("bad config overwritten", err)
		}
	}
	config := filepath.Join(d, "lock.json")
	if e = os.Mkdir(config+".deja-instructions.lock", 0700); e != nil {
		t.Fatal(e)
	}
	_, e = Install("claude", config, binary, store)
	bad(t, e)
	if quoted := shellQuote("a' b;$HOME"); quoted != "'a'\"'\"' b;$HOME'" {
		t.Fatal(quoted)
	}
}
