package instructions

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var instant = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func rule(id string) Rule {
	return Rule{ID: id, Revision: 1, Kind: "constraint", Text: "Preserve unrelated work.", Authority: "owner", Status: "active", Strength: "must", Lifetime: "persistent", Source: "synthetic owner approval"}
}
func snapshot(rs ...Rule) Snapshot { return Snapshot{Version: Version, Rules: rs} }
func ctx() Context                 { return Context{Now: instant} }
func write(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func encoded(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func isolated(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	for _, k := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME"} {
		t.Setenv(k, d)
	}
	for _, k := range []string{"DEJA_INSTRUCTIONS_FILE", "DEJA_TASK_ID", "DEJA_ENVIRONMENT", "DEJA_INSTRUCTION_TEST_WRITER"} {
		t.Setenv(k, "")
	}
	return d
}
func bad(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected refusal")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("synthetic read failure") }

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("synthetic write failure") }

func TestValidation(t *testing.T) {
	cases := map[string]func(*Rule){
		"id": func(r *Rule) { r.ID = "../bad" }, "revision": func(r *Rule) { r.Revision = 0 }, "text": func(r *Rule) { r.Text = "" }, "control": func(r *Rule) { r.Text = "bad\x1b" }, "long": func(r *Rule) { r.Text = strings.Repeat("x", 32769) }, "source": func(r *Rule) { r.Source = "" },
		"kind": func(r *Rule) { r.Kind = "fact" }, "strength": func(r *Rule) { r.Strength = "maybe" }, "authority": func(r *Rule) { r.Authority = "root" }, "status": func(r *Rule) { r.Status = "expired" }, "inferred": func(r *Rule) { r.Authority = "inferred" },
		"absolute preference": func(r *Rule) { r.Strength = "should"; r.Absolute = true }, "preference must": func(r *Rule) { r.Kind = "preference" }, "priority": func(r *Rule) { r.Priority = 1001 },
		"root": func(r *Rule) { r.Scope.Repository = "relative" }, "path scope": func(r *Rule) { r.Scope.PathPrefix = "src" }, "path escape": func(r *Rule) { r.Scope.Repository = filepath.Clean(os.TempDir()); r.Scope.PathPrefix = "../src" }, "tool": func(r *Rule) { r.Scope.Tools = []string{"Bash*"} }, "scope control": func(r *Rule) { r.Scope.Task = "bad\nvalue" },
		"permanent expiry": func(r *Rule) { r.ExpiresAt = &instant }, "lifetime": func(r *Rule) { r.Lifetime = "forever-ish" }, "task": func(r *Rule) { r.Lifetime = "task" }, "session": func(r *Rule) { r.Lifetime = "session" }, "ttl": func(r *Rule) { r.Lifetime = "ttl" },
		"key": func(r *Rule) { r.ConflictKey = "setting" }, "value": func(r *Rule) { r.Value = "one" }, "invalid key": func(r *Rule) { r.ConflictKey = "../key"; r.Value = "one" }, "procedure": func(r *Rule) { r.Kind = "procedure" }, "bad runbook": func(r *Rule) { r.Runbook = &Runbook{Path: "relative"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) { r := rule("test"); mutate(&r); bad(t, snapshot(r).Validate()) })
	}
	for _, lifetime := range []string{"persistent", "task", "session", "ttl"} {
		r := rule("good")
		r.Lifetime = lifetime
		r.Scope.Task = "T"
		r.Scope.Session = "S"
		if lifetime == "ttl" {
			r.ExpiresAt = &instant
		}
		if err := snapshot(r).Validate(); err != nil {
			t.Fatal(err)
		}
	}
	r := rule("candidate")
	r.Authority = "inferred"
	r.Status = "candidate"
	if err := snapshot(r).Validate(); err != nil {
		t.Fatal(err)
	}
	s := snapshot(rule("one"), rule("one"))
	bad(t, s.Validate())
	s.Version = 2
	bad(t, s.Validate())
	s = snapshot()
	s.Rules = make([]Rule, 10001)
	bad(t, s.Validate())
}
func TestExceptionsAndLifetime(t *testing.T) {
	r := rule("absolute")
	r.Absolute = true
	r.Revision = 2
	e := Exception{ID: "exception", RuleID: r.ID, RuleRevision: 2, Task: "task-a", ExpiresAt: instant.Add(time.Hour), ApprovedBy: "owner", Reason: "synthetic temporary exception"}
	s := snapshot(r)
	s.Exceptions = []Exception{e}
	for _, tc := range []struct {
		name, task string
		now        time.Time
		revision   uint64
		want       int
	}{{"active", "task-a", instant, 2, 0}, {"other task", "task-b", instant, 2, 1}, {"unknown task", "", instant, 2, 1}, {"boundary", "task-a", e.ExpiresAt, 2, 1}, {"new revision", "task-a", instant, 3, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			s.Rules[0].Revision = tc.revision
			c := ctx()
			c.Task = tc.task
			c.Now = tc.now
			result, err := Resolve(s, c)
			if err != nil || len(result.Applicable) != tc.want {
				t.Fatalf("%+v %v", result, err)
			}
		})
	}
	s.Rules[0].Revision = 2
	for name, mutate := range map[string]func(*Exception){"id": func(e *Exception) { e.ID = "" }, "target": func(e *Exception) { e.RuleID = "missing" }, "revision": func(e *Exception) { e.RuleRevision = 3 }, "zero revision": func(e *Exception) { e.RuleRevision = 0 }, "task": func(e *Exception) { e.Task = "" }, "expiry": func(e *Exception) { e.ExpiresAt = time.Time{} }, "approval": func(e *Exception) { e.ApprovedBy = "project" }, "reason": func(e *Exception) { e.Reason = "" }} {
		t.Run(name, func(t *testing.T) {
			invalid := e
			mutate(&invalid)
			s.Exceptions = []Exception{invalid}
			bad(t, s.Validate())
		})
	}
	s.Exceptions = []Exception{e, e}
	bad(t, s.Validate())
	s.Exceptions = []Exception{e}
	e.ID = "a-first"
	s.Exceptions = append(s.Exceptions, e)
	c := ctx()
	c.Task = "task-a"
	result, err := Resolve(s, c)
	if err != nil || !strings.Contains(result.Excluded[0].Reason, "a-first") {
		t.Fatalf("unstable exception choice: %+v %v", result, err)
	}
	// Changing an ordinary rule into an absolute invalidates, rather than renews,
	// an older project-approved exception. Its historical record remains valid.
	s.Rules[0].Revision = 3
	s.Exceptions[0].ApprovedBy = "project"
	result, err = Resolve(s, c)
	if err != nil || len(result.Applicable) != 1 {
		t.Fatal(result, err)
	}
	ttl := rule("ttl")
	ttl.Lifetime = "ttl"
	ttl.ExpiresAt = &instant
	candidate := rule("candidate")
	candidate.Status = "candidate"
	result, err = Resolve(snapshot(ttl, candidate, rule("persistent")), ctx())
	if err != nil || len(result.Applicable) != 1 || len(result.Excluded) != 2 {
		t.Fatalf("%+v %v", result, err)
	}
}
func TestScopesAndOrdering(t *testing.T) {
	root := t.TempDir()
	r := rule("scoped")
	r.Scope = Scope{Repository: root, Worktree: root, Task: "T", Session: "S", Environment: "local", PathPrefix: "src", Tools: []string{"Edit", "Write"}}
	c := Context{Repository: root, Worktree: root, Task: "T", Session: "S", Environment: "local", Path: "src/a.go", Tool: "Edit", Now: instant}
	result, err := Resolve(snapshot(r), c)
	if err != nil || len(result.Applicable) != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	for _, field := range []string{"Repository", "Worktree", "Task", "Session", "Environment", "Path", "Tool"} {
		t.Run(field, func(t *testing.T) {
			copy := c
			v := reflect.ValueOf(&copy).Elem().FieldByName(field)
			v.SetString("")
			got, e := Resolve(snapshot(r), copy)
			if e != nil || len(got.Conditional) != 1 {
				t.Fatalf("missing scope guessed: %+v %v", got, e)
			}
			value := "other"
			if field == "Repository" || field == "Worktree" {
				value = filepath.Join(root, "other")
			}
			v.SetString(value)
			got, e = Resolve(snapshot(r), copy)
			if e != nil || len(got.Excluded) != 1 {
				t.Fatalf("scope mismatch: %+v %v", got, e)
			}
		})
	}
	c.Path = "src-other/a.go"
	result, err = Resolve(snapshot(r), c)
	if err != nil || len(result.Excluded) != 1 {
		t.Fatal("path prefix leaked")
	}
	for _, invalid := range []Context{{}, {Now: instant, Path: "../a"}, {Now: instant, Repository: "relative"}} {
		_, e := Resolve(snapshot(r), invalid)
		bad(t, e)
	}
	_, e := Resolve(Snapshot{}, ctx())
	bad(t, e)
	a, b, d := rule("absolute"), rule("high-priority"), rule("preference")
	a.Absolute = true
	a.Priority = -1000
	b.Priority = 1000
	d.Strength = "should"
	d.Priority = 1000
	result, err = Resolve(snapshot(d, b, a), ctx())
	if err != nil || result.Applicable[0].Rule.ID != a.ID || len(result.Applicable) != 3 {
		t.Fatalf("priority waived must: %+v %v", result, err)
	}
	x, _ := Resolve(snapshot(d, b, a), ctx())
	y, _ := Resolve(snapshot(a, b, d), ctx())
	if !reflect.DeepEqual(x, y) {
		t.Fatal("non-deterministic result")
	}
}
func TestConflictAndDefaults(t *testing.T) {
	a, b := rule("a"), rule("b")
	a.ConflictKey = "backend"
	a.Value = "one"
	b.ConflictKey = a.ConflictKey
	b.Value = "two"
	for _, tc := range []struct {
		sa, sb, va, vb string
		conflict       bool
	}{{"must", "must", "one", "two", true}, {"must", "must", "one", "one", false}, {"must", "must_not", "one", "one", true}, {"must", "must_not", "one", "two", false}, {"must_not", "must_not", "one", "two", false}} {
		a.Strength, b.Strength, a.Value, b.Value = tc.sa, tc.sb, tc.va, tc.vb
		got, e := Resolve(snapshot(a, b), ctx())
		if e != nil || (len(got.Conflicts) > 0) != tc.conflict {
			t.Fatalf("%+v %v", got, e)
		}
	}
	a.Strength, b.Strength = "should", "should"
	a.Value, b.Value = "one", "two"
	got, e := Resolve(snapshot(b, a), ctx())
	if e != nil || len(got.Conflicts) != 1 {
		t.Fatal("tied conflicting defaults silently won")
	}
	b.Priority = 1
	got, e = Resolve(snapshot(a, b), ctx())
	if e != nil || len(got.Applicable) != 1 || got.Applicable[0].Rule.ID != "b" {
		t.Fatal(got, e)
	}
	b.Authority = "project"
	got, e = Resolve(snapshot(a, b), ctx())
	if e != nil || got.Applicable[0].Rule.ID != "a" {
		t.Fatal("priority overrode authority")
	}
	b.Authority = "owner"
	b.Scope.Task = "T"
	c := ctx()
	c.Task = "T"
	got, e = Resolve(snapshot(a, b), c)
	if e != nil || got.Applicable[0].Rule.ID != "b" {
		t.Fatal("specific default not selected")
	}
	a.Strength = "must"
	got, e = Resolve(snapshot(a, b), c)
	if e != nil || len(got.Applicable) != 1 || got.Applicable[0].Rule.ID != "a" {
		t.Fatal("default competed with a must")
	}
}
func TestExplicitSupersessionAndEffectiveWindow(t *testing.T) {
	old := rule("old")
	newer := rule("new")
	newer.Supersedes = []string{"old"}
	result, err := Resolve(snapshot(old, newer), ctx())
	if err != nil || len(result.Applicable) != 1 || result.Applicable[0].Rule.ID != "new" {
		t.Fatalf("explicit successor did not win: %#v %v", result, err)
	}
	if len(result.Excluded) != 1 || result.Excluded[0].Reason != "explicitly superseded by new" {
		t.Fatalf("superseded predecessor was hidden: %#v", result.Excluded)
	}

	future := rule("future")
	starts := instant.Add(time.Hour)
	future.EffectiveFrom = &starts
	result, err = Resolve(snapshot(future), ctx())
	if err != nil || len(result.Applicable) != 0 || len(result.Excluded) != 1 || result.Excluded[0].Reason != "not effective yet" {
		t.Fatalf("effective window=%#v %v", result, err)
	}
	future.EffectiveFrom = &instant
	result, err = Resolve(snapshot(future), ctx())
	if err != nil || len(result.Applicable) != 1 {
		t.Fatalf("effective_from boundary excluded its rule: %#v %v", result, err)
	}
	badWindow := rule("bad-window")
	badWindow.EffectiveFrom = &starts
	ends := instant
	badWindow.ExpiresAt = &ends
	bad(t, snapshot(badWindow).Validate())

	first, second := rule("first"), rule("second")
	first.Supersedes, second.Supersedes = []string{"old"}, []string{"old"}
	result, err = Resolve(snapshot(old, first, second), ctx())
	if err != nil || len(result.Conflicts) != 1 || result.Conflicts[0].Key != "supersession:old" || len(result.Applicable) != 3 || len(result.Excluded) != 0 {
		t.Fatalf("competing successors were not explicit and non-destructive: %#v %v", result, err)
	}

	mandatory := rule("mandatory")
	weak := rule("weak")
	weak.Kind, weak.Strength, weak.Supersedes = "preference", "should", []string{"mandatory"}
	bad(t, snapshot(mandatory, weak).Validate())
	ownerRule := rule("owner-rule")
	projectSuccessor := rule("project-successor")
	projectSuccessor.Authority, projectSuccessor.Supersedes = "project", []string{"owner-rule"}
	bad(t, snapshot(ownerRule, projectSuccessor).Validate())
	absolute := rule("absolute")
	absolute.Absolute = true
	nonAbsolute := rule("non-absolute")
	nonAbsolute.Supersedes = []string{"absolute"}
	bad(t, snapshot(absolute, nonAbsolute).Validate())
	absoluteSuccessor := rule("absolute-successor")
	absoluteSuccessor.Absolute, absoluteSuccessor.Supersedes = true, []string{"absolute"}
	if err := snapshot(absolute, absoluteSuccessor).Validate(); err != nil {
		t.Fatalf("equally absolute successor was rejected: %v", err)
	}

	inactive := rule("inactive-successor")
	inactive.Status, inactive.Supersedes = "candidate", []string{"old"}
	result, err = Resolve(snapshot(old, inactive), ctx())
	if err != nil || len(result.Applicable) != 1 || result.Applicable[0].Rule.ID != "old" {
		t.Fatalf("inactive successor superseded a binding rule: %#v %v", result, err)
	}

	superseded := rule("already-superseded")
	superseded.Status = "superseded"
	if err := snapshot(superseded).Validate(); err != nil {
		t.Fatalf("superseded status was rejected: %v", err)
	}

	unknown := rule("unknown")
	unknown.Supersedes = []string{"missing"}
	bad(t, snapshot(unknown).Validate())
	self := rule("self")
	self.Supersedes = []string{"self"}
	bad(t, snapshot(self).Validate())
	duplicate, target := rule("duplicate"), rule("target")
	duplicate.Supersedes = []string{"target", "target"}
	bad(t, snapshot(duplicate, target).Validate())
	cycleA, cycleB := rule("cycle-a"), rule("cycle-b")
	cycleA.Supersedes, cycleB.Supersedes = []string{"cycle-b"}, []string{"cycle-a"}
	bad(t, snapshot(cycleA, cycleB).Validate())
}

func TestStrictJSON(t *testing.T) {
	for _, data := range []string{`{"version":1,"version":2}`, `{"version":1,"rules":[{"id":"a","id":"b"}]}`, `{} {}`, `{"unknown":1}`, `{"version":`, strings.Repeat("[", 66) + strings.Repeat("]", 66), `{"a":]}`, `[}`} {
		var s Snapshot
		bad(t, decode(strings.NewReader(data), &s, MaxRegistryBytes, true))
	}
	var out any
	if e := decode(strings.NewReader(`{"a":[1,true,null,{"b":"c"}]}`), &out, 1000, false); e != nil {
		t.Fatal(e)
	}
	bad(t, decode(strings.NewReader("12345"), &out, 4, false))
	bad(t, decode(failingReader{}, &out, 100, false))
}
