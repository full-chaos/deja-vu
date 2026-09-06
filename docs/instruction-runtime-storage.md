# Instruction runtime storage: workload-driven decision

Status: proposed topology; engine selection open. Updated: 2026-09-06.
Baseline: `feat/instructions-set` at `a6b760b9039e37d1aaf178beba29b0455c67e142`.
This supplements the [initial storage note](instruction-storage.md), not a
storage migration. See the [runtime design](instruction-runtime-design.md)
and [implementation plan](instruction-runtime-plan.md).

## Decision summary

Do not choose the engine from total conversation volume alone. Keep Deja's
existing history index intact during instruction work. Remove full-registry
loading from the instruction action path. For the later stateful multi-agent
runtime, compare SQLite and DuckDB under the same profile-local, single-writer
Deja process, with identical correctness and latency requirements.

This does not preselect SQLite for operational state or require DuckDB to be
analytics-only. DuckDB remains a legitimate operational candidate when one
runtime owns its embedded database. Analytical exports are a separate optional
capability; two databases are not required simply to remember instructions.

V0's JSON backend remains a bounded bootstrap implementation until a measured
replacement is ready. Its `Store.Load/Replace` interface is not sufficient to
make a database migration a performance improvement by itself.

## 1. Separate the data and query shapes

The existing index uses `records.bin`, token posting buckets, and Gob metadata.
Explicit notes use JSONL; some external harness databases are SQLite inputs.
`docs/ARCHITECTURE.md` describes these formats. Therefore there is no existing
Deja-owned SQLite history database to swap wholesale for DuckDB.

| Data class | Typical work | Required property |
| --- | --- | --- |
| Approved rules and procedure metadata | Scoped lookup and occasional revisioned publication | Complete mandatory selection, reproducible revisions |
| Candidate inbox and source cursors | Incremental ingest, review, deduplication | Bounded ingest, idempotency, provenance and privacy |
| Live tasks, obligations and grants | Small correlated reads/writes by many agent clients | Transactions, race handling, reliable handoff |
| Delivery observations | Frequent small records; many repeated/no-op events | Bounded storage, deduplication, truthful completeness |
| Retained receipt/audit analysis | Time-window scans, grouping, joins | Efficient bulk queries and retention |
| Existing conversation search | Token postings and ranked recall | Preserve existing behavior; evaluate separately |

The database may hold large historical volumes while the active working set is
small. Conversely, a small file can still be slow if reopened, reparsed and
fully explained before every tool use. Measure both axes independently.

## 2. Proposed process ownership

In the stateful phase, one explicitly enabled `deja instructions serve` process
owns operational writes for one user/profile. This is a proposed command, not
part of v0. Clients use a private local socket, bounded requests and timeouts;
there is no TCP listener or cloud dependency by default. Use existing process
facilities where suitable, not a deployment platform.

The runtime owns a connection pool appropriate to the engine, a published
immutable in-memory rule catalog, task state and a bounded ingest queue. Rule
selection normally queries the in-memory scoped projection, not SQL on every
rule. Point state mutations use transactions; optional analytical work cannot
starve action admission.

This is a concurrency topology, not an authentication boundary. A private socket
only excludes other users where the OS supports that protection; same-user
agent processes are still within the cooperative threat model.

A single writer is relevant to both candidates. SQLite WAL allows readers with
a writer but still has one writer at a time. DuckDB supports concurrent work
inside a process, while direct cross-process access has different constraints;
its current documentation describes Quack for remote multi-process writes and
labels that protocol beta. None of those facts prevents Deja from owning one
embedded DuckDB instance behind local IPC. Do not add Quack, PostgreSQL or
DuckLake merely to satisfy a local instruction workload. [S1, S2]

V0's stateless CLI remains available while advanced runtime work is developed.
A future offline resolver may read a published snapshot with an explicit
freshness/task-state limitation. It must not perform hidden parallel writes,
grant exceptions, or claim to validate live execution receipts while disconnected.

## 3. Store contracts before engine bindings

Keep the storage boundary semantic. These are logical operations, not final Go
signatures or permission grants:

```text
ReadGeneration(profile) -> generation, schema, published_digest
SelectCandidates(selector, generation) -> records, selection_completeness
GetRule(id, revision) -> immutable_payload
Publish(change_set, expected_generation, request_id) -> committed_generation
MutateTask(change_set, expected_task_revision, request_id) -> task_revision
RecordEvidence(receipt, expected_obligation_revision, request_id) -> receipt_id
ReadTaskPacket(task_id, consistent_revision) -> bindings, obligations, grants
Explain(selector, generation, cursor) -> paged selection reasons
```

`Publish` includes approval provenance, changed rules, membership, exception
changes and an audit event in one transaction. Separate procedure content blobs
may be written first; the transaction references only durable verified blobs.
An unreferenced blob after a crash is collectable. A committed catalog must not
reference missing content.

The change-set digest is bound to the request ID. Retrying the same ID/payload
returns the original result; reusing the ID with different content is an error.
A client timeout is an unknown commit outcome, not proof that the write failed.
Query the operation result before retrying non-idempotent work.

Logical tables/collections:

- `catalog_generations`, `sets`, `set_members`, `rule_revisions`,
  `procedure_revisions`, `exceptions`, and `approval_records`.
- `tasks`, `task_bindings`, `agent_runs`, `obligations`, and `evidence_receipts`.
- `candidates`, `source_cursors`, `delivery_records`, `audit_events`, and
  `operation_results`.

These names are a model, not a mandate for one table per noun. Keep immutable
large text/blobs out of frequently updated rows. Index current revision,
profile/set/repository/task selectors, obligation identity, and event sequence.
Do not use insertion timestamp as a unique key or sole total order. Enforce
foreign references and uniqueness either transactionally in the engine or in
a tested store implementation; do not assume driver defaults enforce them.

## 4. Hot-path selection and caching

The selector must return a conservative superset of applicable rules. Combine
exact-ID buckets with global/unspecified-selector buckets; path prefixes use
component-aware lookup. Unknown selector dimensions retain potential matches.
Final matching remains pure and deterministic. Compare indexed selection to a
full-scan oracle across generated scopes, times and exceptions.

A request caches by catalog generation, task/binding revision, normalized
resources, adapter version and action. Expiry is the minimum of future validity
starts, relevant expirations and configured freshness limits. An empty result
also has a generation and validity boundary; negative caches can otherwise hide
new requirements.

Do not validate the entire catalog twice per request or build every exclusion
explanation for a short hook packet. Validate at publication/load, verify the
published generation, and evaluate selected records. Full diagnostics are paged.
Keep source scanning, embeddings, transcript parsing, procedure discovery and
analytical queries outside action admission.

A broadcast invalidates client caches promptly; correctness must not depend on
receiving it. Every protected operation resolves current catalog/task/evidence
state in the runtime at admission. A generation change during an immutable
artifact's execution is recorded; whether it cancels the operation must be
specified by that action adapter rather than retroactively claimed.

## 5. Durability, recovery and retention

V0 syncs file contents and renames, but does not sync directory entries. Its
archive and current-snapshot publication are separate writes; a crashed writer
can leave a lock. These limitations are documented in `store.go`. They must not
be inherited as production durability claims by a new store.

For each database candidate, test acknowledged publication across process kill,
restart, disk-full, interrupted migration and contention. Distinguish a process
crash test from power-loss durability: the latter also depends on filesystem,
OS and device behavior. Record journal/checkpoint/durability settings explicitly.
Do not lower durability to make one benchmark appear faster. [S1]

Only publish the in-memory generation after durable commit. A crash between
commit and notification is recovered by reloading current state; an uncommitted
change must never be rendered as active. Required evidence and operation
admission records must be durable before permitting a protected action.
Optional no-op delivery telemetry can be batched or sampled with dropped-event
counters, but that history cannot then claim exact delivery counts.

Avoid retaining full repeated packets or unrestricted tool output. Store packet
hashes and revision references, bounded redacted diagnostics, and explicit
emission/observation state. Suggested initial configurable retention is 30 days
for detailed delivery events and 90 days for completed-task operational detail;
these are proposed defaults, not existing behavior. Preserve active tasks,
current rules, live grants and evidence still referenced by open obligations.
Approval/revocation provenance has a separate owner-selected retention policy.
Deletion/forget must propagate through projections and exports.

Analytics reads sealed, privacy-filtered exports or a consistent read snapshot.
If a separate DuckDB analytical file is used, it is rebuildable and not part of
the approval transaction. No dual writes to competing sources of authority.

## 6. What has and has not been measured

The original bundle contains a short synthetic JSON registry probe and a
SQLite-only SQL probe. They do not establish an engine winner or live agent
latency. No new head-to-head result is claimed in this design pass: DuckDB was
not installed in this runtime, and the package-install attempt failed because
the runtime could not resolve its package host.

The checked-in `tools/instruction-storage/compare.py` is still useful for
reproducing isolated lookup/scan/commit/reopen cases. It uses Python drivers,
different bulk-loading implementations, a simple schema and very few scan/
commit repeats by default. It does not test the proposed Go runtime, IPC,
concurrent clients, active-rule selection or failure recovery. Do not use its
file sizes or load times as a general SQLite/DuckDB verdict.

DuckDB's indexing documentation describes ART indexes for constraints and
selective lookups, alongside costs and limitations. The storage spike must
inspect actual plans rather than assuming that a created index is used or
that scan-oriented execution cannot perform point queries. [S3]

## 7. Required comparative experiment

Use the same corpus seed, logical records, result oracle, durability objectives,
hardware and Go/runtime ownership model. Record engine/driver/compiler versions,
CPU/RAM/filesystem, query plans, settings, repeats and errors. Randomize engine
order and run several independent trials; label warm, startup and OS-cold
measurements separately. Caches must have the same semantics on both paths.

| Experiment | Sweep | What it decides |
| --- | --- | --- |
| Catalog selection | 100 / 1,000 / 10,000 / 100,000 total rules; vary applicable count independently | Scope indexing and output cost, not transcript volume |
| Operational retention | 100,000 / 1 million / 10 million synthetic events | Historical volume impact on current lookup/write latency |
| Agent contention | 1 / 4 / 16 / 32 client processes | IPC queueing, transaction conflicts, retry behavior |
| Rule publication | Single-rule edits and bounded set revisions amid reads | Generation visibility and exactly one current revision |
| Task/receipt traffic | Handoff, child runs, check completion and invalidation | Correctness under realistic mixed operations |
| Context reuse | No-op tools, task changes, compaction and resumed children | Saved bytes without incorrect suppression |
| Failure injection | Crash at each publication boundary; timeout; disk full | No lost acknowledged policy or forged completion |
| Analytics isolation | Grouped event queries concurrent with action requests | Whether analytical work harms interaction latency |

Publish p50/p95/p99 latency, throughput at fixed arrival rates, queue depth,
errors/retries, resident memory, allocations, bytes delivered and storage growth.
Avoid a closed-loop test that conceals overload by reducing arrival rate when
queries become slow. Verify every result, including exception expiry, before
including a timing sample as a success. Test rollback-clock and stale-cache cases.

Proposed initial engineering budgets, subject to measurement on target machines:
no-match warm instruction overhead p95 <= 10 ms and p99 <= 25 ms; small matched
packets p95 <= 25 ms excluding actual check execution; cold rule-context delivery
p95 <= 100 ms. Record process/IPC time, not only the resolver function. Large
procedure stages get a separate budget; never compress required content merely
to meet a latency number. Measure combined Deja history plus instruction cost
against the existing harness baseline rather than adding the two budgets.

## 8. Adoption gate

Both candidates must first pass the same semantic and recovery suite. Then
choose on tail latency, concurrent behavior, resource use, packaging, maintenance
and deployment simplicity. Do not select by a bulk-ingest or grouped-scan win
alone. The existing no-runtime-Go-dependencies constraint means a new driver
or external executable needs an explicit packaging decision: supported OS/CPU,
CGO/cross-compilation, binary size, licensing, upgrade and crash isolation.

If SQLite meets the operational targets with simpler packaging, that is evidence
for using it, not an assumption that size never matters. If DuckDB meets those
targets and offers a better total design, use it operationally behind the writer.
If neither does, inspect selection/serialization/queueing before adding another
engine. A compact catalog with optional DuckDB analytics remains a candidate,
not the predetermined architecture.

No automatic conversion of `records.bin`/notes or rewriting external harness
databases is part of this decision. A future history migration needs its own
search-quality, incremental-ingest, privacy, export and rollback evaluation.

## Primary references

[S1] SQLite WAL: https://www.sqlite.org/wal.html and appropriate uses:
https://www.sqlite.org/whentouse.html (checked 2026-09-06).

[S2] DuckDB concurrency: https://duckdb.org/docs/current/connect/concurrency
(checked 2026-09-06; distinguish current docs from LTS behavior).

[S3] DuckDB indexes: https://duckdb.org/docs/current/guides/performance/indexing
(checked 2026-09-06).

Repository evidence: `docs/ARCHITECTURE.md`, `internal/instructions/store.go`,
`internal/instructions/resolve.go`, `internal/instructions/benchmark_test.go`,
and `tools/instruction-storage/compare.py` at the baseline commit above.
