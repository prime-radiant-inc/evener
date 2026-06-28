# 8.1 — Persistence round-trip + replay-idempotence fuzz targets — implementation plan

**Status:** plan. **Date:** 2026-06-28. **Branch:** `wip/fuzzing-toolkit`.
**Charter:** design §8.1. **Pattern to match:** `llm/sse_fuzz_test.go`, `appwire/jsonrpc_fuzz_test.go`.
**Builds on:** design §0–§3 (conventions, `make fuzz` wiring), research §3/§5 (oracle taxonomy).

All four targets are single-input `testing.F` — Go-native auto-promotion turns any crasher into a
permanent `testdata/fuzz/` regression. **No promoter needed** (the charter's expectation holds).

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
(`testing.F` single-input → free auto-promotion). Real on-disk lines seed `f.Add`.

| # | File | Package | Drives | LoC |
|---|---|---|---|---|
| 1 | `agent/schema/snapshot_fuzz_test.go` | `schema` | `SessionMeta` decode + `Save`/`Load` | 80–120 |
| 2 | `agent/internal/jobstore/fold_fuzz_test.go` | `jobstore` | event-log decode + all folds + persist→reload | 120–160 |
| 3 | `agent/transcript_roundtrip_fuzz_test.go` | `agent` | transcript write/read + `ResumeHistory` | 90–130 |
| 4 | `cmd/serf-hub/replay_fuzz_test.go` | `main` | hub replay carry-through (metamorphic) | 80–120 |

Total ~370–530 (charter ~300–500; slightly over because Target 4 — the highest-yield one — is worth
keeping; see open question 4).

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

**Seeds:** a real `*.meta.json` (compact); a meta with `goal`, `pinned_note`, `observed_by`,
`tool_output_limits`; a legacy `{"original_task":"…"}` doc (pins the legacy path); `{}`, `null`,
`not json`.

### Target 2 — `FuzzJobEventLogReplay` (`agent/internal/jobstore/fold_fuzz_test.go`)
Input is a raw JSONL blob (one `Event` per line). `f.Fuzz(func(t, raw []byte))`:
1. Split on `'\n'`; for each non-empty line `json.Unmarshal` into `Event`; skip lines that error
   (mirrors a tolerant reader). Collect `events`.
2. **No panic across every reducer:** `Fold`, `FoldOrdered`, `FoldDelegates`, `FoldWatches`,
   `FoldWatchSends`, `FoldGrants` over `events`.
3. **Fold determinism:** `jsonEq(Fold(events), Fold(events))` (catches map/ordering nondeterminism
   in the reduced output; the folds sort `SliceStable` by `Seq`, so a stable result is the contract).
4. **Persist → reload replay-idempotence (the load-bearing oracle).**
   - `sorted := bySeqAsc(events)` then strip `Seq` (store reassigns it).
   - `s, _ := Open(filepath.Join(t.TempDir(), "jobs.jsonl")); s.AppendBatch(sorted); s.Close()`.
   - `s2, _ := Open(samePath); recs2, _ := s2.Load(); s2.Close()`.
   - assert `jsonEq(Fold(sorted), recs2)`.

**Why this is more than no-panic:** step 4 round-trips every event through `marshal → fsync'd file →
readAll → Fold`. A field that `Append` writes but `readAllLocked` or `applyEvent` drops, a
`Seq`-ordering regression, or a marshal/unmarshal asymmetry on `StructuredResult any` /
`*provenance.Causal` all diverge here. This is the in-memory job state a daemon restart reconstructs
— exactly serf's reload-bug class, at its purest reducer.

**Determinism notes:** compare with `json.Marshal` of the folded maps, not `reflect.DeepEqual` —
JSON normalizes `time.Time` (drops the monotonic reading) and pointer identity. Sort input by `Seq`
*before* stripping so append order == the order `Fold` would have imposed (otherwise the reassigned
`1..N` reorders relative to the original seqs and the comparison is a false positive — see Risks).

**Seeds:** one real line per `EventKind` (`job_started`, `job_session_assigned`, `job_finished`,
`watch_registered`, `watch_send_pending`, `delegate_created`, `watch_read_grant`, …) drawn from a
real `jobs.jsonl`; an out-of-order-seq pair; a duplicate `job_finished` (first-terminal-wins);
empty + garbage lines.

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

**Beyond no-panic:** step 2 is transcript persistence fidelity (the daemon writes turns, the hub /
resume reads them back); step 3 is replay-into-state idempotence for the exact function a resumed
session rebuilds its working history from. A compaction-scan or orphan-repair regression breaks the
fixed point.

**Seeds:** a real transcript (header + user + assistant-with-tool_call + tool_results); one with a
`CHECKPOINT`/`SUMMARY` turn (exercises the compaction branch); one with an orphaned tool result
(exercises `repairOrphanedToolResults`); header-only; garbage.
**Scope:** `APICall` lines decode (no-panic via `readTranscriptFull`) but are not replayed into
state, so no semantic oracle on them (open question 3).

### Target 4 — `FuzzHubReplayCarryThrough` (`cmd/serf-hub/replay_fuzz_test.go`, package `main`)
The reload metamorphic oracle — **the first place to look.** Input is the JSON of one transcript
`Entry`. `f.Fuzz(func(t, raw []byte))`:
1. `var e transcript.Entry; if json.Unmarshal(raw, &e) != nil { return }` — `e.Turn` is the live
   shape (what the agent wrote).
2. Re-encode and decode through the reload types:
   `b, _ := json.Marshal(e); var re hubcore.ReplayEntry; json.Unmarshal(b, &re)`;
   `reconstructed, _ := replayTurnToAgentTurn(re.Turn)`.
3. **Carry-through equality.** Project both the original and the reload-roundtripped turn through the
   *same* shared projector and compare:
   `live := apptranscript.ProjectTurn("turn_1", 1, e.Turn, map[string]string{}, nil)`;
   `reload := apptranscript.ProjectTurn("turn_1", 1, reconstructed, map[string]string{}, nil)`;
   assert `jsonEq(live, reload)`.

**Beyond no-panic:** this isolates `ReplayPart` carry-through fidelity. If `replayTurnToAgentTurn` /
`ReplayPart` fails to carry a content kind (thinking, web_search, audio, document, image, tool_call,
tool_result), the reload projection loses items the live projection kept — divergence. This is the
mechanical generalization of the exact bugs fixed in `0a6b65b0` (thinking) and `ec96619c`
(web_search/audio/document): instead of one hand-written case per kind, the fuzzer finds any kind the
two paths disagree on.

**Determinism notes:** fixed `turnID`/`turnIndex` for both calls (IDs are index-derived); fresh
`toolNames` map per call (`ProjectTurn` mutates and `delete`s from it); default image projector
(`nil`) — the live path's sha/name enrichment is hub-specific and out of scope here (open question 1).
**Seeds:** real assistant turns containing each content kind — text, `thinking`, `redacted_thinking`,
`web_search` (with provider `raw`), `tool_call`, a `communicate` tool_call, `image`, `audio`,
`document`; plus a `TOOL_RESULTS` turn; `{}`; garbage.

---

## 3. Safety & determinism (mandatory)

- **Filesystem isolation.** Every target that writes touches **only** `t.TempDir()`:
  Target 1 → `SaveSessionMeta(t.TempDir(), …)`; Target 2 → `Open(filepath.Join(t.TempDir(), …))`;
  Target 3 → `NewWriter` under `t.TempDir()`. No target reads or writes the real state dir, and none
  reads an env var to find one. Targets 4 builds no files.
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
  false-positives (Risks).

---

## 4. Build order, dependencies, risks, acceptance

### Order
1. **Target 1** (simplest: pure decode + `Save`/`Load`) — establishes the `jsonEq` helper + seed style.
2. **Target 2** (richest reducer; highest mechanical yield) — the persist→reload oracle.
3. **Target 3** (transcript write/read + resume idempotence).
4. **Target 4** (hub carry-through) — *highest historical-bug yield; the one to demo the loop with.*

### Dependencies
None. All four are single-input `testing.F` in modules already in `GO_MODULES`
(`. agent llm auth fuzz` — Makefile:79); `make fuzz` (Makefile:101) already runs `go test -run '^Fuzz'`
per module, so the seed corpus + any saved crasher run in the gate automatically. Crashers
auto-promote to `testdata/fuzz/` — **no `fuzz/promoter` involvement.**

### Risks
- **jobstore seq false-positive** (above) — mitigate by sort-then-strip; covered by a seed with
  out-of-order seqs so the corpus pins the behavior.
- **`any` / pointer fields re-marshal drift** (`StructuredResult`, `Provenance`) — JSON-compare
  handles it; a seed carrying a nested `structured_result` object guards it.
- **`ProjectTurn` map mutation** (Target 4) — fresh map per call, or the second projection sees the
  first's `toolNames` and diverges spuriously.
- **Transcript header requirement** (Target 3) — `readTranscript` needs a header first line; blobs
  without one error out and exercise only the no-panic floor. Seed with valid headers so the corpus
  reaches the round-trip oracle.
- **Speed:** Target 2/3 fsync per append; corpus inputs are tiny, so the gate stays fast. The
  unbounded search is `fuzz-nightly` only.

### Acceptance (design §6 style)
- `make fuzz` green with the four new targets + seed corpora; `make test`/`-race` unaffected.
- **Each target catches an injected bug** (demonstrate red→green, then revert):
  - **T4:** delete `case string(llm.ContentThinking)` in `replayTurnToAgentTurn`
    (`app_threadread.go:217`) → reload projection drops the reasoning item → carry-through diverges.
    (Re-creates the real `0a6b65b0` regression.)
  - **T2:** drop a json tag on `Event` (e.g. `Command`) or skip a field in `applyEvent`
    (`fold.go:290`) at the *marshal/store* boundary → persist→reload fold ≠ in-memory fold.
  - **T1:** break a `ConfigSnapshot` json tag → fixed point fails.
  - **T3:** drop a turn in the write loop or break the compaction scan in `ResumeHistory` → round-trip
    count mismatch / idempotence failure.
- After `go test -fuzz` finds a crasher, the saved `testdata/fuzz/<Target>/<hash>` keeps `make fuzz`
  **red until the seam is fixed**, then green — the free loop, demonstrated end-to-end on Target 4.

---

## 5. Open questions (for Jesse)

1. **Target 4 oracle scope.** Compare reload projection against `ProjectTurn(original Entry turn)`
   (isolates `ReplayPart` carry-through — recommended), or against the *live* hub projector
   (`appprojector`) for a full live-vs-reload metamorphic? The latter has legitimate live/reload
   differences (image sha/name enrichment, in-progress status) that would need allow-listing. I lean
   to the isolated carry-through oracle and treat full live-vs-reload as a later, separate target.
2. **Target 2 extras.** Also assert `Append`-one-at-a-time folds identically to `AppendBatch`
   (cheap, guards the batch path)? And should every fold variant get the persist→reload treatment, or
   is `Fold` (the job records) sufficient as the canonical one with the rest at no-panic + determinism?
3. **`APICall` semantics (Target 3).** `APICall` lines carry `llm.APILogRequest/Response` (large) and
   are never replayed into state — keep them at no-panic decode only (recommended), or add a
   fixed-point on them too?
4. **LoC budget.** Four targets land ~370–530 vs the ~300–500 charter. Keep all four (Target 4 is the
   highest-yield), or fold Target 3's two oracles down / drop one? Recommend keep all four.
5. **Seed sourcing.** Inline `f.Add` + a few hand-pasted real lines now, deferring bulk seeds to 8.4
   (corpus harvesting)? Recommended — don't block on harvesting.
