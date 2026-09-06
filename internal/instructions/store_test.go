package instructions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestStoreCASAndHistory(t *testing.T) {
	d := isolated(t)
	store := FileStore{Path: filepath.Join(d, "registry.json")}
	s := snapshot(rule("a"))
	saved, e := store.Replace(0, s)
	if e != nil || saved.Revision != 1 {
		t.Fatal(saved, e)
	}
	got, e := store.Load()
	if e != nil || !reflect.DeepEqual(got, saved) {
		t.Fatal(got, e)
	}
	if _, e = store.Replace(0, s); !errors.Is(e, ErrConflict) {
		t.Fatalf("lost update allowed: %v", e)
	}
	saved.Rules[0].Text = "Different approved text."
	_, e = store.Replace(1, saved)
	bad(t, e)
	saved.Rules[0].Revision = 2
	got, e = store.Replace(1, saved)
	if e != nil || got.Revision != 2 {
		t.Fatal(got, e)
	}
	archived, e := os.ReadFile(filepath.Join(store.Path+".revisions", "1.json"))
	if e != nil || !bytes.Contains(archived, []byte("Preserve unrelated")) {
		t.Fatal("prior approval lost", e)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Snapshot)
	}{{"delete", func(s *Snapshot) { s.Rules = nil }}, {"new revision", func(s *Snapshot) { r := rule("new"); r.Revision = 3; s.Rules = append(s.Rules, r) }}, {"input revision", func(s *Snapshot) { s.Revision = 0 }}, {"invalid", func(s *Snapshot) { s.Version = 99 }}} {
		t.Run(tc.name, func(t *testing.T) {
			s, e := store.Load()
			if e != nil {
				t.Fatal(e)
			}
			tc.mutate(&s)
			_, e = store.Replace(2, s)
			bad(t, e)
		})
	}
	got, e = store.Load()
	if e != nil {
		t.Fatal(e)
	}
	got.Rules[0].Revision++
	got.Rules[0].Status = "revoked"
	if _, e = store.Replace(2, got); e != nil {
		t.Fatal(e)
	}
	got, e = store.Load()
	if e != nil {
		t.Fatal(e)
	}
	got.Exceptions = []Exception{{ID: "grant", RuleID: "a", RuleRevision: 3, Task: "T", ExpiresAt: instant.Add(time.Hour), ApprovedBy: "owner", Reason: "test"}}
	got, e = store.Replace(3, got)
	if e != nil {
		t.Fatal(e)
	}
	got.Exceptions[0].Task = "other"
	_, e = store.Replace(4, got)
	bad(t, e)
}
func TestStoreFailures(t *testing.T) {
	d := isolated(t)
	p := filepath.Join(d, "registry.json")
	store := FileStore{Path: p}
	_, e := (FileStore{Path: "relative"}).Load()
	bad(t, e)
	_, e = (FileStore{Path: "relative"}).Replace(0, snapshot())
	bad(t, e)
	if _, e = store.Load(); !errors.Is(e, os.ErrNotExist) {
		t.Fatal(e)
	}
	write(t, p, []byte("broken"))
	_, e = store.Load()
	bad(t, e)
	_, e = store.Replace(0, snapshot())
	bad(t, e)
	if e = os.Remove(p); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(p, 0700); e != nil {
		t.Fatal(e)
	}
	_, e = store.Load()
	bad(t, e)
	if e = os.Remove(p); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(p+".lock", 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = store.Replace(0, snapshot()); !errors.Is(e, ErrBusy) {
		t.Fatal(e)
	}
	if e = os.Remove(p + ".lock"); e != nil {
		t.Fatal(e)
	}
	write(t, p, bytes.Repeat([]byte("x"), MaxRegistryBytes+1))
	_, e = store.Load()
	bad(t, e)
	if e = os.Remove(p); e != nil {
		t.Fatal(e)
	}
	bad(t, atomicWrite(filepath.Join(d, "missing", "target"), []byte("x")))
	dir := filepath.Join(d, "directory")
	if e = os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	bad(t, atomicWrite(dir, []byte("x")))
	_, e = readBounded(dir, 100)
	bad(t, e)
	write(t, p+".lock", []byte("locked"))
	if _, e = store.Replace(0, snapshot()); !errors.Is(e, ErrBusy) {
		t.Fatal(e)
	}
	if e = os.Remove(p + ".lock"); e != nil {
		t.Fatal(e)
	}
	s := snapshot()
	s.Revision = ^uint64(0)
	_, e = store.Replace(s.Revision, s)
	bad(t, e)
	if runtime.GOOS != "windows" {
		target := filepath.Join(d, "real")
		write(t, target, encoded(t, snapshot()))
		if e = os.Symlink(target, p); e != nil {
			t.Fatal(e)
		}
		_, e = store.Load()
		bad(t, e)
		if e = os.Remove(p); e != nil {
			t.Fatal(e)
		}
	}
	if e = os.MkdirAll(p+".revisions", 0700); e != nil {
		t.Fatal(e)
	}
	next := snapshot()
	next.Revision = 1
	b, e := json.MarshalIndent(next, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	write(t, filepath.Join(p+".revisions", "1.json"), append(b, '\n'))
	_, e = store.Replace(0, snapshot(rule("different")))
	bad(t, e)
	if _, e = store.Replace(0, snapshot()); e != nil {
		t.Fatal("interrupted same-draft retry failed", e)
	}
}
func TestConcurrentWriters(t *testing.T) {
	d := isolated(t)
	store := FileStore{Path: filepath.Join(d, "registry.json")}
	if _, e := store.Replace(0, snapshot()); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := snapshot(rule(fmt.Sprint("r", i)))
			s.Revision = 1
			_, e := store.Replace(1, s)
			results <- e
		}(i)
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else if !errors.Is(e, ErrBusy) && !errors.Is(e, ErrConflict) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatalf("expected one writer, got %d", success)
	}
}
func TestProcessWriter(t *testing.T) {
	if p := os.Getenv("DEJA_INSTRUCTION_TEST_WRITER"); p != "" {
		s := snapshot(rule("child"))
		s.Revision = 1
		_, e := (FileStore{Path: p}).Replace(1, s)
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(3)
		}
		os.Exit(0)
	}
	d := isolated(t)
	p := filepath.Join(d, "registry.json")
	if _, e := (FileStore{Path: p}).Replace(0, snapshot()); e != nil {
		t.Fatal(e)
	}
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	var commands []*exec.Cmd
	for i := 0; i < 4; i++ {
		cmd := exec.Command(binary, "-test.run=^TestProcessWriter$")
		cmd.Env = append(os.Environ(), "DEJA_INSTRUCTION_TEST_WRITER="+p)
		if e = cmd.Start(); e != nil {
			t.Fatal(e)
		}
		commands = append(commands, cmd)
	}
	success := 0
	for _, cmd := range commands {
		e = cmd.Wait()
		if e == nil {
			success++
		} else {
			var exit *exec.ExitError
			if !errors.As(e, &exit) || exit.ExitCode() != 3 {
				t.Fatal(e)
			}
		}
	}
	if success != 1 {
		t.Fatalf("%d process writes succeeded, want one", success)
	}
}
