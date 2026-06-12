# Recursive Subagents Design

Date: 2026-06-12
Status: approved design (Jesse, interactive brainstorm 2026-06-12 morning); awaiting /par + final review. Implementation gated on the job-control e2e matrix going green.
Inputs: the Track K verified-facts dossier (15 framed decisions, file:line-cited; archived in the session task log), `docs/superpowers/specs/2026-06-11-job-control-watch-mailbox-design.md` (the §3 invariant this design assumes everywhere), `docs/job-control.md` (the contract).

## 0. Decisions (made, not open)

Four principles, chosen explicitly:

1. **General recursion, gated by a per-spawn allowance.** Any delegate may delegate, if and only if its parent granted it the capability. The grant is the law.
2. **Visibility chains, attention stays local.** Every job in a subtree is visible (listable/readable/stoppable) to every ancestor; notifications and watch attention never travel more than one hop.
3. **Width is bounded by a tree-wide running cap** counted over *processing* descendants — idle, turn-ended delegates cost nothing. Cap violations fail loudly with remedies.
4. **Built by composing the existing parent-child link**, not by a registry rewrite. Every per-link mechanism (forwarding, terminal_generation dedupe, crash recovery, output-path reads) composes hop-by-hop.

The mailbox invariant (spec §3: observation persists and wakes; only a session's own loop mutates it; no session synchronously mutates another) is non-negotiable at every depth. It is what makes recursion affordable; nothing in this design weakens it.

## 1. The allowance

`delegate` gains one parameter:

- `delegation_allowance` (integer, default **0**): how many further levels of delegation the child may grant. `0` = the child is a leaf subagent — exactly today's behavior. `N > 0` = the child receives the `delegate` and `job_watch` tools and may spawn children with allowance ≤ N−1.

Rules:

- A session may call `delegate` only if its own allowance > 0 (the root always may). A spawn requesting `delegation_allowance ≥ parent's allowance` is rejected at validation: `invalid_request: delegation_allowance must be less than your own allowance (<A>)`.
- The root's allowance comes from config: `MaxSubagentDepth` is renamed in meaning to "the root's allowance" (`SessionConfig`, default 1 — so the root can delegate leaves by default, and recursion is per-spawn opt-in via the parameter).
- The allowance is persisted: it rides `spawnConfig`, the transcript header (next to `Depth`), and the `DelegateRestoreDescriptor`, so resumed delegates keep their capability exactly.
- **The dossier's three independent depth guards collapse into allowance checks**: the hard `depth > 0` error in `prepareSubagentRun`, the `MaxSubagentDepth` check beside it, and the registry stripping in `session_init.go` all key on `allowance == 0` instead. `sendDelegateMessage`'s root-only guard becomes "own direct delegates only" (§3 below). `depth` remains as bookkeeping for prompting and ATIF export gating, but it is no longer load-bearing for capability.
- The child is TOLD its allowance: the prompt renders "You may delegate work onward up to N more levels" (or the leaf-mode limits block when 0), and the parent knows what it granted because it wrote the call. Capability is explicit at spawn time on both sides — never a mid-run discovery. (Motivating incident: a coordinator agent in the development harness discovered mid-run that it could not dispatch. Serf does better, structurally.)

## 2. Visibility chains (composing the link)

Today `forwardEvent` is single-hop and overwrites `VisibleToSession` per hop, so a grandchild's jobs are invisible to the root. The change:

- Forwarded job events **preserve origin identity**: `OwnerSessionID` is never overwritten. Each record additionally carries `ViaChildSessionID` — the direct child it arrived through at THIS level (per-level field, set by each hop's append).
- Each jobManager's `forwardEvent`, after appending to its own store and doing its one-hop notification work, **calls its own `forward`** — the chain ends at the root (whose `forward` is nil). What chains: `job_started`, `job_finished` (visibility). What does NOT chain: `job_notification_pending` (attention — §3).
- Reads compose today: forwarded records carry `OutputPath` into the owner's directory; an ancestor reads a grandchild's retained output directly, including after the owning session is gone.
- `job_stop` on a non-direct descendant routes **hop-by-hop down the Via chain**: each level resolves its direct child and asks it to stop the next link, using the existing single-hop stop machinery; queued per link, never cross-session lock acquisition. A dead link mid-chain yields `not_controllable` with the link named.
- `job_list(include_nested=true)` at any level shows the full subtree beneath it (records are in its own store via the chain). The default filter still hides nested records.
- Dedupe composes: terminal events carry the once-minted `terminal_generation` verbatim through every hop; the dedupe key `{VisibleSessionID, JobID, TerminalGen}` is already per-level.
- Cost: O(depth) appends per job event, bounded by allowance. Accepted.

## 3. Attention stays local

- **Notifications:** `job_notification_pending` forwards exactly one hop, as today. A coordinator handles its own children's completions; the root hears about its direct delegates only. (The root still SEES grandchild records — it is just not interrupted about them.)
- **Watches:** a session may watch only jobs it owns. The contract's `ParentCanWatch` promise for nested jobs is **amended** to match: "watch what you own; to watch deeper, delegate the watching to the coordinator that owns it." This resolves the standing contract-vs-code contradiction (`target_not_watchable`) in the code's favor, with the principle stated.
- **`job_send_message`:** reaches own direct delegates at every level (the root-only restriction lifts to per-level). `caller` means the immediate parent, at every depth.
- **Grants:** unchanged shape, per-link — an observer asks its parent. (A watch owner mints grants into its own store; observers are its direct delegates by construction, since watches are own-jobs-only.)
- **Watch-send tokens:** `ChildSessionID` routing remains parent↔child; no tree-shaped token routing is needed because watches don't cross levels. (Dossier decision #5 dissolves rather than being solved.)

## 4. Governance: the tree-wide running cap

- **Cap: 16 concurrently running descendant delegate jobs per tree** (hard-coded constant, no config knob). Counted at the root over chained records: a delegate's job record is `running` exactly while its session is processing its task and goes terminal when the turn ends — so idle, turn-ended (resumable) delegates do not count, by construction. Shell jobs do not count against this cap (they have their own bounds).
- Enforcement at spawn: the cap is the ROOT's subtree count, so a mid-tree spawner must read it. Alongside `forward`, each jobManager gains a `parentRunningCount func() int` seam wired at spawn (nil at root): each non-root level's closure simply delegates upward; the root answers "running delegate records in my store" once (its chained records already contain the whole tree, so no summing along the walk — that would double-count). One leaf-lock store read at the root, reached in O(depth). The check runs in `prepareSubagentRun` before any child construction.
- **The error is a teaching surface:** `tree_at_capacity: 16 delegate jobs running across this session tree. Wait for completions (you are notified automatically), job_stop work you no longer need, or narrow your fan-out and retry.` Stable error code `tree_at_capacity` joins the contract's synchronous-error vocabulary.
- Deferred (explicitly, with rationale): per-spawn width/token leases — the allowance could become a `{depth, children, tokens}` vector, but that is new surface for agents to reason about before any real tree has run. Revisit after usage data.

## 5. Tool and prompt surface per allowance

- Registry: allowance > 0 children keep `delegate` + `job_watch`; allowance 0 gets today's stripping. All other job tools are already depth-universal.
- Prompt: **one subagent template with conditional sections** (no third template). `subagent.md.tmpl` gains `{{ if .CanDelegate }}` inclusion of the delegation + background-jobs sections; the leaf-mode "Delegated task limits" block renders only at allowance 0. `delegation.md`'s "Only you can call `delegate` and `job_watch`" is reworded to allowance-truthful text; the section gains the one-line allowance statement.
- Agent types: `agentUsesRootOnlySubagentTools` filtering keys on the spawner's grantable allowance, not depth — an agent type that delegates is offerable wherever allowance permits; the available-agents `DefaultTools` summary reflects what the child will actually have, per grant.
- `DefDelegate`'s description documents `delegation_allowance` in the established voice ("0 = the delegate cannot delegate further; N grants it N more levels; you may grant at most one less than your own allowance").

## 6. Close and interrupt

- Children close **in parallel** under one **tree-wide deadline (15s)** instead of today's sequential per-child 5s stacks: each child's close recursively parallel-closes its own subtree within the shared deadline; expirations finalize as today's abandonment path, honestly.
- The never-set `sub.closed`/`closeTimedOut` flags and other dossier residue (`isRootOnlyJobPresenceTool`, the caller-less `spawnAgent`/`sendInput`/`cancelAgent` Session methods) are swept in this campaign.
- Command-tree close (the deadlock note's stage-4 end state) is the recorded evolution, not v1: today's cascade holds no cross-session locks (verified), so the residual risk is latency, which the parallel close + deadline bounds.

## 7. Crash and restore at depth

- Per-link reconciliation is unchanged: each session reconciles jobs it owns; a resumed delegate finalizes its own orphans.
- New rule for chained records: when a session restores and a chained (non-owned) record's `ViaChildSessionID` link is terminal and non-resumable, the restoring session finalizes that record **`supervision_lost`** with its own freshly-minted generation — exactly-once per level via the existing dedupe key. No zombie grandchildren; honest status.

## 8. Contract and documentation amendments

`docs/job-control.md`: availability matrix gains the allowance dimension (root-only → allowance-gated); `delegation_allowance` on the `delegate` schema; `ParentCanWatch` → own-jobs-only with the delegate-the-watching principle; nested-visibility section rewritten for chains (origin-preserving identity, Via routing, one-hop attention); the caps section gains `tree_at_capacity` and the 16 constant; `caller` semantics stated per-depth; `job_send_message` scope per-level. Prompt sections per §5. The architecture doc's "Ownership and mailboxes" section gains a paragraph: the invariant at depth (chains are leaf-lock appends; no new cross-session mutation surface).

## 9. Testing

agenttest trees at depth 3: allowance enforcement at every level (grant > own−1 rejected; leaf cannot delegate; tool surface matches grant); chain visibility (root lists a grandchild, reads its output after the mid dies, stops it down the Via chain); attention locality (root NOT notified of grandchild terminal; mid IS); cap behavior (idle delegates don't count; 17th spawn fails with `tree_at_capacity`; completion frees a slot); parallel close under deadline with a slow grandchild; the crash matrix (kill mid-tree, restore root, `supervision_lost` exactly-once); allowance persistence across delegate resume. E2e: new scenario cards for the coordinator pattern — a root delegating a coordinator that manages two workers, the shape of the 2026-06-11 overnight run, executed inside serf.

## 10. Rollout

Default `delegation_allowance` 0 and the existing config default mean **merging this changes nothing observable** until a spawn explicitly grants. Recursion ships dark; the coordinator-pattern e2e cards become the proof; capability turns on per-delegation, which is the unit Jesse chose.

## Deferred, deliberately

Width/token leases (§4); command-tree close (§6); transcript-ref scoping in trees (machine-local readability stands, per the mailbox spec's v1 posture); notification fan-up beyond one hop (would defeat delegation's purpose; revisit only with evidence); hub tree-view rendering (serf-hub work, reads the same chained stores).
