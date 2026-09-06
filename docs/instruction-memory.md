# Instruction memory: experimental first pass

This adds `deja instructions`, a separate, opt-in subsystem inside the existing
binary. History recall remains unchanged. The new command is intentionally
absent from the general help until full repository and live-harness validation
is complete; `deja instructions help` describes the experimental interface.

## What it does

Approved instructions are resolved deterministically, not ranked alongside
transcripts. Required rules accumulate. Absolute rules do not decay or lose to
higher numeric priorities. Preferences are defaults, not permission to waive a
requirement. Explicit conflict keys detect incompatible settings; this does not
claim to detect contradictions in arbitrary prose.

The same registry can deliver context to Claude Code and Codex at SessionStart,
UserPromptSubmit, PreToolUse, and SubagentStart. SessionStart also handles
resume/compact events the harness emits. The installer preserves existing Deja
history hooks and unrelated configuration. Installation is separate from
`deja install`, and never bypasses native hook review/trust.

**This is instruction delivery, not enforcement.** Hooks emit no permission
allow/deny decisions. A delivered runbook is not evidence that it was executed;
pre-tool injection does not prove the agent reconsidered its pending call.
Failures produce an INCOMPLETE warning, plus model-visible context for supported
events, rather than silently pretending that no instructions applied.

## Try it in a scratch configuration first

Build with the repository-required Go version. The example is synthetic;
review it before approving it. These commands do not touch existing agent
configuration unless the explicit install commands are run against it.

```sh
go build -o /tmp/deja-instruction-test ./cmd/deja
/tmp/deja-instruction-test instructions example > /tmp/instruction-draft.json
# Review/edit the draft. Its starting registry revision is 0.
/tmp/deja-instruction-test instructions apply \
  --store /tmp/deja-approved-instructions.json \
  --file /tmp/instruction-draft.json --expect 0 --approve
/tmp/deja-instruction-test instructions resolve \
  --store /tmp/deja-approved-instructions.json --json
```

Use unique scratch paths when working in parallel. For a retained installation,
use your installed feature binary rather than a temporary build:

```sh
deja instructions example > instruction-draft.json
# Review before approval. This initializes only an absent registry.
deja instructions apply --file instruction-draft.json --expect 0 --approve
deja instructions install --agent claude
deja instructions install --agent codex
```

The default store is `DEJA_INSTRUCTIONS_FILE`, or
`$XDG_CONFIG_HOME/deja/instructions.json`, or
`~/.config/deja/instructions.json`. Relative store/config overrides are refused.
`install --config ABSOLUTE_FILE --binary ABSOLUTE_DEJA` supports scratch setups.
The installer currently supports macOS/Linux; core tests use portable paths.
Review/trust the generated hook definitions and start a new agent session.
Codex's `additionalContextLimit: 0` prevents its default summarization of the
already-budgeted approved context; this field is not emitted for Claude.

## Record semantics

The schema is JSON, version 1. Each rule needs `id`, positive `revision`, `kind`,
`text`, `authority`, `status`, `strength`, `lifetime`, `scope`, and `source`.
`priority` defaults to zero and `absolute` to false.

| Field | Values / meaning |
| --- | --- |
| kind | constraint, preference, decision, procedure |
| authority | owner, project, inferred; inferred records cannot be active |
| status | candidate, active, revoked |
| strength | must, must_not, should; preferences must use should |
| absolute | Required text; only a current-revision owner-approved exception waives it |
| priority | -1000 through 1000; ordering/default selection, never a waiver of a must |
| lifetime | persistent, task, session, ttl |
| scope | repository, worktree, task, session, environment, path_prefix, tools |
| source | Original provenance; preserve the original statement and reference |
| conflict_key + value | Explicit mutually-exclusive setting, such as backend=local |
| runbook | An absolute local path and lowercase SHA256 for a procedure |

Persistent means until explicitly revised/revoked and forbids an expiry.
Task/session lifetimes require their matching identity. TTL requires
`expires_at` in RFC3339. Expiry is inclusive: at that instant it no longer applies.
Task closure is NOT integrated with a task tracker in v0: clear/change the task
identity or explicitly revoke its rules. Task IDs must not be recycled.

Supply task/environment through `DEJA_TASK_ID`, `DEJA_ENVIRONMENT`, or the
corresponding command flags. Task continuity between agents requires the same
explicit task identity. It is never inferred from prompt wording.

A known scope mismatch excludes a rule. Missing task, environment, tool or path
context makes it **conditional**, not irrelevant or universally binding.
Repository/worktree roots are clean absolute paths. Hook discovery resolves
symlinks and Git linked-worktree metadata; separate clones remain distinct.
Path selectors match components (`src` does not match `src-old`). Tools use exact
canonical hook names. Shell commands, MCP operations and multi-file patches
are not guessed into single-file targets: path requirements remain conditional.

Required constraints accumulate regardless of authority or priority. For keyed
preferences only, higher authority, then more nonempty scope selectors, then
priority determine the default. An exact tied conflict is reported. Counting
scope selectors is a deliberately limited first-pass specificity model, not a
general semantic ordering of all scopes. Unkeyed prose is not conflict-checked.

An exception names `id`, `rule_id`, `rule_revision`, a nonempty `task`, an
`expires_at` deadline, `approved_by`, and a `reason`. It inherits the rule's scope,
never broadens it, and never follows a later rule revision. Retained exception
IDs cannot be mutated to broaden an existing grant; remove it to revoke it and
use a new ID for a replacement.

Procedures require `kind: procedure`, `strength: must`, and
`runbook: {"path": "/absolute/SKILL.md", "sha256": "..."}`. Applicable runbooks
are read and verified against their exact approved bytes before full delivery.
A changed/unavailable runbook produces INCOMPLETE, not stale content.
Conditional runbooks are referenced but not expanded. No step execution or
completion receipt is recorded in this version.

## Review and change instructions

```sh
deja instructions export > instruction-draft.json
# Edit a rule, increment its rule revision by one, retain registry revision N.
deja instructions apply --file instruction-draft.json --expect N --approve
deja instructions resolve --task example-task --tool Bash --json
```

Global registry revision and individual rule revisions are separate. New rules
start at 1. Existing rules cannot be deleted from the current snapshot: revoke
with a new rule revision. Every successful approval archives the new snapshot.
`resolve --json` includes applicable, conditional, excluded reasons and
conflicts; callers MUST inspect conflicts and conditional entries. Text rendering
refuses unresolved conflicts. Nothing extracts/promotes history automatically.

Duplicate JSON keys, unknown record fields, malformed JSON, unsupported schemas,
relative paths and unbounded inputs are refused. A concurrent approval requires
a matching registry revision. Process-wide locks refuse competing writers;
a crashed writer can leave a lock that requires inspection before removal.
No stale lock is automatically broken. Hook config backups retain exact prior
bytes, including large numeric values and unrelated hook definitions.

## Budget, storage and trust boundaries

The first backend is a bounded approved-configuration snapshot, behind a `Store`
interface. It is not a new conversation database. Maximum: 8 MiB and 10,000 rules
or exceptions per collection. It rereads/validates per invocation; large
registries are not a constant-time lookup. Use the benchmarks below to evaluate
that cost before scaling this backend. No per-tool history writes occur.

The context budget is 8,000 UTF-8 bytes, not tokens or complete JSON wire bytes.
All applicable/conditional mandatory text must fit in full or rendering fails.
Only optional defaults may be omitted, with a count. Repeated injection can add
material context cost; delivery receipts/deduplication with compaction-aware
invalidation are not implemented yet.

Writes use a same-directory temporary file, file sync and rename, without
unlinking the old snapshot first. Directory entries are NOT fsynced in v0:
there is no full power-loss durability guarantee. Use local filesystems, not a
shared network mount. Archives are written before the live snapshot; an
interrupted write can be retried with the same approved draft. A conflicting
archived draft is an explicit recovery error. Archive retention/GC is deferred.

`--approve` and `approved_by` are not authentication. An agent with the same OS
user's write access can change the registry/checker. Do not present this as a
security boundary. Keep approved files outside untrusted checkouts; stronger
approval isolation and resource-enforced action gates are separate work.
Do not put secrets in rules/runbooks: delivered instructions enter the agent's
model context. This subsystem does not import Deja's historical redaction or
privacy configuration, nor change its history hooks' fail-open contract.

## Verification and follow-on work

```sh
go test ./internal/instructions -race -count=1 -coverprofile=coverage.out
go vet ./internal/instructions
go test ./internal/instructions -run '^$' -bench . -benchmem
# Also required before merge:
go test ./... -race -count=1
go vet ./...
go build ./cmd/deja
```

Synthetic tests exercise expiry, precedence, conflicts, unknown scope, exact
runbook hashes, budgets, concurrent processes, symlinked new files, configuration
preservation and event JSON. They do not prove bytes reached a real model.
Follow `.claude/commands/harness.md` with scratch profiles and recording endpoints
for both agents, including compaction/subagents and UI inspection, before
claiming end-to-end support. Existing `deja doctor` does not inspect this subsystem.

Remaining: real-harness/version probes, native guidance/skill packaging,
automatic candidate intake from Deja with approval, task/obligation lifecycle,
verified execution receipts, controlled action gates, stable identities across
clones/machines, registry indexing/caching and stronger crash durability.
Storage workload analysis is in [instruction-storage.md](instruction-storage.md).
