// Package instructions resolves approved operating instructions independently of
// ranked conversation recall. It does not infer authority or invoke a model.
package instructions

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const Version = 1
const MaxRegistryBytes = 8 << 20
const DefaultBudget = 8000

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,127}$`)
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Snapshot struct {
	Version    int         `json:"version"`
	Revision   uint64      `json:"revision"`
	Rules      []Rule      `json:"rules"`
	Exceptions []Exception `json:"exceptions,omitempty"`
}

type Rule struct {
	ID            string     `json:"id"`
	Revision      uint64     `json:"revision"`
	Kind          string     `json:"kind"`
	Text          string     `json:"text"`
	Authority     string     `json:"authority"`
	Status        string     `json:"status"`
	Strength      string     `json:"strength"`
	Absolute      bool       `json:"absolute"`
	Priority      int        `json:"priority"`
	Scope         Scope      `json:"scope"`
	Lifetime      string     `json:"lifetime"`
	EffectiveFrom *time.Time `json:"effective_from,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	Supersedes    []string   `json:"supersedes,omitempty"`
	Source        string     `json:"source"`
	ConflictKey   string     `json:"conflict_key,omitempty"`
	Value         string     `json:"value,omitempty"`
	Runbook       *Runbook   `json:"runbook,omitempty"`
}

type Scope struct {
	Repository  string   `json:"repository,omitempty"`
	Worktree    string   `json:"worktree,omitempty"`
	Task        string   `json:"task,omitempty"`
	Session     string   `json:"session,omitempty"`
	Environment string   `json:"environment,omitempty"`
	PathPrefix  string   `json:"path_prefix,omitempty"`
	Tools       []string `json:"tools,omitempty"`
}

type Runbook struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// ApprovedBy is provenance, not authentication. The protected/owner-controlled
// registry is the trust boundary. Exceptions never follow a new rule revision.
type Exception struct {
	ID           string    `json:"id"`
	RuleID       string    `json:"rule_id"`
	RuleRevision uint64    `json:"rule_revision"`
	Task         string    `json:"task"`
	ExpiresAt    time.Time `json:"expires_at"`
	ApprovedBy   string    `json:"approved_by"`
	Reason       string    `json:"reason"`
}

type Context struct {
	Repository  string    `json:"repository,omitempty"`
	Worktree    string    `json:"worktree,omitempty"`
	Task        string    `json:"task,omitempty"`
	Session     string    `json:"session,omitempty"`
	Environment string    `json:"environment,omitempty"`
	Path        string    `json:"path,omitempty"`
	Tool        string    `json:"tool,omitempty"`
	Now         time.Time `json:"-"`
}

func oneOf(s string, choices ...string) bool {
	for _, c := range choices {
		if s == c {
			return true
		}
	}
	return false
}
func cleanText(s string) bool {
	return strings.TrimSpace(s) != "" && !strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\t'
	})
}
func scalar(s string) bool       { return cleanText(s) && !strings.ContainsAny(s, "\n\t\r") }
func absolutePath(s string) bool { return scalar(s) && filepath.IsAbs(s) && filepath.Clean(s) == s }
func relativePath(s string) bool {
	return scalar(s) && s != "." && !strings.ContainsAny(s, `\:`) && !path.IsAbs(s) && path.Clean(s) == s && s != ".." && !strings.HasPrefix(s, "../")
}

func (s Snapshot) Validate() error {
	if s.Version != Version {
		return fmt.Errorf("unsupported instructions version %d", s.Version)
	}
	if len(s.Rules) > 10000 || len(s.Exceptions) > 10000 {
		return fmt.Errorf("registry exceeds 10000 records per collection")
	}
	rules := make(map[string]Rule, len(s.Rules))
	for _, r := range s.Rules {
		if _, ok := rules[r.ID]; ok {
			return fmt.Errorf("duplicate rule %q", r.ID)
		}
		if err := r.validate(); err != nil {
			return fmt.Errorf("rule %q: %w", r.ID, err)
		}
		rules[r.ID] = r
	}
	for _, r := range s.Rules {
		seenSupersedes := make(map[string]bool, len(r.Supersedes))
		for _, id := range r.Supersedes {
			if !identifier.MatchString(id) || id == r.ID || seenSupersedes[id] {
				return fmt.Errorf("rule %q has invalid supersession target %q", r.ID, id)
			}
			predecessor, ok := rules[id]
			if !ok {
				return fmt.Errorf("rule %q supersedes unknown rule %q", r.ID, id)
			}
			if !canSupersede(r, predecessor) {
				return fmt.Errorf("rule %q cannot weaken superseded rule %q", r.ID, id)
			}
			seenSupersedes[id] = true
		}
	}
	if err := validateSupersessionGraph(rules); err != nil {
		return err
	}
	seen := make(map[string]bool, len(s.Exceptions))
	for _, e := range s.Exceptions {
		r, ok := rules[e.RuleID]
		if !identifier.MatchString(e.ID) || seen[e.ID] || !ok || e.RuleRevision == 0 || e.RuleRevision > r.Revision || !scalar(e.Task) || e.ExpiresAt.IsZero() || !scalar(e.ApprovedBy) || !cleanText(e.Reason) {
			return fmt.Errorf("invalid or duplicate exception %q", e.ID)
		}
		if r.Absolute && e.RuleRevision == r.Revision && e.ApprovedBy != "owner" {
			return fmt.Errorf("absolute rule %q requires owner approval", r.ID)
		}
		seen[e.ID] = true
	}
	return nil
}

// Supersession is a directed replacement relation. A cycle would make every
// member both obsolete and authoritative, so reject it at registry approval
// time instead of letting resolution discard binding rules.
func validateSupersessionGraph(rules map[string]Rule) error {
	const (
		unseen = iota
		visiting
		finished
	)
	state := make(map[string]int, len(rules))
	var visit func(string) error
	visit = func(id string) error {
		switch state[id] {
		case visiting:
			return fmt.Errorf("supersession cycle includes rule %q", id)
		case finished:
			return nil
		}
		state[id] = visiting
		for _, next := range rules[id].Supersedes {
			if err := visit(next); err != nil {
				return err
			}
		}
		state[id] = finished
		return nil
	}
	for id := range rules {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

// canSupersede rejects a registry relationship that would waive a protected
// rule. An explicit successor may replace a peer or strengthen it, but cannot
// use a preference or lower authority to disable an absolute, mandatory, or
// owner-authored constraint.
func canSupersede(successor, predecessor Rule) bool {
	if predecessor.Absolute && !successor.Absolute {
		return false
	}
	if predecessor.Strength != "should" && successor.Strength == "should" {
		return false
	}
	return predecessor.Authority != "owner" || successor.Authority == "owner"
}

func (r Rule) validate() error {
	if !identifier.MatchString(r.ID) || r.Revision == 0 {
		return fmt.Errorf("id and positive revision required")
	}
	if !cleanText(r.Text) || len(r.Text) > 32768 || !cleanText(r.Source) {
		return fmt.Errorf("bounded instruction text and provenance required")
	}
	if !oneOf(r.Kind, "constraint", "preference", "decision", "procedure") || !oneOf(r.Authority, "owner", "project", "inferred") || !oneOf(r.Status, "candidate", "active", "superseded", "revoked") || !oneOf(r.Strength, "must", "must_not", "should") {
		return fmt.Errorf("invalid kind, authority, status, or strength")
	}
	if r.Authority == "inferred" && r.Status == "active" {
		return fmt.Errorf("inferred records cannot be active")
	}
	if (r.Absolute && r.Strength == "should") || (r.Kind == "preference" && r.Strength != "should") || r.Priority < -1000 || r.Priority > 1000 {
		return fmt.Errorf("invalid absolute, strength, or priority combination")
	}
	for _, p := range []string{r.Scope.Repository, r.Scope.Worktree} {
		if p != "" && !absolutePath(p) {
			return fmt.Errorf("repository and worktree require clean absolute paths")
		}
	}
	for _, v := range []string{r.Scope.Task, r.Scope.Session, r.Scope.Environment} {
		if v != "" && !scalar(v) {
			return fmt.Errorf("scope identities must be nonempty single lines")
		}
	}
	if r.Scope.PathPrefix != "" && (r.Scope.Repository == "" || !relativePath(r.Scope.PathPrefix)) {
		return fmt.Errorf("path_prefix requires repository and clean relative path")
	}
	for _, t := range r.Scope.Tools {
		if !scalar(t) || strings.ContainsAny(t, "*|") {
			return fmt.Errorf("tools must be exact hook names")
		}
	}
	switch r.Lifetime {
	case "persistent":
		if r.ExpiresAt != nil {
			return fmt.Errorf("persistent rules cannot expire")
		}
	case "task":
		if r.Scope.Task == "" {
			return fmt.Errorf("task lifetime requires task scope")
		}
	case "session":
		if r.Scope.Session == "" {
			return fmt.Errorf("session lifetime requires session scope")
		}
	case "ttl":
		if r.ExpiresAt == nil || r.ExpiresAt.IsZero() {
			return fmt.Errorf("ttl requires expires_at")
		}
	default:
		return fmt.Errorf("invalid lifetime")
	}
	if r.EffectiveFrom != nil && r.ExpiresAt != nil && !r.EffectiveFrom.Before(*r.ExpiresAt) {
		return fmt.Errorf("effective_from must precede expires_at")
	}
	if (r.ConflictKey == "") != (r.Value == "") || (r.ConflictKey != "" && (!identifier.MatchString(r.ConflictKey) || !scalar(r.Value))) {
		return fmt.Errorf("conflict_key and value must be valid and supplied together")
	}
	if r.Kind == "procedure" && (r.Runbook == nil || r.Strength != "must") {
		return fmt.Errorf("procedure requires pinned runbook and must strength")
	}
	if r.Runbook != nil && (r.Kind != "procedure" || !absolutePath(r.Runbook.Path) || !digest.MatchString(r.Runbook.SHA256)) {
		return fmt.Errorf("runbook requires absolute path and lowercase SHA256")
	}
	return nil
}
