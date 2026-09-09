package instructions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const hookMarker = "Loading approved Deja instructions"

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// Install is explicitly invoked, never part of general deja install. It neither
// alters history hooks nor bypasses the agent's review/trust requirements.
func Install(agent, config, binary, store string) (result string, err error) {
	if !oneOf(agent, "claude", "codex") {
		return "", fmt.Errorf("agent must be claude or codex")
	}
	if runtime.GOOS == "windows" {
		return "", fmt.Errorf("experimental hook installer supports macOS/Linux only")
	}
	if !absolutePath(store) {
		return "", fmt.Errorf("store must be a clean absolute path")
	}
	if binary == "" {
		binary, err = os.Executable()
		if err != nil {
			return "", err
		}
	}
	if !absolutePath(binary) {
		return "", fmt.Errorf("binary must be a clean absolute path")
	}
	info, err := os.Stat(binary)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("binary is not an executable regular file")
	}
	if config == "" {
		variable, folder, filename := "CLAUDE_CONFIG_DIR", ".claude", "settings.json"
		if agent == "codex" {
			variable, folder, filename = "CODEX_HOME", ".codex", "hooks.json"
		}
		root := os.Getenv(variable)
		if root == "" {
			home, e := os.UserHomeDir()
			if e != nil {
				return "", e
			}
			root = filepath.Join(home, folder)
		}
		config = filepath.Join(root, filename)
	}
	if !absolutePath(config) {
		return "", fmt.Errorf("config must be a clean absolute path")
	}
	if config == store {
		return "", fmt.Errorf("hook configuration and instruction registry must be different files")
	}
	if err = os.MkdirAll(filepath.Dir(config), 0700); err != nil {
		return "", err
	}
	lock := config + ".deja-instructions.lock"
	if err = os.Mkdir(lock, 0700); err != nil {
		return "", fmt.Errorf("cannot lock config: %w", err)
	}
	defer func() { err = errors.Join(err, os.Remove(lock)) }()
	data := map[string]any{}
	old, readErr := readBounded(config, MaxRegistryBytes)
	if readErr == nil {
		info, e := os.Lstat(config)
		if e != nil {
			return "", e
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("config must be a regular file, not a symlink")
		}
		if err = decode(bytes.NewReader(old), &data, MaxRegistryBytes, true); err != nil {
			return "", err
		}
		if data == nil {
			return "", fmt.Errorf("config must be a JSON object")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return "", readErr
	}
	command := shellQuote(binary) + " instructions hook --agent " + shellQuote(agent) + " --store " + shellQuote(store)
	if err = mergeHooks(data, command, agent); err != nil {
		return "", err
	}
	updated, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", err
	}
	updated = append(updated, '\n')
	if len(updated) > MaxRegistryBytes {
		return "", fmt.Errorf("updated hook config exceeds byte limit")
	}
	if bytes.Equal(old, updated) {
		return config, nil
	}
	if len(old) > 0 {
		backup, e := os.CreateTemp(filepath.Dir(config), filepath.Base(config)+".deja-instructions-backup-*")
		if e != nil {
			return "", e
		}
		_, w := backup.Write(old)
		e = errors.Join(w, backup.Sync(), backup.Close())
		if e != nil {
			return "", e
		}
	}
	if err = atomicWrite(config, updated); err != nil {
		return "", err
	}
	return config, nil
}
func mergeHooks(config map[string]any, command, agent string) error {
	hooks := map[string]any{}
	if existing, ok := config["hooks"]; ok {
		var valid bool
		hooks, valid = existing.(map[string]any)
		if !valid || hooks == nil {
			return fmt.Errorf("hooks must be a JSON object")
		}
	}
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "SubagentStart"} {
		groups := []any{}
		if existing, ok := hooks[event]; ok {
			var valid bool
			groups, valid = existing.([]any)
			if !valid {
				return fmt.Errorf("%s hooks must be an array", event)
			}
		}
		clean := []any{}
		for _, value := range groups {
			group, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid %s matcher group", event)
			}
			handlers, ok := group["hooks"].([]any)
			if !ok {
				return fmt.Errorf("invalid %s handler array", event)
			}
			kept := []any{}
			removed := false
			for _, h := range handlers {
				handler, ok := h.(map[string]any)
				if !ok {
					return fmt.Errorf("invalid %s handler", event)
				}
				cmd, _ := handler["command"].(string)
				if handler["statusMessage"] == hookMarker && strings.Contains(cmd, " instructions hook --agent ") {
					removed = true
					continue
				}
				kept = append(kept, h)
			}
			if removed && len(kept) == 0 {
				continue
			}
			group["hooks"] = kept
			clean = append(clean, group)
		}
		handler := map[string]any{"type": "command", "command": command, "timeout": 5, "statusMessage": hookMarker}
		// Codex otherwise summarizes large additionalContext. We already enforce an
		// explicit byte budget; approved mandatory text must not be summarized away.
		if agent == "codex" {
			handler["additionalContextLimit"] = 0
		}
		clean = append(clean, map[string]any{"hooks": []any{handler}})
		hooks[event] = clean
	}
	config["hooks"] = hooks
	return nil
}
