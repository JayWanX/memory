# Memory algorithm performance audit

Updated: 2026-10-03. Current baseline: default branch `master` at `5c98ff9c`.
Current optimized implementation: `9393edbe`, after merging that baseline in
`6d1511ac`. Work remains isolated in `codex/memory-algorithm-performance`.

[中文报告](../zh/development/memory-algorithm-performance.md)

## Revalidation against the latest master

The default branch is named `master`. It now includes PR #148: covering edge
indexes, scoped supersedes reads, a caller-loaded active-insight snapshot,
per-recall ranked-transition caching, embedding keep-alive, mmap and database
compaction. Those changes are retained. They are part of the **new baseline**,
not attributed to this PR's incremental gains. The historical audit below
preserves the earlier measurements separately.

The merge preserves upstream's correctness rule: rank each complete neighborhood
by intent-weighted structure plus cosine similarity **before** applying the visit
budget. Transition ties still use insight ID and edge type. The request-local
cache now retains those ranked transitions and reuses precomputed scalar cosine
scores instead of recalculating cosine for each edge. Query and intent are fixed
for the lifetime of that cache.

An additional change reuses the vector decode buffer within a synchronous
embedding scan. `DeserializeVector` and `DeserializeVectorInto` share one float32
decoder; callers that need an owned vector still get one. Recall consumes each
vector's score before the next row overwrites the buffer. The stored format,
arithmetic order, invalid-blob behavior and query lifetime are unchanged.

Against current master, both versions already make at most U successful
neighborhood reads for U distinct expanded nodes. Let E_U be the total incident
edges loaded, C the reranked candidates, and d the vector dimension. The cosine
component changes from `O((N + E_U + C)d)` to `O(Nd + E_U + C)`. Both versions
retain `O(sum(deg(v) log deg(v)))` neighborhood sorting before traversal, as
required by the upstream budget rule. Dense neighborhoods therefore remain
expensive; a visit budget does not bound the size of the adjacency lists read.

For fixed dimension d, buffer reuse reduces cumulative temporary decoded-vector
allocation from `O(Nd)` to `O(d)` per recall. With mixed dimensions it allocates
only when a larger capacity is needed. This is an allocation improvement;
exact vector comparison remains `O(Nd)`, scalar caches remain `O(N)`, and the
reported B/op values are cumulative allocation, not peak resident memory. The
ordered-read indexes, local BFS, semantic top-K and transaction statement reuse
retain the bounds described in the historical audit.

### Fresh measurements

The fixture and methodology below are unchanged: N=1/10/100/1,000/10,000,
128-dimensional vectors, real temporary SQLite WAL stores, Apple M4,
Go 1.25.4, one P, five alternating rounds at 100 ms per case. The final dataset
contains **108 cases × 5 rounds × 2 versions = 1,080 samples**. It compares
`5c98ff9c` directly with `9393edbe`; intermediate merge-only results are excluded.
No other local test suite ran concurrently with these measurements.

CPU **µs/op**, current master → final PR, median of five runs:

| Path | N=1 | N=10 | N=100 |
|---|---:|---:|---:|
| ByID | 10.3 → 10.3 | 10.3 → 10.4 | 10.4 → 10.6 |
| AllActive | 11.3 → 11.2 | 33.2 → 31.2 | 262.4 → 216.6 |
| Ranked | 22.9 → 21.4 | 45.6 → 42.3 | 50.1 → 41.4 |
| SourceLatest | 15.9 → 14.4 | 15.9 → 14.7 | 21.0 → 14.6 |
| SourceRecent | 23.6 → 22.7 | 24.0 → 22.9 | 45.8 → 42.9 |
| Entity | 19.7 → 16.5 | 25.6 → 23.5 | 98.0 → 23.6 |
| EntityMiss | 19.2 → 16.3 | 20.8 → 20.6 | 64.4 → 61.7 |
| RankedKeywordMiss | 22.7 → 21.7 | 25.6 → 24.5 | 57.9 → 57.3 |
| Embeddings | 6.1 → 6.5 | 14.3 → 14.6 | 91.1 → 92.0 |
| Keyword | 4.3 → 3.4 | 37.8 → 30.2 | 361.6 → 288.7 |
| Diff | 7.3 → 6.2 | 75.4 → 64.1 | 470.5 → 367.9 |
| RecallKeyword | 74.4 → 72.1 | 318.3 → 302.8 | 2,699.5 → 2,450.1 |
| RecallHybrid | 82.2 → 80.0 | 339.3 → 316.2 | 3,037.5 → 2,555.0 |
| RecallWhy | 47.7 → 45.9 | 453.9 → 423.5 | 3,236.2 → 2,687.0 |
| SemanticEmbedding | 11.6 → 13.4 | 54.0 → 53.5 | 70.6 → 64.0 |
| SemanticTokens | 11.7 → 13.8 | 75.1 → 54.1 | 691.6 → 416.3 |
| Neighborhood | 17.4 → 15.2 | 50.1 → 59.3 | 368.4 → 59.3 |
| AtomicInsert | 75.7 → 76.6 | 278.9 → 177.5 | 2,291.2 → 1,185.3 |
| KnownEntities | 8.0 → 8.5 | 15.8 → 16.0 | 69.5 → 69.1 |
| Retention | 59.3 → 59.1 | 153.5 → 151.5 | 1,107.3 → 1,113.3 |
| EngineProvided | 109.6 → 87.4 | 410.6 → 304.7 | 1,048.6 → 670.7 |

At N=10,000, CPU **ms/op** (lower is better; ratio is master/final):

| Path | CPU ms master → PR | CPU ratio | Allocation KiB master → PR |
|---|---:|---:|---:|
| ByID | 0.0100 → 0.0101 | 0.99× | 2.3 → 2.3 |
| AllActive | 29.2625 → 20.4287 | 1.43× | 14,914.0 → 14,913.3 |
| Ranked | 0.9576 → 0.0398 | 24.04× | 15.9 → 15.9 |
| SourceLatest | 0.6016 → 0.0146 | 41.35× | 2.4 → 2.4 |
| SourceRecent | 0.6606 → 0.0416 | 15.88× | 15.8 → 15.8 |
| Entity | 13.4870 → 0.0235 | 573.77× | 1.5 → 1.5 |
| EntityMiss | 5.5477 → 5.2305 | 1.06× | 0.6 → 0.6 |
| RankedKeywordMiss | 4.0758 → 3.9901 | 1.02× | 1.2 → 1.3 |
| Embeddings | 10.4085 → 9.2843 | 1.12× | 16,160.0 → 16,160.0 |
| Keyword | 33.7453 → 26.5098 | 1.27× | 33,438.9 → 19,688.6 |
| Diff | 36.3317 → 29.0380 | 1.25× | 35,175.5 → 20,903.8 |
| RecallKeyword | 69.9800 → 53.5140 | 1.31× | 49,598.2 → 35,760.8 |
| RecallHybrid | 93.0620 → 66.5310 | 1.40× | 77,004.9 → 47,945.3 |
| RecallWhy | 91.0855 → 67.1605 | 1.36× | 77,072.3 → 48,031.3 |
| SemanticEmbedding | 1.8507 → 0.9890 | 1.87× | 966.1 → 12.4 |
| SemanticTokens | 72.4055 → 41.1787 | 1.76× | 42,593.3 → 22,179.5 |
| Neighborhood | 40.3057 → 0.0595 | 677.77× | 23,425.1 → 9.3 |
| AtomicInsert | 524.3310 → 431.6040 | 1.21× | 12,970.6 → 11,017.7 |
| KnownEntities | 6.7086 → 6.9737 | 0.96× | 1.5 → 1.5 |
| Retention | 170.1040 → 171.6570 | 0.99× | 20,025.9 → 20,025.9 |
| EngineProvided | 37.8323 → 2.3085 | 16.39× | 290.8 → 51.5 |

Dense graph, CPU ms/op and cumulative allocation KiB/op:

| N | CPU ms master → PR | CPU ratio | Allocation KiB master → PR |
|---|---:|---:|---:|
| 1 | 0.0640 → 0.0633 | 1.01× | 11.1 → 9.7 |
| 10 | 0.5254 → 0.4938 | 1.06× | 188.8 → 160.8 |
| 100 | 18.3243 → 16.8662 | 1.09× | 5,553.8 → 5,231.4 |

At N=10,000, hybrid recall allocation fell from 78.85 MB to
49.10 MB per operation (37.7% less). Dense recall at N=100 improves
1.09× over current master. The earlier 6.51× figure used a baseline without
master's transition cache and covering indexes and does not describe this delta.

Small inputs retain fixed-cost tradeoffs: N=10 neighborhood reads take
59.34 µs versus 50.09 µs; N=1 semantic embedding/token paths take 13.36/13.79 µs
versus 11.57/11.75 µs. Known-entity scanning at N=10,000 takes 6.974 ms versus
6.709 ms (about 4% more); retention is near neutral (170.104 → 171.657 ms).
Unmodified embedding loading also varies (10.409 → 9.284 ms), so small timing
ratios should not be interpreted as algorithm changes. ID lookup is near neutral.
The Entity benchmark's large gain is fixture-specific: every record has the entity, so the
indexed read can stop after ten matches. An absent JSON entity or substring
still requires a linear scan. These shared-host timings are observations, not
cold-start or production latency guarantees.

The isolated decode-buffer probe compared the merge commit `6d1511ac` with
`9393edbe`: three alternating rounds, N=10,000, hybrid/WHY recall, 200 ms per
case. Hybrid allocation fell from approximately 59.35 MB to 49.11 MB per call,
about 17%, matching the eliminated 9,999 temporary 128-element float64 buffers.
This probe is separate from the full master-versus-final dataset.

All final samples are saved as [current master](benchmarks/2026-10-03-memory-main.txt)
and [final PR](benchmarks/2026-10-03-memory-synced.txt). The isolated allocation
probe is saved as [decode before](benchmarks/2026-10-03-memory-decode-before.txt)
and [decode after](benchmarks/2026-10-03-memory-decode-after.txt).

### Compatibility and validation after the merge

- `go build -o memory .`, `make test`, and
  `go test -race ./internal/memory/... ./cmd/memory -count=1` passed on the final
  production implementation.
- `MEMORY_EMBED_ENDPOINT=http://127.0.0.1:1 bash scripts/e2e_test.sh`: all
  **258 assertions passed** after the buffer change.
- `npm test --prefix npm/cli`: **13 passed**. The imported OpenCode runtime suite
  and ZCode shell hooks passed locally. The Windows PowerShell hook probe was
  skipped on this macOS host; it belongs to the manual Windows integration suite.
- The independent full-vector traversal oracle is now copied from `5c98ff9c`.
  Cached scores and via labels match for all four intents, three traversal
  budgets, repeated/overlapping anchors, ties, cycles, deleted nodes, real/zero/
  negative query vectors and mismatched dimensions. Upstream's four
  best-transition budget regressions remain covered.
- A shuffled caller-supplied snapshot returns the same time anchors without
  reordering the caller's slice. Decode tests exercise growth, shrinkage,
  invalid bytes, signed zero, subnormals, infinities and NaNs.
- Existing index-plan/migration, bounded-BFS parity, transaction rollback/panic
  and supersedes/compaction tests pass. Equal-score anchor/result order still
  has the pre-existing nondeterminism; no byte-for-byte ordering claim is added.

To reproduce this comparison, use the commands in the historical reproduction
section with **`5c98ff9c` as the baseline and `9393edbe` as the optimized revision**.
Copy the unchanged `performance*_test.go` files from `a21d783b` into the baseline
archive, compile separate binaries and alternate their execution. Paid live
providers, cold filesystem-cache timing and the Agency/Docker integration
umbrella remain outside these measurements.

## Historical audit: 4d312c4e → 766c3965

The rest of this document records the original experiment before the master
sync. Its timings and gains apply only to those historical revisions. In
particular, the original 6.51× dense-recall gain is **not** the incremental gain
over current master; use the fresh comparison above for that decision.

This audit covers the native Memory read/write pipeline: SQLite retrieval,
keyword and vector scoring, multi-signal recall, semantic candidates, bounded
graph traversal, graph writes and retention. Agency authorization, replay and
daemon protocols are outside this change. There is no claim of a universally
optimal implementation: input size, graph density, selectivity and durability
requirements impose different costs.

### What changed

1. Replace three single-column insight indexes with indexes matching active
   timestamp, source/timestamp and importance/timestamp reads. Remove the old
   indexes so writes do not maintain both sets. Query-plan tests verify that
   these ordered reads no longer need a temporary sort. Existing stores upgrade
   on writable open; read-only stores continue to work without migration.
2. Load bounded BFS neighborhoods through endpoint indexes, keeping edge
   insertion order, self-loop deduplication, deleted-node filtering, depth and
   result limits. Unlimited traversal retains bulk loading. Typed neighborhood
   reads use two indexed branches: a factored OR can otherwise make SQLite scan
   every edge of the requested type.
3. Keep per-query keyword and cosine **scores** instead of retaining every token
   set and embedding vector. Stream embedding blobs, compute cosine once per
   vector and reuse it across anchor selection, beam traversal and reranking.
   Reuse already-loaded active insights and cache successful adjacency reads,
   including empty neighborhoods, across anchors. Caches die with the query.
4. Keep only the best K semantic or embedding-only diff candidates with a typed
   heap. Reuse tokenization of the query in semantic token fallback, and merge
   content/tag/entity tokens directly without allocating separate field maps.
5. Reuse SQL's time ordering when it also matches absolute timestamp ordering;
   imported timestamps with different UTC offsets still get the original sort.
6. Skip known-entity extraction scans in explicitly provided entity mode.
   Prepare insight/edge inserts once per transaction and reuse the statements.
   The transaction closes them on commit/rollback; panic cleanup also rolls back
   before clearing the transaction-local pointers.

The scoring formulas, weights, similarity thresholds, RRF constant, anchor
limits, per-anchor visited sets, beam budgets, supersession demotion, exact
duplicate identity checks, causal ordering and embedding failure fallback are
unchanged. Equal-score/equal-time candidates still have no guaranteed relative
order; the old code already combined unordered maps and non-stable sorts. The
optimization does not add approximate nearest-neighbor search or discard
low-ranked candidates before a stage that needs them for score normalization.

### Complexity audit

Let **N** be active insights, **E** graph edges, **L** average text/tag/entity
size, **d** embedding dimensions, **Q** query tokens and **K** requested results.
**M** is the number of matching rows examined by a filtered SQL read. Hash-map
lookups below use expected constant time. Key/payload sizes matter in addition
to the row count.

| Path | Before | After / remaining cost |
|---|---|---|
| ID lookup | B-tree `O(log N)` plus payload decoding | Unchanged control |
| All active insights, sorted by time | `O(N log N + NL)` with temporary sort | `O(N + NL)` ordered index scan |
| Latest/recent by source; unfiltered ranked LIMIT | Scan matching rows and order them; bounded SQLite LIMIT sorting can be `O(M log K)`, latest-one is `O(M)` | `O(log N + K)` for these index-compatible predicates, plus returned payloads |
| Leading-wildcard LIKE / other residual filters | Worst-case scan of candidates | Still worst-case `O(NL)`; an ordering index cannot prove a negative substring match |
| Entity membership in JSON | Inspect matching JSON entries and order candidates | Can stop after K matches in time order; a miss still visits all N rows and their entity entries. No entity inverted index was introduced |
| Keyword ranking | `O(N(L + Q + log K))` | Same asymptotic bound, fewer allocations; recall retains N scalar scores instead of N token sets |
| Exact vector anchor search | `O(Nd + N log K)` | Same exact search bound; computed cosine scores are reused by downstream stages |
| Semantic candidates / embedding-only diff selection | `O(Nd + M log M)` and `O(M)` selection storage | `O(Nd + M log K + K log K)` and `O(K)` selection storage |
| Semantic token fallback | Re-tokenize both texts per pair, then `O(M log M)` sort | Query tokenized once; `O(NL + M log K + K log K)`. Loading all active insights remains linear |
| RRF and time anchors | Combine at most 60 anchors; copy and sort N timestamps | RRF unchanged; `O(N)` order check with no copy for normal UTC data. Mixed-offset fallback remains `O(N log N)` |
| Final reranking / WHY ordering | Candidate scoring plus `O(C log C)` ranking and causal ordering | Ranking and ordering retained. Keyword/cosine signals reused; superseded lookup stays scoped to candidates and fail-closed |
| WHY topological ordering | Indexed outgoing-edge reads, then a heap-based Kahn traversal | Unchanged: `O(C log E + E_out + C log C)` for C returned nodes and their outgoing causal edges; cycles retain the existing fallback |
| Atomic batch of B inserts | B SQL compilations plus B-tree/JSON/durable write costs | One compilation per insert kind per transaction; still `O(B log(N+B))` index work, plus payload and commit I/O |
| Known-entity set / statistics | Whole-store JSON/aggregate scans | Unchanged when required; provided-entity mode avoids the unused scan |
| Retention refresh | Load N rows, aggregate E edges, compute scores, update N rows, sort candidates | Unchanged scan/update work; generally `O(E + N log N)` with current indexes and bounded payload sizes |
| Automatic capacity pruning | Count active rows, select eligible weakest records, then delete/audit a bounded batch | Active count remains `O(N)`; candidate selection can scan/sort eligible rows. Age, immunity, exclusions and atomic audit writes are unchanged |
| Single-vector cosine / serialization | `O(d)` / linear payload size | Unchanged; avoiding repeated calls provides the improvement |
| Batch import with per-item diff/semantic matching | Repeated exact scans as the corpus grows | Still potentially `O(BN + B²)` for fixed text/vector sizes; this PR reduces component costs, not the exact comparison requirement |

For beam search, let **A <= 60** be anchors, **P** node expansions across all
anchors, **U <= P** distinct expanded nodes, **X** processed transitions and
**E_U** the sum of incident edges loaded for those distinct nodes. The original
path issued up to P adjacency queries, reloaded candidate insights and computed
cosine during each transition and final reranking. The new path issues at most
U successful adjacency queries, uses the active insight snapshot already loaded
at recall start and computes each cosine once. The cosine component changes from
`O((N + X + C)d)` to `O(Nd + X + C)`. Heap traversal itself is unchanged. P is
bounded by the existing depth, beam width and per-anchor visit budgets; dense
adjacency lists still cost time to read. Cached adjacency consumes `O(E_U)`
space, in addition to the active insight snapshot and `O(N)` scalar caches.

For a bounded BFS, let **V_b** be expanded nodes, **R_b** distinct encountered
neighbors and **deg(v)** their incident degrees. Full-store loading is replaced
by approximately `O(V_b log E + sum(deg(v) log deg(v)) + R_b log N)` indexed work,
plus payloads. The degree sort preserves the old insertion order. Work depends
on the neighborhood, including missing/deleted endpoints, rather than unrelated
history. A high-degree node can still be expensive. Unlimited BFS retains its
bulk `O(NL + E)` loading and traversal path.

### Measurement method

The committed [benchmarks](../../internal/memory/performance_test.go) use real
temporary SQLite WAL databases and synthetic local vectors. N is **corpus size**,
not Go's adaptive benchmark iteration count. Sizes are 1, 10, 100, 1,000 and
10,000. There are 128 vector dimensions, fixed-width record IDs/text, two tags,
two entities and ten sources. The regular graph is a causal chain with N-1
edges. A separate dense fixture at N=1/10/100 adds N(N-1) semantic edges.

Read limits are 10, semantic candidate limits remain 3/5 and bounded BFS uses
two hops / 20 results. Entity-hit records all contain `SQLite`; `EntityMiss` and
`RankedKeywordMiss` deliberately scan for absent terms. Pure scoring benchmarks
start with inputs already in memory; recall/BFS include their database reads.
Fixtures use historical timestamps, so EngineProvided measures backbone,
entity and semantic work without current-window proximity edges. It replaces
existing edges inside a transaction, keeping the graph size bounded.

Insert measures N new insights into an empty store in one committed transaction;
fixture construction and cleanup are excluded. Retention includes its existing
effective-importance writes. None of these timings include CLI startup,
provider/network latency, embedding generation or a cold filesystem cache.

Host: Apple M4, Darwin/arm64, Go 1.25.4, `-cpu=1`. The same benchmark sources were
compiled against each production revision. Five independent runs per revision
used `-benchtime=100ms`, alternating before/after order between rounds. Tables
show medians. `cpu-ns/op` is process user+system CPU, including GC, measured with
`getrusage`; it excludes descheduling and blocked I/O. Ordinary `ns/op` remains
wall time. The insert benchmark excludes cleanup from both metrics. Other OSes
still run the benchmarks with wall-time/allocation reporting.

The machine was shared, not an isolated performance lab. CPU measurements,
unchanged control paths, allocation counts, query plans and operation counts
are used together; timing ratios are local observations, not latency promises.
`B/op` is cumulative Go allocation per operation, **not peak RSS** or database
file size. Three timing points alone cannot establish a Big-O bound.

#### N=1, 10, 100

CPU **µs/op**, before → after. Each cell is a median of five runs.

| Path | N=1 | N=10 | N=100 |
|---|---:|---:|---:|
| ByID | 10.2 → 10.5 | 10.2 → 10.1 | 10.2 → 10.2 |
| AllActive | 11.2 → 11.3 | 32.9 → 30.7 | 259.4 → 210.1 |
| Ranked | 22.7 → 21.1 | 44.5 → 40.5 | 52.9 → 40.1 |
| SourceLatest | 15.9 → 14.2 | 15.8 → 14.5 | 24.0 → 14.3 |
| SourceRecent | 23.2 → 22.6 | 23.6 → 22.9 | 48.6 → 41.6 |
| Entity | 16.7 → 16.2 | 25.4 → 23.1 | 100.9 → 26.1 |
| EntityMiss | 16.1 → 15.9 | 20.8 → 19.9 | 67.5 → 62.5 |
| RankedKeywordMiss | 22.9 → 21.6 | 25.6 → 24.4 | 59.2 → 55.3 |
| Embeddings | 6.0 → 6.4 | 14.3 → 14.2 | 87.7 → 88.1 |
| Keyword | 4.2 → 3.4 | 37.8 → 30.4 | 355.8 → 290.2 |
| Diff | 7.3 → 6.2 | 74.7 → 64.4 | 440.9 → 368.9 |
| RecallKeyword | 70.9 → 67.8 | 877.3 → 266.4 | 4,839.2 → 2,094.4 |
| RecallHybrid | 78.2 → 75.6 | 919.5 → 283.5 | 6,144.7 → 2,253.8 |
| RecallWhy | 43.7 → 42.0 | 1,121.4 → 344.5 | 7,561.7 → 2,357.0 |
| SemanticEmbedding | 11.5 → 13.1 | 53.5 → 53.0 | 70.6 → 63.2 |
| SemanticTokens | 11.5 → 13.1 | 76.9 → 51.0 | 676.6 → 407.8 |
| Neighborhood | 17.0 → 14.8 | 49.4 → 57.7 | 359.3 → 58.8 |
| Atomic insert batch | 74.8 → 75.0 | 266.4 → 171.7 | 2,246.0 → 1,203.7 |
| KnownEntities | 8.0 → 8.4 | 15.4 → 15.6 | 69.3 → 69.3 |
| Retention | 58.2 → 59.0 | 151.9 → 149.6 | 1,134.6 → 1,104.2 |
| EngineProvided | 97.0 → 85.8 | 388.3 → 300.7 | 1,004.4 → 637.8 |

#### Larger corpus and allocation results

CPU **ms/op**, before → after; allocation is **KiB/op** at N=10,000. Speedup is before CPU / after CPU, not a throughput SLA.

| Path | N=1,000 CPU | N=10,000 CPU | Speedup at 10,000 | Allocation at 10,000 |
|---|---:|---:|---:|---:|
| ByID | 0.010 → 0.010 | 0.011 → 0.010 | 1.05× | 2.3 → 2.3 |
| AllActive | 2.449 → 1.976 | 36.536 → 21.287 | 1.72× | 14,914.0 → 14,913.3 |
| Ranked | 0.123 → 0.039 | 2.294 → 0.040 | 57.33× | 15.9 → 15.9 |
| SourceLatest | 0.070 → 0.015 | 1.193 → 0.015 | 81.60× | 2.4 → 2.4 |
| SourceRecent | 0.098 → 0.042 | 1.169 → 0.042 | 27.85× | 15.8 → 15.8 |
| Entity | 0.948 → 0.023 | 18.365 → 0.024 | 779.19× | 1.5 → 1.5 |
| EntityMiss | 0.499 → 0.468 | 10.710 → 6.614 | 1.62× | 0.6 → 0.6 |
| RankedKeywordMiss | 0.375 → 0.372 | 9.417 → 9.796 | 0.96× | 1.3 → 1.3 |
| Embeddings | 0.800 → 0.805 | 14.801 → 14.857 | 1.00× | 16,160.0 → 16,160.0 |
| Keyword | 3.524 → 2.814 | 33.609 → 27.180 | 1.24× | 33,438.9 → 19,688.6 |
| Diff | 4.106 → 3.043 | 35.969 → 28.939 | 1.24× | 35,175.5 → 20,903.8 |
| RecallKeyword | 10.548 → 6.443 | 76.940 → 53.769 | 1.43× | 49,681.0 → 35,820.2 |
| RecallHybrid | 15.744 → 9.435 | 101.097 → 73.694 | 1.37× | 77,488.4 → 58,103.1 |
| RecallWhy | 17.782 → 10.200 | 108.947 → 75.466 | 1.44× | 77,854.1 → 58,215.7 |
| SemanticEmbedding | 0.221 → 0.144 | 1.892 → 0.962 | 1.97× | 966.1 → 12.4 |
| SemanticTokens | 6.516 → 3.933 | 75.362 → 41.223 | 1.83× | 42,593.3 → 22,179.5 |
| Neighborhood | 3.362 → 0.058 | 45.634 → 0.058 | 782.60× | 23,425.1 → 9.3 |
| Atomic insert batch | 42.133 → 35.761 | 550.995 → 458.416 | 1.20× | 12,970.7 → 11,017.7 |
| KnownEntities | 0.584 → 0.581 | 11.751 → 11.645 | 1.01× | 1.5 → 1.5 |
| Retention | 11.675 → 11.813 | 184.412 → 185.037 | 1.00× | 20,025.9 → 20,025.9 |
| EngineProvided | 3.369 → 0.814 | 56.044 → 2.029 | 27.62× | 290.9 → 51.4 |

#### Dense graph and unchanged controls

| Dense recall | CPU ms/op, before → after | CPU speedup | KiB/op, before → after |
|---|---:|---:|---:|
| N=1 | 0.059 → 0.058 | 1.02× | 11.2 → 9.8 |
| N=10 | 3.515 → 0.506 | 6.94× | 1,447.7 → 236.9 |
| N=100 | 126.394 → 19.429 | 6.51× | 66,880.2 → 10,048.0 |

At N=10,000, unchanged ID lookup and embedding loading are approximately
1.05× and 1.00×, and retention remains approximately 1.00×. These controls
help distinguish the larger algorithmic gains from timing noise. Negative LIKE
lookup remains linear and used about 4% more CPU in this run. At N=10, bounded
BFS uses 57.7 µs versus 49.4 µs for the bulk baseline: extra indexed calls cost
more on tiny graphs. Single-record semantic lookup also has roughly 1.6 µs of
extra fixed overhead. These regressions are included, not removed from the data.

The larger indexes initially increased batch-write CPU cost. Transaction-local
statement reuse brought the final 10,000-row batch from 551.0 ms to 458.4 ms
(about 17% less CPU) with the same commit boundary. Hybrid recall reduced
per-operation allocation from about 75.7 MiB to 56.7 MiB; dense recall at N=100
dropped from about 65.3 MiB to 9.8 MiB. Bounded neighborhood allocation at
N=10,000 is 9,504 bytes rather than 23,987,320 bytes.

The selector's deterministic comparison-count test forces an eviction for every
new item once K=5 is full. Counts at N=1/10/100/1,000/10,000 are
**0 / 37 / 440 / 4,491 / 44,991**, including final sorting of K items. This supports
the code-derived `O(N log K)` bound without a flaky wall-clock CI assertion.

The complete five-run output for every path, including controls and misses, is
saved as [before](benchmarks/2026-10-03-memory-before.txt) and
[after](benchmarks/2026-10-03-memory-after.txt). No exploratory or superseded
implementation measurements are mixed into these files.

### Correctness and verification

- `go build -o memory .` — passed.
- `make test` — passed, including vet, deterministic tests and architecture
  checks. The pure Memory-local top-K leaf is included in the dependency graph
  and regular deterministic test list.
- `go test -race ./internal/memory/... ./cmd/memory -count=1` — passed.
- `MEMORY_EMBED_ENDPOINT=http://127.0.0.1:1 bash scripts/e2e_test.sh` — all
  **258 assertions passed**, with embedding providers unavailable by design.
- Cached traversal is checked against the previous independent implementation
  for all four intents, multiple budgets, multiple anchors, cyclic graphs,
  deleted nodes and mismatched vector dimensions. Scores and via labels agree.
- Indexed bounded BFS is compared with bulk traversal, including order, hop,
  edge identity, limits, filters, self-loops and missing/deleted endpoints.
- Tests cover top-K/full-sort parity, comparison bounds, token-field boundaries,
  request refresh after vector/edge/deletion changes, mixed timestamp offsets,
  index upgrades/reopens, endpoint query plans, transaction commit/rollback,
  statement reuse, edge replacement and panic cleanup.

The Agency/Docker/provider integration umbrella and paid live suite were not
run; these changes do not modify those boundaries. The Memory CLI boundary was
exercised directly. The changes do not add goroutines or change transaction
commit policy, supersession authority or exact-duplicate write suppression.

### Reproduce

On the PR revision:

```sh
go test ./internal/memory -run '^$' -bench BenchmarkMemory \
  -benchmem -benchtime=100ms -count=5 -cpu=1
go test ./internal/memory/internal/topk -run TestComparisonBudget -v
go test ./internal/memory/store -run 'Test(ActiveReadPlans|TraversalReadPlans)' -v
```

For a matched baseline, extract `4d312c4e` into a temporary directory and copy
only `internal/memory/performance*test.go` from this PR into its
`internal/memory/` directory. Compile both with
`go test -c -o <separate-output-binary> ./internal/memory`, then run the binaries
with `-test.run='^$' -test.bench=BenchmarkMemory -test.benchmem
-test.benchtime=100ms -test.count=1 -test.cpu=1` in alternating order for five
rounds. Compare medians of the identically named cases. Do not run other test
suites concurrently with the measurements.

### Remaining limits

Exact keyword/vector matching, JSON entity misses and retention still have
whole-corpus work. Persistent token/entity indexes would need their own
migration, update/delete/rollback and tokenization parity contracts. An ANN
index would additionally need a recall-quality budget, which this task did not
permit sacrificing. Larger composite keys trade some insertion work/storage
for ordered reads; transaction-local statement reuse offsets compilation work,
but does not make durable writes constant-time. Large or unbounded graph walks
still depend on actual graph density. These costs are kept explicit rather than
hidden behind a cache that can become stale between commands.
