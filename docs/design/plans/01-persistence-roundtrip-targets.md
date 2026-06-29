# 8.1 — Persistence round-trip + replay-idempotence fuzz targets — implementation plan

**Status:** plan. **Date:** 2026-06-28. **Branch:** `wip/fuzzing-toolkit`.
**Charter:** design §8.1. **Pattern to match:** `llm/sse_fuzz_test.go`, `appwire/jsonrpc_fuzz_test.go`.
**Builds on:** design §0–§3 (conventions, `make fuzz` wiring), research §3/§5 (oracle taxonomy).

All targets are single-input `testing.F` (a single `[]byte`) — Go-native auto-promotion turns any
crasher into a permanent `testdata/fuzz/` regression. **No promoter needed** (the charter's
expectation holds). Target 4 carries **two** fuzz functions in one file (isolated carry-through +
full live-vs-reload metamorphic); the other three files carry one each.

> **Dependency — read first.** 8.1 **depends on 8.4 (corpus harvesting) and is scheduled to run
> _after_ it.** Every target's seed corpus is harvested by 8.4 from real on-disk artifacts
> (`*.meta.json`, transcript JSONL, **`jobs.jsonl`**). The inline `f.Add` lines below are the
> minimal bootstrap; the load-bearing corpus arrives from 8.4. Do not start 8.1 until 8.4 has
> produced its seeds. See §4 "Dependencies".

**Decisions folded in (resolved 2026-06-28).** Target 4 builds **both** oracles; Target 2 applies
the persist→reload idempotence treatment to **all six** fold variants and also asserts
one-at-a-time `Append` == `AppendBatch`; Target 3 adds an `APICall` round-trip fixed point; seeds
come from 8.4. The LoC budget is **advisory** — keep every target. Full text in §5.

---

## 0. Why this surface first

serf's historical bugs cluster at **reload / replay**, not at write. The dual-projection split is
the recurring fault line: a turn is projected one way live (the agent's own projector) and another
way on reload (transcript JSONL → hub replay types → `apptranscript.ProjectTurn`). When the reload
type can't carry a content kind, that kind silently vanishes on refresh. Real instances on this
branch's history: thinking traces dropped on reload (`0a6b65b0`), then web_search / audio / document
added to the same carry-through (`ec96619c`). **Look at Target 4 (hub replay carry-through) first** —
it directly fuzzes that exact path — then Target 2 (jobstore persist→reload), the next-richest
reducer surface.

---

## 1. Verified persistence seams

Every function, type, and path below was read in the worktree. file:line is the declaration site.

### Seam A — session meta (`agent/schema`, module `agent`)
On-disk: compact JSON, atomic temp+rename, `<dir>/sessions/<id>.meta.json`.

| Role | Symbol | Site |
|---|---|---|
| type | `SessionMeta` (embeds `ConfigSnapshot`, `EnvironmentInfo`, `*GoalSnapshot`) | `agent/schema/snapshot.go:15` |
| type | `GoalSnapshot` | `agent/schema/snapshot.go:74` |
| type | `ConfigSnapshot` (33 wire fields, mirrors engine `SessionConfig`) | `agent/schema/config_snapshot.go:11` |
| **decode (custom)** | `(*SessionMeta).UnmarshalJSON` — legacy `original_task` → `OriginalPrompt` fallback | `agent/schema/snapshot.go:88` |
| **encode+write** | `SaveSessionMeta(dir, meta)` | `agent/schema/snapshot.go:121` |
| **read+decode** | `LoadSessionMeta(dir, id)` | `agent/schema/snapshot.go:146` |
| scan | `ListSessionMetas(dir)` (skips corrupt files silently) | `agent/schema/snapshot.go:161` |

Written-then-read: the full `SessionMeta`. **Asymmetry to exercise:** the custom `UnmarshalJSON`
reads both `original_prompt` and the legacy `original_task`; `MarshalJSON` (default) only writes
`original_prompt`. So the *canonical* form is a fixed point, but a legacy-only input is not — the
oracle must use canonical (post-one-decode) bytes as the fixed-point baseline.
**Embedded timestamps:** `CreatedAt`, `UpdatedAt`, `NameUpdatedAt` (`omitzero`), `Goal.CreatedAt/UpdatedAt`.

### Seam B — transcript JSONL (`agent/transcript` write side, `agent` read/replay side)
On-disk: JSONL, line 1 = `Header`, then `entry` / `api_call` lines. Append-only; resume truncates a
trailing partial line.

| Role | Symbol | Site |
|---|---|---|
| types | `Header`, `Entry` (`Kind/Seq/Turn`), `APICall` | `agent/transcript/transcript.go:28,54,61` |
| type | `schema.Turn` (the replayed unit; embeds `llm.Message`) | `agent/schema/turn.go:33` |
| **write** | `transcript.NewWriter(path, header)` (writes header line) | `agent/transcript/transcript.go:106` |
| append | `Writer.Append(turn)` / `AppendDurable` / `AppendAPICall` | `agent/transcript/transcript.go:140,146,254` |
| **resume-open** | `OpenWriter(path)` (reads, truncates partial last line, recomputes next `seq`) | `agent/transcript/transcript.go:325` |
| **read+decode** | `readTranscript(path)` / `readTranscriptFull(path)` | `agent/transcript_read.go:21,86` |
| strict read | `readStrictChildTranscript(...)` | `agent/transcript_read.go:151` |
| **replay→state** | `ResumeHistory(entries) []schema.Turn` (last-compaction suffix + orphan repair) | `agent/transcript_read.go:276` |
| repair | `repairOrphanedToolResults(history)` | `agent/history_repair.go:13` |

Written-then-read: `Header` + each `Entry.Turn`. `ResumeHistory` is the in-memory state a resumed
session is rebuilt from — the replay-idempotence target.
**Embedded timestamps:** `Header.CreatedAt`, `Turn.Timestamp`, `APICall.Timestamp` (string `ts`).
Readers are unexported in package `agent`, so the read+replay target must live in package `agent`.

### Seam C — jobstore event log (`agent/internal/jobstore`, module `agent`)
On-disk: JSONL, append-only, monotonic `Seq` assigned at append, fsync per append. The folds are
pure reducers — the strongest replay-idempotence surface in serf.

| Role | Symbol | Site |
|---|---|---|
| types | `Event` (flat union), `EventKind` | `agent/internal/jobstore/event.go:33,12` |
| types | `JobRecord`, `DelegateRecord`, `WatchRecord`, `WatchSendState` | `agent/internal/jobstore/record.go:180,95,113,156` |
| **open+recover** | `Store.Open(path)` (recovers next seq) | `agent/internal/jobstore/store.go:31` |
| **append** | `Store.Append(e)` / `AppendBatch(events)` (assign seq, marshal, fsync) | `agent/internal/jobstore/store.go:51,81` |
| **read** | `Store.LoadEvents()` → `readAllLocked` (+ trailing-partial recovery) | `agent/internal/jobstore/store.go:203,222,259` |
| **fold→state** | `Store.Load`/`LoadOrdered`/`LoadDelegates`/`LoadWatches`/`LoadWatchSends`/`LoadGrants` | `agent/internal/jobstore/store.go:114,132,146,160,174,189` |
| reducers | `Fold` / `FoldOrdered` / `FoldDelegates` / `FoldWatches` / `FoldWatchSends` / `FoldGrants` | `agent/internal/jobstore/fold.go:12,35,74,193,236,274` |
| apply | `applyEvent(r, e)` (first-terminal-write-wins) | `agent/internal/jobstore/fold.go:290` |

Written-then-read: each `Event`; the folds reconstruct `JobRecord`/`DelegateRecord`/etc.
**Embedded timestamps:** `Event.TS`, `StartedAt`, `EndedAt`, `WatchSendState.CreatedAt/UpdatedAt`.
**Seq:** caller-supplied seq is ignored on `Append`/`AppendBatch` — the store reassigns `1..N` in
append order (load-bearing for the round-trip oracle, see §3-C). `any` (`StructuredResult`) and
`*provenance.Causal` pointers are present — JSON-compare, not `reflect.DeepEqual`.

### Seam D — hub replay projection (`cmd/serf-hub` package `main`, module `.`)
The reload path. A transcript `Entry` (`schema.Turn`) on disk is re-decoded into the hub's own
`ReplayEntry` shape, converted back to a `schema.Turn`, then projected for the web UI.

| Role | Symbol | Site |
|---|---|---|
| replay types | `ReplayEntry` / `ReplayTurn` / `ReplayPart` (per-kind nullable carriers) | `cmd/serf-hub/internal/hubcore/types.go:17,21,31` |
| **reload convert** | `replayTurnToAgentTurn(turn)` (`ReplayTurn` → `schema.Turn`) | `cmd/serf-hub/app_threadread.go:217` |
| reload project | `appItemsFromReplayTurn(...)` | `cmd/serf-hub/app_threadread.go:200` |
| **shared projector** | `apptranscript.ProjectTurn(turnID, idx, turn, toolNames, imageProjector)` | `internal/apptranscript/apptranscript.go:190` |
| file driver | `apptranscript.TurnsFromFile(path, max, project)` | `internal/apptranscript/apptranscript.go:476` |

Written-then-read: a `schema.Turn` survives `→ Entry JSON → ReplayEntry → replayTurnToAgentTurn →
schema.Turn`. **The defect class:** `ReplayPart` (`types.go:31`) must carry every content kind
`replayTurnToAgentTurn` (`app_threadread.go:217`) must re-emit it; any gap drops content on reload.

### Not in scope (verified absent)
The charter names "`agent/schema/snapshot.go`" as a seam; that file holds only `SessionMeta` —
**there is no whole-session snapshot Save/Load** (history is always recovered from the transcript,
per the `SessionMeta` doc comment). Confirmed: no `BuildSnapshot`/session-`Restore` serializer in
`agent/*.go`. So "session snapshot" = Seam A (meta) + Seam B (transcript), which is what we plan.

---

## 2. The targets

Four files, each next to the code it tests, in that code's module. All inputs are `[]byte`
(`testing.F` single-input → free auto-promotion). Real on-disk lines (harvested by 8.4) seed `f.Add`.

| # | File | Package | Drives | LoC |
|---|---|---|---|---|
| 1 | `agent/schema/snapshot_fuzz_test.go` | `schema` | `SessionMeta` decode + `Save`/`Load` | 80–120 |
| 2 | `agent/internal/jobstore/fold_fuzz_test.go` | `jobstore` | event-log decode + **all six** folds + persist→reload (all folds) + `Append`==`AppendBatch` | 150–200 |
| 3 | `agent/transcript_roundtrip_fuzz_test.go` | `agent` | transcript write/read + `ResumeHistory` + **`APICall` fixed point** | 120–160 |
| 4 | `cmd/serf-hub/replay_fuzz_test.go` | `main` | hub replay carry-through (isolated) **+ full live-vs-reload metamorphic** | 160–220 |

**LoC budget is advisory.** Keep every target and every oracle below; do not trim to a number
(decision 5). The total lands above the ~300–500 charter sketch because the decisions added oracles
(all-fold persist→reload, `Append`==`AppendBatch`, `APICall` fixed point, the second Target-4
metamorphic). That is intended.

### Target 1 — `FuzzSessionMetaRoundTrip` (`agent/schema/snapshot_fuzz_test.go`)
`f.Fuzz(func(t, raw []byte))`:
1. `var m1 SessionMeta; if json.Unmarshal(raw, &m1) != nil { return }` (rejected input: stop).
2. **Fixed point.** `b1, _ := json.Marshal(m1); var m2 …Unmarshal(b1); b2, _ := json.Marshal(m2)` —
   assert `bytes.Equal(b1, b2)`. `b1` (canonical, post-one-decode) is the baseline, which sidesteps
   the `original_task` legacy asymmetry (§1-A).
3. **Persist round-trip.** `dir := t.TempDir(); m1.ID = "fuzz"` (force a safe, fixed id);
   `SaveSessionMeta(dir, m1)`; `m3, _ := LoadSessionMeta(dir, "fuzz")`; assert
   `bytes.Equal(b1, mustMarshal(m3))`.

**Oracles beyond no-panic:** the fixed point proves `MarshalJSON`/`UnmarshalJSON` agree on the full
nested graph (`ConfigSnapshot` + `GoalSnapshot` + maps/slices); the persist round-trip proves the
atomic-write + read path preserves every field (a dropped or mis-tagged field shows up immediately).

**Seeds (from 8.4):** a real `*.meta.json` (compact); a meta with `goal`, `pinned_note`,
`observed_by`, `tool_output_limits`; a legacy `{"original_task":"…"}` doc (pins the legacy path);
`{}`, `null`, `not json`. 8.4 harvests the real `*.meta.json` variants; the malformed inputs are
inline bootstrap.

### Target 2 — `FuzzJobEventLogReplay` (`agent/internal/jobstore/fold_fuzz_test.go`)
Input is a raw JSONL blob (one `Event` per line). `f.Fuzz(func(t, raw []byte))`:
1. Split on `'\n'`; for each non-empty line `json.Unmarshal` into `Event`; skip lines that error
   (mirrors a tolerant reader). Collect `events`.
2. **No panic across every reducer:** `Fold`, `FoldOrdered`, `FoldDelegates`, `FoldWatches`,
   `FoldWatchSends`, `FoldGrants` over `events`.
3. **Fold determinism:** `jsonEq(Fold(events), Fold(events))` (catches map/ordering nondeterminism
   in the reduced output; the folds sort `SliceStable` by `Seq`, so a stable result is the contract).
4. **Persist → reload replay-idempotence for _every_ fold variant (the load-bearing oracle).**
   Decision 2: do **not** stop at `Fold`. Run the persist→reload comparison for all six reducers and
   their matching loaders.
   - `sorted := bySeqAsc(events)` then strip `Seq` (store reassigns it).
   - `s, _ := Open(filepath.Join(t.TempDir(), "jobs.jsonl")); s.AppendBatch(sorted); s.Close()`.
   - `s2, _ := Open(samePath); s2.Close()` after loading each projection.
   - For each `(reducer, loader)` pair, assert `jsonEq(reducer(sorted), loader())`:
     - `Fold` ↔ `Store.Load`
     - `FoldOrdered` ↔ `Store.LoadOrdered`
     - `FoldDelegates` ↔ `Store.LoadDelegates`
     - `FoldWatches` ↔ `Store.LoadWatches`
     - `FoldWatchSends` ↔ `Store.LoadWatchSends`
     - `FoldGrants` ↔ `Store.LoadGrants`
   (`Fold` declarations at `fold.go:12,35,74,193,236,274`; loaders at `store.go:114,132,146,160,174,189`.)
5. **`Append`-one-at-a-time == `AppendBatch` (decision 2).** A second store over a fresh
   `t.TempDir()`: append `sorted` one event per `Store.Append` call, reload, and assert its `Load`
   equals the `AppendBatch` store's `Load` (`jsonEq`). Guards the batch-vs-single seq-assignment and
   write paths against drift (`store.go:51` vs `store.go:81`).

**Why this is more than no-panic:** step 4 round-trips every event through `marshal → fsync'd file →
readAll → Fold`. A field that `Append` writes but `readAllLocked` or `applyEvent` drops, a
`Seq`-ordering regression, or a marshal/unmarshal asymmetry on `StructuredResult any` /
`*provenance.Causal` all diverge here — and now diverge on whichever of the six projections carries
that field, not just job records. This is the in-memory job state a daemon restart reconstructs —
exactly serf's reload-bug class, at its purest reducer.

**Determinism notes:** compare with `json.Marshal` of the folded maps, not `reflect.DeepEqual` —
JSON normalizes `time.Time` (drops the monotonic reading) and pointer identity. Sort input by `Seq`
*before* stripping so append order == the order `Fold` would have imposed (otherwise the reassigned
`1..N` reorders relative to the original seqs and the comparison is a false positive — see Risks).

**Seeds (from 8.4):** one real line per `EventKind` (`job_started`, `job_session_assigned`,
`job_finished`, `watch_registered`, `watch_send_pending`, `delegate_created`, `watch_read_grant`, …)
drawn from a real `jobs.jsonl` **harvested by 8.4** (8.4 is the producer of jobs.jsonl seeds — this
is the most 8.4-dependent target); an out-of-order-seq pair; a duplicate `job_finished`
(first-terminal-wins); empty + garbage lines (inline bootstrap).

### Target 3 — `FuzzTranscriptReplay` (`agent/transcript_roundtrip_fuzz_test.go`, package `agent`)
Package `agent` so it can drive both the exported writer (`agent/transcript`) and the unexported
readers + `ResumeHistory`. Input is a transcript JSONL blob. `f.Fuzz(func(t, raw []byte))`:
1. Write `raw` to `filepath.Join(t.TempDir(), "in.jsonl")`; `data, err := readTranscriptFull(path)`;
   `if err != nil { return }` (no header / unreadable: stop after proving no panic).
2. **Write→read round-trip.** `w, _ := transcript.NewWriter(out, data.Header)`; for each
   `e := range data.Entries { w.Append(e.Turn) }`; `w.Close()`; `_, got, _, _ := readTranscript(out)`.
   Assert the re-read turns equal the originals: `jsonEq(turnsOf(data.Entries), turnsOf(got))`.
   (Proves a `schema.Turn` survives the real append+read path with no content loss.)
3. **`ResumeHistory` idempotence.** `h1 := ResumeHistory(data.Entries)`; wrap `h1` back into
   `[]transcript.Entry` and `h2 := ResumeHistory(thoseEntries)`; assert `jsonEq(h1, h2)`. The
   last-compaction-suffix + orphan-repair result must be a fixed point — re-resuming a resumed
   history must not keep mutating it.
4. **`APICall` round-trip fixed point (decision 3).** `readTranscriptFull` already returns
   `data.APICalls []transcript.APICall` (`transcript_read.go:86`, dispatched by the `api_call`
   kind). Write each through the real append path — `w2, _ := transcript.NewWriter(out2, header);
   for _, c := range data.APICalls { w2.AppendAPICall(c) }; w2.Close()` (`transcript.go:254`) — then
   `data2, _ := readTranscriptFull(out2)` and assert `jsonEq` on the re-read `APICalls`. **Strip
   `Seq` before comparing:** `AppendAPICall` reassigns `Kind`/`Seq` from the writer counter
   (`transcript.go:254`), so the re-read seqs are `1..N` in write order, not the originals — same
   strip-then-compare discipline as Targets 2/3. This raises `APICall` from no-panic decode to a
   persistence fidelity oracle (catches a dropped/mis-tagged field on the large
   `llm.APILogRequest/Response` payloads).

**Beyond no-panic:** step 2 is transcript persistence fidelity (the daemon writes turns, the hub /
resume reads them back); step 3 is replay-into-state idempotence for the exact function a resumed
session rebuilds its working history from; step 4 is the same persistence fidelity for `api_call`
lines. A compaction-scan or orphan-repair regression breaks step 3's fixed point.

**Seeds (from 8.4):** a real transcript (header + user + assistant-with-tool_call + tool_results +
**at least one `api_call` line** so step 4 is reached); one with a `CHECKPOINT`/`SUMMARY` turn
(exercises the compaction branch); one with an orphaned tool result (exercises
`repairOrphanedToolResults`); header-only; garbage. 8.4 harvests the real transcripts; the malformed
inputs are inline bootstrap.

### Target 4 — `cmd/serf-hub/replay_fuzz_test.go`, package `main` — **two fuzz functions**
The reload carry-through surface — **the first place to look.** Decision 1: build **both** the
isolated carry-through oracle **and** the full live-vs-reload metamorphic. Both take the JSON of one
transcript `Entry` as the `[]byte` input; they share a seed corpus and the reload-roundtrip helper.

#### 4a — `FuzzHubReplayCarryThrough` (isolated: reload projection vs `ProjectTurn(original)`)
`f.Fuzz(func(t, raw []byte))`:
1. `var e transcript.Entry; if json.Unmarshal(raw, &e) != nil { return }` — `e.Turn` is the live
   shape (what the agent wrote).
2. Re-encode and decode through the reload types:
   `b, _ := json.Marshal(e); var re hubcore.ReplayEntry; json.Unmarshal(b, &re)`;
   `reconstructed, _ := replayTurnToAgentTurn(re.Turn)` (`app_threadread.go:217`).
3. **Carry-through equality.** Project both the original and the reload-roundtripped turn through the
   *same* shared projector and compare:
   `live := apptranscript.ProjectTurn("turn_1", 1, e.Turn, map[string]string{}, nil)`;
   `reload := apptranscript.ProjectTurn("turn_1", 1, reconstructed, map[string]string{}, nil)`;
   assert `jsonEq(live, reload)`.

**Beyond no-panic:** this isolates `ReplayPart` carry-through fidelity. If `replayTurnToAgentTurn` /
`ReplayPart` (`hubcore/types.go:31`) fails to carry a content kind (thinking, web_search, audio,
document, image, tool_call, tool_result), the reload projection loses items the live projection kept
— divergence. This is the mechanical generalization of the exact bugs fixed in `0a6b65b0` (thinking)
and `ec96619c` (web_search/audio/document): instead of one hand-written case per kind, the fuzzer
finds any kind the two paths disagree on. Both sides use the **same** `ProjectTurn`, so any
divergence is attributable to the `Entry → ReplayEntry → replayTurnToAgentTurn` round-trip alone.

#### 4b — `FuzzHubReplayLiveVsReload` (full metamorphic: reload vs the live `appprojector`)
The end-to-end metamorphic the isolated oracle deliberately excludes: the **live** path is
`appprojector.AppEventProjector.Project(events.SessionEvent) []AppNotification`
(`internal/appprojector/appwire_projection.go:65`), driven the way the server drives it
(`server/appwire_runtime.go:62` `RecordAppEvent`). The reload path is the same
`Entry → ReplayEntry → replayTurnToAgentTurn → ProjectTurn` as 4a. `f.Fuzz(func(t, raw []byte))`:
1. Decode `e transcript.Entry` (return on error), as 4a.
2. **Live side.** Synthesize the `events.SessionEvent` stream that the turn `e.Turn` would have
   produced live (start turn → per-content-part events → end), feed it through a fresh
   `NewAppEventProjector(threadID, ref)` (`appwire_projection.go:51`), and collect the
   `appwire.ThreadItem`s carried in the emitted `AppNotification.Params`
   (`appwire_projection.go:16`). **Building this turn→SessionEvent synthesizer is the bulk of 4b's
   LoC** and the main build cost; it is a test-only helper in package `main`.
3. **Reload side.** `reconstructed, _ := replayTurnToAgentTurn(re.Turn)`;
   `reload := apptranscript.ProjectTurn(...)` (as 4a).
4. **Normalize + allow-list, then compare.** Run both projections' `appwire.ThreadItem` lists through
   a shared `normalize()` that **strips the known-legitimate live/reload differences** before
   `jsonEq`:
   - **image enrichment** — the live path enriches images with sha/name; reload uses
     `DefaultImageProjector` (`apptranscript.go:181`, empty `Name`). Zero both `Name` and any
     sha-bearing field.
   - **in-progress / status** — the live projector can emit `inProgress` items mid-turn;
     reload always emits `completed` (`appwire.TurnStatusCompleted`). Normalize `Status`.
   - any other field the allow-list documents as live-only (IDs that are stream-cursor-derived vs
     index-derived, etc.). **Every entry in the allow-list MUST cite why the difference is
     legitimate** — an unjustified entry would mask a real carry-through bug.

**Beyond no-panic:** 4b catches divergences the isolated oracle cannot — anything where the *live*
projector and the *reload* projector disagree for reasons other than the documented allow-list. 4a
proves the reload round-trip is faithful to `ProjectTurn`; 4b proves `ProjectTurn`-on-reload is
faithful to what the user actually saw live. The allow-list is the contract for "these two paths are
*supposed* to differ here"; a fuzzer hit outside it is a real reload bug.

**Determinism notes (both):** fixed `turnID`/`turnIndex` for every `ProjectTurn` call (IDs are
index-derived); fresh `toolNames` map per call (`ProjectTurn` mutates and `delete`s from it); 4a uses
the default image projector (`nil`); 4b normalizes image enrichment away rather than disabling it.
The synthesized live stream must use only data **from the fuzzed input** (no `time.Now`, no minted
IDs) so runs are reproducible.

**Seeds (from 8.4), shared by 4a and 4b:** real assistant turns containing each content kind — text,
`thinking`, `redacted_thinking`, `web_search` (with provider `raw`), `tool_call`, a `communicate`
tool_call, `image`, `audio`, `document`; plus a `TOOL_RESULTS` turn; `{}`; garbage. 8.4 harvests the
real turns; the malformed inputs are inline bootstrap.

---

## 3. Safety & determinism (mandatory)

- **Filesystem isolation.** Every target that writes touches **only** `t.TempDir()`:
  Target 1 → `SaveSessionMeta(t.TempDir(), …)`; Target 2 → `Open(filepath.Join(t.TempDir(), …))`;
  Target 3 → `NewWriter` under `t.TempDir()`. No target reads or writes the real state dir, and none
  reads an env var to find one. Target 4 (4a + 4b) builds no files — both projectors run in memory.
- **Offline / deterministic.** No network, no goroutine timing. `transcript.Writer` calls
  `time.Now()` only for `lastSync` book-keeping (default `SyncInterval==0` ⇒ fsync every append) — it
  never enters the serialized bytes. We never call `NewTurn`/`NewJobID`/`ulid.Make` (those mint
  time/IDs); all timestamps and IDs come *from the fuzzed input*, so runs are reproducible.
- **Timestamp/ID normalization.** Compare with `json.Marshal` (a small `jsonEq` helper), never
  `reflect.DeepEqual`, on any value that carries `time.Time` or pointers: a `time.Time` decoded from
  JSON has no monotonic reading and re-marshals stably, but a live `time.Time` would not `DeepEqual`
  a decoded one. Targets 1–3 already use post-decode values so this is belt-and-suspenders; Target 2's
  fold output carries `time.Time` + `*provenance.Causal` and **requires** JSON-compare.
- **jobstore seq.** `Append`/`AppendBatch` reassign `Seq`. Sort input by `Seq` ascending and strip it
  before appending so the reload fold sees the same relative order; otherwise the round-trip oracle
  false-positives (Risks). Same discipline for `Append`==`AppendBatch`: append the *same sorted*
  events both ways so the reassigned `1..N` match.
- **transcript `api_call` seq.** `AppendAPICall` likewise reassigns `Kind`/`Seq` from the writer
  counter (`transcript.go:254`). Strip `Seq` (and let `Kind` re-derive) before the Target-3 step-4
  fixed-point compare, exactly as for entries.
- **Target 4b allow-list is load-bearing.** The live-vs-reload normalization may strip **only** the
  documented legitimate differences (image sha/name enrichment, in-progress status, stream-derived
  IDs). Each strip MUST carry a comment citing why it is legitimate; an over-broad allow-list silently
  converts a real reload carry-through bug into a green run. Keep it minimal and reviewed.

---

## 4. Build order, dependencies, risks, acceptance

### Order
1. **Target 1** (simplest: pure decode + `Save`/`Load`) — establishes the `jsonEq` helper + seed style.
2. **Target 2** (richest reducer; highest mechanical yield) — all-fold persist→reload + batch oracle.
3. **Target 3** (transcript write/read + resume idempotence + `APICall` fixed point).
4. **Target 4a** (isolated hub carry-through) — *highest historical-bug yield; the one to demo the loop with.*
5. **Target 4b** (full live-vs-reload metamorphic) — build last; depends on the turn→SessionEvent
   synthesizer + allow-list, the most involved piece.

### Dependencies
**8.1 depends on 8.4 (corpus harvesting) and runs AFTER it (decision 4).** Every target's
load-bearing seed corpus — real `*.meta.json` (T1), real `jobs.jsonl` lines (T2, which 8.4 is the
producer of), real transcripts incl. `api_call` lines (T3), real per-content-kind turns (T4) — is
harvested by 8.4. The inline `f.Add` lines are only the malformed/edge bootstrap; do not treat 8.1 as
startable before 8.4 has delivered seeds. **This is the one hard cross-item dependency in 8.1.**

Otherwise the targets are mutually independent single-input `testing.F` in modules already in
`GO_MODULES` (`. agent llm auth fuzz` — Makefile:79); `make fuzz` (Makefile:101) already runs
`go test -run '^Fuzz'` per module, so the seed corpus + any saved crasher run in the gate
automatically. Crashers auto-promote to `testdata/fuzz/` — **no `fuzz/promoter` involvement.**

### New verification steps introduced by the decisions
- **T2:** verify each of the six `(reducer, loader)` pairs round-trips (not just `Fold`/`Load`), and
  that one-at-a-time `Append` equals `AppendBatch`.
- **T3:** verify the `APICall` fixed point reaches step 4 (seed must carry an `api_call` line) and that
  `Seq` is stripped before compare.
- **T4b:** verify the turn→SessionEvent synthesizer covers every content kind in the seed corpus, and
  **review the allow-list** — each stripped field must be justified (image enrichment, status,
  stream-derived IDs). Red→green the metamorphic by widening then re-tightening the allow-list to
  confirm it actually gates.

### Risks
- **jobstore seq false-positive** (above) — mitigate by sort-then-strip; covered by a seed with
  out-of-order seqs so the corpus pins the behavior. Applies to the `Append`==`AppendBatch` oracle too.
- **`any` / pointer fields re-marshal drift** (`StructuredResult`, `Provenance`) — JSON-compare
  handles it; a seed carrying a nested `structured_result` object guards it.
- **`ProjectTurn` map mutation** (Target 4) — fresh map per call, or the second projection sees the
  first's `toolNames` and diverges spuriously.
- **Target 4b allow-list over-broad** (above) — an unjustified strip masks a real reload bug. Keep the
  allow-list minimal, commented, and reviewed; the red→green widen/tighten check guards it.
- **Target 4b synthesizer drift** — if the turn→SessionEvent helper omits a content kind, 4b silently
  stops exercising it. The seed corpus per-kind coverage + the synthesizer-coverage verification step
  guard this.
- **Transcript header requirement** (Target 3) — `readTranscript`/`readTranscriptFull` need a header
  first line; blobs without one error out and exercise only the no-panic floor. Seed with valid
  headers so the corpus reaches the round-trip oracles.
- **Speed:** Target 2/3 fsync per append; corpus inputs are tiny, so the gate stays fast. The
  unbounded search is `fuzz-nightly` only.

### Acceptance (design §6 style)
- `make fuzz` green with the four new files (five fuzz functions: T1, T2, T3, T4a, T4b) + seed
  corpora harvested by 8.4; `make test`/`-race` unaffected.
- **Each oracle catches an injected bug** (demonstrate red→green, then revert):
  - **T4a:** delete `case string(llm.ContentThinking)` in `replayTurnToAgentTurn`
    (`app_threadread.go:217`) → reload projection drops the reasoning item → carry-through diverges.
    (Re-creates the real `0a6b65b0` regression.)
  - **T4b:** the same deletion → the live-vs-reload metamorphic diverges on the thinking item too;
    **and** verify a too-broad allow-list (strip everything) goes green — proving the allow-list gates.
  - **T2:** drop a json tag on `Event` (e.g. `Command`) or skip a field in `applyEvent`
    (`fold.go:290`) at the *marshal/store* boundary → persist→reload fold ≠ in-memory fold, on
    whichever of the six projections carries that field. Also: make `AppendBatch` skip a field
    `Append` keeps → the `Append`==`AppendBatch` oracle diverges.
  - **T1:** break a `ConfigSnapshot` json tag → fixed point fails.
  - **T3:** drop a turn in the write loop or break the compaction scan in `ResumeHistory` → round-trip
    count mismatch / idempotence failure; drop/mis-tag an `APICall` field → step-4 fixed point fails.
- After `go test -fuzz` finds a crasher, the saved `testdata/fuzz/<Target>/<hash>` keeps `make fuzz`
  **red until the seam is fixed**, then green — the free loop, demonstrated end-to-end on Target 4a.

---

## 5. Decisions (resolved 2026-06-28)

All open questions are resolved with Jesse. Incorporated into the body above; recorded here verbatim.

1. **Target 4 oracle scope — build BOTH.** The isolated carry-through oracle (reload projection vs
   `ProjectTurn(original Entry turn)`) **and** a full live-vs-reload metamorphic (reload vs the live
   `appprojector`), with an explicit allow-list for the legitimate live/reload differences (image
   sha/name enrichment, in-progress status). → Target 4 = 4a + 4b (§2).
2. **Target 2 — ALL fold variants get persist→reload.** Every reducer (`Fold`, `FoldOrdered`,
   `FoldDelegates`, `FoldWatches`, `FoldWatchSends`, `FoldGrants`) gets the persist→reload idempotence
   treatment, not just `Fold`. Also assert one-at-a-time `Append` == `AppendBatch`. → §2 Target 2
   steps 4–5.
3. **Target 3 — `APICall` round-trip fixed point.** `api_call` lines get a round-trip fixed-point
   oracle, not just no-panic decode. → §2 Target 3 step 4.
4. **Seeds — 8.1 DEPENDS ON 8.4 and runs AFTER it.** 8.1 consumes the seeds 8.4 harvests (including
   the `jobs.jsonl` seeds 8.4 produces). This is the one hard cross-item dependency. → §0 banner +
   §4 Dependencies.
5. **LoC budget is advisory.** Keep all targets and all oracles; do not trim to a number. → §2 table
   note.
