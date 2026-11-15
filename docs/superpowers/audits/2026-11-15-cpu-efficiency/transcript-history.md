# Transcript and durable-history CPU audit

## Verdict

The largest repeated cost is context-pressure estimation: without a usable API token measurement, each model request rebuilds an estimator message slice from all history. Turn-addressed past-thread reads and derived totals also walk growing index tables on each request, while resume always scans and decodes the full transcript even when history resumes after a checkpoint. The cleanest paths are item-based paging and cached journal folds: they seek or extend indexed state rather than re-reading transcript and child-journal bodies on every query.

## Findings, ranked

Ranked by expected CPU saved × frequency divided by fix risk. The rankings are estimates; the listed benchmarks measure only the named workloads and do not establish production request frequency.

### 1. **High: Context-pressure fallback reconstructs every message on every model request**

**Location and call path:** `agent/session_model_call.go:480` → `contextmgr` strategy `ManageContext` → `agent/internal/contextmgr/compact.go:114-152` (`MaybeCompact`, `compactWithCheckpoint`) → `pressureWithProfile` → `agent/internal/contextmgr/pressure.go:276-282,351-361` (`estimateUsedTokensFor`, `estimateTokensFor`).

**Mechanism:** `MaybeCompact` runs before each LLM request. When `lastTokens` is absent or its measurement is unusable, `estimateUsedTokensFor` passes the entire history to `estimateTokensFor`; that function allocates a new `[]llm.Message`, walks each turn, and applies `TrimThinkingForReplayMessage` before estimating. After a model response that does not supply a usable token count, the next request repeats this work over the enlarged history. With a valid token baseline it estimates only turns added since that baseline, which is the efficient path. A compaction pass may also estimate before and after a layer and recalculate pressure between layers.

**Evidence, read:** The branch and work are explicit at `pressure.go:276-282,351-361`; `compact.go:114-117` documents the per-request call frequency, and `session_model_call.go:480` is the runtime caller. No benchmark isolated fallback estimation.

**Scale:** One full-history estimator pass per model request when exact usage is unavailable: O(turns + message content bytes), with a `[]llm.Message` allocation proportional to the turn count. Otherwise the estimate is O(new turns since the last measurement). This can compound over many rounds if the provider omits input-token usage.

**Fix design:** Keep a profile-keyed incremental estimate alongside the history, or cache the per-turn `llm.TrimThinkingForReplayMessage`/token contribution and add only newly appended turns. Reset or recompute on profile-target changes, history rewrites, and checkpoint/summary folds; `Manager.SetProfile` already clears the exact-token measurement. Preserve the estimator's protocol and thinking-replay rules. Tests must cover profile switches, history replacement, appended turns, and parity with `estimateTokensFor`; a stale cache could make compaction miss a context limit.

**How to verify the win:** Add a benchmark around `MaybeCompact`/`estimateUsedTokensFor` with no token baseline and histories of increasing length, then run `go test ./agent/internal/contextmgr -run '^$' -bench 'Pressure|Estimate' -benchmem`. Compare allocations and ns/op per request at the same history sizes; verify a valid-baseline case still scales with only appended turns.

### 2. **High: Turn-addressed past-thread requests scan all index records**

**Location and call path:** past AppWire turn-window request → `cmd/evener-hub/app_threadread.go:619-643` (`pastEntryTurnWindowItems`) → `Index.ResolveTurnWindow` → `internal/transcriptindex/turn_window.go:68-100,122-151,163-194,196-241`.

**Mechanism:** Each request builds the full ordered turn list by reading every turn-summary record and fetching each turn ID (`indexedTurnOrder`). It then selects the requested range and scans every item record in `candidatesForTurnSlots`, retaining only records whose turn slot is selected. The read avoids parsing transcript JSON, but it still walks the entire compact sidecar for a small `latest:N`, `exact`, `since`, or `before` selection.

**Evidence, read:** `indexedTurnOrder` iterates from slot zero through `x.turns.n`; `selectTurnRange` searches backward for named boundaries; `candidatesForTurnSlots` iterates from item slot zero through `x.items.n`. The AppWire entrypoint calls this path for each turn-window request.

**Scale:** O(all turns + all indexed items) per turn-window request, plus projection of the selected entries. This grows linearly with transcript history, even when the requested window is constant-sized. It scans sidecar records, not raw transcript bytes.

**Fix design:** Add an append-maintained secondary lookup from turn ID to turn slot and item postings/ranges, then resolve the turn selector and read only the selected turns' item records. A single contiguous item range is not always sufficient: the code documents that another turn can be recorded inside a turn, so preserve chronological order across multiple runs. Keep the existing scan as a validation/rebuild path until parity is established. Tests must cover interleaved turns, turns with no items, prelude items, append/rebuild, and stale snapshots; postings add disk/memory and sidecar-format complexity.

**How to verify the win:** Add `BenchmarkResolveTurnWindow` with fixed `latest:10` and exact selectors against increasing turn/item counts. Run `go test ./internal/transcriptindex -run '^$' -bench 'ResolveTurnWindow' -benchmem`; report bytes/records read as well as allocations and time. The fixed-page cost should stop growing with total item count.

### 3. **High: Derived usage totals reread every turn summary for each past-thread read**

**Location and call path:** past item/thread read with totals → `cmd/evener-hub/app_threadread.go:600-612` or `619-643` → `pastIndexDerivedTotals` at `app_threadread.go:650-655` → `Index.DerivedTotals` at `internal/transcriptindex/window.go:113-163`.

**Mechanism:** When totals are included, the read calls `DerivedTotals` even though the response page is bounded. That method reads and decodes turn records in 256-record chunks from the beginning through `x.turns.n`, accumulating usage and failed-tool counts. It does not rescan the transcript, but repeated thread reads redo the full summary aggregation.

**Evidence, read:** `app_threadread.go:610-612,641-655` invokes totals on the request path. `window.go:122-163` loops over every turn record and sums the selected span.

**Scale:** O(turns) compact records per request that asks for derived totals; memory is bounded to a 256-record read buffer plus small accumulators. Repeated polling of the same long session repeats the scan.

**Fix design:** Persist a cumulative usage/failure prefix for each turn boundary, or equivalent block totals, as part of the transcript index. Answer ordinary boundaries by subtracting prefix values and inspect only the boundary turn. Retain the existing fallback when `fromEntryOrdinal` cuts through a turn or an unreadable/inexact record prevents authoritative totals. Tests must compare every boundary and fork divergence against `DerivedTotals`' current behavior, including unreadable and `FailedInexact` turns. This increases sidecar writes and format-version churn but keeps total reads bounded by blocks/boundaries.

**How to verify the win:** Add a `DerivedTotals` benchmark across increasing indexed turn counts and multiple `fromEntryOrdinal` boundaries; run `go test ./internal/transcriptindex -run '^$' -bench 'DerivedTotals' -benchmem`. Compare records read, allocations, and ns/op for the same requested suffix.

### 4. **High: Resume reparses and retains the full transcript before trimming to the last checkpoint**

**Location and call path:** `session_init` resume → `agent/session_init.go:1051-1074` calls `transcript.OpenWriterForSession` → `agent/transcript/transcript.go:1121-1145` calls `scanForResume` → `transcript.go:1151-1197` decodes each complete entry and appends it to `[]Entry` → restore projection at `session_init.go:1103-1128,1152-1168` and `agent/transcript_read.go:148-159` retains only the last checkpoint/summary and following history for model context.

**Mechanism:** A normal resume scans the file once from byte zero, validates and JSON-decodes each complete entry, and retains every decoded `Entry`. The resumed model history is then projected from the checkpoint anchor, but other restore work also needs older typed records for compaction receipts, sequence reservation, attention, and client mutations. There is no incremental parse from a prior resume offset. Restore-time recovery that appends entries can trigger another full `readTranscriptFull` at `session_init.go:1481-1496`.

**Evidence, read:** `scanForResume` loops over every complete line and calls `DecodeEntry` before appending at `transcript.go:1165-1197`. `resumeHistoryIndexedWithSuperseded` starts from `entries[retainedFrom(entries):]` at `transcript_read.go:148-159`. `session_init.go:1103-1111` explains why lifecycle reconciliation deliberately reads pre-checkpoint entries too.

**Scale:** At least one O(file bytes) framing/JSON parse and O(entry count) retained decoded objects per ordinary resume; conditional recovery rereads the full file after appending. The resumed model-context slice may be small, but parse and temporary retained-memory costs still scale with all historical records. Index catch-up does not remove this writer-resume scan.

**Fix design:** Introduce a durable, append-updated restore summary for fields required from pre-checkpoint history (sequence/ordinal, typed lifecycle receipts, mutation identities, attention fold state), and a validated checkpoint boundary for history projection. Then resume can reconstruct old summaries from that snapshot and decode only the suffix. Preserve fail-closed validation: trusting a snapshot without proving its transcript identity, coverage, and complete-line boundary could hide corruption or skip receipts. Tests must cover crash between transcript append and summary commit, damaged sidecar, corrupted pre-checkpoint line, forks, and resumed history parity. Whether to keep full semantic validation of discarded pre-checkpoint JSON is an open behavior/risk decision.

**How to verify the win:** Extend `BenchmarkNewSession`/resume fixtures with long transcripts and a checkpoint near the end, plus counters for bytes passed to `DecodeEntry` and retained entries. Run `go test ./agent -run '^$' -bench 'ResumeCoordinates|NewSession' -benchmem`; the important before/after measures are decoded bytes and B/op on the resumed fixture, not total `NewSession` time alone.

### 5. **Medium: A cold or invalidated transcript-index rebuild has large CPU and allocation cost**

**Location and call path:** any index user opening a missing/stale sidecar → `transcriptindex.Cache.Acquire` → `Open`/`CatchUp` → `Index.extend` at `internal/transcriptindex/index.go:711-718` → full `rebuild`/`buildNew` → scan and projection in `internal/transcriptindex/build.go`.

**Mechanism:** A missing build, a stale build, or a transcript that is no longer the same append-grown file triggers a full reindex. This is not paid on each read: a healthy existing sidecar adopts its records and ordinary appends extend only new lines. But a rebuild parses and projects every complete transcript entry and creates index records; the existing benchmark shows allocations far above input size.

**Evidence, executed:** `GOCACHE="$EVENER_SCRATCH_DIR/go-build" go test ./internal/transcriptindex -run '^$' -bench . -benchmem -benchtime=1x` passed. `BenchmarkRebuild/50MB`: 50,013,460 bytes, 22,303 entries; 276,307,416 ns/op, 172.62 MB/s, 391,442,544 B/op, 914,150 allocs/op. `BenchmarkRebuild/200MB`: 200,137,290 bytes, 89,210 entries; 899,787,666 ns/op, 212.12 MB/s, 1,548,690,104 B/op, 3,659,422 allocs/op. These are synthetic full rebuilds, not production cold-start measurements.

**Scale:** O(transcript bytes and entry count) on a cold rebuild or invalidation, not on the steady-state read/append path. The measured 200 MB case allocated about 1.55 GB once. Frequency depends on sidecar loss, corruption, format/projection changes, or transcript rewrites; the cache's existing fast path makes this a lower-frequency concern than per-request scans.

**Fix design:** Profile the full-build path and reduce avoidable temporary objects in `builder.apply`/projection and record encoding; preserve the authoritative full rebuild and its corruption checks. Avoid replacing JSON semantics with a partial parser unless parity with the schema-aware decoder is proven. Changes could affect malformed-line quarantine, tool-result projection, and sidecar consistency. Keep incremental catch-up behavior unchanged.

**How to verify the win:** Run `go test ./internal/transcriptindex -run '^$' -bench 'BenchmarkRebuild' -benchmem -benchtime=1x` on the same Go version and host; compare `B/op`, `allocs/op`, MB/s, and entries/s at both sizes. Also run the package's rebuild/index parity tests after any implementation.

## Clean

- **Healthy index reads do not reparse the entire transcript.** `Index.extend` returns immediately when the requested length is already covered (`internal/transcriptindex/index.go:723-724`); append growth validates the covered identity/tail and scans only the new suffix (`index.go:912-929,780`). Executed `BenchmarkCatchUpSmallAppends`: 64 bytes, 220,167 ns/17,944 B/90 allocs; 4,096 bytes, 216,917 ns/49,528 B/93 allocs; 131,072 bytes, 456,000 ns/1,087,240 B/87 allocs. These are one-shot timings and include test setup outside the timed loop as documented by the benchmark.
- **Item-position paging uses a binary search in the item sidecar.** `Index.Before`/`CandidateAt` call `rank` and `findItem`; `findItem` calls `x.items.search` (`internal/transcriptindex/window.go:232-284,328-368`). After locating the range, `window` projects only its selected span. Executed `BenchmarkReadAtHistorical5000`: cold 888,417 ns/6,214,120 B/1,215 allocs; cached 168,791 ns/381,904 B/1,002 allocs. This does not apply to turn-addressed `ResolveTurnWindow`, finding 2.
- **The fold cache incrementally extends journal folds and coalesces same-path work.** `agent/internal/foldcache/foldcache.go:181-210,252-299` describes and implements bounded cached values, staleness checks, and singleflight extension. Historical delegate/job readers use that cache; the fold is not recomputed from every child transcript on each parent read. Executed delegate-store benchmark: `BenchmarkFoldWideTree/width=100` 103,333 ns/166,952 B/411 allocs; width 2,000 1,437,334 ns/3,434,192 B/8,049 allocs. This measures `Fold(events)` itself, not the cached parent-read path.

## Open questions

- Should resume keep strict semantic validation of every pre-checkpoint record, even when a durable restore summary could provide the state needed from old entries? Skipping old JSON decoding saves CPU but can change corruption detection unless the summary also proves full coverage and integrity.
- Is a turn-ID/posting sidecar worth its storage and format-version cost to make turn-addressed reads proportional to the selected window? The existing item-position API already has indexed seek behavior.
- Is additional persistent prefix metadata for usage/failure totals worth the sidecar write and migration cost for repeated past-thread reads with totals?
- What share of model routes return usable input-token counts? That production frequency determines the priority of caching the context estimator; the audit did not sample live sessions.
