# W8 timestamp units — Turn/ThreadItem epochs → milliseconds

Status: **DONE_WITH_CONCERNS** (one scoping nuance flagged, §Concerns)
Branch: `w8-timestamps` · commits `0424a6126..06e86cbb2` (2, on `57de2dd36`)

## Jesse's ruling (verbatim, relayed by coordinator)
> "kill the back-compat in favor of better code going forward."

No compatibility handling: no dual-unit reading, no migration, no shims. Every
absolute-epoch wire timestamp the frontend consumes as a wall-clock is now
epoch-milliseconds — matching `SerfThread.ActiveTurnStartedAt` (fixed in the
prior work-clock task), `WorkMillis`, and `DurationMS`.

## Producer receipts — `.Unix()` (seconds) → `.UnixMilli()` (ms)
Each stamps a wire `*int64` the reducer reads via `epochMsToISO` (reducer.ts:124-125,
215-216). All nine were seconds; all are now ms.

| Site | Wire field |
|---|---|
| internal/appprojector/appwire_projection.go:381 | ThreadItem.StartedAt (tool-call start) |
| …:455 | ThreadItem.CompletedAt (tool-call end) |
| …:458 | ThreadItem.StartedAt (recorded start on settled item) |
| …:820 → applyPendingTiming:930 | Turn.CompletedAt (field renamed `pendingCompletedAtUnix`→`pendingCompletedAtMillis`) |
| …:913 (startedTurn) | Turn.StartedAt |
| internal/apptranscript/apptranscript.go:354 | ThreadItem.StartedAt (assistant tool-call) |
| …:390 | ThreadItem.CompletedAt (tool-result) |
| …:562 | Turn.StartedAt (turnsFromFile) |
| internal/apptranscript/turn_index.go:916 | Turn.StartedAt (bounded-cache path) |

Misleading `unix`/`startUnix` locals renamed to `ms`/`startMs`. `.UnixNano()` at
turn_index.go:474/524 (`ModTimeUnixNS`, file-mtime cache identity) untouched — not a
wire timestamp.

## Server-side consumer catch (would have regressed silently)
`cmd/serf-hub/web_format.go:74` `activeTurnRunningFor` rendered the workspace
partial's "running for" clock via `time.Since(time.Unix(*turn.StartedAt, 0))` —
reading `Turn.StartedAt` as **seconds**. After the producer→ms change this misreads
the value (start ~55000 CE → clamped "1s"). Fixed to `time.UnixMilli`. This is the
only server-side reader of a wire Turn/Item timestamp (grep of `time.Unix(` across
hub/server/internal; the only other hit, `hubcore/session_order.go:111`, is the
CreatedAt seconds contract — see below).

## Doc updates
- `appwire.Turn.StartedAt/CompletedAt` and `appwire.ThreadItem.StartedAt/CompletedAt`:
  ms unit documented.
- `hubapi/types.go:129` (coordinator note 1): stale "unix-seconds" anchor comment →
  "Unix epoch-milliseconds" (comment-only; the TUI contract's routes/shapes untouched).
- Generated output: `go generate ./appwire/...` (owns `docs/appwire-protocol.md` +
  `types.gen.ts`) produced **no diff** — field doc comments do not propagate to
  generated files; `internal/appwirets` drift test `TestGeneratedFileCurrent` passes.
  No generated twin to commit.

## RED evidence (RED-first, per producer + consumer)
- Producers: flipped the existing value assertions `.Unix()`→`.UnixMilli()` in
  appwire_projection_test.go / apptranscript_test.go / turn_index_test.go → RED against
  the seconds producers, e.g. `turn/started StartedAt=1700000000, want 1700000000000`
  (appwire_projection_test.go:263); `turn.StartedAt=1700000000, want 1700000000000`
  (apptranscript_test.go:68); `turn_1 StartedAt=… want=1700000001000`
  (turn_index_test.go:108). GREEN after the producers moved to `.UnixMilli()`.
- Consumer: new `TestActiveTurnRunningForReadsStartedAtAsMillis` →
  `activeTurnRunningFor = "1s", want "2m"` (web_format_test.go:28) under the
  seconds-reading code; GREEN after `time.UnixMilli`.
- Test-side wire constructions that built `Turn.StartedAt` in seconds
  (web_test.go:560 + 3 cov fuzz tests) updated to ms for wire-truth.

## Mutation proofs — one duration net per frontend consumer
- **subagentModule** (`durationLabel`, previously untested): added a wire-true net —
  a settled delegate item whose ISO start/complete come from a realistic epoch-ms
  (12s apart) must render `"12s"`. Mutation: swap the ISO diff operands → negative →
  `undefined` → "12s" not found → RED. Reverted.
- **reasoningFormat** (`thoughtSeconds`): existing ms-scale ISO net
  (reasoningFormat.test.ts:79-101). Mutation: `elapsedSeconds` `/1000`→`/1` →
  `expected 4400 to be 4` → RED. Reverted.
- Wire-int64(ms) → ISO boundary that feeds both consumers is locked by
  reducer.test.ts:508-521 (turn) and :1539-1548 (item) — both ms-scale.
- No frontend fixture encoded seconds-scale (real epoch-seconds ~1.7e9) values; the
  fixtures use ms-scale synthetic/realistic values, so none needed updating.

## Persistence — old data renders CORRECTLY (coordinator note 2, verified)
Receipt: `agent/schema/turn.go:41` — `Timestamp time.Time \`json:"timestamp"\``, with
**no** custom `MarshalJSON` on `schema.Turn` (grep clean). Go serialises `time.Time`
as an RFC3339 **string** on disk; the appprojector/apptranscript producers PROJECT it
to an int64 only on read (now `.UnixMilli()`). There is no baked-seconds number in any
persisted transcript, so the `.Unix()`→`.UnixMilli()` change is read-time only:
**old transcripts render correctly under the ms projection.** No migration exists to
skip; the "old data renders degraded" caveat from Jesse's ruling dissolves entirely.

## Gates (per commit, AND-chained)
- `go build ./...` ✓ · `go test ./internal/appprojector ./internal/apptranscript ./appwire
  ./hubapi ./cmd/serf-hub/... ./cmd/serf-tui/... ./server ./cmd/serf` ✓
- `npx tsc --noEmit` ✓ · `npx vitest run` ✓ 243 files / **3478** (baseline 3477 + 1 new)
  · `npm run lint` ✓ (biome) · `npm run build` ✓ + `git restore dist/PLACEHOLDER` ✓

## Concerns
- **"ALL absolute epochs on the wire are ms" is not literally true — and shouldn't be.**
  `appwire.Thread.CreatedAt/UpdatedAt` is a SEPARATE hub-internal **seconds** contract,
  produced by `hubcore.UnixSeconds` (session_order.go:114 → `t.Unix()`) +
  `local_daemon.go:584` (`.Unix()`), consumed only server-side by `hubcore.UnixTime`
  (session_order.go:107 → `time.Unix(seconds,0)`), `AgeString`, and session ordering.
  The React frontend NEVER reads `thread.createdAt/updatedAt` (grep empty; the
  types.gen.ts:767-768 mirror is unused). Converting it to ms would break tree
  age/ordering (`time.Unix(ms,0)` → year ~55000) for zero frontend benefit. I therefore
  scoped the migration to the frontend-consumed Turn/ThreadItem/ActiveTurnStartedAt
  epochs and left the CreatedAt/UpdatedAt seconds contract intact, documenting the unit
  per-field rather than writing a false blanket "all wire epochs are ms" note. Flagging
  in case a fully-uniform wire is desired — that is a larger, separate change into
  hub session-ordering with no live bug driving it.
