package instructions

import (
	"fmt"
	"sort"
	"strings"
)

type Selection struct {
	Rule   Rule   `json:"rule"`
	Reason string `json:"reason"`
}
type Conflict struct {
	Key     string   `json:"key"`
	RuleIDs []string `json:"rule_ids"`
}
type Result struct {
	Revision    uint64      `json:"revision"`
	Applicable  []Selection `json:"applicable"`
	Conditional []Selection `json:"conditional"`
	Excluded    []Selection `json:"excluded"`
	Conflicts   []Conflict  `json:"conflicts"`
}

// Resolve has no clock, store, search-score or agent dependency. Mandatory
// instructions accumulate. Unknown scope stays conditional, not inapplicable.
func Resolve(s Snapshot, c Context) (Result, error) {
	out := Result{Revision: s.Revision, Applicable: []Selection{}, Conditional: []Selection{}, Excluded: []Selection{}, Conflicts: []Conflict{}}
	if err := s.Validate(); err != nil {
		return out, err
	}
	if c.Now.IsZero() {
		return out, fmt.Errorf("resolution requires an explicit clock")
	}
	if c.Path != "" && !relativePath(c.Path) {
		return out, fmt.Errorf("context path must be clean and repository-relative")
	}
	for _, p := range []string{c.Repository, c.Worktree} {
		if p != "" && !absolutePath(p) {
			return out, fmt.Errorf("context roots must be clean absolute paths")
		}
	}
	grants := make(map[string]Exception)
	for _, e := range s.Exceptions {
		if c.Task == "" || e.Task != c.Task || !c.Now.Before(e.ExpiresAt) {
			continue
		}
		key := fmt.Sprintf("%s@%d", e.RuleID, e.RuleRevision)
		if old, ok := grants[key]; !ok || e.ID < old.ID {
			grants[key] = e
		}
	}
	for _, r := range s.Rules {
		item := Selection{Rule: r}
		if r.Status != "active" {
			item.Reason = r.Status
			out.Excluded = append(out.Excluded, item)
			continue
		}
		if r.EffectiveFrom != nil && c.Now.Before(*r.EffectiveFrom) {
			item.Reason = "not effective yet"
			out.Excluded = append(out.Excluded, item)
			continue
		}
		if r.ExpiresAt != nil && !c.Now.Before(*r.ExpiresAt) {
			item.Reason = "expired"
			out.Excluded = append(out.Excluded, item)
			continue
		}
		state, reason := match(r.Scope, c)
		item.Reason = reason
		if state == "excluded" {
			out.Excluded = append(out.Excluded, item)
			continue
		}
		if e, ok := grants[fmt.Sprintf("%s@%d", r.ID, r.Revision)]; ok {
			item.Reason = "exception: " + e.ID
			out.Excluded = append(out.Excluded, item)
			continue
		}
		if state == "conditional" {
			out.Conditional = append(out.Conditional, item)
		} else {
			out.Applicable = append(out.Applicable, item)
		}
	}
	sortSelections(out.Applicable)
	sortSelections(out.Conditional)
	var supersessionConflicts []Conflict
	out.Applicable, out.Excluded, supersessionConflicts = applySupersession(out.Applicable, out.Excluded)
	out.Conflicts = append(out.Conflicts, supersessionConflicts...)
	groups := make(map[string][]Rule)
	for _, item := range out.Applicable {
		if item.Rule.ConflictKey != "" {
			groups[item.Rule.ConflictKey] = append(groups[item.Rule.ConflictKey], item.Rule)
		}
	}
	required := make(map[string]bool)
	// Grouping by explicit decision value avoids pairwise comparisons at volume.
	for key, rs := range groups {
		must, not := make(map[string][]string), make(map[string][]string)
		for _, r := range rs {
			switch r.Strength {
			case "must":
				must[r.Value] = append(must[r.Value], r.ID)
				required[key] = true
			case "must_not":
				not[r.Value] = append(not[r.Value], r.ID)
				required[key] = true
			}
		}
		ids := make(map[string]bool)
		for value, yes := range must {
			if len(must) > 1 || len(not[value]) > 0 {
				for _, id := range yes {
					ids[id] = true
				}
			}
			for _, id := range not[value] {
				ids[id] = true
			}
		}
		if len(ids) > 0 {
			conflict := Conflict{Key: key}
			for id := range ids {
				conflict.RuleIDs = append(conflict.RuleIDs, id)
			}
			sort.Strings(conflict.RuleIDs)
			out.Conflicts = append(out.Conflicts, conflict)
		}
	}
	kept := make([]Selection, 0, len(out.Applicable))
	defaults := make(map[string]Rule)
	ties := make(map[string]map[string]bool)
	for _, item := range out.Applicable {
		r := item.Rule
		if r.Strength != "should" || r.ConflictKey == "" {
			kept = append(kept, item)
			continue
		}
		if required[r.ConflictKey] {
			item.Reason = "required constraint controls this key"
			out.Excluded = append(out.Excluded, item)
			continue
		}
		winner, ok := defaults[r.ConflictKey]
		if !ok {
			defaults[r.ConflictKey] = r
			kept = append(kept, item)
			continue
		}
		if authority(winner) == authority(r) && specificity(winner.Scope) == specificity(r.Scope) && winner.Priority == r.Priority && winner.Value != r.Value {
			if ties[r.ConflictKey] == nil {
				ties[r.ConflictKey] = map[string]bool{winner.ID: true}
			}
			ties[r.ConflictKey][r.ID] = true
			kept = append(kept, item)
			continue
		}
		item.Reason = "default ordered below: " + winner.ID
		out.Excluded = append(out.Excluded, item)
	}
	for key, ids := range ties {
		conflict := Conflict{Key: key}
		for id := range ids {
			conflict.RuleIDs = append(conflict.RuleIDs, id)
		}
		sort.Strings(conflict.RuleIDs)
		out.Conflicts = append(out.Conflicts, conflict)
	}
	out.Applicable = kept
	sortSelections(out.Excluded)
	sort.Slice(out.Conflicts, func(i, j int) bool { return out.Conflicts[i].Key < out.Conflicts[j].Key })
	return out, nil
}

// applySupersession only removes a rule when one, applicable active rule names
// it as its successor. Several successors are an explicit unresolved conflict:
// guessing a winner would make an approved instruction disappear silently.
func applySupersession(applicable, excluded []Selection) ([]Selection, []Selection, []Conflict) {
	byID := make(map[string]Selection, len(applicable))
	for _, item := range applicable {
		byID[item.Rule.ID] = item
	}
	successors := make(map[string][]string)
	for _, item := range applicable {
		for _, target := range item.Rule.Supersedes {
			if _, ok := byID[target]; ok {
				successors[target] = append(successors[target], item.Rule.ID)
			}
		}
	}
	drop := make(map[string]string)
	var conflicts []Conflict
	for target, ids := range successors {
		sort.Strings(ids)
		if len(ids) == 1 {
			drop[target] = ids[0]
			continue
		}
		conflicts = append(conflicts, Conflict{Key: "supersession:" + target, RuleIDs: append([]string{target}, ids...)})
	}
	kept := make([]Selection, 0, len(applicable))
	for _, item := range applicable {
		if successor, ok := drop[item.Rule.ID]; ok {
			item.Reason = "explicitly superseded by " + successor
			excluded = append(excluded, item)
			continue
		}
		kept = append(kept, item)
	}
	sortSelections(kept)
	return kept, excluded, conflicts
}

func match(s Scope, c Context) (string, string) {
	missing := []string{}
	for _, p := range []struct{ name, want, got string }{{"repository", s.Repository, c.Repository}, {"worktree", s.Worktree, c.Worktree}, {"task", s.Task, c.Task}, {"session", s.Session, c.Session}, {"environment", s.Environment, c.Environment}} {
		if p.want == "" {
			continue
		}
		if p.got == "" {
			missing = append(missing, p.name)
		} else if p.want != p.got {
			return "excluded", p.name + " mismatch"
		}
	}
	if s.PathPrefix != "" {
		if c.Path == "" {
			missing = append(missing, "path")
		} else if c.Path != s.PathPrefix && !strings.HasPrefix(c.Path, s.PathPrefix+"/") {
			return "excluded", "path mismatch"
		}
	}
	if len(s.Tools) > 0 {
		if c.Tool == "" {
			missing = append(missing, "tool")
		} else if !oneOf(c.Tool, s.Tools...) {
			return "excluded", "tool mismatch"
		}
	}
	if len(missing) > 0 {
		return "conditional", "unknown: " + strings.Join(missing, ", ")
	}
	return "applicable", "all selectors match"
}
func authority(r Rule) int {
	switch r.Authority {
	case "owner":
		return 2
	case "project":
		return 1
	}
	return 0
}
func specificity(s Scope) int {
	n := 0
	for _, v := range []string{s.Repository, s.Worktree, s.Task, s.Session, s.Environment, s.PathPrefix} {
		if v != "" {
			n++
		}
	}
	if len(s.Tools) > 0 {
		n++
	}
	return n
}
func before(a, b Rule) bool {
	if (a.Strength == "should") != (b.Strength == "should") {
		return a.Strength != "should"
	}
	if a.Absolute != b.Absolute {
		return a.Absolute
	}
	if authority(a) != authority(b) {
		return authority(a) > authority(b)
	}
	if specificity(a.Scope) != specificity(b.Scope) {
		return specificity(a.Scope) > specificity(b.Scope)
	}
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	return a.ID < b.ID
}
func sortSelections(items []Selection) {
	sort.Slice(items, func(i, j int) bool { return before(items[i].Rule, items[j].Rule) })
}
