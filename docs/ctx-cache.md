# Local context cache and resume contract

`deja ctx` is the local, agent-independent resume layer. It materializes the
working state an agent needs now; it does not replace Deja history, a code
graph, ACR, or an external source of truth. Snapshot data is local by default
under `~/.cache/deja/ctx` (or `DEJA_CTX_DIR`).

## Start with resume

For substantive work, call `deja ctx resume --workspace PATH` and add
`--task ID` when an active task is known. MCP clients call the listed `deja`
tool with `mode: "ctx_resume"` and the same fields. A successful packet is the working context to use before
reconstructing project history. Applicable `absolute` and `required`
instructions are binding.

Historical Deja memory is L4 supporting evidence, not the normal startup path.
Use `ctx lookup`, the `deja` tool with `mode: "ctx_lookup"`, recall, or another source only when the
packet has a required gap, the snapshot is stale or invalid, verification needs
an authoritative source, or more evidence is needed. A dirty worktree, HEAD,
branch, task, and instruction changes are freshness inputs; a refresh records
what needs validation rather than asserting old conclusions remain true.

## Layers and packet

The packet has deterministic layers: L0 instructions, L1 project context, L2
active task, L3 relevant session notes, L4 historical memory on demand, and L5
verification sources. It includes identity, objective, instructions, project
and task state, decisions, tests, next actions, evidence, explicit gaps,
conflicts, provenance, and component freshness. Meaningful items have a stable
ID, concise text, and source. `ctx explain --item ID` shows why an item applies.

An unresolved conflict remains visible as a gap/blocker. Instruction resolution
checks explicit supersession, scope, priority, authority and effective dates.
Mandatory instructions accumulate; a ranking tie cannot waive one. Semantic
retrieval does not decide whether an applicable absolute instruction is included.

## Lifecycle

`resume` finds the newest valid snapshot, applies cheap local deltas when
possible, and renders a bounded packet. It does not perform broad historical
searches as routine startup work. `status` reports component freshness.
`refresh` incrementally reconciles changed Git, task, instruction, and identity
metadata. `diff` and `history` inspect immutable snapshots. `invalidate` marks
the whole packet, a layer, task, or source stale; `--source ALIAS` identifies a
source-specific invalidation.

After meaningful durable progress, call `deja ctx checkpoint` or the `deja`
tool with `mode: "ctx_checkpoint"` with structured confirmed findings, decisions,
implementation state, known failures, test results, unresolved questions, and
next actions. Do not checkpoint private reasoning or chain-of-thought. Checkpoints preserve
durable conclusions and provenance while keeping temporary reasoning out of the
cache. `deja ctx promote --item ID --to project|task|permanent|ephemeral`
changes an item's retention layer.

## Instructions

`deja instructions` is an experimental, opt-in advisory delivery path for
standing instructions, independent of historical retrieval. Its commands are
`example`, `apply`, `export`, `resolve`, `hook`, and `install`. The registry is
`DEJA_INSTRUCTIONS_FILE` when configured; otherwise it is
`$XDG_CONFIG_HOME/deja/instructions.json`, falling back to
`~/.config/deja/instructions.json`.

Registry rules carry `absolute`, strength (`must`, `must_not`, or `should`),
numeric priority, repository/worktree/task/session/environment/path/tool
scope, lifetime, active/superseded/revoked status, expiry, supersession, and
source. The ctx packet adapts them to `absolute`, `required`, or `preferred`
priority and permanent/task/ephemeral/durable retention. `ctx resume` includes
known applicable active absolute and required records deterministically; a
missing registry, incomplete required scope, or unresolved instruction conflict
is a required gap.

Checkpointed instructions are proposals, not approvals. Their text, scope,
provenance and proposed priority are retained, and mandatory proposals expose
an approval gap. Activate standing instructions through the approved registry
using `deja instructions apply --file FILE --expect REVISION --approve`.
Checkpointing or promoting an item cannot manufacture registry approval.

## Local sources and version markers

A checkpoint may declare `sources`: local descriptors with `name`, `layer`,
and an absolute `path` (plus an optional version). A source file is strict
State JSON and may populate only its declared `project`, `task`, `memory`, or
`session` layer. Checkpoint materializes and hashes those files; refresh
rereads only changed sources. `--versions '{"task":"v2"}'` on the CLI or
`component_versions` in MCP provides local adapter markers. Without an adapter,
an unknown component version becomes a required validation gap instead of a
claim that the component is current.

## Interfaces and limits

The CLI lifecycle is `resume`, `status`, `refresh`, `checkpoint`, `diff`,
`history`, `lookup`, `explain`, `invalidate`, and `promote`. MCP exposes the
matching `ctx_*` modes, including `ctx_history` and `ctx_promote`, through the
single listed `deja` tool. Legacy `deja_ctx_*` aliases and historical tool
names remain callable only for already-wired clients; agents should invoke
the listed tool with its matching `mode`.

`--budget`/`token_budget` is enforced using a conservative rendered-byte bound,
not an exact tokenizer count. The renderer retains absolute/required
instructions, objective, blockers, decisions, state, and next actions before
lower-priority evidence and history. It rejects a budget that cannot contain
the irreducible packet instead of silently dropping binding instructions.

Snapshots are immutable history plus small current pointers. They are designed
for local point lookups and bounded packet assembly; no remote service or
vector search is required for a cache hit. Local metrics cover resume hits and
misses, refreshes, checkpoints, lookups, gaps, and snapshot/packet size.

## Acceptance traceability

The following regression tests are the executable trace for this contract:

| Requirement | Regression evidence |
| --- | --- |
| Local resume, identity freshness, invalidation, immutable history, and durable checkpoint validation | `TestCheckpointResumeRefreshAndInvalidate`, `TestBranchSwitchReusesSnapshotAndCreatesValidationGap`, `TestConcurrentCheckpointsHaveUniqueVersions`, and `TestCheckpointRejectsAmbiguousStructuredState` in `internal/ctxcache/cache_test.go` |
| Bounded public packets and lossless checkpoint decoding | `TestCtxCachePublicBudgetPreservesRequiredInstructions`, `TestCtxCachePublicBudgetValidation`, and `TestCtxCachePublicRejectsLossyCheckpointInput` in `cmd/deja/ctx_cache_acceptance_test.go` |
| Local-source isolation, content-hash removal, provenance, supplied version gaps, and strict source JSON | `TestCtxCacheLocalSourceFixtureRefreshesOnlyTask` with `fixtures/synthetic/ctx/task-in-progress.json` and `fixtures/synthetic/ctx/task-validated.json`; `TestSourceAcceptanceRefreshPreservesOtherLayersAndProvenance`, `TestSourceAcceptanceSourceRemovalAndManualVersionRequireValidation`, and `TestSourceAcceptanceRejectsMalformedOwnershipUnknownAndDuplicateJSON` in `internal/ctxcache/source_acceptance_test.go` |
| Instruction provenance, required gaps, supersession, scoped approval, and candidate handling | `TestResolveInstructionsUsesApprovedFixtureAndPreservesProvenance`, `TestRegistrySupersessionAndRefreshCannotResurrectRule`, `TestCheckpointInstructionsBindScopeDatesSupersessionAndConflicts`, and `TestCheckpointProposalScopeGapsAndSemanticApprovalMatch` in `internal/ctxcache/instructions_test.go` |
| Explicit conflicts, semantic diffs, explainability, promotion, and local metrics | `TestSourceAcceptanceConflictsPromotionDiffAndExplain` and `TestSourceAcceptanceMetricsRecordActualOperations` in `internal/ctxcache/source_acceptance_test.go` |
| Recovery clears generated gaps while preserving user-authored blockers | `TestSourceRecoveryPreservesUserUnavailableGap`, `TestSourceRecoveryKeepsSameSubjectManualGap`, and `TestConflictResolutionClearsOnlyGeneratedGap` in `internal/ctxcache/cache_test.go` |

Three intentional limits define the boundary of these tests and APIs:

- `--budget` and `token_budget` use a conservative rendered-byte bound. They do not emulate a model-specific tokenizer.
- Sources are local file adapters plus CLI/MCP version markers supplied by the caller. Resume does not poll or synchronize remote systems.
- The native instruction registry is the approval authority. A checkpoint can retain a candidate and show its gap, but cannot activate or approve that instruction.
