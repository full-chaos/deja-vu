// Package ctxcache implements Deja's local, agent-independent working-context
// cache. It deliberately has no dependency on the historical search index:
// resume is a filesystem cache operation, while lookup remains an explicit
// fallback owned by the caller.
package ctxcache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Identity struct {
	WorkspaceID    string `json:"workspace_id"`
	Repository     string `json:"repository"`
	RepositoryRoot string `json:"repository_root"`
	Worktree       string `json:"worktree"`
	Branch         string `json:"branch,omitempty"`
	GitHead        string `json:"git_head,omitempty"`
	WorktreeState  string `json:"worktree_state,omitempty"`
	TaskID         string `json:"task_id,omitempty"`
	ProjectID      string `json:"project_id,omitempty"`
}

type Item struct {
	ID         string   `json:"id"`
	Text       string   `json:"text"`
	Status     string   `json:"status,omitempty"`
	Durability string   `json:"durability,omitempty"`
	Source     string   `json:"source"`
	Supports   []string `json:"supports,omitempty"`
}

type Gap struct {
	Subject       string `json:"subject"`
	Severity      string `json:"severity"`
	Reason        string `json:"reason,omitempty"`
	RetrievalHint string `json:"retrieval_hint,omitempty"`
}

type Conflict struct {
	Subject    string   `json:"subject"`
	Candidates []string `json:"candidates"`
	Resolution string   `json:"resolution"`
	Reason     string   `json:"reason,omitempty"`
}

type State struct {
	Objective   string         `json:"objective,omitempty"`
	Status      string         `json:"status,omitempty"`
	Project     map[string]any `json:"project,omitempty"`
	Confirmed   []Item         `json:"confirmed,omitempty"`
	Implemented []Item         `json:"implemented,omitempty"`
	Failing     []Item         `json:"failing,omitempty"`
	Unknown     []Item         `json:"unknown,omitempty"`
	Decisions   []Item         `json:"decisions,omitempty"`
	Tests       map[string]any `json:"tests,omitempty"`
	NextActions []Item         `json:"next_actions,omitempty"`
	Session     []Item         `json:"session,omitempty"`
	Evidence    []Item         `json:"evidence,omitempty"`
	Gaps        []Gap          `json:"gaps,omitempty"`
	Conflicts   []Conflict     `json:"conflicts,omitempty"`
}

type Freshness struct {
	SnapshotVersion uint64    `json:"snapshot_version"`
	GitHead         string    `json:"git_head,omitempty"`
	WorktreeState   string    `json:"worktree_state,omitempty"`
	Checkpoint      uint64    `json:"checkpoint_version"`
	GeneratedAt     time.Time `json:"generated_at"`
}

type Snapshot struct {
	ID          string    `json:"snapshot_id"`
	Fingerprint string    `json:"fingerprint"`
	Identity    Identity  `json:"identity"`
	State       State     `json:"state"`
	Freshness   Freshness `json:"freshness"`
	Invalid     []string  `json:"invalid_layers,omitempty"`
}

type ResumeResult struct {
	CacheStatus string   `json:"cache_status"`
	Snapshot    Snapshot `json:"context"`
	Truncated   bool     `json:"truncated,omitempty"`
}

type Status struct {
	Status          string   `json:"status"`
	SnapshotID      string   `json:"snapshot_id,omitempty"`
	Fingerprint     string   `json:"fingerprint,omitempty"`
	Changed         []string `json:"changed,omitempty"`
	RefreshRequired []string `json:"refresh_required,omitempty"`
	Metrics         Metrics  `json:"metrics"`
}

type Metrics struct {
	ResumeTotal       uint64 `json:"ctx_resume_total"`
	ResumeCacheHits   uint64 `json:"ctx_resume_cache_hit_total"`
	ResumeCacheMisses uint64 `json:"ctx_resume_cache_miss_total"`
	RefreshTotal      uint64 `json:"ctx_refresh_total"`
	CheckpointTotal   uint64 `json:"ctx_checkpoint_total"`
	LookupTotal       uint64 `json:"ctx_lookup_total"`
	GapTotal          uint64 `json:"ctx_gap_total"`
}

type Change struct {
	Kind    string `json:"kind"`
	Section string `json:"section"`
	ID      string `json:"id"`
	Text    string `json:"text"`
}

func Root(indexDir string) string {
	if d := os.Getenv("DEJA_CTX_DIR"); d != "" {
		return d
	}
	return filepath.Join(filepath.Dir(indexDir), "ctx")
}

func ResolveIdentity(workspace, task string) (Identity, error) {
	if workspace == "" {
		workspace = "."
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return Identity{}, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err == nil {
		abs = real
	}
	root := git(abs, "rev-parse", "--show-toplevel")
	if root == "" {
		root = abs
	}
	remote := git(root, "config", "--get", "remote.origin.url")
	if remote == "" {
		remote = root
	}
	status := git(root, "status", "--porcelain=v1", "--untracked-files=normal")
	statusHash := sha256.Sum256([]byte(status))
	id := Identity{Repository: remote, RepositoryRoot: root, Worktree: abs, Branch: git(root, "branch", "--show-current"), GitHead: git(root, "rev-parse", "HEAD"), WorktreeState: hex.EncodeToString(statusHash[:]), TaskID: task, ProjectID: filepath.Base(root)}
	h := sha256.Sum256([]byte(remote + "\x00" + root))
	id.WorkspaceID = hex.EncodeToString(h[:8])
	return id, nil
}

func git(dir string, args ...string) string {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func fingerprint(id Identity) string {
	h := sha256.Sum256([]byte(strings.Join([]string{id.WorkspaceID, id.Repository, id.Branch, id.TaskID, id.ProjectID}, "\x00")))
	return hex.EncodeToString(h[:])
}

func key(id Identity) string {
	// Branch is freshness, not cache identity: keeping it out of the pointer key
	// lets a branch switch reuse project state and refresh only task/Git layers.
	h := sha256.Sum256([]byte(id.WorkspaceID + "\x00" + id.TaskID))
	return hex.EncodeToString(h[:12])
}
func currentPath(root string, id Identity) string {
	return filepath.Join(root, "current", key(id)+".json")
}

func Load(root string, id Identity) (Snapshot, error) {
	b, err := os.ReadFile(currentPath(root, id))
	if err != nil {
		return Snapshot{}, err
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return Snapshot{}, fmt.Errorf("decode context snapshot: %w", err)
	}
	return s, nil
}

func save(root string, s Snapshot) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Join(root, "current"), 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, "snapshots"), 0700); err != nil {
		return err
	}
	history := filepath.Join(root, "snapshots", s.ID+".json")
	if err := writeExclusive(history, b); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Join(root, "current"), ".snapshot-*.tmp")
	if err != nil {
		_ = os.Remove(history)
		return err
	}
	tmpPath := tmp.Name()
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		_ = os.Remove(history)
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		_ = os.Remove(history)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		_ = os.Remove(history)
		return err
	}
	if err := os.Rename(tmpPath, currentPath(root, s.Identity)); err != nil {
		_ = os.Remove(tmpPath)
		_ = os.Remove(history)
		return err
	}
	return nil
}

func writeExclusive(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(b)
	return err
}

func lock(root string) (func(), error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(root, ".write.lock")
	for i := 0; i < 100; i++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("context cache is busy")
}

func Resume(root string, id Identity, budget int) (ResumeResult, error) {
	started := time.Now()
	if budget <= 0 {
		budget = 10000
	}
	s, err := Load(root, id)
	if errors.Is(err, os.ErrNotExist) {
		r := ResumeResult{CacheStatus: "miss", Snapshot: emptySnapshot(id)}
		truncated, trimErr := trimResult(&r, budget)
		if trimErr != nil {
			return ResumeResult{}, trimErr
		}
		r.Truncated = truncated
		recordMetric(root, "resume_miss", time.Since(started), len(r.Snapshot.State.Gaps))
		return r, nil
	}
	if err != nil {
		return ResumeResult{}, err
	}
	status := "hit"
	if len(changes(s, id)) > 0 || len(s.Invalid) > 0 {
		status = "stale"
	}
	r := ResumeResult{CacheStatus: status, Snapshot: s}
	truncated, err := trimResult(&r, budget)
	if err != nil {
		return ResumeResult{}, err
	}
	r.Truncated = truncated
	recordMetric(root, "resume_"+status, time.Since(started), len(r.Snapshot.State.Gaps))
	return r, nil
}

func emptySnapshot(id Identity) Snapshot {
	return Snapshot{Fingerprint: fingerprint(id), Identity: id, State: State{Status: "unknown", Gaps: []Gap{{Subject: "working context", Severity: "required", Reason: "no local checkpoint exists", RetrievalHint: "checkpoint durable state or use historical lookup"}}}}
}

func Checkpoint(root string, id Identity, state State) (Snapshot, error) {
	if err := validateState(state); err != nil {
		return Snapshot{}, err
	}
	unlock, err := lock(root)
	if err != nil {
		return Snapshot{}, err
	}
	defer unlock()
	previous, err := Load(root, id)
	if errors.Is(err, os.ErrNotExist) {
		previous = Snapshot{}
	} else if err != nil {
		return Snapshot{}, fmt.Errorf("load previous checkpoint: %w", err)
	}
	version := previous.Freshness.SnapshotVersion + 1
	checkpoint := previous.Freshness.Checkpoint + 1
	now := time.Now().UTC()
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", key(id), version, now.UnixNano())))
	s := Snapshot{ID: hex.EncodeToString(sum[:12]), Fingerprint: fingerprint(id), Identity: id, State: normalize(state), Freshness: Freshness{SnapshotVersion: version, GitHead: id.GitHead, WorktreeState: id.WorktreeState, Checkpoint: checkpoint, GeneratedAt: now}}
	err = save(root, s)
	if err == nil {
		recordMetric(root, "checkpoint", 0, len(s.State.Gaps))
	}
	return s, err
}

func validateState(s State) error {
	seen := map[string]bool{}
	sets := map[string][]Item{"confirmed": s.Confirmed, "implemented": s.Implemented, "failing": s.Failing, "unknown": s.Unknown, "decisions": s.Decisions, "next_actions": s.NextActions, "session": s.Session, "evidence": s.Evidence}
	for section, items := range sets {
		for _, item := range items {
			if strings.TrimSpace(item.Text) == "" {
				return fmt.Errorf("%s item %q needs text", section, item.ID)
			}
			if item.ID != "" && seen[item.ID] {
				return fmt.Errorf("duplicate context item id %q", item.ID)
			}
			seen[item.ID] = item.ID != ""
		}
	}
	for _, gap := range s.Gaps {
		if strings.TrimSpace(gap.Subject) == "" || strings.TrimSpace(gap.Severity) == "" {
			return fmt.Errorf("each gap needs subject and severity")
		}
	}
	for _, conflict := range s.Conflicts {
		if strings.TrimSpace(conflict.Subject) == "" || len(conflict.Candidates) < 2 || strings.TrimSpace(conflict.Resolution) == "" {
			return fmt.Errorf("each conflict needs a subject, at least two candidates, and a resolution")
		}
	}
	return nil
}

func Refresh(root string, id Identity) (Snapshot, []string, error) {
	unlock, lockErr := lock(root)
	if lockErr != nil {
		return Snapshot{}, nil, lockErr
	}
	defer unlock()
	s, err := Load(root, id)
	if errors.Is(err, os.ErrNotExist) {
		s = emptySnapshot(id)
	} else if err != nil {
		return Snapshot{}, nil, err
	}
	c := changes(s, id)
	previousHead := s.Identity.GitHead
	s.Identity = id
	s.Fingerprint = fingerprint(id)
	s.Invalid = nil
	s.Freshness.SnapshotVersion++
	s.Freshness.GitHead = id.GitHead
	s.Freshness.WorktreeState = id.WorktreeState
	s.Freshness.GeneratedAt = time.Now().UTC()
	if contains(c, "git") || contains(c, "worktree") || contains(c, "task") {
		s.State.Gaps = upsertGap(s.State.Gaps, Gap{Subject: "working state after repository change", Severity: "required", Reason: fmt.Sprintf("repository state changed from %s to %s; Git freshness was updated but checkpoint conclusions require agent validation", short(previousHead), short(id.GitHead)), RetrievalHint: "inspect the local diff, then checkpoint confirmed task state"})
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", key(id), s.Freshness.SnapshotVersion, s.Freshness.GeneratedAt.UnixNano())))
	s.ID = hex.EncodeToString(sum[:12])
	err = save(root, s)
	if err == nil {
		recordMetric(root, "refresh", 0, len(s.State.Gaps))
	}
	return s, c, err
}

func upsertGap(gaps []Gap, gap Gap) []Gap {
	for i := range gaps {
		if gaps[i].Subject == gap.Subject {
			gaps[i] = gap
			return gaps
		}
	}
	return append(gaps, gap)
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func short(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	if value == "" {
		return "none"
	}
	return value
}

func Inspect(root string, id Identity) (Status, error) {
	s, err := Load(root, id)
	if errors.Is(err, os.ErrNotExist) {
		return Status{Status: "missing", Changed: []string{"snapshot"}, RefreshRequired: []string{"task", "git"}, Metrics: ReadMetrics(root)}, nil
	}
	if err != nil {
		return Status{}, err
	}
	c := changes(s, id)
	c = append(c, s.Invalid...)
	c = unique(c)
	st := Status{Status: "valid", SnapshotID: s.ID, Fingerprint: s.Fingerprint, Changed: c, Metrics: ReadMetrics(root)}
	if len(c) > 0 {
		st.Status = "stale"
		st.RefreshRequired = c
	}
	return st, nil
}

func changes(s Snapshot, id Identity) []string {
	var c []string
	if s.Identity.Branch != id.Branch {
		c = append(c, "task")
	}
	if s.Identity.GitHead != id.GitHead {
		c = append(c, "git")
	}
	if s.Identity.WorktreeState != id.WorktreeState {
		c = append(c, "worktree")
	}
	if s.Fingerprint != fingerprint(id) {
		c = append(c, "identity")
	}
	return unique(c)
}

func Invalidate(root string, id Identity, layer string) error {
	unlock, err := lock(root)
	if err != nil {
		return err
	}
	defer unlock()
	s, err := Load(root, id)
	if err != nil {
		return err
	}
	if layer == "" {
		layer = "all"
	}
	s.Invalid = unique(append(s.Invalid, layer))
	s.Freshness.SnapshotVersion++
	s.Freshness.GeneratedAt = time.Now().UTC()
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", key(id), s.Freshness.SnapshotVersion, s.Freshness.GeneratedAt.UnixNano())))
	s.ID = hex.EncodeToString(sum[:12])
	return save(root, s)
}

func History(root string, id Identity) ([]Snapshot, error) {
	entries, err := os.ReadDir(filepath.Join(root, "snapshots"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Snapshot
	for _, e := range entries {
		b, x := os.ReadFile(filepath.Join(root, "snapshots", e.Name()))
		if x != nil {
			continue
		}
		var s Snapshot
		if json.Unmarshal(b, &s) == nil && key(s.Identity) == key(id) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Freshness.GeneratedAt.After(out[j].Freshness.GeneratedAt) })
	return out, nil
}

func Diff(a, b Snapshot) []Change {
	var out []Change
	old := flatten(a.State)
	cur := flatten(b.State)
	for k, v := range cur {
		if old[k] != v {
			kind := "+"
			if _, ok := old[k]; ok {
				kind = "~"
			}
			parts := strings.SplitN(k, "/", 2)
			out = append(out, Change{kind, parts[0], parts[1], v})
		}
	}
	for k, v := range old {
		if _, ok := cur[k]; !ok {
			parts := strings.SplitN(k, "/", 2)
			out = append(out, Change{"-", parts[0], parts[1], v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Section+out[i].ID < out[j].Section+out[j].ID })
	return out
}
func flatten(s State) map[string]string {
	m := map[string]string{"metadata/objective": s.Objective, "metadata/status": s.Status}
	if b, err := json.Marshal(s.Tests); err == nil && string(b) != "null" {
		m["tests/results"] = string(b)
	}
	for _, gap := range s.Gaps {
		b, _ := json.Marshal(gap)
		m["gaps/"+gap.Subject] = string(b)
	}
	if b, err := json.Marshal(s.Project); err == nil && string(b) != "null" {
		m["project/context"] = string(b)
	}
	for _, conflict := range s.Conflicts {
		b, _ := json.Marshal(conflict)
		m["conflicts/"+conflict.Subject] = string(b)
	}
	sets := map[string][]Item{"confirmed": s.Confirmed, "implemented": s.Implemented, "failing": s.Failing, "unknown": s.Unknown, "decisions": s.Decisions, "next_actions": s.NextActions, "session": s.Session, "evidence": s.Evidence}
	for sec, items := range sets {
		for _, i := range items {
			m[sec+"/"+i.ID] = i.Text
		}
	}
	return m
}

func FindItem(s Snapshot, id string) (Item, string, bool) {
	for sec, items := range map[string][]Item{"confirmed": s.State.Confirmed, "implemented": s.State.Implemented, "failing": s.State.Failing, "unknown": s.State.Unknown, "decisions": s.State.Decisions, "next_actions": s.State.NextActions, "session": s.State.Session, "evidence": s.State.Evidence} {
		for _, i := range items {
			if i.ID == id {
				return i, sec, true
			}
		}
	}
	return Item{}, "", false
}

func normalize(s State) State {
	n := 0
	sets := []*[]Item{&s.Confirmed, &s.Implemented, &s.Failing, &s.Unknown, &s.Decisions, &s.NextActions, &s.Session, &s.Evidence}
	for _, set := range sets {
		for i := range *set {
			item := &(*set)[i]
			if item.ID == "" {
				n++
				item.ID = fmt.Sprintf("item-%03d", n)
			}
			if item.Source == "" {
				item.Source = "checkpoint://local"
			}
		}
	}
	return s
}

// trimResult uses a conservative four-characters-per-token estimate over the
// complete response, not only State. Required gaps and the objective stay. A
// budget too small for that irreducible packet is rejected rather than quietly
// returning an oversized response.
func trimResult(r *ResumeResult, budget int) (bool, error) {
	limit := budget * 4
	b, _ := json.Marshal(r)
	if len(b) <= limit {
		return false, nil
	}
	r.Truncated = true
	s := &r.Snapshot.State
	// Test detail is supporting evidence; explicit failing items remain until
	// every lower-priority section has been removed.
	s.Tests = nil
	s.Session = nil
	s.Evidence = nil
	s.Project = nil
	for _, set := range []*[]Item{&s.Unknown, &s.Confirmed, &s.Implemented, &s.NextActions, &s.Decisions, &s.Failing} {
		for len(*set) > 0 {
			*set = (*set)[:len(*set)-1]
			b, _ = json.Marshal(r)
			if len(b) <= limit {
				return true, nil
			}
		}
	}
	b, _ = json.Marshal(r)
	if len(b) > limit {
		return false, fmt.Errorf("token budget %d is too small for required context metadata and gaps (minimum approximately %d)", budget, (len(b)+3)/4)
	}
	return true, nil
}

func unique(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, v := range in {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

type metricEvent struct {
	Kind      string `json:"kind"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Gaps      int    `json:"gaps,omitempty"`
}

func recordMetric(root, kind string, latency time.Duration, gaps int) {
	if os.MkdirAll(root, 0700) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(root, "metrics.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(metricEvent{Kind: kind, LatencyMS: latency.Milliseconds(), Gaps: gaps})
	_, _ = f.Write(append(b, '\n'))
}

func RecordLookup(root string) { recordMetric(root, "lookup", 0, 0) }

func ReadMetrics(root string) Metrics {
	b, err := os.ReadFile(filepath.Join(root, "metrics.jsonl"))
	if err != nil {
		return Metrics{}
	}
	var m Metrics
	for _, line := range strings.Split(string(b), "\n") {
		var e metricEvent
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		switch e.Kind {
		case "resume_hit", "resume_stale", "resume_miss":
			m.ResumeTotal++
			if e.Kind == "resume_hit" {
				m.ResumeCacheHits++
			}
			if e.Kind == "resume_miss" {
				m.ResumeCacheMisses++
			}
		case "refresh":
			m.RefreshTotal++
		case "checkpoint":
			m.CheckpointTotal++
		case "lookup":
			m.LookupTotal++
		}
		m.GapTotal += uint64(e.Gaps)
	}
	return m
}
