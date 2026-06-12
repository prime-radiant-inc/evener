# Recursive Subagents Design (recursion-minimal)

Date: 2026-06-12 (v2 — descoped per Jesse + 28 /par findings folded; v1 with visibility chains is in git history `b85f9850`)
Status: awaiting Jesse's review. Implementation gated on the job-control e2e matrix going green.
Inputs: the Track K dossier; `docs/superpowers/specs/2026-06-11-job-control-watch-mailbox-design.md` (the §3 invariant, assumed everywhere); `docs/job-control.md`; two adversarial reviews (A: 16 findings, B: 12 — the deaf-coordinator hole, the five-seam guard inventory, the resume-path cap bypass, and the defaults arithmetic all came from them).

## 0. Decisions (made, not open)

1. **General recursion, gated by a per-spawn allowance.** The grant is the law.
2. **Opaque subtrees with on-demand visibility** (descoped from event chains, Jesse 2026-06-12): each level *manages* only its direct children; ancestors get **read-time tree resolution** via `job_list` (decision 5), transcript reads by ref, and stop-the-coordinator. Event-forwarding chains are the recorded evolution, triggered by evidence that roots need *direct* grandchild management rather than management through coordinators.
3. **Attention stays local — and coordinators must actually hear their children** (the /par-critical fix): notifications about a job reach exactly its owner's model; an idle owner is woken, not bypassed.
4. **Width bounded by a tree-wide running counter** (atomic, check-and-reserve; idle costs nothing; loud errors).
5. **`job_list` can render the live descendant tree on demand** (Jesse's nuance): inefficient is fine; it is a supervision query, priced when asked.

The mailbox invariant — observation persists and wakes; only a session's own loop mutates it; no session synchronously mutates another — is non-negotiable at every depth.

## 1. The allowance

`delegate` gains `delegation_allowance` (integer, default **0**). 0 = today's leaf subagent exactly. N>0 = the child receives `delegate` + `job_watch`, may grant ≤ N−1 onward, and is told its allowance in its prompt. A spawn requesting allowance ≥ the granter's own is rejected: `invalid_request: delegation_allowance must be less than your own allowance (<A>)`.

- Root allowance = config (`MaxSubagentDepth`, default 1). **Defaults arithmetic stated plainly (/par A#9, B#8): under defaults the root may only grant 0 — enabling recursion requires BOTH raising the config and granting per spawn.** That is the intended double opt-in; §10 repeats it.
- Persistence: allowance rides `spawnConfig`, the transcript header (beside `Depth`), and the `DelegateRestoreDescriptor` — resumed delegates keep their capability exactly.
- **The guard inventory is FIVE seams, not three** (/par A#6, B#6 — implementer-trap gold). All collapse to allowance checks:
  1. the hard `depth > 0` error (`agent/subagents.go:296-298`),
  2. the `MaxSubagentDepth` check beside it (`:299-301`),
  3. registry stripping at child init (`agent/session_init.go:531-535`),
  4. **the default subagent tool-policy deny-list** (`baseSubagentToolPolicy`, `agent/subagents.go:163-174` — applied independently at `session_init.go:528-530`; without this an allowance>0 child still loses the tools),
  5. **restored-delegate tool validation** (`validateRestoredDelegateRequiredTools`, `agent/job_delegate.go:706-719` — unconditionally deletes root-only names from frozen requirements; without this a coordinator-type delegate **fails to resume**).
  Plus: `sendDelegateMessage`'s root-only guard becomes "own direct delegates" (§3), and the grant_tools rejection text ("top-level only", `agent/subagents.go:418-420`) becomes allowance-truthful. `depth` survives for prompting/ATIF bookkeeping only.

## 2. Visibility: live-walk `job_list`

No event chaining. `job_list` gains `include_descendants` (boolean, default false): at read time, walk the live tree — own records, then for each live direct child in `s.subagents`, recurse into its jobManager's store (jobstore is Session-free with its own locking; leaf-lock reads only), annotating each row with `owner_session_id` and `depth`. O(tree) reads on demand.

- Honest limit, documented in the tool description: the walk sees the **live** subtree. A dead coordinator contributes its own terminal record; resume it to dig deeper (its store remains on disk for the hub, which assembles tree views by walking session stores directly — serf-hub work, no agent-core dependency).
- The default `job_list` and `include_nested` behaviors are unchanged. Delegate-job records gain a `ParentJobID` stamp at creation (today only shell jobs stamp it — /par A#1; needed so descendant rows annotate consistently and the existing nested filter treats delegate jobs uniformly). No forwarding change.
- `job_read_output` on a descendant row: works for records the walk surfaced via the owner's store (read-only, same leaf-lock path). `job_stop` on a non-direct descendant: rejected with guidance — `not_controllable: job %q is owned by descendant session %q; stop its coordinator or job_send_message it` (management through coordinators is the model; stop-the-subtree = stop its root, which cascades today).

## 3. Attention local + the wake driver

**The /par-critical fix (A#2, B#1): today an idle delegate is deaf** — its notification queue has no driver (`notifyFunc` is server-wired for the root only), while the one-hop `job_notification_pending` forward interrupts the *parent's* model about jobs the parent doesn't own (A#4/B#4: two levels get interrupted today; the wrong one has the loop). Both fixed by one re-routing:

- A forwarded `job_notification_pending` for a job the receiver does NOT own stops rendering to the receiver's model. It becomes a **wake request**: at the receiver's next loop boundary (or idle wake), it **resumes the owning child for a notification turn** — the same queued, owner-loop-executed shape as every other delivery in the mailbox design. The child's model handles its own children's completions; the parent's model hears only about jobs it owns.
- **Wake propagation:** if the receiver is itself an undriven idle delegate, its jobManager forwards the wake request one more hop — the request climbs to the first driven session (ultimately the served root), which walks it back down by resuming one level. O(depth) hops, queue-appends and kicks only, invariant-clean.
- **Per-level drain ownership** (/par A#5, B#5): `drainPendingWatchSends` stops *executing* a child's delegate-targeted pendings with the parent's `sendDelegateMessage` (which the per-level scope rule would reject). The parent's drain handles only its own rail; child-held pendings are revived via the wake driver and delivered on the owner's loop. The existing child-iteration survives solely for caller-token re-routing (the restore corner), which already routes by `ChildSessionID`.
- Watches: own jobs only (unchanged from v1; the contract's `ParentCanWatch` is amended to "watch what you own; delegate watching"). `job_send_message`: own direct delegates; `caller` = immediate parent. `send.to="caller"` for a mid-level watch owner resolves to that owner's own rail — pinned explicitly (B#5).
- Grants: per-link, unchanged (observers are direct delegates of the watch owner by construction).

## 4. Governance: the running counter

A `*treeCounter` (atomic count + cap **16**) is created by the root and handed down through `spawnConfig` — every level shares the pointer.

- **Check-and-reserve, not read-then-act** (/par A#13, B#7): spawn and resume paths atomically increment-or-reject; a delegate's terminal finalize decrements. Idle (turn-ended) delegates hold no reservation, exactly the chosen semantics.
- **Resume paths are gated too** (/par A#7): `attachDelegateJobFromWatch` and every resume that creates a running delegate record reserves the same counter — not just `prepareSubagentRun`.
- The error teaches: `tree_at_capacity: 16 delegate jobs running across this session tree. Wait for completions (you are notified automatically), job_stop work you no longer need, or narrow your fan-out and retry.` Stable code `tree_at_capacity` joins the contract's synchronous-error vocabulary.
- Restart: the root rebuilds the counter at restore from its own running records; resumed descendants re-reserve as they re-attach. A detached orphan subtree is uncounted until resumed — accepted v1 looseness, documented.
- Honesty (/par A#14, A#10): shell jobs are NOT covered by this cap and currently have **no** concurrency bound anywhere — a standing contract gap (`docs/job-control.md:1170-1179`) this design does not close. And the counter binds *existing* single-level fan-out the moment it merges (a 17th concurrent root delegate fails loudly today-shaped) — see §10.

## 5. Prompt and tool surface per allowance

One subagent template with conditional sections: `{{ if .CanDelegate }}` includes the delegation + background-jobs sections (texts parameterized; "Only you can call `delegate`" dies); the leaf-mode limits block renders at allowance 0; the child's allowance is stated in its prompt. Agent-type filtering, the available-agents `DefaultTools` summary, and `DefDelegate`'s enum/description key on grantable allowance — a parent sees at spawn time what its child will be able to do. `DefDelegate` documents `delegation_allowance` in the established voice. Haiku comprehension gate before landing, as always.

## 6. Close and residue

Close is **unchanged**: today's sequential recursive cascade is correct (no cross-session lock acquisition, dossier-verified) and its latency is acceptable until e2e proves otherwise — parallel close was cut with the chains (/par A#16's deadline-vs-store hazard dies with it). The residue sweep stays: never-set `sub.closed`/`closeTimedOut`, caller-less `isRootOnlyJobPresenceTool`, legacy `spawnAgent`/`sendInput`/`cancelAgent`.

## 7. Crash and restore

**Per-link reconciliation, unchanged and now sufficient**: with no chained records there is nothing new to lie. Each session reconciles jobs it owns (`stopped/runtime_lost` exactly-once per the existing contract clause); a mid's subtree records reconcile when the mid is resumed — same lazy rule as today, stated as the designed behavior rather than a gap. The v1 `supervision_lost` machinery is deleted with the chains (/par A#3/A#4/A#11/A#12 die here).

## 8. Contract amendments (`docs/job-control.md`)

Availability matrix: root-only → allowance-gated (the five-seam collapse). `delegate` schema: `delegation_allowance` + the double-opt-in note. `ParentCanWatch` → own-jobs + delegate-the-watching. Notification routing: owner's model only; the wake-request behavior for forwarded pendings (amends the current "wake the owning session"-adjacent text and the one-hop description). `job_list`: `include_descendants` live-walk + its live-only limit. `job_stop` descendant guidance error. Caps section: `tree_at_capacity`, the counter semantics, the shell-bound gap acknowledged. `caller` and `job_send_message` scope per level. Status matrix untouched (no new statuses — §7).

## 9. Testing

Depth-3 agenttest trees: allowance enforcement per level incl. the ≥-own rejection; **the deaf-coordinator regression** (coordinator backgrounds workers, ends turn; workers finish; the coordinator is woken, ITS model gets the notification turn, the root's model does NOT — the headline test, red against today); wake propagation with an idle middle; live-walk `job_list` (annotations, live-only limit, default unchanged); counter (reserve on spawn AND resume, idle frees, 17th fails with `tree_at_capacity`, restart rebuild); coordinator-type delegate **resume** (guard seam 5 — red today); per-level send/stop scoping errors; allowance persistence across resume. E2e: coordinator-pattern cards (root → coordinator → two workers — the 2026-06-11 overnight shape inside serf), run with the raised config.

## 10. Rollout (honest)

Two things change at merge even with no grants (/par A#10): the running counter binds existing single-level fan-out (16 concurrent root delegates — generous against today's observed usage, but a behavior change, stated), and notification re-routing applies wherever a forwarded pending lands (today that path only fires for nested shell jobs' owners — behavior improves: the owner's model is woken instead of the parent's being interrupted). Recursion itself stays dark behind the double opt-in: raise `MaxSubagentDepth`, then grant per spawn.

## Deferred, deliberately

Event-forwarding visibility chains (evolution; trigger: evidence of roots needing direct grandchild management); parallel close; width/token leases; transcript-ref scoping in trees; notification fan-up beyond the wake request; hub tree rendering (store-walking, serf-hub side).
