# Sidecar circuit breaker — implementation plan

Date: 2026-06-19
Branch: `wip/sidecar-circuit-breaker` (worktree `.worktrees/sidecar-circuit-breaker`), on `main` (the doctor work is landed).
Companion to: `2026-06-19-sidecar-circuit-breaker-design.md` (the *what/why*). This is the *how/sequence*.
Anchors below are current as of `8a12938e` (line numbers in the design's §8 had drifted; these supersede them).

---

## 1. Corrected code anchors (verified live, supersedes design §8)

| Thing | Design §8 said | Actual (8a12938e) |
|---|---|---|
| Suppression call sites | one, `:1651` | **four**: `onSessionEvent` `:1684`, plus `:1803`, `:2028`, `:2103` |
| `shouldSuppressWatch` def | `:1691` | `:1724` → `provenance.ContainsWatch` `:1728` |
| Delivery stamp (`WithWatch`) | `:970` | `watchSendSnapshot` `:935`; also `:2141` (no-send notification) |
| Coalescing `Union` | `:2730` | `recordWatchSendPending` `:2900` (`provenance.Union(existing, state)`) |
| Hard-forbid ("line 931") | `:931` | `validateWatchDeliveryLoop` `:883`, error text `:895` |
| Frame render (NEW anchor) | — | `buildWatchFrame` `:3806`; `writeWatchFrameProvenance` `:3874`; delivery's `frame` field `:221`; `snapshotWatchSendFrame` `:2225` |
| Delivery settle (NEW anchor) | — | `settleWatchSendDelivered` `:329` (appends `EventWatchSendDelivered`) |
| Existing volume breaker (NEW) | — | `watchConfig.deliveries` `:133`; `recordWatchDeliveryLocked`/`autoClearWatchOverBudget` `:340–343` |
| Depth rule home | `doctor/watches.go deliverySelfLoop` re-derives it | **delete it**; rule moves to `provenance`, owned by the *runtime*; doctor reads recorded telemetry |
| Durable record | — | `agent/internal/jobstore/record.go:158` `WatchSendState.DiagnosticReason` (new `SelfInfluenceDepth` field sits beside it); `EventWatchSendDropped` carries the drop reason |
| Provenance types | `provenance.go` | `Causal{WatchKeys, Chain, ChainTruncated}`, `Entry{Kind,WatchID,WatchGeneration,DeliveryID,...}`, `ContainsWatch`, `WithWatch`, `maxDiagnosticChain=16` |

**There are four suppression sites, not one.** The policy flip is a shared helper swapped in at all four, not a one-line edit in `onSessionEvent`.

---

## 2. Architecture locked by recon

1. **Inform transport = render into the frame.** `buildWatchFrame` already composes what the watcher sees and already writes provenance into it (`writeWatchFrameProvenance`). The gradient line is a `<system-reminder>…</system-reminder>` string prepended in `buildWatchFrame` when depth > 0 — matching the spec's "into the delivered frame, system-reminder-style." No separate steer, no second payload.

2. **Depth primitive is pure, in the data plane, owned by the runtime.** Add to `package provenance`:
   ```go
   // SelfInfluenceDepth counts distinct *delivered* prior deliveries of watchID in
   // p.Chain. Coalescing-aware: a hop counts only if delivered(hop.DeliveryID) — a
   // superseded pending unions its chain entry into the survivor (job_watch.go:2900)
   // but never independently delivered, so it must not inflate depth.
   //   generation == ""     → count across all generations (the runaway fuse scope)
   //   generation == "<gen>" → count only that arming      (the gradient scope)
   func SelfInfluenceDepth(p *Causal, watchID, generation string, delivered func(string) bool) int
   ```
   Only the **runtime** calls this — to enforce the fuse and size the gradient. The doctor does **not** reuse it (see point 7). No `excludeDeliveryID` param: the runtime decides *before* its own delivery is stamped, so there is no own-hop to skip; that param existed only for the doctor's post-hoc re-derivation, which is going away.

3. **`delivered(id)` source = a jm-level delivered set.** Add `jm.deliveredWatchSendIDs map[string]struct{}`, populated in `settleWatchSendDelivered` (`:329`, the one choke point where a delivery becomes durable-delivered). Each watch's deliveries settle in its own jm, and depth only counts hops with `watch_id == cfg.watchID`, so a per-jm set keyed by delivery_id is sufficient and correct even across sidecars. (Pruning is a non-blocking follow-up; deliveries are already volume-capped per watch.)

4. **Two scopes, two purposes** (design §3, now concrete):
   - **Inform gate** = `ContainsWatch(ev.Provenance, watchID, cfg.generation)` (generation-scoped; a genuine re-arm is fresh).
   - **Gradient depth** = `SelfInfluenceDepth(ev.Provenance, watchID, cfg.generation, "", delivered)`.
   - **Fuse depth** = `SelfInfluenceDepth(ev.Provenance, watchID, "", "", delivered)` (generation-agnostic → re-arm can't reset and evade).

5. **Truncation feeds the inform, not the fuse (revised after stress-testing).** `WatchKeys` never truncate, but `Chain` truncates at 16 (keepHead 8 / keepTail 8). Truncation keeps the most-recent 8 hops — and a *pure* runaway's recent hops are all its own deliveries — so `fuseDepth` still reaches `N` under truncation for the case that matters. The only runaways the count misses are ones *diluted* by other watches' hops; those keep getting **delivered** (so they hit the volume breaker, below). Making `ChainTruncated` a *hard* fuse would instead risk a false-positive — dropping a legit send in a busy multi-sidecar session whose chain merely exceeds 16 hops. So truncation only **sharpens the gradient** (Step 4 renders the pointed line); it never drops.

6. **Fuse: `fuseDepth >= N` only; drop the send, keep the watch armed.** The hook is `recordWatchSend` (`:2287`) — every send funnels through it and it already does a persist-then-drop for unresolvable targets, so a `runaway` drop mirrors it exactly (`dropWatchSend(state, cfg, "runaway")`) and gives the doctor durable telemetry. Dropping breaks the causal chain (no frame → no watcher reaction → no next event), so the runaway branch terminates while the watch stays armed for genuine depth-0 triggers. **Two breakers compose:** the depth fuse (`N=8`) is the early surgical cut for clean recursion; the existing **volume breaker (`watchDeliveryBudget = 50`, auto-clears the watch) is the guaranteed floor** for anything diluted/exotic the count misses. The **no-send notification** path has empty delivery IDs (depth is always 0), so it is bounded by the volume breaker alone — always-notify, volume-capped.

7. **The runtime records breaker telemetry; the doctor reads it (does not re-derive).** Depth is computed once for the fuse + gradient; stamp it on the `WatchSendState` record (`SelfInfluenceDepth`, beside `DiagnosticReason`) so it is durable and structured. A fuse trip is already durable via `EventWatchSendDropped.DiagnosticReason="runaway"`. The doctor reports breaker telemetry by **reading** these facts and stops owning a self-loop rule — a forensic tool observes recorded reality, it does not re-simulate the runtime. (The lone exception, out of scope for v1: a deliberate *audit* that re-derives depth independently to catch a runtime miscount — which must intentionally NOT share the runtime's code.)

---

## 3. Open decisions (recommendations — ratify before/at build)

| # | Decision | Recommendation | Why |
|---|---|---|---|
| D1 | Fuse depth `N` | **8**, const `runawaySelfInfluenceDepth` (count-only) | Volume breaker (50) guarantees termination for anything the count misses, so no truncation hard-fuse. Not config in v1 (YAGNI). |
| D2 | Gradient wording | depth 1: `↳ this turn responded to your last message.` · climbing: `you're ~N exchanges deep responding to your own influence — consider disengaging.` | Design §2.2 verbatim intent; terse→pointed. Final copy in the test. |
| D3 | Fuse escalation | **No** auto-clear-on-repeated-runaway in v1 | Drop-the-delivery already guarantees termination; clearing is the volume breaker's job. Revisit if telemetry shows armed-but-perma-fused watches. |
| D4 | Relax forbid (877) | **Yes, in this change** | Design §5; universal inform+fuse dissolves the reason. Caller-self-delivery becomes tagged+fused like everything else. |
| D5 | Inform on truncated-but-self | Render the **pointed** (max) gradient line | We know it's self-influenced and deep; surface that even when the exact number is lost. |

---

## 4. Build sequence (TDD; each step red → green before the next)

**Step 0 — depth primitive in `provenance` (runtime-only).** `agent/provenance`.
- Test `provenance_test.go`: empty→0; one delivered prior→1; a coalesced-away (not-delivered) hop→0; duplicate delivery_id→1; generation filter (matched vs other gen); foreign watch_id ignored; non-`watch` entries ignored.
- Impl: add `SelfInfluenceDepth(p, watchID, generation, delivered)`. No doctor change here — its `deliverySelfLoop` is *deleted* in Step 6, not refactored.

**Step 1 — delivered set.** `agent/job_watch.go`.
- Test: a delivery_id is reported delivered only after `settleWatchSendDelivered`; unknown id → false.
- Impl: `jm.deliveredWatchSendIDs` map (init in the jobManager constructor — find it near the `pending` map init `:1022`); set under `jm.mu` in `settleWatchSendDelivered`; locked accessor `watchSendDeliveredLocked(id) bool`. Confirm lock discipline at the read sites in Step 3.

**Step 2 — classify self-influence once (the #2 gate helper), carry + stamp it.** `agent/job_watch.go` (+ `record.go`).
- Test: `classifySelfInfluenceLocked(cfg, prov)` returns `{self bool, gradientDepth, fuseDepth int, truncated bool}` for: depth-0 event; one-deep self event; coalesced-inflated event (still counts 1); re-armed (new generation) event (fuse counts, gradient resets); `ChainTruncated && self` sets `truncated`.
- Impl: add `classifySelfInfluenceLocked` — the single decision the four suppression sites will call (reads the delivered set under `jm.mu`), replacing `shouldSuppressWatch`. Carry `gradientDepth`/`self`/`truncated` on `watchSendDelivery`, and stamp `SelfInfluenceDepth` onto the `WatchSendState` record (new field at `record.go:158`) so it is durable for the doctor.

**Step 3 — the policy flip + fuse.** `agent/job_watch.go` (+ `record.go`). Two slices.

**3a — fuse core (unit-testable in isolation).**
- Impl: add `SelfInfluenceDepth int` to `WatchSendState` (`record.go:158`, `omitempty`); add carry-fields `selfInfluence`/`gradientDepth`/`fuseDepth`/`truncated` to `watchSendDelivery`; stamp `state.SelfInfluenceDepth = d.fuseDepth` in `watchSendState` (`:2342`); add `const runawaySelfInfluenceDepth = 8`; in `recordWatchSend` (`:2287`), after the pending persists, mirror the unresolvable-target drop — `if d.fuseDepth >= runawaySelfInfluenceDepth { return …, jm.dropWatchSend(state, d.cfg, "runaway") }`.
- Test: a delivery with `fuseDepth >= 8` → `recordWatchSend` drops it (`EventWatchSendDropped`, `DiagnosticReason="runaway"`, the dropped state carries `SelfInfluenceDepth`, no pending remains); `fuseDepth < 8` → persists pending with `SelfInfluenceDepth` stamped through.

**3b — wire classify in (the flip).** At the four sites (`onSessionEvent:1684`, output_match `:1803`, terminal flush `:2028`, `fireProgressTick:2103`), replace `if shouldSuppressWatch(cfg, prov) { … }`: in the **send** branch compute `c := jm.classifySelfInfluenceLocked(cfg, prov)` and stash `c` onto the built delivery (always append — no suppression); in the **no-send** branch simply stop suppressing (always notify; volume breaker bounds it). Delete `shouldSuppressWatch`; re-point/remove the unit tests asserting old suppression. Update the `session_lifecycle.go:~877` comment ("so a same-watch loop is suppressed").

**Step 4 — the inform line.** `agent/job_watch.go` `buildWatchFrame`.
- Test: frame for depth 0 has no notice; depth 1 has the terse `<system-reminder>` line; climbing depth has the pointed line; truncated-self has the pointed line.
- Impl: thread `gradientDepth`/`self`/`truncated` into `buildWatchFrame` (via the delivery / the synthetic cfg built in `snapshotWatchSendFrame:2236`); add `selfInfluenceNotice(depth int, truncated bool) string` and prepend it.

**Step 5 — relax the forbid.** `agent/job_watch.go` `validateWatchDeliveryLoop:883`.
- Test: a self-delivering `assistant.tool` / `communicate` / wildcard watch is now accepted (no `invalid_request`); the existing rejection tests are inverted/removed.
- Impl: relax `validateWatchDeliveryLoop` to `return nil` (or delete the function + call, per what reads cleanest); confirm no other callers depend on the error.

**Step 6 — re-point `serf-doctor watches --self-loops` to *read* recorded telemetry.** `agent/doctor/watches.go` (+ `cmd/serf-doctor`, tests).
- Test: `--self-loops` reports per-watch **max stamped depth** and **whether the fuse fired** (a `runaway` drop); a merely self-influenced (bounded) watch is **not** flagged; only unbounded/fused is. Update `watches_test.go` and `cmd/serf-doctor/main_test.go` (the `SELF-LOOP` assertion changes meaning).
- Impl: **delete `deliverySelfLoop` and the `SelfLoop` field** — the doctor stops re-deriving. Read the stamped `SelfInfluenceDepth` off the delivered/dropped records and the `runaway` `DiagnosticReason` it already surfaces (watches.go:316). No shared compute with the runtime.

**Step 7 — re-baseline scenarios + docs.** `test/scenarios/`, `docs/skills/doctoring-serf/`.
- Rewrite `job-watch-actually-monty-python-injection.md` and `job-watch-observer-snide-thread.md` to assert **deliver-and-bound** (inform line appears; depth climbs; fuse caps a runaway) instead of **drop**.
- Fix `doctoring-serf` `failure-modes.md`/runbooks that assert "healthy ⇒ zero self-loops" → "self-influence is normal; flag unbounded depth / runaway drops."

**Step 8 — prompt breaker paragraph.** (Find the agent system prompt source.)
- Add one paragraph: a sidecar reads the depth `<system-reminder>` line and is expected to back off / change tack / disengage as it climbs; the machinery hard-stops a runaway at depth N. The broader "how to use sidecar agents + subagents" guide is a **separate** task (its own spec) — not smuggled in here.

---

## 5. Verification

- `make test` (all modules: `.`, `agent`, `llm`, `auth`) + `make lint`, `-race -short` green at each step.
- **Live E2E feel-test** (the design is behavioral): run the rewritten monty-python + snide scenarios against a real provider; confirm (a) the worker now *sees* its echoes with the gradient line, (b) the sidecar disengages as depth climbs, (c) a forced runaway trips the fuse with `runaway` drops and terminates. Unit tests can't judge "does the sidecar actually back off" — that's the E2E's job (the lesson from the launch-time-watch revert).
- `serf-doctor watches --self-loops` on the live session shows the new depth/breaker telemetry (max depth, fuse fired y/n), not a bare loop count.

---

## 6. Out of scope (explicit)

- Config-tunable `N`, fuse-escalation-to-clear, delivered-set pruning — deferred (YAGNI; revisit on telemetry).
- The full sidecar/subagent **usage guide** — separate spec/task (design §6 note).
- Any change to the provenance **data plane** (WatchKeys/Chain/Union/WithWatch) — unchanged; only its *use* flips.
