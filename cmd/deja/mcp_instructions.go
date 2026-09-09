package main

import (
	"strings"

	"github.com/vshulcz/deja-vu/internal/index"
)

// mcpInstructions is returned from the MCP initialize handshake. Hosts that
// support the field put it in the system prompt, which gives us an auto-recall
// channel on harnesses that have no hooks of their own.
func mcpInstructions(dir string) string {
	var b strings.Builder
	b.WriteString("At the start of substantive work, call the listed `deja` tool with `mode: \"ctx_resume\"` for the current workspace and active task when known. ")
	b.WriteString("Treat applicable absolute and required instructions in that context as binding, and use the returned working state before independently reconstructing project history. ")
	b.WriteString("Use historical recall or other source retrieval only for a required gap, stale or invalid context, source-of-truth verification, or evidence the packet lacks. ")
	b.WriteString("After meaningful durable progress, call the listed `deja` tool with `mode: \"ctx_checkpoint\"` and confirmed findings, decisions, implementation state, failures, test results, unresolved questions, and next actions; never checkpoint private reasoning. ")
	b.WriteString("deja indexes this user's past sessions across every AI coding tool they use. ")
	b.WriteString("Use the deja recall mode when one of those allowed historical-retrieval cases needs past-session evidence, including a prior error or decision.")
	if s := readWarmupStatus(dir); s != nil {
		b.WriteString(" The index is still building (")
		b.WriteString(s.progress())
		b.WriteString("); recall works now but covers more history as it finishes.")
	} else if warmupJustRequested(dir) && index.HasManifest(dir) {
		// A harness with no auto-recall wiring reads this and nothing else. A
		// rebuild that has not reported yet left it with the ordinary
		// instructions and an index this build cannot read (#879).
		b.WriteString(" The index is being rebuilt right now; recall covers more history once it finishes.")
	}
	return b.String()
}
