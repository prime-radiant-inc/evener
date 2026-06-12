# Recursive Subagents Design (recursion-minimal, drive-down)

Date: 2026-06-12 (v3 — §3 rebuilt as drive-down per Jesse's challenge + the second 23-finding /par round; v2's wake-driver §3 and v1's visibility chains are in git history `7a65b6d8` / `b85f9850`)
Status: awaiting Jesse's review. Implementation gated on the job-control e2e matrix going green.
Inputs: the Track K dossier; the mailbox design (`docs/superpowers/specs/2026-06-11-job-control-watch-mailbox-design.md`, §3 invariant assumed everywhere); `docs/job-control.md`; two /par rounds (28 + 23 findings — reviewer line citations preserved below where load-bearing).

## 0. Decisions (made, not open)

1. **General recursion, gated by a per-spawn allowance.** The grant is the law.
2. **Opaque subtrees with on-demand visibility**: each level manages its direct children; ancestors get read-time tree resolution via `job_list` (decision 5), transcript reads by ref, and subtree teardown via their direct child. Event-forwarding chains stay deferred (trigger: evidence that roots need direct grandchild management).
3. **Drive-down delivery** (Jesse, after challenging the wake prohibition): *every session's notifications render in its own turns; a session's parent ensures it gets turns.* Each driven session, at its own loop boundaries, resumes direct children that have undelivered attention. The root is driven by serve.go; the tree is therefore eventually-driven, level by level. The contract's `:981` ("never resumed solely to deliver a notification") is amended — it encoded missing infrastructure, not a principle; the root has been woken-to-deliver since the mailbox design shipped, and the asymmetry was never principled.
4. **Width bounded by a tree-wide running counter** (atomic check-and-reserve; idle costs nothing; loud errors).
5. **`job_list` renders the live descendant tree on demand** (`include_descendants`).

The mailbox invariant — observation persists and wakes; only a session's own loop mutates it; no session synchronously mutates another — holds at every depth. Drive-down is that invariant applied recursively: a parent never mutates a child; it *runs* the child, and the child's own loop delivers.

## 1. The allowance

`delegate` gains `delegation_allowance` (integer, default **0**). 0 = today's leaf subagent exactly. N>0 = the child receives `delegate` + `job_watch`, may grant ≤ N−1 onward, and is told its allowance in its prompt. A grant ≥ the granter's own allowance is rejected: `invalid_request: delegation_allowance must be less than your own allowance (<A>)`.

- Root allowance = config (`MaxSubagentDepth`, default 1). **Double opt-in, stated plainly: under defaults the root may only grant 0 — enabling recursion requires raising the config AND granting per spawn.**
- Persistence: allowance rides `spawnConfig`, the transcript header (beside `Depth`), and the `DelegateRestoreDescriptor`.
- **The capability seam inventory (all become allowance checks):**
  1. the hard `depth > 0` error (`agent/subagents.go:296-298`);
  2. the `MaxSubagentDepth` check beside it (`:299-301`);
  3. registry stripping at child init (`agent/session_init.go:531-535`);
  4. the default subagent tool-policy deny-list (`baseSubagentToolPolicy`, `agent/subagents.go:163-174`, applied at `session_init.go:528-530`);
  5. restored-delegate tool validation (`validateRestoredDelegateRequiredTools`, `agent/job_delegate.go:706-719`) — it deletes root-only names from the **registered set**, so a coordinator type's frozen requirement *fails* validation and the delegate cannot resume (mechanism per /par-A; the sibling child-side check `validateRestoredDelegateTools` `:696-704` self-heals once seam 3 is allowance-aware);
  6. **the agent-type spawn rejection** (`agentUsesRootOnlySubagentTools`, `agent/subagents.go:310-312`) and its prompt-side filters (`agent/session_tools.go:506`, `:529`, summary `:499`) — keyed on grantable allowance.
  Plus: `sendDelegateMessage`'s root-only guard becomes "own direct delegates" (§3), and the grant_tools rejection text (`agent/subagents.go:418-420`) becomes allowance-truthful.
- **Typed-agent rule (decided):** an agent type's tool list governs *what* the child gets; allowance governs *whether* delegation tools are grantable at all. A type listing `delegate` is spawnable only with grantable allowance > 0; allowance never injects tools into a type that doesn't list them; the default (untyped) child with allowance > 0 gets `delegate` + `job_watch` added to today's default surface.

## 2. Visibility and control

- **Delegate jobs join one-hop forwarding** (this kills the /par orphan-record cluster): delegate-job creation stamps `ParentJobID` from the ambient parent job and forwards its `job_started` one hop, exactly as shell creation already does (`agent/jobs.go:443` vs the gap at `agent/job_delegate.go:1150-1172`). Forwarded records therefore always carry owner/type identity; the generic finalize forwarding (`agent/jobs.go:859/874`) stops producing type-less phantoms in the parent's default `job_list`. This is NOT the deferred chains: strictly one hop, the existing mechanism, applied to the job type it inexplicably skipped.
- **`job_list(include_descendants=true)`** walks the live tree at read time: own records, then recurse into each live direct child's jobManager store (leaf-lock reads). **Dedupe rule:** the owner's record is authoritative; a forwarded copy of the same `job_id` in an ancestor store is suppressed from the walk output. Rows carry `owner_session_id` + `depth`. The walk sees the live subtree; a dead coordinator contributes its own terminal record (resume it to dig deeper). Default `job_list` and `include_nested` semantics unchanged.
- **Reads at depth ≥ 2:** the walk records the resolution path; `job_read_output` accepts descendant rows by resolving through the same path (recursive owner resolution — each hop the existing single-hop `ownerJobManagerFor` step). Snapshot-only; `block=true` on a depth ≥ 2 read is rejected like granted reads.
- **Stop:** `job_stop` on a coordinator's delegate job **cascades**: cancel-finalize also stops the coordinator's own running jobs (its workers' delegate jobs and shell jobs), recursively — making the §0.2 "stop the subtree = stop its direct child" guidance true (today it is false: `cancelDelegateSub` cancels only the coordinator's turn, `agent/job_delegate.go:1263-1274`, and workers survive orphaned). `job_stop` on a non-direct descendant row: `not_controllable` with the named coordinator and the cascade guidance. Session Close already cascades and is unchanged.

## 3. Drive-down delivery

**The rule:** notifications render only in their owner's turns. A parent never renders a non-owned job's notification; instead, at its own loop boundaries, it **drives** (resumes) any direct child with undelivered attention.

- **Drive signals** (checked at the same boundaries that drain watch sends today): (a) a forwarded pending in the parent's store for a child-owned job — owner-identifiable now via §2's delegate-start forwarding; (b) the child's jobManager reports pending watch sends or queued notifications (`hasPendingWatchSends` + a queue peek — the parent's drain already iterates child jms; this re-purposes that traversal as signal-reading only).
- **The drive action:** launch the child's own drain loop for one notification turn — the `EntryNotification` path the root runs at serve-wake, via a new launch mode on the existing subagent run machinery. **No delegate job record is minted** (this is the child processing its own queue, not new tasked work); the §4 counter is reserved for the turn's duration; nothing new is notification-armed. If the turn needs to tell the parent something, `job_send_message(caller)` exists and is already legal for children.
- **Settle:** the parent's forwarded pending copy is marked delivered at successful drive **handoff**. Safe because the parent's copy is a *drive signal*, not the delivery ledger — the child's own durable queue (which armed at the original enqueue and re-arms at the child's restore) is the ledger, and the contract's no-loss clause (`:990`) keeps its exact meaning on the owner's copy. Restore re-arm at the parent filters to owned + direct-child-owned records (closing the /par restart-wake-storm).
- **Failure fallback:** child non-resumable (validation failure, closed, descriptor-less) → the parent renders the pending itself, prefixed `child unreachable:` — attention escalates one honest level instead of vanishing. At `tree_at_capacity`, the drive retries at subsequent boundaries (the signal is durable; no retry daemon needed) — and the cap error text drops "you are notified automatically" in favor of "completions free slots; retry" (the /par-caught untruth at saturation).
- **Stop-gating (no resurrection):** a child whose latest delegate record terminated by deliberate stop (`stopped_by_parent` family) is not driven for attention that predates the stop; new work (a fresh send/spawn) clears the gate. Pinned: "latest record for that child session" resolves the one-session-many-records ambiguity.
- **Mid-owner caller sends:** a mid-level watch owner's `send.to="caller"` frames render in the **mid's own** drive turn. The v2-contradicting child-iteration re-route (child caller tokens onto the parent's rail, `agent/job_watch.go:2580-2592`) is **deleted**; the drive signal replaces it. (Root behavior unchanged: root caller sends already render on the root's rail.)
- **`job_send_message`:** own direct delegates at every level; `caller` = immediate parent. Watches: own jobs only. Grants: per-link, unchanged.
- **Contract amendments, named:** `:981` (resume-to-deliver, now parent-loop-driven — the clause this section exists to change), `:1056` (parent-renders → parent-drives for delegate-owned jobs; the parent still renders its OWN jobs' notifications, including its direct delegates' terminals — the root is told when its coordinator finishes, which is its job ending, not noise), `:988-990` (receiver-copy handoff settle), guidance `:40`.

## 4. Governance: the running counter

A `*treeCounter` (atomic, cap **16**) created by the root, handed down through `spawnConfig`.

- Check-and-reserve on every path that creates a running delegate turn: spawn (`prepareSubagentRun`), resume (`attachDelegateJobFromWatch` and siblings), and §3 drive turns. Release on terminal finalize **and on the abandon path** (`abandonRunningJob` — the /par-caught leak).
- Idle (turn-ended) delegates hold no reservation. Restart: root rebuilds from its post-reconciliation state (zero), descendants re-reserve as they re-attach; a detached orphan subtree is uncounted until resumed — accepted v1 looseness, documented.
- Error: `tree_at_capacity: 16 delegate jobs running across this session tree. Wait for completions to free slots, job_stop work you no longer need, or narrow your fan-out and retry.`
- Honesty: shell jobs have **no** concurrency bound anywhere (standing contract gap `:1170-1179`, not closed here). Memory dimension (/par-A): each level retains up to 128 terminal children whose Sessions stay live in memory — `maxRetainedTerminal` is per-level, so deep trees multiply retained live Sessions (~128^depth worst case); v1 acknowledges this as a documented bound-gap alongside shells, with per-level retention reduction noted as the cheap future fix.

## 5. Prompt and tool surface per allowance

One subagent template with conditional sections (`{{ if .CanDelegate }}`): delegation + background-jobs sections for allowance > 0 (texts parameterized; "Only you can call `delegate`" dies); the leaf limits-block at allowance 0; the allowance stated in the child's prompt. Agent-type filtering, `DefaultTools` summaries, and `DefDelegate`'s enum/description key on grantable allowance (§1 seam 6). `DefDelegate` documents `delegation_allowance`; the background-jobs section gains one drive-down sentence ("your delegates handle their own children's completions; you are told when YOUR delegates finish"). Haiku comprehension gate before landing.

## 6. Close and residue

Close is unchanged (sequential recursive cascade; correct; latency acceptable until e2e proves otherwise). Residue sweep: never-set `sub.closed`/`closeTimedOut`, caller-less `isRootOnlyJobPresenceTool`, legacy `spawnAgent`/`sendInput`/`cancelAgent`.

## 7. Crash and restore

Per-link reconciliation, unchanged: each session reconciles jobs it owns; a mid's subtree reconciles when the mid is next driven or resumed — and under §3 the mid IS driven by its parent when undelivered attention exists, so post-restore catch-up is now systematic rather than lazy-on-explicit-resume. No new statuses, no chained-record disposition (nothing chains).

## 8. Contract amendments

Availability matrix (allowance-gated, six seams); `delegation_allowance` schema + double opt-in; delegate-start forwarding + the dedupe rule; `include_descendants` + depth-read resolution + stop-cascade and its guidance error; drive-down (`:981`, `:1056`, `:988-990`, `:40`); `ParentCanWatch` → own-jobs + delegate-the-watching; caps (`tree_at_capacity`, counter semantics, shell + retention gaps acknowledged); `caller`/`job_send_message` per-level scope.

## 9. Testing

Depth-3 agenttest trees: allowance enforcement per level; **the deaf-coordinator regression, drive-down form** (coordinator backgrounds workers, ends turn; workers finish; the parent drives the coordinator; the coordinator's model gets the notification turn; the root's model is told only when the coordinator itself finishes — red against today); drive at depth 3 with an idle middle (root drives mid; mid, once driven, drives its child at its own boundary); handoff settle + crash-between-handoff-and-render (child's ledger re-arms; nothing lost; no restart wake-storm); stop-cascade (workers actually stop; no resurrection — gated drive); fallback render for a non-resumable child; counter (reserve on spawn/resume/drive, release on finalize AND abandon, idle frees, 17th fails, restart rebuild); live-walk (annotations, dedupe, depth-2 read resolution, live-only limit); coordinator-type delegate resume (seam 5 — red today); mid-owner caller frames render mid-side; allowance persistence across resume. E2e: coordinator-pattern cards (the 2026-06-11 overnight shape inside serf) with the raised config.

## 10. Rollout (honest)

At merge, with no grants: the counter binds existing single-level fan-out (16 concurrent root delegates fail loudly — disclosed); delegate-start forwarding changes what nested stores contain (invisible to the default list, but stated); drive-down changes the existing nested-shell case — the owner is driven instead of the parent's model being interrupted about a job it didn't create (the parent still gets its own delegate's terminal). Recursion stays dark behind the double opt-in.

## Deferred, deliberately

Visibility chains (trigger named); parallel close; width/token leases; transcript-ref scoping; per-level retention tuning; persistent always-driven children (drive-down is the stepping stone: a persistent child is one whose parent drives it continuously).
