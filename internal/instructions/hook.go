package instructions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type HookInput struct {
	Event   string          `json:"hook_event_name"`
	CWD     string          `json:"cwd"`
	Session string          `json:"session_id"`
	Tool    string          `json:"tool_name"`
	Input   json.RawMessage `json:"tool_input"`
}

// Hook is advisory in v0. No allow/deny decisions or compliance claims are made.
// Parse/load/resolve failures are visible, including in model context when the
// event supports it. This does not depend on the history hook's fail-open path.
func Hook(store Store, agent, task, environment string, in io.Reader, out io.Writer, now time.Time) error {
	if !oneOf(agent, "claude", "codex") {
		return fmt.Errorf("agent must be claude or codex")
	}
	var input HookInput
	if err := decode(in, &input, 1<<20, false); err != nil {
		return hookWarning(out, "", "malformed hook input: "+err.Error())
	}
	if !oneOf(input.Event, "SessionStart", "UserPromptSubmit", "PreToolUse", "SubagentStart") {
		return hookWarning(out, "", "unsupported event "+input.Event)
	}
	warn := func(err error) error { return hookWarning(out, input.Event, err.Error()) }
	repo, worktree, err := Workspace(input.CWD)
	if err != nil {
		return warn(err)
	}
	c := Context{Repository: repo, Worktree: worktree, Task: task, Environment: environment, Session: input.Session, Tool: input.Tool, Now: now}
	// Shells, MCP tools and patches can affect multiple paths. Never infer their
	// targets from command substrings. Those path selectors remain conditional.
	if oneOf(input.Tool, "Read", "Write", "Edit") {
		var args struct {
			FilePath string `json:"file_path"`
			Path     string `json:"path"`
		}
		if len(input.Input) > 0 {
			if err = json.Unmarshal(input.Input, &args); err != nil {
				return warn(fmt.Errorf("malformed file-tool arguments"))
			}
		}
		target := args.FilePath
		if target == "" {
			target = args.Path
		}
		if target != "" {
			if !filepath.IsAbs(target) {
				target = filepath.Join(input.CWD, target)
			}
			target, err = canonicalTarget(target)
			if err != nil {
				return warn(err)
			}
			rel, e := filepath.Rel(worktree, target)
			if e == nil && relativePath(filepath.ToSlash(rel)) {
				c.Path = filepath.ToSlash(rel)
			}
		}
	}
	snapshot, err := store.Load()
	if err != nil {
		return warn(fmt.Errorf("cannot load approved registry: %w", err))
	}
	result, err := Resolve(snapshot, c)
	if err != nil {
		return warn(err)
	}
	text, err := Render(result, DefaultBudget)
	if err != nil {
		return warn(err)
	}
	if text == "" {
		return nil
	}
	return json.NewEncoder(out).Encode(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": input.Event, "additionalContext": text}})
}
func hookWarning(out io.Writer, event, reason string) error {
	message := "Deja instruction context INCOMPLETE: " + reason + ". Resolve before relying on remembered instructions; this hook has not blocked the action."
	result := map[string]any{"systemMessage": message}
	if event != "" {
		result["hookSpecificOutput"] = map[string]string{"hookEventName": event, "additionalContext": message}
	}
	return json.NewEncoder(out).Encode(result)
}

// Resolve the nearest existing ancestor, including for a new file underneath a
// symlink. A dangling symlink is an error, not a lexical in-repository target.
func canonicalTarget(target string) (string, error) {
	var tail []string
	for current := filepath.Clean(target); ; current = filepath.Dir(current) {
		_, err := os.Lstat(current)
		if err == nil {
			root, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(tail) - 1; i >= 0; i-- {
				root = filepath.Join(root, tail[i])
			}
			return root, nil
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(current) == current {
			return "", err
		}
		tail = append(tail, filepath.Base(current))
	}
}

// Workspace follows Git worktree metadata without invoking git. Separate clones
// remain distinct; bare/common layouts without a main .git root are unsupported.
func Workspace(cwd string) (repository, worktree string, err error) {
	if !absolutePath(cwd) {
		return "", "", fmt.Errorf("cwd must be a clean absolute path")
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("cwd is not a directory")
	}
	for root := cwd; ; root = filepath.Dir(root) {
		git := filepath.Join(root, ".git")
		info, e := os.Stat(git)
		if e == nil {
			if !info.IsDir() {
				b, e := readBounded(git, 4096)
				if e != nil {
					return "", "", e
				}
				if !strings.HasPrefix(string(b), "gitdir: ") {
					return "", "", fmt.Errorf("invalid .git file")
				}
				git = strings.TrimSpace(strings.TrimPrefix(string(b), "gitdir: "))
				if git == "" {
					return "", "", fmt.Errorf("empty gitdir")
				}
				if !filepath.IsAbs(git) {
					git = filepath.Join(root, git)
				}
			}
			common, e := readBounded(filepath.Join(git, "commondir"), 4096)
			if errors.Is(e, os.ErrNotExist) {
				return root, root, nil
			}
			if e != nil {
				return "", "", e
			}
			dir := strings.TrimSpace(string(common))
			if dir == "" {
				return "", "", fmt.Errorf("empty git commondir")
			}
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(git, dir)
			}
			dir, e = filepath.EvalSymlinks(dir)
			if e != nil {
				return "", "", e
			}
			if filepath.Base(dir) != ".git" {
				return "", "", fmt.Errorf("unsupported bare/common Git layout")
			}
			return filepath.Dir(dir), root, nil
		}
		if !errors.Is(e, os.ErrNotExist) {
			return "", "", e
		}
		if filepath.Dir(root) == root {
			break
		}
	}
	return cwd, cwd, nil
}
