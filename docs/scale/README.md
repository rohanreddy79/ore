# Scale testing: breaking Ore on purpose and fixing what breaks

This folder holds the harness, the recorded results and the findings from pushing Ore past its limits: a 100-million-line repository, hundreds of concurrent clients, dozens of concurrent agent and pipeline runs. It is written to be rerun by anyone: every number below comes from `results/*.jsonl` produced by `scripts/scale/run.sh` and `cmd/orebench`, on one laptop (8 CPUs, 9 GB RAM, Docker limited to 4 CPUs and 2 GB).

What a laptop can and cannot prove: it can find the breakpoints of one Ore process on one database with real repositories and real git clients, which is the unit that everything larger is built from (one cell). It cannot prove ten billion users; that is a question of cells, object storage and routing, designed in `docs/03-architecture.md` §4 and §6 and `docs/13-scale-and-horizons.md`, and the per-cell numbers here are what those designs multiply.

## The harness

- `bin/orebench genrepo` builds a synthetic repository of N files × L lines of code-like Go (compresses like source); `--commits` adds history.
- `bin/orebench push | api | commits | agents | pipelines` drive a running server concurrently and print JSON lines with p50/p95/p99/max latency, throughput, error kinds and the first bytes of error bodies.
- `scripts/scale/run.sh <results.jsonl> [--db url] [--executor host|docker] -- '<scenario>' ...` starts an isolated server, samples its RSS, descriptors and goroutines every second, runs the scenarios and appends a server summary (peak RSS, peak descriptors, errors and panics in the log).
- `scripts/scale/round1.sh` runs the 100M-line repository round; `scripts/scale/round2.sh` runs the mixed-traffic round on SQLite and on a throwaway Postgres container. Both take a result-file prefix so reruns after fixes sit next to the originals.

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

## Round 1b: the 100M-line repository with sparse paths (SQLite)

`results/r1b-100m-sparse-sqlite.jsonl`, the same scenarios after the repository declares `paths:` in `.ore/pipeline.yaml`:

| Operation | Round 1 | Round 1b |
|---|---|---|
| Push of the 446 MB pack | 11.6 s | 9.3 s |
| Refs, log, file reads with 20 clients | p50 25–27 ms | p50 16–18 ms |
| Recursive tree listing with 20 clients | p50 681 ms | p50 565 ms (still 3.5 MB per response; the per-commit cache came after this run) |
| Server-side commits on the 100k-file tree | p50 655 ms | p50 566 ms |
| Pipeline run | 51 s | **1.0 s** (2 paths materialized in ~0.7 s) |
| Agent run | 100 s | sparse checkout in 0.9 s, then **failed**: `git add -A` refused the agent's edits outside the sparse cone. Fixed with `git add -A --sparse` (test added); measured again in Round 3c below |
| Server peak RSS | 325 MB | 238 MB |

## Round 2: mixed traffic on the 1M-line repository (SQLite and Postgres)

`results/r2-sqlite.jsonl`, `results/r2-postgres.jsonl` (Postgres 16 in Docker, 4 CPUs). Ten repositories pushed, then at once: 200 API clients (tree, file, log, events, changes, issues) for 30 s, 50 writers landing 500 commits on one repository, 20 concurrent pipeline runs (40 in all), 10 concurrent agent runs (20 in all); then 300 API clients for 20 s.

| Scenario | SQLite | Postgres |
|---|---|---|
| API, 200 clients | 280 req/s, p50 157 ms, p99 2.4 s; git-backed reads p50 1.4–1.5 s | 158 req/s, p50 641 ms, p99 3.7 s; git-backed reads p50 2.1–2.4 s |
| 500 commits from 50 writers | all landed, p50 2.5 s, p99 20 s, 10.8/s | all landed, p50 3.6 s, p99 23 s, 8.2/s |
| 40 pipeline runs, 20 at a time | 40/40 succeeded, p50 60 s each | 40/40, p50 85 s |
| 20 agent runs, 10 at a time | 20/20 blocked at L2 (as designed), p50 94 s | **14/20**: one "open change" duplicate key, five issue creations answered 500 |
| API, 300 clients | **101 client-side dial/read timeouts**, 9 × `POST /orgs` 500 | 0 errors, 5 × `POST /orgs` 500 |
| Server peak RSS / descriptors | 130 MB / 1,988 | 111 MB / 1,848 |

What broke, and the fixes:

| What broke | Cause | Fix |
|---|---|---|
| Creating an org or repository that exists answered 500 | unique violation not mapped | 409 on either database (test) |
| Postgres only: concurrent issue and change creation failed with `duplicate key value violates unique constraint` on (repository, number) | numbers were allocated as `MAX(number)+1` with no lock; under READ COMMITTED two transactions read the same maximum. SQLite's `BEGIN IMMEDIATE` serializes writers, which hid the race | the repository row is locked (`SELECT … FOR UPDATE`, a no-op on SQLite) before a number is allocated; the same for patch-set numbers and the GitHub importer; a 48-way concurrency test runs on both dialects |
| Git-backed reads took 1.4 s under load while ledger reads took 10 ms | every read spawned one or two git processes (`rev-parse`, then the read); 200 clients meant hundreds of forks queueing on 8 CPUs | a branch resolves from its ref file or `packed-refs` without a process (`gitx.ResolveRef`; tags and abbreviations still go through git); log, file and tree results are cached per commit in a byte-bounded cache (`ORE_READ_CACHE_MB`, default 256) with one shared load per key, so a hot branch costs no process at all |
| 20 pipeline runs at once took 60–85 s each; materializing the 10k-file tree took 5–24 s per run under load | no concurrency cap, and every run extracted the whole tree from the pack | runs beyond the CPU count queue (`ORE_PIPELINE_CONCURRENCY`), agents beyond half of it (`ORE_AGENT_CONCURRENCY`); materialized trees are cached per (repository, commit, paths) and cloned copy-on-write for each run (APFS `clonefile`, btrfs/xfs reflink; a plain copy elsewhere), the last four trees per repository kept (`ORE_TREE_CACHE_KEEP`) |
| 300 fresh clients: connection timeouts and resets | the kernel's accept backlog (128 on macOS, 4096 on Linux ≥ 5.4) overflowed on the burst; the server never saw those connections | `ore doctor` warns when `somaxconn` is under 1,024 and says what to set; `orebench api --ramp` spreads client start-up like a load balancer would |
| Postgres pool fixed at 16 connections | | sized for request concurrency (4 × CPUs, 8–64), `ORE_DB_MAX_CONNS` overrides |
| Agents could not use the server's Anthropic key | Claude Code prefers a stored interactive login over `ANTHROPIC_API_KEY`, so a stale login on the host overrode the key | agent runs get their own `CLAUDE_CONFIG_DIR` beside the logs when a key is set (test) |
| The harness truncated error kinds at 60 characters | | 200 characters including the response body's first bytes |

## Round 3: the mixed round again after the fixes

`results/r3-sqlite.jsonl`, `results/r3-postgres.jsonl`, same mix and same machine:

| Scenario | SQLite, Round 2 → 3 | Postgres, Round 2 → 3 |
|---|---|---|
| API, 200 clients | 280 → **822 req/s**; p99 2.4 s → 0.98 s; git-backed reads p50 1.4 s → 0.17 s | 158 → 445 req/s; p99 3.7 s → 1.36 s |
| 500 commits from 50 writers | p99 20 s → **4.3 s**; 10.8 → 20.7 commits/s | p99 23 s → 7.2 s; 8.2 → 14.2/s |
| 40 pipeline runs | 137 s → 116 s for the batch (8 at a time) | 40/40 |
| 20 agent runs | 20/20, p50 94 → 78 s (they queue behind the pipelines) | **20/20** (was 14/20), 0 errors |
| API, 300 clients | 101 errors → **0**; 257 → ~3,850 req/s; p50 47–69 ms per kind | 508 req/s, p99 1.9 s; 407 connection resets from the 128-entry backlog |
| Server peak RSS | 113 MB | 134 MB |

Round 3b measures the same reads on an otherwise idle server with 50 clients (`results/r3b-dbonly-*.jsonl`): SQLite answers ledger reads at 29,400 req/s (p50 1.4 ms) and cached git reads at 5,050 req/s; Postgres in Docker at 8,280 req/s (p50 5.4 ms) and 3,590 req/s, zero errors. The distance between these and the mixed round is the host's CPU, shared with 50 writers, 8 pipeline runs and the agents.

## Round 3c: the 100M-line repository again after the fixes

`results/r3-100m-sqlite.jsonl` (`scripts/scale/round1.sh … r3`), the Round 1 scenarios plus 200 clients on the paged endpoints:

| Operation | Round 1 | Round 1b | Round 3c |
|---|---|---|---|
| Push of the 446 MB pack | 11.6 s | 9.3 s | 13.1 s |
| Recursive tree listing (100k paths), 20 clients | p50 681 ms | p50 565 ms | **p50 15.5 ms**, p99 78 ms |
| Log and file reads, 20 clients | p50 25–27 ms | p50 16–18 ms | **p50 3.2 ms** |
| 20 clients, all kinds | 92 req/s | 126 req/s | **1,147 req/s**, 0 errors |
| 200 clients: directory listings, file, log, refs | — | — | **2,105 req/s**, p50 1.8–2.2 ms per kind; `refs` p50 429 ms (the one endpoint that still spawned a process: fixed below, remeasured in Round 4c) |
| Server-side commits on the 100k-file tree | p50 655 ms | p50 566 ms | p50 808 ms (while 200 clients read) |
| Pipeline run | 51 s | 1.0 s | 1.0 s (sparse; 0.7 s cloned from the tree cache, 1.5 s on the first run) |
| Agent run to the verify gate | 100 s | failed | **3.5 s** (sparse checkout 0.8–1.3 s, edit, push, change opened, pipeline run) |
| Server peak RSS | 325 MB | 238 MB | 173 MB |

The ref listing spawned `git for-each-ref` per request; it now reads `packed-refs` and the loose ref files directly (verified against `for-each-ref` in a test; reftable repositories fall back to git), and listing pages are encoded once per commit and served as bytes.

## Round 4 and 5: pipelines that clone instead of extract

The Round 3 pipeline logs showed where their time went: with 8 runs materializing at once beside 200 API clients and 50 writers, extracting the 10k-file tree took 5–24 s per run (1.6 s on an idle machine). Round 4 (`results/r4-*.jsonl`) cached each tree and copied it per run with per-file clones (`cp -c`), which was no faster under load (11–30 s: 10,000 syscalls queue like 10,000 extractions). Round 5 (`results/r5-*.jsonl`) clones the cached tree with one directory `clonefile(2)` call (32 ms for 2,000 files while the same machine is saturated) and, on Linux, falls back to per-file reflinks.

| Scenario | Round 3 | Round 4 | Round 5 |
|---|---|---|---|
| 40 pipeline runs, 20 at a time (SQLite) | 116 s for the batch | 138 s | **28 s**, p50 9.3 s per run including queueing |
| 40 pipeline runs (Postgres) | — | 154 s | 44 s |
| 20 agent runs, 10 at a time (SQLite) | 180 s, p50 78 s | 228 s | **130 s, p50 49 s** |
| API, 200 clients, mixed (SQLite) | 822 req/s | 1,528 req/s | **2,177 req/s**, p99 439 ms |
| API, 200 clients, mixed (Postgres) | 445 req/s | 556 req/s | 900 req/s |
| API, 300 fresh clients at once (SQLite) | 0 errors, ~3,850 req/s | 1,162 resets | **9,918 req/s, 0 errors** |
| API, 300 clients ramped over 2 s (SQLite) | — | — | **14,324 req/s**, p50 17 ms, p99 62 ms |
| API, 300 clients at once / ramped (Postgres) | 407 resets | 0 errors, 2,285 req/s | 45 resets, 4,091 req/s / 0 errors, 5,516 req/s |
| 500 commits from 50 writers (SQLite) | p50 2.3 s, p99 4.3 s | p50 1.7 s, p99 3.3 s | p50 1.7 s, **p99 15 s** |
| Server peak RSS (SQLite / Postgres) | 113 / 134 MB | 111 / 95 MB | 78 / 74 MB |

Whether 300 simultaneous connections are reset is decided by the 128-entry kernel backlog on this machine, not by Ore: the same burst sees 0 or 1,162 resets from one round to the next, and the ramped burst never sees one. The agent batches also show where the remaining time is: an agent run waits for its own pipeline run, which queued behind the 40 bench runs.

Two more fixes went in after Round 5:
- A tree for a fresh commit is built from the nearest cached tree plus the diff (clone, delete removed paths, extract changed ones) instead of extracting everything: the 20 agent commits each cost their changed files, not 10,000 extractions. Deletions, file-to-directory and directory-to-file changes and mode changes are covered by a test.
- Server-side commits to one branch now take turns inside the process. Fifty writers racing with compare-and-swap all land, but every lost race costs three git processes and a retry, which is where the p99 of 15 s came from; queued writers keep the same throughput with a bounded, first-come tail. The compare-and-swap stays, for pushes and other processes.

## Round 6: incremental trees and queued writers

`results/r6-sqlite.jsonl`, `results/r6-postgres.jsonl`, the same mix with the two fixes above:

| Scenario | Round 5 | Round 6 (SQLite) | Round 6 (Postgres) |
|---|---|---|---|
| 20 agent runs, 10 at a time | 130 s, p50 49 s | **108 s, p50 34 s** | 115 s, p50 34 s |
| Trees for the agents' fresh commits | extracted, 4–38 s each under load | **built from the previous tree and the diff: 1.1 s average, 2.0 s worst** (16 of 16); 5 cold extractions remain, one per repository | same |
| 40 pipeline runs | 28 s | 27 s | 34 s |
| 500 commits from 50 writers | p50 1.7 s, p99 15 s | p50 1.6 s, p99 11.9 s, 18.2/s | p50 2.0 s, p99 16 s, 14.7/s |
| API, 200 clients, mixed | 2,177 req/s | **2,461 req/s**, p99 369 ms | 1,045 req/s |
| API, 300 clients ramped over 2 s | 14,324 req/s | **16,976 req/s**, p50 14.5 ms, p99 53 ms, 0 errors | 6,144 req/s, 0 errors |
| API, 300 clients at once | 0 resets | 433 resets (kernel backlog) | 264 resets |
| Server peak RSS | 78 / 74 MB | 77 MB | 69 MB |

The commit tail is now the queue itself: fifty writers on one branch, each commit about 240 ms under this load, so the fiftieth in line waits twelve seconds; throughput is the same as racing, with no wasted git work and a first-come order. Spreading writers across branches, or across repositories, removes the queue; a single hot branch is serialized by the ref itself in any design.

## Round 4c: the 100M-line repository after the ref and page fixes

`results/r4-100m-sqlite.jsonl`:

| Operation | Round 3c | Round 4c |
|---|---|---|
| 20 clients: recursive tree (100k paths, 20,000 per page), log, file, refs | 1,147 req/s, tree p50 15.5 ms | **20,704 req/s**, every kind p50 0.6–0.8 ms, p99 under 5.5 ms (3.4 GB/s of listing pages over loopback) |
| 200 clients: directory listings, file, log, refs | 2,105 req/s, refs p50 429 ms | **27,694 req/s**, p50 6.2 ms, p99 23 ms, refs p50 6.3 ms, 0 errors |
| Server-side commits on the 100k-file tree | p50 808 ms | p50 1.17 s (while 200 clients read) |
| Pipeline run (sparse) | 1.0 s | 0.5 s |
| Agent run to the verify gate | 3.5 s | 3.0 s |
| Server peak RSS | 173 MB | 70 MB |

## Tuning knobs (all optional; defaults come from the CPU count)

| Variable | Default | What it bounds |
|---|---|---|
| `ORE_GIT_CONCURRENCY` | 4 × CPUs (min 8) | git processes running at once in one server |
| `ORE_PIPELINE_CONCURRENCY` | CPUs (min 2) | pipeline runs executing at once; the rest wait as queued |
| `ORE_AGENT_CONCURRENCY` | CPUs ÷ 2 (min 2) | agent runs executing at once |
| `ORE_DB_MAX_CONNS` | SQLite 2 × CPUs (4–32); Postgres 4 × CPUs (8–64) | database connections |
| `ORE_READ_CACHE_MB` | 256 | per-commit cache of tree listings, encoded listing pages, logs and files under 4 MB |
| `ORE_TREE_CACHE` / `ORE_TREE_CACHE_KEEP` | on / 4 | materialized trees kept per repository under `<data>/cache/trees`, cloned per run |
| `net.core.somaxconn` (kernel) | Linux 4096, macOS 128 | connections queued before the server accepts them; `ore doctor` warns under 1,024 |

## Known limits that remain (and where the design takes them)

- A full checkout of a 100M-line tree is inherently tens of seconds and gigabytes; sparse paths remove it for declared areas, and the virtual-filesystem client in `docs/09-roadmap.md` v3 removes it for everything.
- One SQLite file is one writer; mixed write-heavy traffic will queue there. The Postgres profile is the team/cloud answer; Round 2 measures both.
- One process, one disk: beyond that are cells and object-storage git (`docs/03` §4, §6).
- Kernel settings are part of the deployment: an accept backlog under 1,024 resets connection bursts before Ore sees them (`ore doctor` says so); Postgres inside Docker Desktop costs 4 ms per query where a native server costs under 1 ms.
- The tree cache trades disk for CPU: four trees per repository, copy-on-write clones per run. On a filesystem without reflinks (ext4) a run still copies the tree once, which is cheaper than extracting it but not free.
