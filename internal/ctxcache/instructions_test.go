package ctxcache

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vshulcz/deja-vu/internal/instructions"
)

var instructionNow = time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

func instructionIdentity(root, task string) Identity {
	return Identity{WorkspaceID: "workspace", Repository: "origin", RepositoryRoot: root, Worktree: root, ProjectID: "deja-vu", TaskID: task}
}

func nativeRule(id string) instructions.Rule {
	return instructions.Rule{ID: id, Revision: 1, Kind: "constraint", Text: "Use the approved operating rule.", Authority: "owner", Status: "active", Strength: "must", Lifetime: "persistent", Source: "file:///workspace/AGENTS.md"}
}

func writeRegistry(t *testing.T, rules ...instructions.Rule) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "approved.json")
	store := instructions.FileStore{Path: path}
	if _, err := store.Replace(0, instructions.Snapshot{Version: instructions.Version, Rules: rules}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveInstructionsUsesApprovedFixtureAndPreservesProvenance(t *testing.T) {
	path, err := filepath.Abs("testdata/approved-registry.json")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveInstructionsFromPath(path, instructionIdentity(t.TempDir(), "TASK-1"), instructionNow)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Version != 7 || len(resolved.Instructions) != 1 {
		t.Fatalf("resolved=%#v", resolved)
	}
	rule := resolved.Instructions[0]
	if rule.ID != "repo.validate-before-close" || rule.Priority != "absolute" || rule.Scope != "global" || rule.Retention != "permanent" || rule.Status != "active" || rule.Source != "file:///workspace/AGENTS.md" || !strings.HasPrefix(rule.RegistrySource, "deja://instruction/") {
		t.Fatalf("instruction metadata lost: %#v", rule)
	}
	explanation, ok := ExplainInstruction(resolved, rule.ID)
	if !ok || explanation.Disposition != "applicable" || explanation.RegistryVersion != 7 {
		t.Fatalf("missing instruction explain data: %#v %v", explanation, ok)
	}
}

func TestResolveInstructionsMakesUnknownRequiredScopeAndConflictsExplicit(t *testing.T) {
	root := t.TempDir()
	scoped := nativeRule("task-rule")
	scoped.Scope.Task = "TASK-1"
	path := writeRegistry(t, scoped)
	resolved, err := ResolveInstructionsFromPath(path, instructionIdentity(root, ""), instructionNow)
	if err != nil || len(resolved.Instructions) != 0 || len(resolved.Gaps) != 1 || resolved.Gaps[0].Severity != "required" {
		t.Fatalf("unknown scope was silently absent: %#v %v", resolved, err)
	}
	a, b := nativeRule("backend-a"), nativeRule("backend-b")
	a.ConflictKey, a.Value = "backend", "one"
	b.ConflictKey, b.Value = "backend", "two"
	path = writeRegistry(t, a, b)
	resolved, err = ResolveInstructionsFromPath(path, instructionIdentity(root, "TASK-1"), instructionNow)
	if err != nil || len(resolved.Conflicts) != 1 || resolved.Conflicts[0].Resolution != "unresolved" || len(resolved.Gaps) != 1 {
		t.Fatalf("conflict was not a blocker: %#v %v", resolved, err)
	}
}

func TestRegistrySupersessionAndRefreshCannotResurrectRule(t *testing.T) {
	root := t.TempDir()
	old, newer := nativeRule("old"), nativeRule("new")
	newer.Supersedes = []string{"old"}
	path := writeRegistry(t, old, newer)
	resolved, err := ResolveInstructionsFromPath(path, instructionIdentity(root, "TASK-1"), instructionNow)
	if err != nil || len(resolved.Instructions) != 1 || resolved.Instructions[0].ID != "new" || len(resolved.Conflicts) != 1 || resolved.Conflicts[0].Resolution != "new" {
		t.Fatalf("supersession=%#v %v", resolved, err)
	}
	if why, ok := ExplainInstruction(resolved, "old"); !ok || why.Disposition != "excluded" || why.ResolutionReason != "explicitly superseded by new" {
		t.Fatalf("superseded rule is not explainable: %#v %v", why, ok)
	}
	store := instructions.FileStore{Path: path}
	current, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for i := range current.Rules {
		current.Rules[i].Revision++
		current.Rules[i].Status = "revoked"
	}
	if _, err = store.Replace(current.Revision, current); err != nil {
		t.Fatal(err)
	}
	refreshed, err := ResolveInstructionsFromPath(path, instructionIdentity(root, "TASK-1"), instructionNow)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := ReconcileInstructions(resolved.Instructions, refreshed, instructionIdentity(root, "TASK-1"), instructionNow)
	if err != nil || len(merged.Instructions) != 0 {
		t.Fatalf("revoked registry instruction resurrected: %#v %v", merged, err)
	}
}

func TestCheckpointInstructionsBindScopeDatesSupersessionAndConflicts(t *testing.T) {
	root := t.TempDir()
	id := instructionIdentity(root, "TASK-1")
	old := Instruction{ID: "old", Version: 1, Text: "Use the old procedure.", Priority: "required", Scope: "repository", Retention: "project", Status: "active", Source: "checkpoint://old"}
	newer := Instruction{ID: "new", Version: 1, Text: "Use the new procedure.", Priority: "required", Scope: "repository", Retention: "project", Status: "active", Source: "checkpoint://new", Supersedes: []string{"old"}}
	normalized, err := NormalizeCheckpointInstructions([]Instruction{old, newer}, id)
	if err != nil || normalized[0].ScopeTarget != root || normalized[0].Status != "candidate" {
		t.Fatalf("repository binding=%#v %v", normalized, err)
	}
	resolved, err := ResolveCheckpointInstructions(normalized, id, instructionNow)
	if err != nil || len(resolved.Instructions) != 0 || len(resolved.Gaps) != 2 {
		t.Fatalf("unapproved checkpoint rules became binding=%#v %v", resolved, err)
	}
	if why, ok := ExplainInstruction(resolved, "new"); !ok || why.Disposition != "candidate" {
		t.Fatalf("candidate is not explainable: %#v %v", why, ok)
	}
	future := normalized[1]
	future.ID, future.Supersedes = "future", nil
	starts := instructionNow.Add(time.Hour)
	future.EffectiveFrom = &starts
	resolved, err = ResolveCheckpointInstructions([]Instruction{future}, id, instructionNow)
	if err != nil || len(resolved.Instructions) != 0 || len(resolved.Gaps) != 0 {
		t.Fatalf("future rule was applied: %#v %v", resolved, err)
	}
	if why, ok := ExplainInstruction(resolved, "future"); !ok || why.Disposition != "excluded" || why.ResolutionReason != "not effective yet" {
		t.Fatalf("future proposal was not preserved as excluded: %#v %v", why, ok)
	}
	a, b := normalized[1], normalized[1]
	a.ID, a.Supersedes, a.ConflictKey, a.ConflictValue = "first", nil, "mode", "one"
	b.ID, b.Supersedes, b.ConflictKey, b.ConflictValue = "second", nil, "mode", "two"
	conflicting, err := NormalizeCheckpointInstructions([]Instruction{a, b}, id)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err = ResolveCheckpointInstructions(conflicting, id, instructionNow)
	if err != nil || len(resolved.Conflicts) != 0 || len(resolved.Gaps) != 2 {
		t.Fatalf("unapproved conflicting rules were not retained as proposed: %#v %v", resolved, err)
	}
	cycleA, cycleB := a, b
	cycleA.ID, cycleB.ID = "cycle-a", "cycle-b"
	cycleA.Supersedes, cycleB.Supersedes = []string{"cycle-b"}, []string{"cycle-a"}
	if _, err = NormalizeCheckpointInstructions([]Instruction{cycleA, cycleB}, id); err == nil {
		t.Fatal("checkpoint supersession cycle accepted")
	}
	if _, err = NormalizeCheckpointInstructions([]Instruction{{ID: "bad", Version: 1, Text: "bad", Priority: "required", Scope: "repository", ScopeTarget: filepath.Join(root, "other"), Retention: "project", Status: "active", Source: "checkpoint://bad"}}, id); err == nil {
		t.Fatal("foreign repository instruction accepted")
	}
}

func TestUnavailableExplicitRegistryIsRequiredGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	resolved, err := ResolveInstructionsFromPath(path, instructionIdentity(t.TempDir(), "TASK-1"), instructionNow)
	if err != nil || len(resolved.Gaps) != 1 || resolved.Gaps[0].Severity != "required" {
		t.Fatalf("missing registry=%#v %v", resolved, err)
	}
	if _, err = ResolveInstructions(brokenInstructionStore{}, instructionIdentity(t.TempDir(), "TASK-1"), instructionNow); err != nil {
		t.Fatal(err)
	}
}

func TestInstructionResolutionRejectsInvalidInputsAndRetainsRegistryCandidates(t *testing.T) {
	id := instructionIdentity(t.TempDir(), "TASK-1")
	if _, err := ResolveInstructions(brokenInstructionStore{}, id, time.Time{}); err == nil {
		t.Fatal("zero resolution clock accepted")
	}
	invalid := staticInstructionStore{snapshot: instructions.Snapshot{Version: 999}}
	resolved, err := ResolveInstructions(invalid, id, instructionNow)
	if err != nil || len(resolved.Gaps) != 1 || !strings.Contains(resolved.Gaps[0].Reason, "could not be resolved") {
		t.Fatalf("invalid registry did not become gap: %#v %v", resolved, err)
	}
	candidate := nativeRule("registry-candidate")
	candidate.Status = "candidate"
	path := writeRegistry(t, candidate)
	resolved, err = ResolveInstructionsFromPath(path, id, instructionNow)
	if err != nil || len(resolved.Instructions) != 0 || len(resolved.Gaps) != 1 || resolved.Gaps[0].Subject != "proposed instruction: registry-candidate" {
		t.Fatalf("registry candidate was not retained as approval gap: %#v %v", resolved, err)
	}
	if why, ok := resolved.Explain[candidate.ID]; !ok || why.Disposition != "excluded" || why.ResolutionReason != "candidate" {
		t.Fatalf("registry candidate was not explainable: %#v %v", why, ok)
	}

	bad := Instruction{ID: "bad", Version: 1, Text: "Bad snapshot record.", Priority: "invalid", Scope: "global", Retention: "durable", Status: "candidate", Source: "checkpoint://bad"}
	if _, err := ResolveCheckpointInstructions([]Instruction{bad}, id, instructionNow); err == nil {
		t.Fatal("corrupt checkpoint record accepted")
	}
	valid := Instruction{ID: "valid", Version: 1, Text: "A valid proposal.", Priority: "required", Scope: "global", Retention: "durable", Status: "candidate", Source: "checkpoint://valid"}
	if _, err := ResolveCheckpointInstructions([]Instruction{valid, valid}, id, instructionNow); err == nil {
		t.Fatal("duplicate checkpoint records accepted")
	}
	cycleA, cycleB := valid, valid
	cycleA.ID, cycleA.Supersedes = "cycle-a", []string{"cycle-b"}
	cycleB.ID, cycleB.Supersedes = "cycle-b", []string{"cycle-a"}
	if _, err := ResolveCheckpointInstructions([]Instruction{cycleA, cycleB}, id, instructionNow); err == nil {
		t.Fatal("cyclic checkpoint records accepted")
	}
}

func TestReconcileApprovedRegistryActivatesMatchingCheckpointCandidate(t *testing.T) {
	raw := Instruction{ID: "same", Version: 1, Text: "Approved registry rule.", Priority: "required", Scope: "global", Retention: "durable", Status: "active", Source: "checkpoint://same"}
	registryRule := raw
	registryRule.Source = "file:///workspace/AGENTS.md"
	registryRule.RegistrySource = "deja://instruction/same@1"
	registry := InstructionResolution{Version: 1, Instructions: []Instruction{registryRule}, Explain: map[string]InstructionExplanation{"same": explanation(registryRule, 1, "applicable", "scope matches")}}
	resolved, err := ReconcileInstructions([]Instruction{raw}, registry, instructionIdentity(t.TempDir(), "TASK-1"), instructionNow)
	if err != nil || len(resolved.Instructions) != 1 || resolved.Instructions[0].RegistrySource == "" || len(resolved.Conflicts) != 0 || len(resolved.Gaps) != 0 {
		t.Fatalf("approved registry did not replace checkpoint candidate: %#v %v", resolved, err)
	}
	if why := resolved.Explain["same"]; !strings.Contains(why.ResolutionReason, "checkpoint://same") {
		t.Fatalf("approved match lost proposal provenance: %#v", why)
	}
}

func TestReconcileSameIDDoesNotApproveDifferentCheckpointProposal(t *testing.T) {
	raw := Instruction{ID: "same", Version: 1, Text: "Unreviewed proposal.", Priority: "required", Scope: "global", Retention: "durable", Status: "candidate", Source: "checkpoint://same"}
	registryRule := raw
	registryRule.Text = "Unrelated approved rule."
	registryRule.Source = "file:///workspace/AGENTS.md"
	registryRule.Status = "active"
	registryRule.RegistrySource = "deja://instruction/same@1"
	registry := InstructionResolution{Version: 1, Instructions: []Instruction{registryRule}, Explain: map[string]InstructionExplanation{"same": explanation(registryRule, 1, "applicable", "scope matches")}}
	resolved, err := ReconcileInstructions([]Instruction{raw}, registry, instructionIdentity(t.TempDir(), "TASK-1"), instructionNow)
	if err != nil || len(resolved.Instructions) != 1 || resolved.Instructions[0].RegistrySource == "" || len(resolved.Conflicts) != 1 || resolved.Conflicts[0].Resolution != "unresolved" || len(resolved.Gaps) != 1 || !strings.Contains(resolved.Gaps[0].Reason, "different semantics") {
		t.Fatalf("id collision hid approved rule or accidentally approved proposal: %#v %v", resolved, err)
	}
	why, ok := ExplainInstruction(resolved, "same")
	if !ok || why.Disposition != "applicable" || why.Instruction.Source != "file:///workspace/AGENTS.md" {
		t.Fatalf("approved rule was not explainable: %#v %v", why, ok)
	}
	proposal, ok := resolved.Explain["proposal:same"]
	if !ok || proposal.Disposition != "candidate" || proposal.Instruction.Source != "checkpoint://same" {
		t.Fatalf("proposal provenance was lost: %#v %v", proposal, ok)
	}
}

func TestCheckpointRetainsRawScopedInstructionsForFutureResolutionAndExplain(t *testing.T) {
	t.Setenv("DEJA_INSTRUCTIONS_FILE", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	cacheRoot := t.TempDir()
	id := instructionIdentity(root, "TASK-1")
	state := State{Instructions: []Instruction{{ID: "path-rule", Version: 1, Text: "Review this path before closure.", Priority: "required", Scope: "path", ScopeTarget: "internal/ctxcache", Retention: "task", Status: "active", Source: "checkpoint://path-rule"}}}
	snapshot, err := Checkpoint(cacheRoot, id, state)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.State.InstructionRecords) != 1 || len(snapshot.State.Instructions) != 0 {
		t.Fatalf("raw scoped rule was not retained separately: %#v", snapshot.State)
	}
	explanation, ok := snapshot.State.InstructionExplain["path-rule"]
	if !ok || explanation.Disposition != "conditional" || !strings.Contains(explanation.ResolutionReason, "unknown: path") {
		t.Fatalf("proposed checkpoint instruction is not explainable: %#v %v", explanation, ok)
	}
	loaded, err := Load(cacheRoot, id)
	if err != nil || len(loaded.State.InstructionRecords) != 1 || len(loaded.State.Instructions) != 0 {
		t.Fatalf("instruction records did not round trip: %#v %v", loaded.State, err)
	}
	explained, err := Explain(cacheRoot, id, "path-rule")
	if err != nil || !strings.Contains(explained.Why, "conditional") || explained.Instruction == nil || explained.Instruction.ID != "path-rule" {
		t.Fatalf("ctx explain lost excluded instruction provenance: %#v %v", explained, err)
	}
}

func TestInstructionResolutionBranchCoverage(t *testing.T) {
	root := t.TempDir()
	id := instructionIdentity(root, "TASK-1")
	base := Instruction{ID: "rule", Version: 1, Text: "Proposed rule.", Priority: "required", Scope: "global", Retention: "durable", Status: "active", Source: "checkpoint://rule"}

	check := func(t *testing.T, rule Instruction, wantDisposition string, wantGaps int, wantSeverity string) {
		t.Helper()
		resolved, err := ResolveCheckpointInstructions([]Instruction{rule}, id, instructionNow)
		if err != nil || len(resolved.Instructions) != 0 || len(resolved.Gaps) != wantGaps {
			t.Fatalf("resolution=%#v %v", resolved, err)
		}
		why, ok := ExplainInstruction(resolved, rule.ID)
		if !ok || why.Disposition != wantDisposition {
			t.Fatalf("explanation=%#v %v", why, ok)
		}
		if wantGaps != 0 && resolved.Gaps[0].Severity != wantSeverity {
			t.Fatalf("gap=%#v", resolved.Gaps[0])
		}
	}

	check(t, base, "candidate", 1, "required")
	superseded := base
	superseded.ID, superseded.Status = "superseded", "superseded"
	check(t, superseded, "excluded", 0, "")
	future := base
	future.ID = "future"
	start := instructionNow.Add(time.Hour)
	future.EffectiveFrom = &start
	check(t, future, "excluded", 0, "")
	expired := base
	expired.ID = "expired"
	end := instructionNow.Add(-time.Hour)
	expired.EffectiveUntil = &end
	check(t, expired, "excluded", 0, "")
	projectMismatch := base
	projectMismatch.ID, projectMismatch.Scope, projectMismatch.ScopeTarget = "project-mismatch", "project", "other"
	check(t, projectMismatch, "excluded", 0, "")
	repositoryMismatch := base
	repositoryMismatch.ID, repositoryMismatch.Scope, repositoryMismatch.ScopeTarget = "repository-mismatch", "repository", filepath.Join(root, "other")
	check(t, repositoryMismatch, "excluded", 0, "")
	taskMismatch := base
	taskMismatch.ID, taskMismatch.Scope, taskMismatch.ScopeTarget = "task-mismatch", "task", "other"
	check(t, taskMismatch, "excluded", 0, "")
	pathRule := base
	pathRule.ID, pathRule.Scope, pathRule.ScopeTarget = "path-unknown", "path", "internal"
	check(t, pathRule, "conditional", 1, "advisory")
	session := base
	session.ID, session.Scope, session.ScopeTarget = "session-unknown", "session", "session-1"
	check(t, session, "conditional", 1, "advisory")

	project := base
	project.ID, project.Scope, project.ScopeTarget = "project", "project", ""
	repository := base
	repository.ID, repository.Scope, repository.ScopeTarget = "repository", "repository", ""
	task := base
	task.ID, task.Scope, task.ScopeTarget = "task", "task", ""
	normalized, err := NormalizeCheckpointInstructions([]Instruction{project, repository, task, session, pathRule}, id)
	if err != nil || normalized[0].ScopeTarget != id.ProjectID || normalized[1].ScopeTarget != root || normalized[2].ScopeTarget != id.TaskID {
		t.Fatalf("scope normalization=%#v %v", normalized, err)
	}
	for _, rule := range normalized {
		if rule.Status != "candidate" {
			t.Fatalf("checkpoint rule became active: %#v", rule)
		}
	}
	invalid := base
	invalid.Scope, invalid.ScopeTarget = "path", "../escape"
	if _, err := NormalizeCheckpointInstructions([]Instruction{invalid}, id); err == nil {
		t.Fatal("escaped path scope accepted")
	}
}

func TestMaterializeInstructionStoreConfiguration(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DEJA_INSTRUCTIONS_FILE", "")
	result, err := MaterializeInstructions(instructionIdentity(t.TempDir(), "TASK-1"))
	if err != nil || len(result.Instructions) != 0 || len(result.Gaps) != 0 {
		t.Fatalf("implicit absent registry=%#v %v", result, err)
	}
	t.Setenv("DEJA_INSTRUCTIONS_FILE", "relative")
	result, err = MaterializeInstructions(instructionIdentity(t.TempDir(), "TASK-1"))
	if err != nil || len(result.Gaps) != 1 || result.Gaps[0].Severity != "required" {
		t.Fatalf("invalid configured registry=%#v %v", result, err)
	}
}

func TestCheckpointInstructionSchemaRejectsUntrustedMalformedRecords(t *testing.T) {
	id := instructionIdentity(t.TempDir(), "TASK-1")
	valid := Instruction{ID: "valid", Version: 1, Text: "Proposed operating rule.", Priority: "required", Scope: "global", Retention: "durable", Status: "candidate", Source: "checkpoint://valid"}
	equal := instructionNow
	for name, mutate := range map[string]func(*Instruction){
		"missing id":           func(r *Instruction) { r.ID = "" },
		"empty text":           func(r *Instruction) { r.Text = " " },
		"missing source":       func(r *Instruction) { r.Source = "" },
		"zero version":         func(r *Instruction) { r.Version = 0 },
		"bad priority":         func(r *Instruction) { r.Priority = "urgent" },
		"bad scope":            func(r *Instruction) { r.Scope = "team" },
		"bad retention":        func(r *Instruction) { r.Retention = "forever" },
		"bad status":           func(r *Instruction) { r.Status = "approved" },
		"absolute mismatch":    func(r *Instruction) { r.Absolute = true },
		"bad effective window": func(r *Instruction) { r.EffectiveFrom, r.EffectiveUntil = &equal, &equal },
		"half conflict":        func(r *Instruction) { r.ConflictKey = "mode" },
		"bad conflict key":     func(r *Instruction) { r.ConflictKey, r.ConflictValue = "bad key", "one" },
		"self supersedes":      func(r *Instruction) { r.Supersedes = []string{"valid"} },
	} {
		t.Run(name, func(t *testing.T) {
			rule := valid
			mutate(&rule)
			if _, err := NormalizeCheckpointInstructions([]Instruction{rule}, id); err == nil {
				t.Fatal("malformed checkpoint instruction accepted")
			}
		})
	}

	for name, input := range map[string][]Instruction{
		"forged registry source": {{ID: "forged", Version: 1, Text: valid.Text, Priority: "required", Scope: "global", Retention: "durable", Status: "candidate", Source: valid.Source, RegistrySource: "deja://instruction/forged@1"}},
		"duplicate id":           {valid, valid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeCheckpointInstructions(input, id); err == nil {
				t.Fatal("untrusted checkpoint input accepted")
			}
		})
	}

	project := valid
	project.Scope = "project"
	if _, err := NormalizeCheckpointInstructions([]Instruction{project}, Identity{}); err == nil {
		t.Fatal("project proposal without identity accepted")
	}
	repository := valid
	repository.Scope, repository.ScopeTarget = "repository", "relative"
	if _, err := NormalizeCheckpointInstructions([]Instruction{repository}, id); err == nil {
		t.Fatal("repository proposal without clean absolute identity accepted")
	}
	task := valid
	task.Scope = "task"
	if _, err := NormalizeCheckpointInstructions([]Instruction{task}, instructionIdentity(t.TempDir(), "")); err == nil {
		t.Fatal("task proposal without identity accepted")
	}
	session := valid
	session.Scope = "session"
	if _, err := NormalizeCheckpointInstructions([]Instruction{session}, id); err == nil {
		t.Fatal("session proposal without selector accepted")
	}
	global := valid
	global.ScopeTarget = "unexpected"
	if _, err := NormalizeCheckpointInstructions([]Instruction{global}, id); err == nil {
		t.Fatal("global proposal with selector accepted")
	}
}

func TestCheckpointInstructionBindingValidationAndResolutionOrdering(t *testing.T) {
	root := t.TempDir()
	id := instructionIdentity(root, "TASK-1")
	base := Instruction{ID: "proposal", Version: 1, Text: "A proposed operating rule.", Priority: "required", Scope: "global", Retention: "durable", Status: "candidate", Source: "checkpoint://proposal"}
	for name, mutate := range map[string]func(*Instruction){
		"project mismatch": func(r *Instruction) { r.Scope, r.ScopeTarget = "project", "other-project" },
		"repository mismatch": func(r *Instruction) {
			r.Scope, r.ScopeTarget = "repository", filepath.Join(root, "other-repository")
		},
		"task mismatch": func(r *Instruction) { r.Scope, r.ScopeTarget = "task", "OTHER-1" },
	} {
		t.Run(name, func(t *testing.T) {
			rule := base
			mutate(&rule)
			if _, err := NormalizeCheckpointInstructions([]Instruction{rule}, id); err == nil {
				t.Fatal("foreign scoped proposal accepted")
			}
		})
	}
	if _, err := ResolveCheckpointInstructions([]Instruction{base}, id, time.Time{}); err == nil {
		t.Fatal("zero checkpoint-resolution clock accepted")
	}

	matching := base
	matching.ID = "matching"
	registryRule := matching
	registryRule.Status, registryRule.Source, registryRule.RegistrySource = "active", "file:///workspace/AGENTS.md", "deja://instruction/matching@1"
	registry := InstructionResolution{Version: 1, Instructions: []Instruction{registryRule}, Explain: map[string]InstructionExplanation{"matching": explanation(registryRule, 1, "applicable", "scope matches")}}
	otherA, otherZ := base, base
	otherA.ID, otherA.Source = "other-a", "checkpoint://other-a"
	otherZ.ID, otherZ.Source = "other-z", "checkpoint://other-z"
	resolved, err := ReconcileInstructions([]Instruction{matching, otherZ, otherA}, registry, id, instructionNow)
	if err != nil || len(resolved.Instructions) != 1 || len(resolved.Gaps) != 2 || resolved.Gaps[0].Subject != "proposed instruction: other-a" || resolved.Gaps[1].Subject != "proposed instruction: other-z" {
		t.Fatalf("proposal reconciliation was not deterministic: %#v %v", resolved, err)
	}
	corrupt := base
	corrupt.Priority = "bad"
	if _, err := ReconcileInstructions([]Instruction{corrupt}, registry, id, instructionNow); err == nil {
		t.Fatal("corrupt persisted proposal was reconciled")
	}
}

func TestApprovedInstructionAdaptersPreserveNativeScopeAndRetention(t *testing.T) {
	root := t.TempDir()
	now := instructionNow
	preferred := nativeRule("preferred")
	preferred.Kind, preferred.Strength = "preference", "should"
	task := nativeRule("task")
	task.Lifetime, task.Scope.Task = "task", "TASK-1"
	session := nativeRule("session")
	session.Lifetime, session.Scope.Session = "session", "session-1"
	pathRule := nativeRule("path")
	pathRule.Lifetime, pathRule.Scope.Repository, pathRule.Scope.PathPrefix, pathRule.ExpiresAt = "ttl", root, "internal", ptrTime(now.Add(time.Hour))
	worktree := nativeRule("worktree")
	worktree.Scope.Worktree = root
	repository := nativeRule("repository")
	repository.Scope.Repository = root
	path := writeRegistry(t, preferred, task, session, pathRule, worktree, repository)
	resolved, err := ResolveInstructionsFromPath(path, instructionIdentity(root, "TASK-1"), now)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]struct{ priority, scope, target, retention, disposition string }{
		"preferred":  {"preferred", "global", "", "permanent", "applicable"},
		"task":       {"required", "task", "TASK-1", "task", "applicable"},
		"session":    {"required", "session", "session-1", "ephemeral", "conditional"},
		"path":       {"required", "path", "internal", "durable", "conditional"},
		"worktree":   {"required", "repository", root, "permanent", "applicable"},
		"repository": {"required", "repository", root, "permanent", "applicable"},
	} {
		got, ok := resolved.Explain[id]
		if !ok || got.Instruction.Priority != want.priority || got.Instruction.Scope != want.scope || got.Instruction.ScopeTarget != want.target || got.Instruction.Retention != want.retention || got.Disposition != want.disposition {
			t.Fatalf("%s adapter=%#v", id, got)
		}
	}
}

func ptrTime(value time.Time) *time.Time { return &value }

func TestCheckpointProposalScopeGapsAndSemanticApprovalMatch(t *testing.T) {
	root := t.TempDir()
	proposalStart := instructionNow.Add(-time.Minute)
	proposal := Instruction{ID: "proposal", Version: 1, Text: "Use an approved process.", Priority: "required", Scope: "project", ScopeTarget: "deja-vu", Retention: "durable", Status: "candidate", EffectiveFrom: &proposalStart, Supersedes: []string{"other"}, Source: "checkpoint://proposal"}
	for name, id := range map[string]Identity{
		"project":    {RepositoryRoot: root},
		"repository": {ProjectID: "deja-vu"},
		"task":       {ProjectID: "deja-vu", RepositoryRoot: root},
	} {
		t.Run(name, func(t *testing.T) {
			rule := proposal
			switch name {
			case "repository":
				rule.Scope, rule.ScopeTarget = "repository", root
			case "task":
				rule.Scope, rule.ScopeTarget = "task", "TASK-1"
			}
			resolved, err := ResolveCheckpointInstructions([]Instruction{rule}, id, instructionNow)
			if err != nil || len(resolved.Gaps) != 1 || resolved.Gaps[0].Severity != "advisory" {
				t.Fatalf("unknown %s scope=%#v %v", name, resolved, err)
			}
		})
	}

	registryRule := proposal
	registryRule.Source, registryRule.RegistrySource, registryRule.Status = "file:///workspace/AGENTS.md", "deja://instruction/proposal@1", "active"
	registry := InstructionResolution{Version: 1, Instructions: []Instruction{registryRule}, Explain: map[string]InstructionExplanation{"proposal": explanation(registryRule, 1, "applicable", "scope matches")}}
	for name, mutate := range map[string]func(*Instruction){
		"same":              func(*Instruction) {},
		"priority":          func(r *Instruction) { r.Priority = "preferred" },
		"absolute":          func(r *Instruction) { r.Priority, r.Absolute = "absolute", true },
		"scope":             func(r *Instruction) { r.Scope, r.ScopeTarget = "task", "TASK-1" },
		"retention":         func(r *Instruction) { r.Retention = "task" },
		"effective from":    func(r *Instruction) { r.EffectiveFrom = ptrTime(instructionNow.Add(time.Minute)) },
		"effective until":   func(r *Instruction) { r.EffectiveUntil = ptrTime(instructionNow.Add(time.Hour)) },
		"supersedes length": func(r *Instruction) { r.Supersedes = nil },
		"supersedes":        func(r *Instruction) { r.Supersedes = []string{"different"} },
		"conflict key":      func(r *Instruction) { r.ConflictKey, r.ConflictValue = "mode", "one" },
	} {
		t.Run(name, func(t *testing.T) {
			raw := proposal
			mutate(&raw)
			resolved, err := ReconcileInstructions([]Instruction{raw}, registry, instructionIdentity(root, "TASK-1"), instructionNow)
			if err != nil {
				t.Fatal(err)
			}
			if name == "same" {
				if len(resolved.Gaps) != 0 || len(resolved.Conflicts) != 0 || strings.Contains(resolved.Explain["proposal"].ResolutionReason, "proposal:") {
					t.Fatalf("matching proposal did not receive approval evidence: %#v", resolved)
				}
				return
			}
			if len(resolved.Instructions) != 1 || len(resolved.Gaps) != 1 || len(resolved.Conflicts) != 1 || resolved.Explain["proposal:"+proposal.ID].Instruction.Source != proposal.Source {
				t.Fatalf("semantic difference was accidentally approved: %#v", resolved)
			}
		})
	}
}

func TestRegistryGapIsReplacedWhenApprovedRegistryAppears(t *testing.T) {
	registryPath := filepath.Join(t.TempDir(), "approved.json")
	t.Setenv("DEJA_INSTRUCTIONS_FILE", registryPath)
	cacheRoot, workspace := t.TempDir(), t.TempDir()
	id := instructionIdentity(workspace, "TASK-1")
	checkpoint, err := Checkpoint(cacheRoot, id, State{Objective: "ship"})
	if err != nil || !hasInstructionGap(checkpoint.State.Gaps, "approved instructions") {
		t.Fatalf("missing registry did not become an explicit gap: %#v %v", checkpoint, err)
	}
	if len(checkpoint.State.Gaps) == 0 || checkpoint.State.Gaps[0].Source != instructionResolutionSource {
		t.Fatalf("registry gap is not materializer-owned: %#v", checkpoint.State.Gaps)
	}
	store := instructions.FileStore{Path: registryPath}
	rule := nativeRule("approved-after-gap")
	rule.Absolute = true
	if _, err = store.Replace(0, instructions.Snapshot{Version: instructions.Version, Rules: []instructions.Rule{rule}}); err != nil {
		t.Fatal(err)
	}
	refreshed, _, err := Refresh(cacheRoot, id)
	if err != nil || hasInstructionGap(refreshed.State.Gaps, "approved instructions") || len(refreshed.State.Instructions) != 1 {
		t.Fatalf("registry recovery retained stale gap: %#v %v", refreshed, err)
	}
	resumed, err := Resume(cacheRoot, id, 10_000)
	if err != nil || hasInstructionGap(resumed.Snapshot.State.Gaps, "approved instructions") {
		t.Fatalf("resume resurrected resolved registry gap: %#v %v", resumed, err)
	}
}

func hasInstructionGap(gaps []Gap, subject string) bool {
	for _, gap := range gaps {
		if gap.Subject == subject {
			return true
		}
	}
	return false
}

type brokenInstructionStore struct{}

func (brokenInstructionStore) Load() (instructions.Snapshot, error) {
	return instructions.Snapshot{}, errors.New("synthetic read error")
}
func (brokenInstructionStore) Replace(uint64, instructions.Snapshot) (instructions.Snapshot, error) {
	return instructions.Snapshot{}, os.ErrPermission
}

type staticInstructionStore struct{ snapshot instructions.Snapshot }

func (s staticInstructionStore) Load() (instructions.Snapshot, error) { return s.snapshot, nil }
func (s staticInstructionStore) Replace(uint64, instructions.Snapshot) (instructions.Snapshot, error) {
	return instructions.Snapshot{}, os.ErrPermission
}
