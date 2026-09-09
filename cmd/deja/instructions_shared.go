package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vshulcz/deja-vu/internal/instructions"
)

// runSharedInstructionHook is selected only by explicitly installed flags. The
// payload is read once and both resolvers run in this process. Ordinary hooks
// retain their existing fast path, output formats and budgets.
func runSharedInstructionHook(dir, event string, args []string, stdin io.Reader, stdout io.Writer) (bool, error) {
	enabled := false
	for _, arg := range args {
		if arg == "--instructions-store" || arg == "--instructions-agent" || strings.HasPrefix(arg, "--instructions-store=") || strings.HasPrefix(arg, "--instructions-agent=") {
			enabled = true
		}
	}
	if !enabled {
		return false, nil
	}
	f := flag.NewFlagSet("shared instruction hook", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	store := f.String("instructions-store", "", "approved registry")
	agent := f.String("instructions-agent", "", "claude or codex")
	var plain, once bool
	f.BoolVar(&plain, "plain", false, "plain output")
	f.BoolVar(&once, "once", false, "once per session")
	if err := f.Parse(args); err != nil {
		return true, err
	}
	if f.NArg() != 0 || !filepath.IsAbs(*store) || filepath.Clean(*store) != *store || (*agent != "claude" && *agent != "codex") {
		return true, fmt.Errorf("shared instructions require a clean absolute --instructions-store and --instructions-agent claude|codex")
	}
	raw := readHookPayload(stdin, hookStdinWait)
	var approved bytes.Buffer
	recall := sharedRecallOutput{budget: instructions.DefaultBudget}
	err := instructions.Hook(instructions.FileStore{Path: *store}, *agent, os.Getenv("DEJA_TASK_ID"), os.Getenv("DEJA_ENVIRONMENT"), bytes.NewReader(raw), &approved, time.Now())
	if err != nil {
		return true, err
	}
	var packet struct {
		SystemMessage string `json:"systemMessage"`
		Specific      struct {
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if approved.Len() > 0 {
		if err := json.Unmarshal(approved.Bytes(), &packet); err != nil {
			return true, err
		}
	}
	text := packet.Specific.Context
	if text == "" {
		text = packet.SystemMessage
	}
	if text != "" {
		recall.budget -= min(len(text), instructions.DefaultBudget) + 1
	}
	switch event {
	case "SessionStart":
		err = runHookContextIO(dir, false, once, bytes.NewReader(raw), &recall)
	case "UserPromptSubmit":
		err = runHookPromptMode(dir, bytes.NewReader(raw), &recall, false)
	case "PreToolUse":
		err = runHookToolMode(dir, bytes.NewReader(raw), &recall, hookToolClaude)
	default:
		return true, fmt.Errorf("unsupported shared hook event %q", event)
	}
	if err != nil {
		return true, err
	}
	if recall.omitted && recall.Len() == 0 {
		if err := json.NewEncoder(&recall).Encode(map[string]string{"systemMessage": sharedRecallOmitted}); err != nil {
			return true, err
		}
	}
	return true, emitSharedInstructionContext(event, recall.Bytes(), approved.Bytes(), stdout, plain)
}

// Required instructions get the context budget first. Recall is omitted as a
// whole if it does not fit: neither approved rules nor a framed recall block is
// cut in half. Preserve other hook fields, including tool-input updates and UI
// messages, while emitting exactly one response.
func emitSharedInstructionContext(event string, recall, approved []byte, out io.Writer, plain bool) error {
	response := map[string]any{}
	if len(bytes.TrimSpace(recall)) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(recall))
		decoder.UseNumber()
		if err := decoder.Decode(&response); err != nil {
			return err
		}
	}
	instruction := map[string]any{}
	if len(bytes.TrimSpace(approved)) > 0 {
		if err := json.Unmarshal(approved, &instruction); err != nil {
			return err
		}
	}
	specific, _ := response["hookSpecificOutput"].(map[string]any)
	if specific == nil {
		specific = map[string]any{}
	}
	extra, _ := instruction["hookSpecificOutput"].(map[string]any)
	ruleText, _ := extra["additionalContext"].(string)
	history, _ := specific["additionalContext"].(string)
	notice, _ := response["systemMessage"].(string)
	warning, _ := instruction["systemMessage"].(string)
	if ruleText == "" && warning != "" {
		ruleText = warning
	}
	if len(ruleText) > instructions.DefaultBudget {
		ruleText = "Deja instruction context INCOMPLETE: warning exceeds the context budget. Resolve the registry before relying on remembered instructions; this hook has not blocked the action."
		warning = ruleText
	}
	context := ruleText
	if history != "" {
		joined := joinNotes(ruleText, history)
		if len(joined) <= instructions.DefaultBudget {
			context = joined
		} else {
			notice = joinNotes(notice, sharedRecallOmitted)
		}
	}
	notice = joinNotes(warning, notice)
	if notice != "" {
		response["systemMessage"] = notice
	}
	if context != "" || len(specific) > 0 {
		specific["hookEventName"] = event
		specific["additionalContext"] = context
		response["hookSpecificOutput"] = specific
	}
	if plain {
		_, err := io.WriteString(out, context)
		return err
	}
	if len(response) == 0 {
		return nil
	}
	return json.NewEncoder(out).Encode(response)
}

const sharedRecallOmitted = "Deja omitted recalled history to keep approved instructions within the shared context budget."

// Admission happens before a history producer records or deduplicates a block.
// Otherwise an omitted block would be recorded as served and disappear on the
// next prompt even after there was room to deliver it.
type sharedRecallOutput struct {
	bytes.Buffer
	budget   int
	omitted  bool
	nudgeDir string
}

func allowHookContext(out io.Writer, text string) bool {
	if shared, ok := out.(*sharedRecallOutput); ok {
		if len(text) > shared.budget {
			shared.omitted = true
			return false
		}
		if shared.nudgeDir != "" && strings.Contains(text, failureNudgeText) {
			markNudge(shared.nudgeDir)
			shared.nudgeDir = ""
		}
	}
	return true
}

func failureNudgeForOutput(dir, prompt string, out io.Writer) string {
	shared, ok := out.(*sharedRecallOutput)
	if !ok {
		return failureNudge(dir, prompt)
	}
	if !reportsFailure(prompt) || !nudgeAvailable(dir) {
		return ""
	}
	shared.nudgeDir = dir
	return failureNudgeText
}
