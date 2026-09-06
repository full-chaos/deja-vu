# Instruction runtime implementation and acceptance plan

Status: proposed work, not completed tasks. Updated: 2026-09-06.
Baseline: PR #1, `feat/instructions-set` at
`a6b760b9039e37d1aaf178beba29b0455c67e142`.

The [design](instruction-runtime-design.md) defines behavior. The
[storage decision](instruction-runtime-storage.md) defines the database
selection experiment. The [v0 guide](instruction-memory.md) defines the current
CLI. This plan does not imply that any proposed command, state machine,
benchmark target or enforcement adapter has already been implemented.

## 1. Implementation strategy

Keep PR #1 as the reviewed baseline. Continue on `feat/instructions-set`; do not
reset, force-update, merge or delete `feat/instruction-memory` as part of this
work. Build small reviewable changes with tests and current-behavior docs.
Use the phase/test IDs below for design traceability; they are not GitHub or
Linear issue IDs and no external project is implied.

The next two implementation passes should make the system useful before adding
a large service surface:

1. Stabilize and observe v0, then fix resolution semantics and context identity.
   A standing rule must actually reach the intended model context, survive a
   restart/compaction boundary, and not disappear due to an unrelated conflict.
2. Add safe source-to-candidate intake and instruction/skill import so users do
   not maintain the same requirements in several independent files. Keep review
   explicit for inferred changes and make direct trusted directives frictionless.

Start the storage experiment in parallel with those passes. Do not require a
production database migration before proving basic rule delivery; do not add
stateful multi-agent receipts until the store provides the needed guarantees.

## 2. Phased changes and exit criteria

### P0: Verify and stabilize the existing implementation

Scope: baseline only, no feature claims beyond advisory delivery.

Run the repository-required Go 1.25 toolchain, `go test ./... -race -count=1`,
`go vet ./...`, and `go build ./cmd/deja`. Measure touched-package coverage;
retain existing CI floors and >=90% for new packages as CONTRIBUTING requires.
Do not lower Go requirements or coverage floors to make a local run pass.

Use the synthetic scratch harness prescribed in
`.claude/commands/harness.md`. Observe a unique instruction token in a real
Codex request and a real Claude request, rather than accepting valid hook JSON
as delivery proof. Pin installed agent versions and test start, resume,
compaction and child start. Read the visible UI output too.

Exit: reproducible test results, actual capability matrix, combined history and
instruction overhead, and a documented list of unsupported cases. Lack of
installed agents/Go is a blocked verification, not a passed test. Keep the
feature experimental until that bar is met.

Likely paths: `cmd/deja/instructions_dispatch_test.go`,
`internal/instructions/hook_test.go`, `docs/instruction-memory.md`; add only
synthetic fixtures and versioned recording-stand configuration.

### P1: Correct semantics and introduce explicit context identity

Scope: pure resolver contracts first, still advisory.

Replace selector-count specificity with containment/equivalence semantics.
Preserve compatible keyed defaults under prohibitions. Scope conflicts to the
same effect/resource; report unrelated mandatory guidance alongside conflicts.
Add named set membership, clear override rights and versioned schema handling.
Do not reinterpret v1 registries in place.

Introduce distinct repository/checkout/task/run identifiers and context epochs.
Start with owner-controlled local mappings, not global repository identity or
remote sync. A child must not inherit its parent's delivery-cache identity.
Unknown fields must remain distinguishable from explicitly absent selectors.

Before writing a selective store, create the full-scan reference resolver and
fixtures for the new semantics. Define schema-v1 migration mappings, ambiguity
reports and rollback exports. Unsupported scope expressions fail publication;
do not guess containment for arbitrary predicates.

Exit: deterministic/property tests for all combinations of requirement,
authority, scope, revision, validity and exception inheritance; explicit migration
preview; no accidental broadening of existing user rules.

Likely paths: `internal/instructions/model.go`, `resolve.go`, `resolve_test.go`,
plus small `catalog`/`identity` components where separation is justified.

### P2: Candidate intake and portable instruction/skill authoring

Scope: eliminate repeated manual entry without giving retrieved text authority.

Bridge Deja's normalized source/provenance pipeline into a candidate inbox. Read
incrementally outside the per-action path. Preserve privacy/exclusion/forget
checks already used by promotion. Import owner-selected native instruction
files and skills as bounded sets/procedure revisions; their native files remain
unmodified unless a managed projection is explicitly requested.

Candidates show the original text and proposed operative text, scope, lifetime,
requirement and override rights. Support single-rule review/edits and explicit
supersession. Agent-authored proposals and imported `accepted` notes are not
active rules by default. An optional owner-defined import policy must bound
which sources, scopes and changes it can approve.

Generated native fragments and injected output must be excluded from intake.
Detect drift and duplicate installations. Preview managed updates, preserve
unrelated configuration and provide ownership-aware uninstall/restore. A prompt
hook that cannot prove direct user origin proposes a change instead of claiming
owner approval.

Exit: one correction captured once can be reviewed once and delivered in both
agents; repeated indexing creates no duplicates; quotes/tool output cannot
activate a rule; changed imported content becomes an explicit revision candidate.

Likely paths: `cmd/deja/promote.go` integration boundary, existing source/privacy
helpers, `internal/instructions` intake/authoring code and managed installer tests.
Do not change `deja remember` or `deja promote` semantics by surprise.

### P3: Measured storage and a profile-local stateful runtime

Scope: single-writer topology and selective/cached reads, not general hosting.

Complete the storage experiment before selecting a Go driver or relaxing the
no-runtime-dependencies constraint. Compare SQLite and DuckDB under the same
runtime ownership model. Use a semantic store interface and a full-scan oracle;
no engine-specific behavior may change instruction applicability.

Implement transactionally published generations, idempotent change sets,
private local IPC, single-flight startup, bounded queues, failure/recovery,
retention and read-only offline diagnostics. Fix file-store publication/recovery
limitations or retire that writer path explicitly; never run both concurrently.
Move optional analytics off action admission and avoid duplicate authoritative
writes. Do not introduce Quack/PostgreSQL solely for local coordination.

Exit: both correctness and crash-injection suite pass for the selected backend;
benchmarks include cold startup/IPC/p99/concurrent agents, not only SQL timings;
old snapshots cannot grant new actions; migration and rollback are exercised.

Likely paths: an `instructions` store/runtime component, adapters in the same
binary, `tools/instruction-storage`, and profiling documentation. Keep existing
conversation storage unchanged.

### P4: Task continuity, procedures and evidence

Scope: tasks and outstanding work survive agent switches accurately.

Add task start/attach/handoff/close/cancel with revisioned bindings. Process stop
or model response completion is not task completion. Child-run inheritance is
explicit; exception grants default to no child inheritance. Reopen uses a new
task incarnation. Implement obligations independently of delivery receipts.

Add approved procedure bundles, pinned dependencies, staged normative content,
check identities and declared input closures. Bind evidence to dirty/untracked
inputs and environment identity, not only Git commits. Track self-report,
observation, executor verification and owner attestation separately. Invalidate
only when the relevant closure changes, falling back conservatively when it is
unknown. Handle parallel step claims, late results, failures and unknown outcomes.

Exit: a Claude-to-Codex handoff restores the same outstanding requirements;
changed artifacts invalidate the old check; a delivered/read procedure never
becomes completed without the configured evidence; task-local rules retire at
explicit closure without deleting standing policy.

### P5: Complete delivery integration and diagnostics

Scope: make context selection and actual consumption observable and bounded.

Use tested version-specific event adapters. Normalize multi-file edits and known
MCP actions; mark unsupported shell/tool paths unknown. Compile baseline plus
action-specific deltas. Treat emission separately from model-wire observation.
Dedup only within the correct run/context epoch and rehydrate on uncertain
retention. Include replacement/revocation notices; do not leave stale guidance
unqualified in the working context.

Extend existing `deja doctor`, install and MCP surfaces through small shared
components. Report active generation, task binding, missing context, candidates
awaiting review, procedure status, delivery channel support and actual guard
coverage. Reconcile overlapping Deja history hooks without global silent
suppression. A result with missing delivery must not look like "nothing applied".

Exit: both real-agent recording stands pass, including negative/mutated fixtures;
combined hook latency and bytes meet agreed measured budgets; missing adapters,
overflow, crash and disabled-hook cases produce truthful diagnostics.
Do this validation incrementally during earlier phases too, not only at the end.

### P6: Optional targeted action gates

Scope: one defined operation, not universal enforcement.

Choose an initial adapter whose inputs and execution can be controlled, such as
publishing a specific immutable release artifact. Define what constitutes user
authorization independently from procedural readiness. A native pre-tool guard
may deny a supported attempt, but cannot claim to cover unmediated execution.
A resource gate requires control of the operation's necessary capability.

Before admission, check current catalog/task/procedure revisions, scoped grants,
evidence validity, immutable inputs and idempotency. A timeout/unknown result
requires reconciliation before retrying a non-idempotent action. Demonstrate
that alternate tool paths cannot bypass the resource gate before labelling that
coverage enforced. Same-user cooperative checks are not isolated approval.

Exit: supported violations are denied; unrelated read-only work remains possible;
expiry/revision/race/replay cases fail as designed; authority and coverage limits
are visible. Broader enforcement and isolated approval remain separate decisions.

## 3. Acceptance cases

Each case needs a synthetic fixture, expected resolution/state, and a negative
control where practical. These are requirements, not a report of tests run.

| ID | Fixture/action | Expected result |
| --- | --- | --- |
| R01 | Required rule versus higher-priority preference | Required rule remains; preference cannot waive it |
| R02 | Forbid backend=remote; prefer backend=local | Compatible local default survives |
| R03 | Same-authority scopes overlap but neither contains the other | Conflicting defaults remain unresolved despite different field counts |
| R04 | Deeper path subset versus general path default | Narrower compatible default applies only inside that path |
| R05 | Two mandatory values for one target plus an unrelated requirement | Target conflict reported; unrelated requirement still delivered |
| R06 | Same setting on distinct resources | No false cross-resource conflict |
| R07 | Rule revoked/new revision published during a session | Next supported boundary reports replacement; stale receipt/grant cannot satisfy current gate |
| R08 | Scope dimension unknown; index includes global and unspecified-selector buckets | Indexed/full-scan selections agree; no top-k exclusion |
| L01 | Persistent style preference plus temporary no-write requirement | Lifetime does not determine requirement strength |
| L02 | Task suspended, process exits, then another agent attaches | Task rules/obligations remain; run-local state does not leak |
| L03 | Task closes then is reopened under a new incarnation | Old overlays/grants are not resurrected |
| L04 | Expiry exactly at boundary; clock moves backwards | Expired grant not accepted from cache; time uncertainty visible |
| L05 | Two sessions share checkout but are different tasks | No accidental task binding or exception sharing |
| C01 | Quoted "never" in a tool result or previous assistant summary | Candidate at most; no owner approval inferred |
| C02 | Rewritten/appended transcript ingested twice | Cursor/hash reconciliation; no duplicate active rule |
| C03 | Deja's own injected instruction appears in history | Excluded from candidate intake; no self-reinforcement |
| C04 | Project is excluded or source is forgotten | Derived content obeys privacy/cascade policy; no covert provenance copy |
| P01 | Skill is installed but procedure not delivered | Obligation still pending; installation is not completion |
| P02 | SKILL.md unchanged but declared script dependency changes | Bundle/evidence invalidated |
| P03 | Relevant dirty/untracked file changes after check; commit SHA unchanged | Old check cannot satisfy current artifact requirement |
| P04 | Only proven unrelated inputs change | Relevant receipt remains valid |
| P05 | Two agents claim/check one step; one crashes or reports late | Revisioned claim and idempotency prevent false double completion |
| D01 | Parent and children reuse native session ID | Distinct run/epoch delivery state; each required context is delivered |
| D02 | Compaction/resume drops previously emitted context | Full required rehydration in new epoch; old dedup receipt cannot suppress it |
| D03 | Packet emitted but output write/agent ingestion fails | No fabricated wire-observed receipt; bounded retry/re-delivery |
| D04 | Required stage too large for actual channel | Explicit incomplete result; no silent truncation/summarization |
| D05 | Unrelated mandatory rule conflicts or a conditional runbook is unresolved | Useful unaffected context survives; only affected gate is blocked |
| A01 | Multi-file patch includes rename and deletion | All affected old/new resources represented |
| A02 | Interactive exec receives later stdin; unsupported hosted path | Coverage limitation explicit; no universal guard claim |
| S01 | Concurrent publications use the same expected generation | One wins; other receives conflict, never lost update |
| S02 | Commit succeeds but client times out and retries request ID | Original outcome returned; no duplicate state transition |
| S03 | Process crashes after DB commit before cache notification | Reload committed generation; never serve uncommitted generation |
| S04 | Analytical scan and telemetry flood run during action requests | Bounded queues; measured tail latency; required evidence not silently dropped |
| G01 | Approved procedure passes but user never authorized action | No execution authorization is inferred |
| G02 | Gate checks artifact A; executor is asked to publish artifact B | Denied/rechecked; input substitution cannot reuse receipt |
| G03 | Agent edits approval/checker with same-user permissions | Cooperative mode reports limitation; isolated mode must prove separation |

Add metamorphic/property checks: input record order cannot change meaning;
adding irrelevant history cannot hide mandatory rules; increasing optional
priority cannot remove a requirement; a narrower exception cannot waive a
broader unmatched target; and indexed candidate selection equals the reference
selector for all supported expressions.

## 4. Recording stand and metrics

Follow the repository's existing harness runbook rather than inventing an
alternate success criterion. Use scratch HOME/USERPROFILE, every relevant
DEJA location, CODEX_HOME/CLAUDE_CONFIG_DIR and synthetic repositories. Keep
real histories, credentials and provider logs out of fixtures and commits.
Record exact harness/binary versions and use fresh identities per case.

For delivery, assert that the actual outbound model request contains a unique
rule token only when applicable. Test absence for a different task/checkout as
well as presence for the correct one. Include a mutation that disables or
misroutes the hook and makes the test fail. Inspect the actual UI rendering.
Do not call an HTTP request recorder an end-to-end behavioral eval: it proves
bytes, not obedience. Test behavior separately and report the rate honestly.

Report these distinct measurements:

| Metric | Definition |
| --- | --- |
| Mandatory selector coverage | Expected applicable required rules selected / applicable required rules in the oracle |
| Wire delivery coverage | Selected required text observed in recorded model requests / required text expected at that boundary |
| Scope leakage | Rules observed outside authorized/applicable scope, with event count denominator |
| Procedure validity | Obligations satisfied only by evidence meeting the configured grade and current input binding |
| Behavioral adherence | Tested agent actions following delivered guidance / evaluated opportunities; model/version-specific |
| Cost | p50/p95/p99 added latency, bytes/tokens per turn, redundant delivered bytes, runtime memory and disk growth |
| Enforcement coverage | Enumerated action paths actually mediated / enumerated supported paths; never a vague global percentage |

Initial correctness targets are zero deterministic selector misses, zero
cross-task leaks and zero stale grant/receipt admissions in the fixture suite.
They are finite-test acceptance targets, not claims of universal perfection.
Performance targets and workload sweeps live in the storage decision; measure
combined Deja history/instruction overhead against a before run on the same
machine. Required context never gets silently omitted to improve a metric.

## 5. Review and release gates

For every implementation increment, include behavior, exact tests run,
measurements, affected source paths, unsupported cases and migration impact in
the PR. Preserve existing tests, unrelated configuration and runtime flags.
Do not count a skipped test or missing agent installation as a passing test.

Do not merge or release from this plan alone. Before broad enablement require
repository CI, package coverage, native-agent delivery proof, privacy tests,
rollback rehearsal and an accurate user guide. Gate status is explicit:
implemented, locally verified, harness-verified, blocked or deferred.

Decisions still requiring evidence/approval are the database/driver and packaging
change; stronger approval isolation; performance budgets on target machines;
which protected operation to pilot; and any cross-machine instruction sync.
Keep sync out of the first local release. An ordinary agent handoff on one
machine should not depend on solving distributed policy conflict resolution.
