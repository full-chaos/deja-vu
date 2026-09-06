# Storage decision: no blanket SQLite-to-DuckDB migration

Status: provisional; a history storage migration is deliberately not in this PR.

The existing Deja index is not SQLite. `docs/ARCHITECTURE.md` describes
`records.bin`, token posting buckets and Gob metadata under an `index.db`
directory. Primary explicit notes use JSONL. Some harness source databases are
read using the SQLite CLI. Changing those external source formats is not a Deja
storage-engine decision.

Instruction configuration, action receipts and historical analytics are three
different workloads. This PR only implements the first, behind a backend
interface. Versioned JSON is a reviewable first backend, not a recommendation
for high-volume records or an argument against DuckDB.

For selection, compare:

| Workload | Necessary measurement |
| --- | --- |
| Active instruction resolution | Small scoped reads, complete mandatory recall, hook startup and parse cost |
| Rule approval and exception changes | Atomic writes, revision checks, recovery and concurrent processes |
| Action/receipt history | Append throughput, bounded retention, scan and grouping cost |
| Transcript research | Search postings/FTS versus scans; scope filtering and replay/update costs |

SQLite's suitability cannot be inferred from database size alone. Its official
usage guide describes efficient embedded/local storage and a single writer per
database file with concurrent readers. Indexes, query shapes, transactions and
contention need measurement, not a blanket claim that SQLite is slow.

DuckDB is an important candidate for grouped event/receipt analytics. Its
current concurrency documentation distinguishes direct embedded access (one
read/write process, or multiple read-only processes) from multi-process write
access through Quack's remote protocol. Quack is documented as beta. Choosing
that path changes the architecture: a shared writer/server is not the same as
many independent hooks opening one embedded database for writes.

A likely future split worth testing is a compact active-rule projection for
hooks with DuckDB for retained receipt/history analysis. That is a hypothesis,
not an implemented or benchmark-proven decision. A direct DuckDB backend also
needs packaging and Go dependency decisions; this PR adds no runtime Go deps.

## Reproducible probes

```sh
# Bounded registry: resolution alone and load+resolve+render, 100/1000/10000 rules.
go test ./internal/instructions -run '^$' -bench . -benchmem

# Synthetic SQL workloads. DuckDB's Python package must already be available.
python3 tools/instruction-storage/compare.py --rows 100000
python3 tools/instruction-storage/compare.py --rows 1000000

# An explicit one-engine run is not a comparison.
python3 tools/instruction-storage/compare.py --engines sqlite --rows 100000
```

The SQL probe verifies primary-key lookups and grouped results and measures
warm indexed reads, grouped scans, single-row commits, and reopen+lookup cost.
It creates only temporary synthetic data and records engine versions. SQLite
uses WAL/FULL; DuckDB query threads are one. Bulk loaders differ, so loader
seconds are labeled non-comparable. It does not measure OS cold caches, process
startup, real Deja retrieval, Quack, multi-process concurrency, or a workload
with agent-specific payload distributions. Do not generalize it into an engine
winner without the missing tests.

The registry probe uses one applicable rule and many explicitly unrelated tasks.
It measures complete explanation construction too; it is not a million-message
history benchmark. Large-snapshot JSON validation/copying is a real limitation
of this first backend. Keep runtime lookup optimization separate from storage
migration, and test actual hook latency/context size before enabling broadly.

## Primary references (checked September 6, 2026)

- Existing repository: `docs/ARCHITECTURE.md`, index format and notes source.
- SQLite: https://www.sqlite.org/whentouse.html
- DuckDB: https://duckdb.org/docs/current/connect/concurrency
- Codex: https://developers.openai.com/codex/hooks
- Claude Code: https://code.claude.com/docs/en/hooks
