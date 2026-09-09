package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/vshulcz/deja-vu/internal/ctxcache"
)

var ctxCacheCommands = map[string]bool{"resume": true, "refresh": true, "checkpoint": true, "status": true, "diff": true, "explain": true, "invalidate": true, "history": true, "lookup": true}

func isCtxCacheCommand(s string) bool { return ctxCacheCommands[s] }

type ctxOptions struct {
	workspace, task, layer, item, query, state string
	budget                                     int
	json                                       bool
}

func parseCtxOptions(args []string) (ctxOptions, error) {
	var o ctxOptions
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--json":
			o.json = true
		case "--workspace", "--task", "--layer", "--item", "--query", "--state", "--budget":
			if i+1 >= len(args) {
				return o, fmt.Errorf("%s needs a value", a)
			}
			i++
			v := args[i]
			switch a {
			case "--workspace":
				o.workspace = v
			case "--task":
				o.task = v
			case "--layer":
				o.layer = v
			case "--item":
				o.item = v
			case "--query":
				o.query = v
			case "--state":
				o.state = v
			case "--budget":
				n, e := strconv.Atoi(v)
				if e != nil || n < 1 {
					return o, fmt.Errorf("--budget needs a positive integer")
				}
				o.budget = n
			}
		default:
			return o, fmt.Errorf("unknown ctx option %q", a)
		}
	}
	return o, nil
}

func runCtxCache(indexDir string, args []string, in io.Reader, out io.Writer) error {
	action := args[0]
	o, err := parseCtxOptions(args[1:])
	if err != nil {
		return err
	}
	id, err := ctxcache.ResolveIdentity(o.workspace, o.task)
	if err != nil {
		return err
	}
	root := ctxcache.Root(indexDir)
	write := func(v any) error {
		b, e := json.MarshalIndent(v, "", "  ")
		if e == nil {
			_, e = fmt.Fprintf(out, "%s\n", b)
		}
		return e
	}
	switch action {
	case "resume":
		r, e := ctxcache.Resume(root, id, o.budget)
		if e != nil {
			return e
		}
		return write(r)
	case "status":
		s, e := ctxcache.Inspect(root, id)
		if e != nil {
			return e
		}
		return write(s)
	case "refresh":
		previous, _ := ctxcache.Load(root, id)
		s, c, e := ctxcache.Refresh(root, id)
		if e != nil {
			return e
		}
		return write(map[string]any{"previous_snapshot": previous.ID, "new_snapshot": s.ID, "detected_changes": c, "refreshed_layers": []string{"identity", "freshness"}, "context": s})
	case "checkpoint":
		var b []byte
		if o.state != "" {
			b = []byte(o.state)
		} else {
			b, err = io.ReadAll(in)
			if err != nil {
				return err
			}
		}
		if len(strings.TrimSpace(string(b))) == 0 {
			return fmt.Errorf("ctx checkpoint needs structured JSON on stdin or in --state")
		}
		var state ctxcache.State
		if err = json.Unmarshal(b, &state); err != nil {
			return fmt.Errorf("decode checkpoint state: %w", err)
		}
		s, e := ctxcache.Checkpoint(root, id, state)
		if e != nil {
			return e
		}
		return write(s)
	case "history":
		h, e := ctxcache.History(root, id)
		if e != nil {
			return e
		}
		return write(h)
	case "diff":
		h, e := ctxcache.History(root, id)
		if e != nil {
			return e
		}
		if len(h) < 2 {
			return fmt.Errorf("ctx diff needs at least two snapshots")
		}
		return write(ctxcache.Diff(h[1], h[0]))
	case "invalidate":
		e := ctxcache.Invalidate(root, id, o.layer)
		if e != nil {
			return e
		}
		return write(map[string]any{"invalidated": valueOr(o.layer, "all")})
	case "explain":
		if o.item == "" {
			return fmt.Errorf("ctx explain needs --item")
		}
		s, e := ctxcache.Load(root, id)
		if e != nil {
			return e
		}
		item, section, ok := ctxcache.FindItem(s, o.item)
		if !ok {
			return fmt.Errorf("context item %q not found", o.item)
		}
		return write(map[string]any{"item": item, "section": section, "why": "active item from the current checkpoint for this workspace and task"})
	case "lookup":
		if strings.TrimSpace(o.query) == "" {
			return fmt.Errorf("ctx lookup needs --query")
		}
		ctxcache.RecordLookup(root)
		return cmdCtx(indexDir, []string{"--", o.query})
	default:
		return errors.New("unknown ctx subcommand")
	}
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
