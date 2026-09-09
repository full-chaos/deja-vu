package instructions

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const Help = `deja instructions (experimental; opt-in advisory delivery)
  example                         print a starter registry
  apply --file FILE --expect N --approve
                                  approve a registry revision with compare-and-swap
  export                          print current registry for editing
  resolve [--repo ROOT] [--worktree ROOT] [--task ID] [--session ID]
          [--environment NAME] [--tool NAME] [--path RELATIVE] [--json]
  hook --agent claude|codex        read a lifecycle event on stdin
  install --agent claude|codex     attach approved instruction context to deja hooks

All commands accept --store ABSOLUTE_FILE. Default: DEJA_INSTRUCTIONS_FILE,
then XDG_CONFIG_HOME/deja/instructions.json, then ~/.config/deja/instructions.json.
Task/environment default to DEJA_TASK_ID and DEJA_ENVIRONMENT, never prompt text.
Runbooks are delivered, not executed or certified. See docs/instruction-memory.md.
`

func DefaultPath() (string, error) {
	p := os.Getenv("DEJA_INSTRUCTIONS_FILE")
	if p == "" {
		root := os.Getenv("XDG_CONFIG_HOME")
		if root == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			root = filepath.Join(home, ".config")
		}
		p = filepath.Join(root, "deja", "instructions.json")
	}
	if !absolutePath(p) {
		return "", fmt.Errorf("store location must be a clean absolute path")
	}
	return p, nil
}
func Run(args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 || oneOf(args[0], "help", "--help", "-h") {
		_, err := io.WriteString(out, Help)
		return err
	}
	name := args[0]
	if !oneOf(name, "example", "apply", "export", "resolve", "hook", "install") {
		return fmt.Errorf("unknown instructions command %q", name)
	}
	f := flag.NewFlagSet("instructions "+name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	storePath := f.String("store", "", "approved registry file")
	var file, expected, agent, config, binary string
	var approve, asJSON bool
	var c Context
	budget := DefaultBudget
	switch name {
	case "apply":
		f.StringVar(&file, "file", "", "registry to approve")
		f.StringVar(&expected, "expect", "", "expected revision")
		f.BoolVar(&approve, "approve", false, "explicitly approve registry")
	case "resolve", "hook":
		f.StringVar(&c.Task, "task", os.Getenv("DEJA_TASK_ID"), "task identity")
		f.StringVar(&c.Environment, "environment", os.Getenv("DEJA_ENVIRONMENT"), "environment")
		if name == "hook" {
			f.StringVar(&agent, "agent", "", "claude or codex")
		} else {
			f.StringVar(&c.Repository, "repo", "", "main repository root")
			f.StringVar(&c.Worktree, "worktree", "", "checkout root")
			f.StringVar(&c.Session, "session", "", "session identity")
			f.StringVar(&c.Tool, "tool", "", "exact hook tool name")
			f.StringVar(&c.Path, "path", "", "relative path")
			f.BoolVar(&asJSON, "json", false, "resolution explanations")
			f.IntVar(&budget, "budget", DefaultBudget, "context byte budget")
		}
	case "install":
		f.StringVar(&agent, "agent", "", "claude or codex")
		f.StringVar(&config, "config", "", "absolute config path")
		f.StringVar(&binary, "binary", "", "absolute deja executable")
	}
	if err := f.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			_, err = io.WriteString(out, Help)
		}
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", f.Arg(0))
	}
	if name == "example" {
		return json.NewEncoder(out).Encode(Snapshot{Version: Version, Rules: []Rule{{ID: "work.preserve-unrelated", Revision: 1, Kind: "constraint", Text: "Preserve unrelated work. Do not discard changes outside the task.", Authority: "owner", Status: "active", Strength: "must", Absolute: true, Lifetime: "persistent", Source: "Example: review and explicitly approve before installation."}}})
	}
	if *storePath == "" {
		p, err := DefaultPath()
		if err != nil {
			return err
		}
		*storePath = p
	}
	if !absolutePath(*storePath) {
		return fmt.Errorf("--store must be a clean absolute path")
	}
	store := FileStore{Path: *storePath}
	switch name {
	case "apply":
		if !approve || file == "" || expected == "" {
			return fmt.Errorf("apply requires --file, --expect and --approve")
		}
		n, err := strconv.ParseUint(expected, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid expected revision: %w", err)
		}
		b, err := readBounded(file, MaxRegistryBytes)
		if err != nil {
			return err
		}
		var next Snapshot
		if err = decode(bytes.NewReader(b), &next, MaxRegistryBytes, true); err != nil {
			return err
		}
		saved, err := store.Replace(n, next)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(saved)
	case "export":
		s, err := store.Load()
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(s)
	case "hook":
		return Hook(store, agent, c.Task, c.Environment, in, out, time.Now())
	case "install":
		if _, err := store.Load(); err != nil {
			return fmt.Errorf("approve a valid registry before installing hooks: %w", err)
		}
		p, err := Install(agent, config, binary, *storePath)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "Instruction hooks written to %s. Review/trust them in the agent, then start a new session. Delivery is advisory, not enforcement.\n", p)
		return err
	default:
		if c.Repository == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			repo, wt, err := Workspace(cwd)
			if err != nil {
				return err
			}
			c.Repository = repo
			if c.Worktree == "" {
				c.Worktree = wt
			}
		}
		c.Now = time.Now()
		s, err := store.Load()
		if err != nil {
			return err
		}
		result, err := Resolve(s, c)
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(out).Encode(result)
		}
		text, err := Render(result, budget)
		if err != nil {
			return err
		}
		_, err = io.WriteString(out, text)
		return err
	}
}
