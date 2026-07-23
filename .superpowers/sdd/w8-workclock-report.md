# W8 work-clock units — root-cause + fix

Status: **DONE**
Branch: `w8-periphery` · commits `c448bd808..aadeef645` (2, on `73326f7a3`)

## Symptom (wave8-report P1)
Session StatusRow work-clock rendered `~495274h` during a real turn. The live React
fiber showed `model.activeTurnStartedAt` parsing to `1784774627` — exactly
`Date.now()/1000` — i.e. an epoch-**seconds** anchor read as epoch-**ms**, so
`totalWorkMillis` computed `now(ms) − anchor(seconds) ≈ now − 1970 ≈ 1.78e12 ms`.
Second half: the anchor was not cleared when the session went `awaiting`, so the
idle clock kept ticking.

## Evidence chain

### 1. The documented wire contract = epoch-MILLISECONDS
- Frontend model doc: `cmd/serf-hub/frontend/src/protocol/model.ts:119-122` — "converted
  from the wire's **epoch-ms** SerfThread.ActiveTurnStartedAt."
- Consumer doc: `.../chrome/statusFormat.ts:50` — "A real turn start is a positive
  **Unix-epoch-ms** wall-clock time."
- Reducer conversion: `.../protocol/reducer.ts:266` `epochMsToISO(thread.serf.activeTurnStartedAt)`
  → `reducer.ts:83` `new Date(ms)` (treats the value as ms).
- Sibling field on the same struct is `WorkMillis` (ms). A work-clock feature whose
  banked total is ms and whose live anchor is seconds is internally incoherent.
- The Go wire type `appwire/types.go:258-266` and `server/server.go:99-107` carried **no**
  explicit unit before this task (silent). The only unit *documentation* anywhere named ms
  (frontend). The wave author's own recommendation (below) is ms.

### 2. Producers — every absolute epoch on the Go side is stamped `.Unix()` (seconds)
The field in question has exactly one live producer:
- `agent/session_metrics.go:18-28` `ActiveTurnStartedAtUnix()` returned
  `s.turnStartedAt.Unix()` — **seconds**, and its doc literally said "(seconds)".
- Flows: `cmd/serf/serve.go:561` `SetWorkMetricsFunc(... sess.ActiveTurnStartedAtUnix())`
  → `server/server_handlers.go:316` / `server/appwire_runtime.go:819-876`
  → `appwire.SerfThread.ActiveTurnStartedAt`.
- Introduced this way from day one: commit `0244a715f` (Jesse, 2026-07-04) — never a
  regression; the seconds/ms mismatch was latent until the work-clock consumed it.

Repo-wide, `.UnixMilli()` had **zero** occurrences and `.Unix()` was the universal idiom
for absolute epochs (`internal/appprojector/appwire_projection.go:381,455,458,820,913`,
`internal/apptranscript/apptranscript.go:354,390,562,916`,
`cmd/serf-hub/internal/appsource/local_daemon.go:584`). Durations, by contrast, are ms
(`DurationMS` via `.Milliseconds()`, `WorkMillis`). So the Go worldview is coherent
(absolute=seconds, duration=ms); the incoherence is purely at the wire boundary, where the
frontend expects absolute timestamps in ms.

### 3. Legacy consumer — no de-facto seconds contract existed
- `cmd/serf-hub/assets/renderer.js` (still present at this HEAD) **never references**
  `activeTurnStartedAt` / `active_turn_started_at`. The old UI predates the field's
  consumption, so there is no legacy `*1000` / `/1000` precedent to honor.
- TUI: `cmd/serf-tui/hub_types.go:100,278` **mirrors** the int64 into a struct but never
  computes elapsed from it. No consumer depended on the seconds value.
- The hub never computes elapsed from the field server-side (relays only:
  `web_format.go:59`, `web_workspace.go:495`, `web_api_tree.go:558,812,917`).

### 4. Rest semantics — the anchor is hydration-only, never cleared
- `activeTurnStartedAt` has **no live push** (model.ts:122); it is set once by
  `hydrateThread` (reducer.ts:266) and the producer returns 0 only for a *fresh pull*
  while not processing — but the frontend never re-pulls mid-session.
- `reducer.ts:574-577` (`thread/status/changed`) updated only `status`; it did **not**
  clear the anchor. `StatusRow.tsx:130` feeds `model.activeTurnStartedAt` to
  `totalWorkMillis` **unconditionally**. So a cold hydrate mid-turn left a live anchor that
  kept ticking after the turn ended → the "still ticking while awaiting" half.

## Chosen layers + rationale

Two honest layers, matching wave8-report **Decision #1** ("the Go unit fix as the root
cause + the frontend status guard as defense-in-depth"):

1. **Producer (Go) → emit epoch-ms.** The documented contract for this field is ms; the
   producer was the lone violator *for this field*. Fixed at the honest layer rather than
   the consumer because `epochMsToISO` is a **shared** helper also fed frontend-ms `now`
   (reducer.ts:157,203,356) — flipping it to seconds→ms would double-convert those and
   break `observedStartedAt/observedCompletedAt`. Renamed
   `ActiveTurnStartedAtUnix → ActiveTurnStartedAtMillis` (`.UnixMilli()`) so the name no
   longer lies; documented the ms unit on `appwire.SerfThread` and `server.StatusInfo`.
2. **Reducer → clear the anchor on rest transitions** (model honesty for the awaiting
   half). Cleared on the two transitions the reducer already handles:
   `thread/status/changed` to any non-active `status.type`, and `turn/completed`. The model
   now never carries a live anchor at rest, so the clock freezes at the banked total.

Both existing `<=0` nets are **retained** unchanged (they guard the unset-sentinel `0`,
orthogonal to units): `reducer.ts:83` (epochMsToISO) and `statusFormat.ts:61`.

## RED evidence
- Go: flipped `agent/session_state_test.go` expectation to `.UnixMilli()` against the
  unchanged seconds producer →
  `mid-turn ActiveTurnStartedAtUnix() = 1000, want 1000000` (FAIL). Then GREEN after
  `.UnixMilli()`. Fuzz assertion `status_support_program_fuzz_test.go:109` moved in lockstep.
- Frontend: two new wire-true tests (drive real `hydrateThread` + `applyNotification`)
  failed against the unchanged reducer — anchor survived as `"2023-11-14T22:13:20.000Z"`
  (note: the ms value `1_700_000_000_000` yields a *real 2026-era* date, confirming ms end
  to end) instead of `undefined`. GREEN after the two reducer clears.

## Mutation proofs
- Remove the `thread/status/changed` clear → "non-active clears" FAILS (RED run).
- Remove the `turn/completed` clear → "turn/completed clears" FAILS (RED run).
- Replace the active-guard with unconditional `undefined` → "staying active preserves"
  FAILS (M1, reverted). Guard is load-bearing, not vacuous.

## Gates (per commit, AND-chained, from worktree root / frontend)
- `go build ./...` ✓ · `go test ./agent ./server ./cmd/serf ./cmd/serf-hub/... ./cmd/serf-tui ./appwire` ✓
- `npx tsc --noEmit` ✓ · `npx vitest run` ✓ 243 files / **3477** tests (baseline 3474 + 3 new)
  · `npm run lint` ✓ (biome, no fixes) · `npm run build` ✓ + `git restore dist/PLACEHOLDER` ✓

## Related finding — NOT fixed (out of scope, flagged for Jesse)
`Turn.StartedAt`, `ThreadItem.StartedAt/CompletedAt` share the identical
seconds-on-the-wire defect (`internal/appprojector/appwire_projection.go:381,455,458,913`,
`internal/apptranscript/apptranscript.go`), consumed as ms by
`reasoningFormat.ts:43-48` (`(end-start)/1000`) and
`transcript/tools/subagentModule.tsx:128-129` (`new Date().getTime()` diff). Because both
endpoints are the *same* unit, the manifestation is only a **1000×-too-small / floored**
duration (e.g. every "Thought for Ns" floors to `1s`), never the catastrophic mixed-unit
`now − anchor`. This task was scoped to the work-clock (`ActiveTurnStartedAt`); fixing the
transcript timestamps touches live-proven rendering + appprojector/apptranscript and their
tests, so it is surfaced here rather than silently widened. The clean global fix is the
same pattern (`.Unix()` → `.UnixMilli()` for all three, RED-first). After this task
`ActiveTurnStartedAt` is the one ms absolute-epoch on the wire — documented as such on the
struct to prevent confusion.
