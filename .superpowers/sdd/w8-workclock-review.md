# W8 work-clock units — adversarial review

**Verdict: APPROVED** (one Minor doc-drift finding + two informational notes; none block merge)

Reviewer scope: cross-layer units fix for the live-proven `~495274h` work clock.
Range `73326f7a3..57de2dd36` (3 commits). Worktree `webui-w8-periphery`, HEAD `57de2dd36`.
All findings verified against code; every gate re-run from this worktree; two frontend
clears and the Go producer contract mutation-verified by the reviewer. Tree left clean
(HEAD unchanged; all mutation edits `git restore`d).

## Gate summary (re-run by reviewer, all green)
`go build ./...` exit 0 · `go test ./agent ./server ./cmd/serf ./cmd/serf-hub/... ./cmd/serf-tui ./appwire` all `ok` · `tsc --noEmit` 0 · `vitest run` bare **243 files / 3477 tests passed** · `biome ci` 667 files, no fixes · `npm run build` built, `dist/PLACEHOLDER` restored, tree clean · `go generate ./appwire/...` produced **no diff** (generated `types.gen.ts` + `docs/appwire-protocol.md` are not stale).

## Probe outcomes
- **P1 evidence chain — HOLDS.** `epochMsToISO` (reducer.ts:78-84) is genuinely shared: fed the wire anchor at :266 *and* frontend-ms `now` at :157/:203/:356 — a seconds→ms flip there would double-convert `observedStartedAt/observedCompletedAt`, so the producer is the correct layer. `session_metrics.go` is the sole live producer; every downstream is a verbatim relay. Contract-doc = ms confirmed on model.ts:121, statusFormat.ts, and the two edited Go structs.
- **P2 cross-consumer — SAFE, one doc blemish.** No Go code computes elapsed from the field anywhere (arithmetic grep clean; only the assignment at appwire_runtime.go:876). Legacy `renderer.js` + all `cmd/serf-hub/assets/*.js` + `input_strip.html` never reference it (fixer's claim verified). TUI mirrors it from the appwire path (hub_types.go:278) but has zero compute/render site — a dead mirror. React reducer reads it as ms (now correct). **Finding 1** (hubapi doc drift) is the only issue.
- **P3 rename completeness — COMPLETE.** Zero `ActiveTurnStartedAtUnix` references remain. Both edited struct comments (appwire/types.go:264-266, server/server.go:105-106) state MILLISECONDS. Generated files regenerate to a no-op diff — no uncommitted generated twin.
- **P4 clear-on-rest — CORRECT, mutations bite.** Clears on exactly `turn/completed` (:451) and `thread/status/changed`→non-active (:589). Reviewer removed each clear and ran the targeted test: `turn/completed clears…` FAILS without :451; `…non-active status clears…` FAILS without the :589 guard (other two work-clock tests still pass → guard load-bearing). No other stale-anchor path: `turn/started` sets no anchor (no live push); `escalation/resolved` correctly leaves it (turn may continue); fork + reconnect re-hydrate self-heal via unconditional :266.
- **P5 RED-first + fixtures — GENUINE.** Reviewer reverted only the producer to `.Unix()` with the test unchanged → `mid-turn ActiveTurnStartedAtMillis() = 1000, want 1000000` FAIL. Fixtures use ms-scale wire-true values (`1_700_000_000_000`). Exactly 3 new frontend tests; Go changes are renames + assertion flips (no count change); 3474→3477 confirmed by bare vitest.
- **P6 gates — all green** (see gate summary). Reviewer-run, tree clean afterward.
- **P7 deferred sibling — receipts ACCURATE.** `Turn.StartedAt`/`ThreadItem.StartedAt/CompletedAt` stamp `.Unix()` at appwire_projection.go:381/455/458/913 (+ :820 completion) and apptranscript.go:354/390/562. Consumed as ms by reasoningFormat.ts:48 (`(end-start)/1000`, `Math.max(1,…)`) and subagentModule.tsx:129 (Date diff). Symptom is bounded: both endpoints same unit → the 1970 offset cancels in the difference → 1000×-small / floored-to-1s, never the catastrophic `now − anchor`. After this fix `ActiveTurnStartedAt` is the sole correctly-ms absolute epoch through `epochMsToISO`. See Note B on the deferral rationale.

## Findings

### Minor
1. **`hubapi/types.go:128-130` doc comment is now unit-inaccurate.** It reads
   "ActiveTurnStartedAt is the unix-**seconds** timestamp"; after this fix the value on
   `hubapi.SessionDetail` (the hub's HTTP JSON API, `active_turn_started_at`) is
   epoch-ms. The fix documented ms on `appwire.SerfThread` and `server.StatusInfo` but
   left this third JSON-serialized relay's comment stating the old unit — a direct
   counterexample to the report's "documented the ms unit" completeness. **No runtime
   impact:** no in-repo consumer computes from this field (the TUI uses the appwire
   websocket path, not this JSON; `.ActiveTurnStartedAt` has zero elapsed-computing read
   sites repo-wide). It is a documentation-accuracy defect on a public API surface, on the
   exact axis the task was fixing. Recommend updating the comment to "unix epoch
   milliseconds" (1-line), ideally in this change or folded into the P7 sibling sweep.
   Not merge-blocking.

## Informational (not defects)
- **Note A — mixed-units wire is intentional and documented.** Post-fix, `ActiveTurnStartedAt` is ms while its item/turn timestamp siblings remain seconds. The fixer flagged this explicitly on the struct and scoped the sibling to Jesse. It is a maintenance trap only until the P7 sweep lands; Finding 1 is the one spot where that reality wasn't propagated.
- **Note B — P7 backward-compat is *not* the real barrier.** Transcript `Turn.Timestamp` is a `time.Time` (`.IsZero()`/`.Unix()` applied on read, apptranscript.go:353-354/561-562); the int64 seconds are projected on-read, never persisted as raw ints. So the sibling fix is a clean `.Unix()`→`.UnixMilli()` projection change with no persisted-format migration — old transcript files re-project fine. The deferral is justified on scope/risk grounds (it touches live-proven rendering + appprojector/apptranscript and their tests), which is legitimate, but Jesse should know the persisted-format entanglement hypothesis does not hold.

## Correctness spot-checks (no issue found)
- Live happy path repaired: mid-turn re-hydrate now yields a correctly-scaled ms anchor (~1.7e12) → `totalWorkMillis` computes a sane `now − anchor`; the very path that produced 495274h.
- Rest behavior honest: with the anchor cleared, `StatusRow` shows the banked `workMillis`; freezing (vs. a stale live push) is correct given neither field has a live push — a pre-existing limitation this fix does not worsen.
- Both `<=0` sentinel nets (reducer.ts:83, statusFormat.ts) correctly retained as orthogonal unset guards; a positive seconds anchor slipping past them was precisely why producer+reducer (not a guard tweak) was required.
- All Go relay-fidelity tests inject their own values (unit-agnostic) — none encoded seconds semantics, so the producer flip breaks none (confirmed green).
