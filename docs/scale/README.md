# Scale testing: breaking Ore on purpose and fixing what breaks

This folder holds the harness, the recorded results and the findings from pushing Ore past its limits: a 100-million-line repository, hundreds of concurrent clients, dozens of concurrent agent and pipeline runs. It is written to be rerun by anyone: every number below comes from `results/*.jsonl` produced by `scripts/scale/run.sh` and `cmd/orebench`, on one laptop (8 CPUs, 9 GB RAM, Docker limited to 4 CPUs and 2 GB).

What a laptop can and cannot prove: it can find the breakpoints of one Ore process on one database with real repositories and real git clients, which is the unit that everything larger is built from (one cell). It cannot prove ten billion users; that is a question of cells, object storage and routing, designed in `docs/03-architecture.md` §4 and §6 and `docs/13-scale-and-horizons.md`, and the per-cell numbers here are what those designs multiply.

## The harness

- `bin/orebench genrepo` builds a synthetic repository of N files × L lines of code-like Go (compresses like source); `--commits` adds history.
- `bin/orebench push | api | commits | agents | pipelines` drive a running server concurrently and print JSON lines with p50/p95/p99/max latency, throughput, error kinds and the first bytes of error bodies.
- `scripts/scale/run.sh <results.jsonl> [--db url] [--executor host|docker] -- '<scenario>' ...` starts an isolated server, samples its RSS, descriptors and goroutines every second, runs the scenarios and appends a server summary (peak RSS, peak descriptors, errors and panics in the log).
- `scripts/scale/round2.sh` runs the mixed-traffic round on SQLite and on a throwaway Postgres container.

Repositories used: `repo1m` (10,000 files, 1M lines, 22 MB, 82 MB of loose objects) and `repo100m` (100,000 files, 100M lines, 2.14 GB of source, 446 MB packed; generated in 173 s).

## Round 0: baseline on the 1M-line repository (SQLite, host executor)

Everything broke on the first run (`results/r0-baseline-sqlite.jsonl`):

| What broke | Cause | Fix |
|---|---|---|
| Every push over HTTP failed ("remote end hung up", HTTP 400) | Go's `net/http/cgi` rejects chunked request bodies, and git switches to chunked encoding for any push over 1 MiB (`http.postBuffer`); every earlier test had pushed tiny repositories | `internal/githttp` now runs `git upload-pack`/`receive-pack --stateless-rpc` directly, streaming stdin and stdout (gzip request bodies supported), no CGI |
| Tree, file and log reads returned 500 on an empty repository | git's "unknown revision" surfaced as an internal error | mapped to 404 (`gitErr`) |
| 5 of 200 concurrent server-side commits returned 500 | compare-and-swap lost the race on a busy branch and gave up | commits are lock-free: rebuild on the new tip and retry (up to 8 times), 409 only if the branch never settles |
| Pipeline and agent runs failed | downstream of the push failure | — |
| Bursts of 500s at the end of each load window | client cancellations at the deadline were logged as server errors | context cancellation now answers 499 and is not an error |
| All agent runs exited 127 | the harness did not quote a path with a space | harness fix |
| Unbounded JSON bodies | no limit on API request bodies | 64 MiB cap (`http.MaxBytesReader`) |
| Live streams buffered | the operations middleware's response wrapper had no `Flush` | passthrough added (`cmd/ore/metrics.go`) |

After the fixes (`results/r0b-fixed-sqlite.jsonl`): 5 pushes of 82 MB each at 1.6 s; 100 concurrent API clients at ~690 requests/s overall (tree p50 182 ms, file p50 138 ms, events p50 81 ms, p99 under 480 ms), zero errors; 200 commits from 20 writers on one repository, all landed (p50 453 ms, p99 1.4 s, 37 commits/s); 10 pipeline runs succeeded; server peak 76 MB RSS, 329 descriptors.

## Round 1: the 100-million-line repository (SQLite, host executor)

`results/r1-100m-sqlite.jsonl`, before the sparse fixes:

| Operation | Result |
|---|---|
| Push of the 446 MB pack (100k files) | 11.6 s, success |
| Refs, log, file reads with 20 clients | p50 25–27 ms, p99 under 140 ms |
| Recursive tree listing with 20 clients | 3.5 MB per response, p50 681 ms, p99 1.6 s: **a scale bug** |
| Server-side commits on the 100k-file tree | p50 655 ms (20 commits, 5 writers, no errors) |
| Pipeline run (full materialization of 2.1 GB per run) | 51 s each: **a scale bug** |
| Agent run (full clone copying the pack, full checkout, then verify) | 100 s each: **a scale bug** |
| Server peak RSS | 325 MB |

Fixes:
- Tree listings are scoped: `?path=` for a subtree, `?depth=1` for one directory's children (directories end with `/`), `limit` (default 20,000, max 100,000) and `offset`, with `X-Ore-Tree-Total` and `X-Ore-Tree-Truncated` headers. The MCP `repos_tree` tool takes `path` and `depth` and caps at 5,000 entries with `truncated: true`. The agent prompt lists top-level entries only and the Dockerfile check is one object lookup.
- Sparse materialization: a pipeline file may declare `paths:` (the repository's active area, read straight from git before any checkout); pipelines materialize only those paths and agent workspaces use a cone-mode sparse checkout of them. Workspaces are shared clones (no pack copy) and are deleted in the background after the run.
- SQLite is opened with a connection pool sized for concurrent readers (WAL, `BEGIN IMMEDIATE` for writers, 30 s busy timeout) instead of a single connection.

Round 1b reruns the same scenarios on the sparse repository (`results/r1b-100m-sparse-sqlite.jsonl`); Round 2 runs mixed traffic (`results/r2-sqlite.jsonl`, `results/r2-postgres.jsonl`). Their numbers are appended below as they complete.

## Known limits that remain (and where the design takes them)

- A full checkout of a 100M-line tree is inherently tens of seconds and gigabytes; sparse paths remove it for declared areas, and the virtual-filesystem client in `docs/09-roadmap.md` v3 removes it for everything.
- One SQLite file is one writer; mixed write-heavy traffic will queue there. The Postgres profile is the team/cloud answer; Round 2 measures both.
- One process, one disk: beyond that are cells and object-storage git (`docs/03` §4, §6).
