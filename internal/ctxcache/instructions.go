package ctxcache

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vshulcz/deja-vu/internal/instructions"
)

const instructionResolutionSource = "deja://instructions"

// Instruction is the L0 form stored in a ctx snapshot. It deliberately keeps
// the registry's stable rule identity and provenance instead of rendered text:
// resolution can be repeated after a branch, scope, or registry change without
// treating a previous packet as the source of authority.
type Instruction struct {
	ID             string     `json:"id"`
	Version        uint64     `json:"version"`
	Text           string     `json:"text"`
	Priority       string     `json:"priority"`
	Absolute       bool       `json:"absolute,omitempty"`
	Scope          string     `json:"scope"`
	ScopeTarget    string     `json:"scope_target,omitempty"`
	Retention      string     `json:"retention"`
	Status         string     `json:"status"`
	EffectiveFrom  *time.Time `json:"effective_from,omitempty"`
	EffectiveUntil *time.Time `json:"effective_until,omitempty"`
	Supersedes     []string   `json:"supersedes,omitempty"`
	ConflictKey    string     `json:"conflict_key,omitempty"`
	ConflictValue  string     `json:"conflict_value,omitempty"`
	Source         string     `json:"source"`
	RegistrySource string     `json:"registry_source"`
	Authority      string     `json:"authority,omitempty"`
	Reason         string     `json:"reason,omitempty"`
}

// InstructionExplanation is retained separately from the active L0 slice so
// ctx explain can show why a rule was included, conditional, superseded, or
// excluded without representing non-applicable rules as active context.
type InstructionExplanation struct {
	Instruction      Instruction `json:"instruction"`
	RegistryVersion  uint64      `json:"registry_version"`
	Disposition      string      `json:"disposition"`
	ResolutionReason string      `json:"resolution_reason"`
}

type InstructionResolution struct {
	Version      uint64                            `json:"version"`
	Instructions []Instruction                     `json:"instructions"`
	Gaps         []Gap                             `json:"gaps,omitempty"`
	Conflicts    []Conflict                        `json:"conflicts,omitempty"`
	Explain      map[string]InstructionExplanation `json:"explain,omitempty"`
}

// DefaultInstructionStore locates the native approved-instruction registry.
// The registry remains the authoritative source: ctx only materializes a
// resolved packet and never writes rules or restores them from snapshots.
func DefaultInstructionStore() (instructions.FileStore, error) {
	path, err := instructions.DefaultPath()
	if err != nil {
		return instructions.FileStore{}, err
	}
	return instructions.FileStore{Path: path}, nil
}

func ResolveInstructionsFromPath(path string, id Identity, now time.Time) (InstructionResolution, error) {
	return ResolveInstructions(instructions.FileStore{Path: path}, id, now)
}

// MaterializeInstructions reads the opt-in native registry. An absent implicit
// default registry means instruction delivery has not been configured yet; an
// explicitly selected but absent registry is a required gap.
func MaterializeInstructions(id Identity) (InstructionResolution, error) {
	store, err := DefaultInstructionStore()
	if err != nil {
		return InstructionResolution{Instructions: []Instruction{}, Explain: map[string]InstructionExplanation{}, Gaps: []Gap{{Subject: "approved instructions", Severity: "required", Reason: "invalid instruction store configuration: " + err.Error(), RetrievalHint: "configure a clean absolute DEJA_INSTRUCTIONS_FILE", Source: instructionResolutionSource}}}, nil
	}
	if _, err := os.Stat(store.Path); os.IsNotExist(err) && os.Getenv("DEJA_INSTRUCTIONS_FILE") == "" {
		return InstructionResolution{Instructions: []Instruction{}, Explain: map[string]InstructionExplanation{}}, nil
	}
	return ResolveInstructions(store, id, time.Now().UTC())
}

// ResolveInstructions materializes known applicable rules deterministically.
// A missing or unreadable registry is a required L0 gap, never permission to
// reuse a previous registry-backed instruction packet. Rules whose scopes lack
// known context become explicit gaps, preserving them for a later resolution.
func ResolveInstructions(store instructions.Store, id Identity, now time.Time) (InstructionResolution, error) {
	out := InstructionResolution{Instructions: []Instruction{}, Explain: map[string]InstructionExplanation{}}
	if now.IsZero() {
		return out, fmt.Errorf("instruction resolution requires a clock")
	}
	snapshot, err := store.Load()
	if err != nil {
		reason := "approved instruction registry is unavailable"
		if os.IsNotExist(err) {
			reason = "no approved instruction registry exists"
		}
		out.Gaps = []Gap{{Subject: "approved instructions", Severity: "required", Reason: reason, RetrievalHint: "repair or approve the local Deja instruction registry before relying on L0 context", Source: instructionResolutionSource}}
		return out, nil
	}
	result, err := instructions.Resolve(snapshot, instructionContext(id, now))
	if err != nil {
		out.Gaps = []Gap{{Subject: "approved instructions", Severity: "required", Reason: "instruction registry could not be resolved: " + err.Error(), RetrievalHint: "repair the local Deja instruction registry and refresh context", Source: instructionResolutionSource}}
		return out, nil
	}
	out.Version = result.Revision
	for _, item := range result.Applicable {
		instruction := adaptInstruction(item)
		out.Instructions = append(out.Instructions, instruction)
		out.Explain[instruction.ID] = explanation(instruction, result.Revision, "applicable", item.Reason)
	}
	for _, item := range result.Conditional {
		instruction := adaptInstruction(item)
		out.Explain[instruction.ID] = explanation(instruction, result.Revision, "conditional", item.Reason)
		severity := "advisory"
		if required(item.Rule) {
			severity = "required"
		}
		out.Gaps = append(out.Gaps, Gap{Subject: "instruction scope: " + item.Rule.ID, Severity: severity, Reason: item.Reason, RetrievalHint: "supply the missing instruction scope and resolve context again", Source: instructionResolutionSource})
	}
	for _, item := range result.Excluded {
		instruction := adaptInstruction(item)
		out.Explain[instruction.ID] = explanation(instruction, result.Revision, "excluded", item.Reason)
		if item.Rule.Status == "candidate" && required(item.Rule) {
			out.Gaps = append(out.Gaps, Gap{Subject: "proposed instruction: " + item.Rule.ID, Severity: "required", Reason: "rule is not approved for activation", RetrievalHint: "review and approve the rule with deja instructions apply --approve", Source: instructionResolutionSource})
		}
		if successor := strings.TrimPrefix(item.Reason, "explicitly superseded by "); successor != item.Reason {
			out.Conflicts = append(out.Conflicts, Conflict{Subject: "instruction: " + item.Rule.ID, Candidates: []string{item.Rule.ID, successor}, Resolution: successor, Reason: "explicit_supersession", Source: instructionResolutionSource})
		}
	}
	for _, conflict := range result.Conflicts {
		out.Conflicts = append(out.Conflicts, Conflict{Subject: "instruction: " + conflict.Key, Candidates: append([]string(nil), conflict.RuleIDs...), Resolution: "unresolved", Reason: "conflicting applicable instruction rules", Source: instructionResolutionSource})
		out.Gaps = append(out.Gaps, Gap{Subject: "instruction conflict: " + conflict.Key, Severity: "required", Reason: "applicable approved instructions conflict", RetrievalHint: "obtain an owner-approved supersession or scoped exception", Source: instructionResolutionSource})
	}
	sort.Slice(out.Instructions, func(i, j int) bool { return out.Instructions[i].ID < out.Instructions[j].ID })
	sort.Slice(out.Gaps, func(i, j int) bool { return out.Gaps[i].Subject < out.Gaps[j].Subject })
	sort.Slice(out.Conflicts, func(i, j int) bool { return out.Conflicts[i].Subject < out.Conflicts[j].Subject })
	return out, nil
}

func ExplainInstruction(resolution InstructionResolution, id string) (InstructionExplanation, bool) {
	explanation, ok := resolution.Explain[id]
	return explanation, ok
}

// NormalizeCheckpointInstructions validates agent-provided structured rules and
// records the identity to which a non-global rule was checkpointed. Call this
// at checkpoint time; later refreshes use ResolveCheckpointInstructions so a
// task or repository switch becomes an explicit exclusion or scope gap instead
// of rewriting the original instruction's applicability.
func NormalizeCheckpointInstructions(input []Instruction, id Identity) ([]Instruction, error) {
	out := append([]Instruction(nil), input...)
	seen := make(map[string]bool, len(out))
	for i := range out {
		rule := &out[i]
		if rule.RegistrySource != "" {
			return nil, fmt.Errorf("checkpoint instruction %q cannot claim registry provenance", rule.ID)
		}
		if err := validateInstructionShape(*rule); err != nil {
			return nil, err
		}
		if seen[rule.ID] {
			return nil, fmt.Errorf("duplicate instruction id %q", rule.ID)
		}
		seen[rule.ID] = true
		switch rule.Scope {
		case "global", "organization":
			if rule.ScopeTarget != "" {
				return nil, fmt.Errorf("%s instruction %q cannot have scope_target", rule.Scope, rule.ID)
			}
		case "project":
			if id.ProjectID == "" {
				return nil, fmt.Errorf("project instruction %q needs project identity", rule.ID)
			}
			if rule.ScopeTarget == "" {
				rule.ScopeTarget = id.ProjectID
			}
			if rule.ScopeTarget != id.ProjectID {
				return nil, fmt.Errorf("project instruction %q is not bound to this project", rule.ID)
			}
		case "repository":
			if id.RepositoryRoot == "" || !filepathIsCleanAbsolute(id.RepositoryRoot) {
				return nil, fmt.Errorf("repository instruction %q needs repository identity", rule.ID)
			}
			if rule.ScopeTarget == "" {
				rule.ScopeTarget = id.RepositoryRoot
			}
			if !filepathIsCleanAbsolute(rule.ScopeTarget) || rule.ScopeTarget != id.RepositoryRoot {
				return nil, fmt.Errorf("repository instruction %q is not bound to this repository", rule.ID)
			}
		case "path":
			if !cleanRelativePath(rule.ScopeTarget) {
				return nil, fmt.Errorf("path instruction %q needs a clean repository-relative scope_target", rule.ID)
			}
		case "task":
			if id.TaskID == "" {
				return nil, fmt.Errorf("task instruction %q needs a task identity", rule.ID)
			}
			if rule.ScopeTarget == "" {
				rule.ScopeTarget = id.TaskID
			}
			if rule.ScopeTarget != id.TaskID {
				return nil, fmt.Errorf("task instruction %q is not bound to this task", rule.ID)
			}
		case "session":
			if strings.TrimSpace(rule.ScopeTarget) == "" {
				return nil, fmt.Errorf("session instruction %q needs scope_target", rule.ID)
			}
		}
		// A checkpoint is an agent observation, not an owner approval. Retain
		// the requested strength and scope for later registry review, but never
		// let it become a binding L0 rule until instructions apply --approve
		// records the matching rule in the native registry.
		if rule.Status == "active" {
			rule.Status = "candidate"
		}
	}
	if err := validateCheckpointSupersessionGraph(out); err != nil {
		return nil, err
	}
	return out, nil
}

// ResolveCheckpointInstructions keeps raw checkpoint proposals for future
// scope reevaluation. A checkpoint cannot activate a binding instruction:
// applicable required proposals remain explicit approval gaps, while unknown
// path and session scope produces an advisory gap for later resolution.
func ResolveCheckpointInstructions(input []Instruction, id Identity, now time.Time) (InstructionResolution, error) {
	out := InstructionResolution{Instructions: []Instruction{}, Explain: map[string]InstructionExplanation{}}
	if now.IsZero() {
		return out, fmt.Errorf("checkpoint instruction resolution requires a clock")
	}
	byID := make(map[string]Instruction, len(input))
	for _, rule := range input {
		if err := validateInstructionShape(rule); err != nil {
			return out, err
		}
		if _, exists := byID[rule.ID]; exists {
			return out, fmt.Errorf("duplicate instruction id %q", rule.ID)
		}
		byID[rule.ID] = rule
	}
	if err := validateCheckpointSupersessionGraph(input); err != nil {
		return out, err
	}
	for _, rule := range input {
		disposition, reason := checkpointDisposition(rule, id, now)
		out.Explain[rule.ID] = explanation(rule, rule.Version, disposition, reason)
		switch disposition {
		case "conditional":
			out.Gaps = append(out.Gaps, Gap{Subject: "instruction scope: " + rule.ID, Severity: "advisory", Reason: reason, RetrievalHint: "supply the missing instruction scope and resolve context again", Source: instructionResolutionSource})
		case "candidate":
			if requiredInstruction(rule) {
				out.Gaps = append(out.Gaps, Gap{Subject: "proposed instruction: " + rule.ID, Severity: "required", Reason: reason, RetrievalHint: "review and approve the matching rule with deja instructions apply --approve", Source: instructionResolutionSource})
			}
		}
	}
	sort.Slice(out.Gaps, func(i, j int) bool { return out.Gaps[i].Subject < out.Gaps[j].Subject })
	sort.Slice(out.Conflicts, func(i, j int) bool { return out.Conflicts[i].Subject < out.Conflicts[j].Subject })
	return out, nil
}

// ReconcileInstructions drops only registry-derived materialized values from a
// prior snapshot. Raw checkpoint rules are re-resolved against the new identity
// and registry rules are reread, so a deleted/revoked registry rule cannot be
// resurrected by a refresh.
func ReconcileInstructions(previous []Instruction, registry InstructionResolution, id Identity, now time.Time) (InstructionResolution, error) {
	raw := make([]Instruction, 0, len(previous))
	for _, rule := range previous {
		if rule.RegistrySource == "" {
			raw = append(raw, rule)
		}
	}
	checkpoint, err := ResolveCheckpointInstructions(raw, id, now)
	if err != nil {
		return InstructionResolution{}, err
	}
	out := InstructionResolution{Version: registry.Version, Gaps: append(checkpoint.Gaps, registry.Gaps...), Conflicts: append(checkpoint.Conflicts, registry.Conflicts...), Explain: map[string]InstructionExplanation{}}
	for key, value := range checkpoint.Explain {
		out.Explain[key] = value
	}
	rawByID := make(map[string]Instruction, len(raw))
	for _, rule := range raw {
		rawByID[rule.ID] = rule
	}
	approvedMatch := make(map[string]bool)
	for _, rule := range registry.Instructions {
		proposal, exists := rawByID[rule.ID]
		if !exists {
			continue
		}
		if sameInstructionSemantics(proposal, rule) {
			approvedMatch[rule.ID] = true
			out.Gaps = withoutProposedInstructionGap(out.Gaps, rule.ID)
			continue
		}
		out.Conflicts = append(out.Conflicts, Conflict{Subject: "instruction identity: " + rule.ID, Candidates: []string{"checkpoint:" + rule.ID, "registry:" + rule.ID}, Resolution: "unresolved", Reason: "checkpoint proposal and approved registry rule have different semantics", Source: instructionResolutionSource})
		out.Gaps = upsertGap(out.Gaps, Gap{Subject: "proposed instruction: " + rule.ID, Severity: "required", Reason: "an approved rule uses this id with different semantics", RetrievalHint: "give the proposal a distinct id or obtain explicit supersession", Source: instructionResolutionSource})
	}
	out.Instructions = append(out.Instructions, registry.Instructions...)
	for key, value := range registry.Explain {
		if _, differs := rawByID[key]; differs && !approvedMatch[key] {
			out.Explain["proposal:"+key] = out.Explain[key]
		}
		if approvedMatch[key] {
			value.ResolutionReason += "; matches checkpoint proposal from " + rawByID[key].Source
		}
		out.Explain[key] = value
	}
	sort.Slice(out.Instructions, func(i, j int) bool { return out.Instructions[i].ID < out.Instructions[j].ID })
	sort.Slice(out.Gaps, func(i, j int) bool { return out.Gaps[i].Subject < out.Gaps[j].Subject })
	sort.Slice(out.Conflicts, func(i, j int) bool { return out.Conflicts[i].Subject < out.Conflicts[j].Subject })
	return out, nil
}

func sameInstructionSemantics(a, b Instruction) bool {
	return a.Text == b.Text && a.Priority == b.Priority && a.Absolute == b.Absolute && a.Scope == b.Scope && a.ScopeTarget == b.ScopeTarget && a.Retention == b.Retention && sameTime(a.EffectiveFrom, b.EffectiveFrom) && sameTime(a.EffectiveUntil, b.EffectiveUntil) && sameStringSet(a.Supersedes, b.Supersedes) && a.ConflictKey == b.ConflictKey && a.ConflictValue == b.ConflictValue
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, value := range a {
		seen[value]++
	}
	for _, value := range b {
		seen[value]--
		if seen[value] < 0 {
			return false
		}
	}
	return true
}

func withoutProposedInstructionGap(gaps []Gap, id string) []Gap {
	subject := "proposed instruction: " + id
	out := gaps[:0]
	for _, gap := range gaps {
		if gap.Subject != subject {
			out = append(out, gap)
		}
	}
	return out
}

func instructionContext(id Identity, now time.Time) instructions.Context {
	return instructions.Context{Repository: id.RepositoryRoot, Worktree: id.Worktree, Task: id.TaskID, Now: now}
}

func adaptInstruction(item instructions.Selection) Instruction {
	rule := item.Rule
	return Instruction{
		ID:             rule.ID,
		Version:        rule.Revision,
		Text:           rule.Text,
		Priority:       priority(rule),
		Absolute:       rule.Absolute,
		Scope:          nativeScope(rule.Scope),
		ScopeTarget:    nativeScopeTarget(rule.Scope),
		Retention:      retention(rule.Lifetime),
		Status:         rule.Status,
		EffectiveFrom:  rule.EffectiveFrom,
		EffectiveUntil: rule.ExpiresAt,
		Supersedes:     append([]string(nil), rule.Supersedes...),
		ConflictKey:    rule.ConflictKey,
		ConflictValue:  rule.Value,
		Source:         rule.Source,
		RegistrySource: fmt.Sprintf("deja://instruction/%s@%d", rule.ID, rule.Revision),
		Authority:      rule.Authority,
		Reason:         item.Reason,
	}
}

func explanation(instruction Instruction, version uint64, disposition, reason string) InstructionExplanation {
	return InstructionExplanation{Instruction: instruction, RegistryVersion: version, Disposition: disposition, ResolutionReason: reason}
}

func required(rule instructions.Rule) bool { return rule.Absolute || rule.Strength != "should" }

func priority(rule instructions.Rule) string {
	if rule.Absolute {
		return "absolute"
	}
	if rule.Strength == "should" {
		return "preferred"
	}
	return "required"
}

func retention(lifetime string) string {
	switch lifetime {
	case "persistent":
		return "permanent"
	case "task":
		return "task"
	case "session":
		return "ephemeral"
	default:
		return "durable"
	}
}

func nativeScope(scope instructions.Scope) string {
	switch {
	case scope.Session != "":
		return "session"
	case scope.Task != "":
		return "task"
	case scope.PathPrefix != "":
		return "path"
	case scope.Repository != "" || scope.Worktree != "":
		return "repository"
	default:
		return "global"
	}
}

func nativeScopeTarget(scope instructions.Scope) string {
	switch nativeScope(scope) {
	case "session":
		return scope.Session
	case "task":
		return scope.Task
	case "path":
		return scope.PathPrefix
	case "repository":
		if scope.Worktree != "" {
			return scope.Worktree
		}
		return scope.Repository
	default:
		return ""
	}
}

func validateInstructionShape(rule Instruction) error {
	if !validInstructionID(rule.ID) || strings.TrimSpace(rule.Text) == "" || strings.TrimSpace(rule.Source) == "" {
		return fmt.Errorf("instruction %q needs an id, text, and source", rule.ID)
	}
	if rule.Version == 0 {
		return fmt.Errorf("instruction %q needs a positive version", rule.ID)
	}
	if !oneOfInstruction(rule.Priority, "absolute", "required", "preferred", "advisory") || !oneOfInstruction(rule.Scope, "global", "organization", "project", "repository", "path", "task", "session") || !oneOfInstruction(rule.Retention, "permanent", "durable", "project", "task", "ephemeral") || !oneOfInstruction(rule.Status, "candidate", "active", "superseded", "revoked") {
		return fmt.Errorf("instruction %q has invalid priority, scope, retention, or status", rule.ID)
	}
	if rule.Absolute != (rule.Priority == "absolute") {
		return fmt.Errorf("instruction %q must use absolute priority exactly when absolute is true", rule.ID)
	}
	if rule.EffectiveFrom != nil && rule.EffectiveUntil != nil && !rule.EffectiveFrom.Before(*rule.EffectiveUntil) {
		return fmt.Errorf("instruction %q has an invalid effective range", rule.ID)
	}
	if (rule.ConflictKey == "") != (rule.ConflictValue == "") || (rule.ConflictKey != "" && !validInstructionID(rule.ConflictKey)) {
		return fmt.Errorf("instruction %q needs valid paired conflict metadata", rule.ID)
	}
	seen := make(map[string]bool, len(rule.Supersedes))
	for _, id := range rule.Supersedes {
		if !validInstructionID(id) || id == rule.ID || seen[id] {
			return fmt.Errorf("instruction %q has invalid supersedes metadata", rule.ID)
		}
		seen[id] = true
	}
	return nil
}

func checkpointDisposition(rule Instruction, id Identity, now time.Time) (string, string) {
	if rule.Status == "superseded" || rule.Status == "revoked" {
		return "excluded", rule.Status
	}
	if rule.EffectiveFrom != nil && now.Before(*rule.EffectiveFrom) {
		return "excluded", "not effective yet"
	}
	if rule.EffectiveUntil != nil && !now.Before(*rule.EffectiveUntil) {
		return "excluded", "expired"
	}
	switch rule.Scope {
	case "global", "organization":
		return "candidate", "checkpoint instructions require native registry approval"
	case "project":
		if id.ProjectID == "" {
			return "conditional", "unknown: project"
		}
		if id.ProjectID != rule.ScopeTarget {
			return "excluded", "project mismatch"
		}
	case "repository":
		if id.RepositoryRoot == "" {
			return "conditional", "unknown: repository"
		}
		if id.RepositoryRoot != rule.ScopeTarget {
			return "excluded", "repository mismatch"
		}
	case "task":
		if id.TaskID == "" {
			return "conditional", "unknown: task"
		}
		if id.TaskID != rule.ScopeTarget {
			return "excluded", "task mismatch"
		}
	case "path", "session":
		return "conditional", "unknown: " + rule.Scope
	}
	return "candidate", "checkpoint instructions require native registry approval"
}

func requiredInstruction(rule Instruction) bool {
	return rule.Priority == "absolute" || rule.Priority == "required"
}

func validInstructionID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for i, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '.' && r != '_' && r != ':' && r != '-' {
			return false
		}
		if i == 0 && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// A checkpoint may name a future registry rule as a supersession target, so
// only edges whose targets are in this checkpoint participate in cycle
// validation. Any complete cycle in the local record is rejected before it can
// reach materialization.
func validateCheckpointSupersessionGraph(rules []Instruction) error {
	byID := make(map[string]Instruction, len(rules))
	for _, rule := range rules {
		byID[rule.ID] = rule
	}
	const (
		unseen = iota
		visiting
		finished
	)
	state := make(map[string]int, len(byID))
	var visit func(string) error
	visit = func(id string) error {
		switch state[id] {
		case visiting:
			return fmt.Errorf("instruction supersession cycle includes %q", id)
		case finished:
			return nil
		}
		state[id] = visiting
		for _, next := range byID[id].Supersedes {
			if _, known := byID[next]; !known {
				continue
			}
			if err := visit(next); err != nil {
				return err
			}
		}
		state[id] = finished
		return nil
	}
	for id := range byID {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func oneOfInstruction(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

func filepathIsCleanAbsolute(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value
}
func cleanRelativePath(value string) bool {
	return value != "" && !filepath.IsAbs(value) && filepath.Clean(value) == value && value != "." && value != ".." && !strings.HasPrefix(value, ".."+string(filepath.Separator)) && path.Clean(value) == value
}
