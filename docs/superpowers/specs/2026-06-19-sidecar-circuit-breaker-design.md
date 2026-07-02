# Sidecar circuit breaker — design (replaces echo suppression)

Date: 2026-06-19
Status: design spec, ready to build. Branch `wip/sidecar-circuit-breaker` (on main; the doctor tooling is landed on main).
Supersedes the *policy* of the observer-watch causal-provenance feature (now merged to main): the causal-provenance **data plane stays**; its **echo-suppression policy is replaced** by inform + breaker.

---

## 1. The decision

Echo suppression — silently dropping, at the watch machinery, any event that descends causally from a prior delivery of the same watch — is **wrong-layered**. It blinds the sidecar to exactly the events it most wants: the worker reacting to the sidecar's own nudges. "Never see what you caused" throws away the signal that makes an observer useful.

**Replace it with: always deliver, inform the sidecar, let the sidecar self-regulate — with a hard runaway fuse behind it.** No per-watch mode; this is the behavior for all watches. Conceptually simpler than a suppression predicate, and it puts the policy where the judgment is.

The causal-provenance data plane (`provenance.Causal` = the deduped `WatchKeys` set + the diagnostic `Chain`; `WithWatch` stamping; the active-provenance turn lifecycle) is correct and **unchanged**. Only the *use* of it flips: from "suppress" to "annotate + bound."

---

## 2. The model

1. **Always deliver.** Remove the suppression skip — `onSessionEvent` (`agent/job_watch.go:1651`, `if shouldSuppressWatch(cfg, ev.Provenance) { continue }`) no longer drops the delivery.
2. **Inform on self-influence.** When a delivery's triggering event already carries this watch's key (`provenance.ContainsWatch(ev.Provenance, watchID, generation)` — the predicate that used to suppress), inject a **one English line** into the delivered frame via serf's standard `<system-reminder>`-style notification helper. The line carries a **depth gradient**, not a flat flag:
   - depth 1 (direct): terse — *"↳ this turn responded to your last message."*
   - climbing: pointed — *"you're ~N exchanges deep responding to your own influence; consider disengaging."*
   The gradient is the breaker input; a flat "influenced by you" would land on nearly every frame (reacting-to-the-worker-reacting-to-you is the *normal* case) and give the sidecar nothing to act on.
3. **The sidecar owns the policy.** It reads the depth line and decides — not "never respond to my cause," but "respond, and back off / change tack / disengage as the loop tightens." This is the circuit breaker; it lives in the sidecar's judgment (and its persona prompt, §6).
4. **Hard fuse floor.** Because the sidecar is an LLM and won't reliably stop, the machinery keeps a hard cap: at **depth ≥ N**, stop delivering and record a drop with `DiagnosticReason = "runaway"`. The sidecar is the breaker; the fuse is the floor that guarantees termination and bounds token/cost.

---

## 3. The depth metric — coalescing-aware (this is the bug we just fixed)

Depth must be **the count of distinct *delivered* prior deliveries of this watch in the lineage** — NOT raw `Chain` hop count. Coalescing unions provenance by design (`agent/job_watch.go:~2730`, `state.Provenance = provenance.Union(existing.Provenance, state.Provenance)`, "the coalesced frame stands in for both the superseded delivery and this one"), so a survivor's `Chain` carries superseded same-slot predecessors that never independently delivered. Counting raw hops trips the fuse on a false runaway.

This is exactly the correction shipped in serf-doctor (`fix(doctor): self-loop verdict must not flag coalesced-away predecessors`, commit `8990c8da`): a prior `Chain` hop counts only when its `delivery_id` actually delivered. **Reuse that logic** for the depth metric.

**Two keyings, two purposes:**
- The **inform / self-detection** keys on `(watch_id, generation)` (`ContainsWatch`) — a genuine re-arm (new generation) is genuinely fresh and resets the inform.
- The **runaway fuse** counts distinct delivered priors **by `watch_id` only** (ignore generation), so a pathological "re-arm every delivery" watch can't reset its depth and evade the fuse.

---

## 4. Cross-sidecar / multi-hop cycles — handled by the per-watch mechanism, no special case

In any cycle `W1 → W2 → … → Wk → W1` (each watch's delivery influences the next session, whose event triggers the next watch), each watch's key is added to the provenance and **rides around the loop**:

- After one lap, the event arriving back at `W1` carries `W1`'s own key → `ContainsWatch(event, W1)` is true → **W1 self-detects and is informed.** Each `Wi` self-detects once its key completes a lap (lag ≈ cycle length, never infinite).
- Each watch's depth (its own delivered priors in the lineage) grows once per lap → **its fuse trips after N laps → the cycle terminates.**

So the per-watch inform+fuse *composes* to arbitrary cycle lengths with no cross-sidecar special case — an argument *for* the per-watch design. (Caveat already covered by §3's `watch_id`-only fuse: a re-arm-every-lap watch is the only evasion, and the `watch_id`-only fuse closes it.)

---

## 5. Relax the hard-forbid (line 931)

`agent/job_watch.go:931` currently rejects watching `assistant.tool` / `assistant.message` / `communicate` with delivery **back to the caller** ("each delivery causes the next event — a feedback loop; use an observer delegate"). Universal inform+breaker dissolves that reason — the loop is now bounded by the fuse and surfaced by the inform. **Relax it:** caller-self-delivery becomes allowed, tagged and fused like everything else. Keeping a special-case forbid contradicts "conceptually simpler."

---

## 6. Consequences (must land with or right after the policy flip)

- **serf-doctor's self-loop concept inverts.** Today the doctor's invariant is "healthy ⇒ zero self-loop deliveries." Under inform+breaker, self-influenced deliveries are *normal and expected*. Re-point `serf-doctor watches --self-loops` from "find a bug" to **depth & breaker telemetry**: report per-watch max depth, whether the fuse fired (`runaway` drops), and flag only *unbounded* depth / fuse trips. The coalescing-aware depth logic is already in `agent/doctor/watches.go`.
- **Re-baseline the suppression-era e2e scenarios.** `test/scenarios/job-watch-actually-monty-python-injection.md` and `job-watch-observer-snide-thread.md` were written to prove echoes get **dropped**; they now prove echoes get **delivered-and-bounded** (the inform line appears; depth climbs; the fuse caps a runaway). Rewrite the cards + assertions.
- **Prompt docs.** Add a **breaker paragraph** to the agent system prompt: how a sidecar reads the depth line and is expected to disengage as it climbs. The broader **"how to use sidecar agents and subagents"** guide is a separate, meatier follow-up task (its own spec), not smuggled into this change.

---

## 7. Build plan

1. **Policy flip** (the core): in `onSessionEvent`, stop dropping on `ContainsWatch`; instead compute the coalescing-aware depth (§3), inject the depth-gradient `<system-reminder>` line (§2.2) on self-influenced deliveries, and apply the hard fuse at depth N (§2.4, drop with reason `runaway`). Relax line 931 (§5). Pick N (start ~16? reuse `maxDiagnosticChain` or a dedicated config — decide at build).
2. **Re-baseline + re-point** (§6): rewrite the two e2e scenario cards; re-point `serf-doctor watches --self-loops` to depth/breaker telemetry; update the doctoring-serf `failure-modes.md`/runbooks where they assert "zero self-loops."
3. **Prompt** (§6): the breaker paragraph now; the sidecar/subagent usage guide as its own task.

---

## 8. Verified code anchors (for the implementer)

- Suppression call site: `agent/job_watch.go:1651` (`onSessionEvent`); predicate `shouldSuppressWatch` at `:1691` → `provenance.ContainsWatch`.
- Delivery stamp: `watchSendSnapshot` at `:951`, `provenance.WithWatch(root.Provenance, …)` at `:970` (same `ev.Provenance` the suppression checks).
- Coalescing union: `:~2730` (`recordWatchSendPending`, `provenance.Union(existing.Provenance, state.Provenance)`, `CoalescedCount++`).
- Notification-turn provenance adoption: `agent/session_lifecycle.go:~877` (`replaceActiveProvenance(notificationProvenance)`, "so a same-watch loop is suppressed" — comment to update).
- Active-provenance lifecycle: `agent/session_provenance.go` (`activeCausalProvenance`/`replace`/`union`; events stamped with active provenance).
- Hard-forbid to relax: `agent/job_watch.go:931`.
- Provenance types: `agent/provenance/provenance.go` (`Causal{WatchKeys, Chain, ChainTruncated}`, `ContainsWatch`, `WithWatch`, `maxDiagnosticChain=16`).
- Coalescing-aware depth reference impl: `agent/doctor/watches.go` `deliverySelfLoop` (commit `8990c8da`).
