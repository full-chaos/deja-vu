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

// InstructionSuffix is the exact opt-in tail that an ordinary deja lifecycle
// hook carries. It is deliberately a shell fragment rather than an argv slice:
// Claude and Codex persist hook commands as strings. Callers must not evaluate
// it while inspecting a configuration; StripInstructionSuffix parses only this
// narrow, canonical form.
func InstructionSuffix(store, agent string) (string, error) {
	if !absolutePath(store) {
		return "", fmt.Errorf("store must be a clean absolute path")
	}
	if !oneOf(agent, "claude", "codex") {
		return "", fmt.Errorf("agent must be claude or codex")
	}
	return " --instructions-store " + shellQuote(store) + " --instructions-agent " + agent, nil
}

// WithInstructionSuffix attaches the canonical instruction-store options to a
// plain deja hook command.
func WithInstructionSuffix(command, store, agent string) (string, error) {
	suffix, err := InstructionSuffix(store, agent)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("hook command must not be empty")
	}
	return command + suffix, nil
}

// StripInstructionSuffix recognizes exactly the suffix emitted by
// WithInstructionSuffix. It does not invoke a shell or accept general shell
// syntax: the store word is a canonical single-quoted word, including the
// standard quote-escape sequence for apostrophes.
func StripInstructionSuffix(command string) (base, store, agent string, ok bool) {
	const storeFlag = " --instructions-store "
	// A legal absolute path can itself contain the flag text. Try each textual
	// occurrence and accept only one whose remaining bytes are our exact
	// canonical suffix; no shell syntax is evaluated here.
	for start := 0; start < len(command); {
		i := strings.Index(command[start:], storeFlag)
		if i < 0 {
			break
		}
		i += start
		candidateBase := command[:i]
		rest := command[i+len(storeFlag):]
		candidateStore, candidateRest, parsed := parseQuotedStore(rest)
		if parsed && strings.HasPrefix(candidateRest, " --instructions-agent ") {
			candidateAgent := strings.TrimPrefix(candidateRest, " --instructions-agent ")
			suffix, err := InstructionSuffix(candidateStore, candidateAgent)
			if err == nil && command == candidateBase+suffix {
				return candidateBase, candidateStore, candidateAgent, true
			}
		}
		start = i + len(storeFlag)
	}
	return "", "", "", false
}

// parseQuotedStore accepts the output of shellQuote only. Shell's concatenated
// single/double/single quote escape for an apostrophe is the one exception to a
// simple single-quoted word. The unconsumed suffix begins with a space.
func parseQuotedStore(s string) (value, rest string, ok bool) {
	if !strings.HasPrefix(s, "'") {
		return "", "", false
	}
	s = s[1:]
	for {
		i := strings.IndexByte(s, '\'')
		if i < 0 {
			return "", "", false
		}
		value += s[:i]
		s = s[i+1:]
		if strings.HasPrefix(s, "\"'\"'") {
			value += "'"
			s = s[4:]
			continue
		}
		return value, s, true
	}
}

// Install is explicitly invoked, never part of general deja install. It augments
// the existing lifecycle hooks and does not bypass review/trust requirements.
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
	if err = mergeHooks(data, binary, store, agent); err != nil {
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
func mergeHooks(config map[string]any, binary, store, agent string) error {
	hooks := map[string]any{}
	if existing, ok := config["hooks"]; ok {
		var valid bool
		hooks, valid = existing.(map[string]any)
		if !valid || hooks == nil {
			return fmt.Errorf("hooks must be a JSON object")
		}
	}
	shared := map[string]string{
		"SessionStart":     "hook-context",
		"UserPromptSubmit": "hook-prompt",
		"PreToolUse":       "hook-tool",
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
		var adopted bool
		var appendIntegrated bool
		var standalone bool
		if sub, sharedEvent := shared[event]; sharedEvent {
			standalone = false
			_ = sub
		} else {
			standalone = true
		}
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
				if handler["statusMessage"] == hookMarker && markedStandaloneInstructionHook(cmd, binary) {
					removed = true
					continue
				}
				if sub, sharedEvent := shared[event]; sharedEvent && handler["type"] == "command" {
					kind, _ := sharedHookKindOf(cmd, binary, sub)
					if kind == sharedHookWrapper {
						return fmt.Errorf("cannot safely integrate instructions into wrapped %s hook", event)
					}
					if kind == sharedHookOwned {
						if adopted {
							removed = true
							continue
						}
						adopted = true
						if event == "PreToolUse" && groupHasForeignHandlers(handlers) {
							// A matcher governs the whole group. Leave the reader's
							// group intact and put the combined command in an unfiltered
							// group below.
							removed = true
							appendIntegrated = true
							continue
						}
						// The explicit binary is part of this installer contract. An
						// older deja/deja-hook command may not understand the opt-in
						// flags, so adopt its ownership but point at the validated
						// executable supplied for this install.
						command, e := WithInstructionSuffix(sharedBase(binary, sub), store, agent)
						if e != nil {
							return e
						}
						handler["command"] = command
						if agent == "codex" {
							handler["additionalContextLimit"] = 0
						}
						if event == "PreToolUse" {
							delete(group, "matcher")
						}
					}
				}
				kept = append(kept, h)
			}
			if removed && len(kept) == 0 {
				continue
			}
			group["hooks"] = kept
			clean = append(clean, group)
		}
		if standalone {
			command := shellQuote(binary) + " instructions hook --agent " + shellQuote(agent) + " --store " + shellQuote(store)
			handler := instructionHandler(command, agent)
			clean = append(clean, map[string]any{"hooks": []any{handler}})
		} else if !adopted || appendIntegrated {
			sub := shared[event]
			command, e := WithInstructionSuffix(sharedBase(binary, sub), store, agent)
			if e != nil {
				return e
			}
			clean = append(clean, map[string]any{"hooks": []any{instructionHandler(command, agent)}})
		}
		hooks[event] = clean
	}
	config["hooks"] = hooks
	return nil
}

func instructionHandler(command, agent string) map[string]any {
	handler := map[string]any{"type": "command", "command": command, "timeout": 5, "statusMessage": hookMarker}
	// Codex otherwise summarizes large additionalContext. We already enforce an
	// explicit byte budget; approved mandatory text must not be summarized away.
	if agent == "codex" {
		handler["additionalContextLimit"] = 0
	}
	return handler
}

type sharedCommandKind uint8

const (
	sharedHookOther sharedCommandKind = iota
	sharedHookOwned
	sharedHookWrapper
)

// sharedHookKind recognizes only a bare deja/deja-hook command, or the exact
// executable supplied to Install. Anything that merely contains one is a
// wrapper the reader owns; adding another command beside it would fire twice,
// so the caller rejects that ambiguity instead.
func sharedHookKindOf(command, binary, sub string) (sharedCommandKind, bool) {
	base, _, _, integrated := StripInstructionSuffix(command)
	if integrated {
		command = base
	}
	if token, ok := bareHookToken(command, sub); ok && knownDejaToken(token, binary) {
		return sharedHookOwned, integrated
	}
	if strings.Contains(command, " "+sub) && strings.Contains(command, "deja") {
		return sharedHookWrapper, false
	}
	return sharedHookOther, false
}

func sharedBase(binary, sub string) string { return shellQuote(binary) + " " + sub }

// markedStandaloneInstructionHook identifies the old installer line only when
// it is a bare command deja could itself have written. A status message is
// reader-visible text, not an ownership marker for an arbitrary shell chain.
func markedStandaloneInstructionHook(command, binary string) bool {
	exe, rest, ok := parseBareShellToken(command)
	if !ok || !knownDejaToken(exe, binary) {
		return false
	}
	const agentFlag = " instructions hook --agent "
	if !strings.HasPrefix(rest, agentFlag) {
		return false
	}
	agent, rest, ok := parseQuotedStore(strings.TrimPrefix(rest, agentFlag))
	if !ok || !oneOf(agent, "claude", "codex") {
		return false
	}
	const storeFlag = " --store "
	if !strings.HasPrefix(rest, storeFlag) {
		return false
	}
	store, rest, ok := parseQuotedStore(strings.TrimPrefix(rest, storeFlag))
	return ok && rest == "" && absolutePath(store)
}

// bareHookToken extracts the executable from a two-word command without
// interpreting shell syntax. The quoted case accepts only shellQuote's form;
// everything with a wrapper, redirect, assignment, or extra argument is left
// to its owner.
func bareHookToken(command, sub string) (string, bool) {
	want := " " + sub
	if !strings.HasSuffix(command, want) {
		return "", false
	}
	token, rest, ok := parseBareShellToken(strings.TrimSuffix(command, want))
	return token, ok && rest == ""
}

// parseBareShellToken accepts exactly one shell word: an unquoted path with no
// shell metacharacters, or shellQuote's single-quoted form. It returns the
// unconsumed command text (which starts with a space when present) without ever
// evaluating it.
func parseBareShellToken(command string) (token, rest string, ok bool) {
	if command == "" {
		return "", "", false
	}
	if strings.HasPrefix(command, "'") {
		return parseQuotedStore(command)
	}
	i := strings.IndexAny(command, " \t")
	if i < 0 {
		token = command
	} else {
		token, rest = command[:i], command[i:]
	}
	if token == "" || strings.ContainsAny(token, "'\";|&<>`$\r\n\\*?[]{}()!~#=") {
		return "", "", false
	}
	return token, rest, true
}

// IsBareHookCommand reports whether command is exactly one executable token
// followed by sub. It accepts the canonical single-quoted token that
// shellQuote emits, but does not parse or evaluate arbitrary shell syntax.
func IsBareHookCommand(command, sub string) bool {
	_, ok := bareHookToken(command, sub)
	return ok
}

func knownDejaToken(token, binary string) bool {
	if token == binary {
		return true
	}
	base := token
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.ToLower(base)
	return base == "deja" || base == "deja-hook"
}

func groupHasForeignHandlers(handlers []any) bool {
	return len(handlers) != 1
}
