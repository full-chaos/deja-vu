#!/usr/bin/env python3
"""Synthetic SQLite/DuckDB workload probe; never reads agent history.

Run both engines on the same machine. Loader implementations differ and load
seconds must not be read as an apples-to-apples engine comparison. Query results
are verified. This is not a multi-process concurrency or Deja search benchmark.
"""
from __future__ import annotations

import argparse
import csv
import importlib
import json
import platform
import random
import sqlite3
import statistics
import tempfile
import time
from pathlib import Path
from typing import Any


def measure(operation: Any, repeats: int) -> dict[str, float]:
    durations = []
    for _ in range(repeats):
        start = time.perf_counter_ns()
        operation()
        durations.append((time.perf_counter_ns() - start) / 1_000_000)
    durations.sort()
    return {"median_ms": statistics.median(durations),
            "p95_ms": durations[min(len(durations) - 1, int(len(durations) * .95))]}


def probe(engine: str, directory: Path, csv_path: Path, rows: int,
          samples: int, scans: int, commits: int) -> dict[str, Any]:
    module = sqlite3 if engine == "sqlite" else importlib.import_module("duckdb")
    db = directory / (engine + ".db")
    connection = module.connect(str(db))
    try:
        if engine == "sqlite":
            connection.execute("PRAGMA journal_mode=WAL")
            connection.execute("PRAGMA synchronous=FULL")
        else:
            connection.execute("SET threads=1")
        connection.execute("CREATE TABLE events (id BIGINT PRIMARY KEY, scope INTEGER, flag INTEGER, payload VARCHAR)")
        start = time.perf_counter()
        if engine == "sqlite":
            with csv_path.open(newline="") as stream:
                connection.executemany("INSERT INTO events VALUES (?, ?, ?, ?)", csv.reader(stream))
            connection.commit()
        else:
            source = str(csv_path).replace("'", "''")
            connection.execute(f"COPY events FROM '{source}' (HEADER FALSE, DELIMITER ',')")
        load_seconds = time.perf_counter() - start
        connection.execute("CREATE INDEX scope_index ON events(scope)")
        connection.commit()
        query = "SELECT scope, COUNT(*), SUM(flag) FROM events GROUP BY scope ORDER BY scope"
        expected: dict[int, list[int]] = {}
        for i in range(rows):
            bucket = expected.setdefault(i % 256, [0, 0])
            bucket[0] += 1
            bucket[1] += i % 3
        actual = [(int(g), int(n), int(s)) for g, n, s in connection.execute(query).fetchall()]
        assert actual == [(g, *expected[g]) for g in sorted(expected)], "aggregation mismatch"
        rng = random.Random(0)
        ids = iter(rng.randrange(rows) for _ in range(samples))

        def point() -> None:
            identifier = next(ids)
            result = connection.execute("SELECT payload FROM events WHERE id=?", [identifier]).fetchone()
            assert result == (f"synthetic-event-{identifier}",), "point lookup mismatch"

        points = measure(point, samples)
        aggregations = measure(lambda: connection.execute(query).fetchall(), scans)

        def update() -> None:
            connection.execute("BEGIN TRANSACTION")
            connection.execute("UPDATE events SET flag=flag+1 WHERE id=0")
            connection.commit()

        mutations = measure(update, commits)
        assert connection.execute("SELECT flag FROM events WHERE id=0").fetchone()[0] == commits
    finally:
        connection.close()

    def reopen() -> None:
        handle = module.connect(str(db))
        try:
            assert handle.execute("SELECT COUNT(*) FROM events WHERE id=0").fetchone()[0] == 1
        finally:
            handle.close()

    return {"engine": engine,
            "version": sqlite3.sqlite_version if engine == "sqlite" else module.__version__,
            "rows": rows, "load_seconds_not_comparable": load_seconds,
            "warm_primary_key_lookup": points, "warm_grouped_scan": aggregations,
            "single_row_commits": mutations, "reopen_and_lookup": measure(reopen, min(20, samples)),
            "database_bytes_after_close": db.stat().st_size}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--engines", nargs="+", choices=("sqlite", "duckdb"), default=["sqlite", "duckdb"])
    parser.add_argument("--rows", type=int, default=100_000)
    parser.add_argument("--samples", type=int, default=200)
    parser.add_argument("--scans", type=int, default=5)
    parser.add_argument("--commits", type=int, default=25)
    args = parser.parse_args()
    if min(args.rows, args.samples, args.scans, args.commits) < 1:
        parser.error("all counts must be positive")
    if "duckdb" in args.engines:
        try:
            importlib.import_module("duckdb")
        except ImportError:
            parser.error("DuckDB Python package is required for that engine; no dependencies are installed automatically")
    with tempfile.TemporaryDirectory(prefix="deja-storage-probe-") as temporary:
        directory = Path(temporary)
        csv_path = directory / "synthetic.csv"
        with csv_path.open("w", newline="") as stream:
            csv.writer(stream).writerows((i, i % 256, i % 3, f"synthetic-event-{i}") for i in range(args.rows))
        report = {"python": platform.python_version(), "platform": platform.system(),
                  "notes": ["Synthetic warm-cache microbenchmarks; no agent history.",
                            "Single-process measurements; concurrency and process startup are not measured.",
                            "SQLite WAL/FULL and DuckDB defaults; DuckDB query threads fixed to one.",
                            "Bulk loader implementations differ; compare query/commit timings separately."],
                  "results": [probe(e, directory, csv_path, args.rows, args.samples, args.scans, args.commits)
                              for e in dict.fromkeys(args.engines)]}
        print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
