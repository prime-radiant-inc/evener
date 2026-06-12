# Recursive Subagents — Verified-Facts Dossier (Track K)

Date: 2026-06-12. Produced by a read-only research agent at `d851bd10` (job-control-spec) as the brainstorm input for `docs/superpowers/specs/2026-06-12-recursive-subagents-design.md`. Line citations are to that ref and drift with later commits — verify before relying. The working tree at capture time carried the in-flight Phase 4 T1 edits; those are marked **[working tree]** and have since landed (`cae404ce`..`e2d2a0c2`).

> Handoff note: the spec went through three versions (v1 chains `b85f9850`, v2 wake-driver `7a65b6d8`, v3 drive-down `81971c63`) and two /par rounds (28 + 23 findings) after this dossier; the dossier's facts remain the ground truth the spec builds on, but its §10 open questions are now ANSWERED by the spec — read it for evidence, not for decisions.

## 1. Depth machinery today

**The depth field.**
- `Session.depth int` — `agent/session.go:108`; guarded by `s.mu` (lock comment `agent/session.go:62-66`). Set from `cfg.spawn.depth` at `agent/session_init.go:104` (NewSession) and `:299` (Restore).
- `spawnConfig.depth` — `agent/session_config.go:214-215`. The struct doc (`:185-189`) warns: spawn fields are `json:"-"`, never persisted; restored sessions reconstruct parent linkage from the transcript header, **not** this struct — "do NOT … repopulate these on restore, or restored subagents would gain a non-zero depth and break ATIF root-export gating and the subagent-management-is-top-level guards." Restore re-injects spawn only when `restoreCfg.spawn.parentSessionID != ""` (`agent/session_init.go:252-254`), i.e. only the delegate-resume path (`agent/job_delegate.go:563-576` sets `depth: s.depth + 1`).
- Persisted depth: transcript `Header.Depth` (`agent/session_init.go:178`, `:390`).
- Incremented exactly one place for live spawns: `subCfg.spawn.depth = depth + 1` (`agent/subagents.go:349`).

**Depth reads / root-only enforcement (every consumer found):**

| Site | Effect |
|---|---|
| `agent/subagents.go:292-301` (`prepareSubagentRun`) | Two **independent** guards: `depth > 0` → error "subagent management is top-level only" (`:296-298`); `depth >= maxDepth` → "subagent depth limit reached" (`:299-301`). With `MaxSubagentDepth` default 1 they coincide, but the first is depth-keyed, not knob-keyed — raising the knob alone does not enable recursion. |
| `agent/job_delegate.go:249-254` (`sendDelegateMessage`) | `depth > 0` → "not_controllable: concrete delegate job targets are root-only". The `caller` alias is handled **before** this check (`:214-244`), so children can message their caller at any depth. |
| `agent/session_init.go:531-535` | `if s.depth > 0` → `s.reg.Remove(name)` for each of `rootOnlySubagentTools()`. Registry-level enforcement. |
| `agent/session_init.go:474` | `SystemPromptFile` override honored only at depth 0. |
| `agent/session_prompts.go:154-157` | Template selection: `templateName = "system"`; `if s.depth > 0 { templateName = "subagent" }` — **binary**: depth 1 and depth 5 render identical prompts. |
| `agent/session_lifecycle.go:154` | ATIF export only when `spawn.depth == 0`. |

**The root-only tool lists and all consumers.**
- `rootOnlyJobControlTools = []string{"delegate", "job_watch"}` — `agent/session_tools_jobs.go:43`.
- `rootOnlyJobPresenceTools` — `agent/subagents.go:53` (identical content, separate list).
- Union: `rootOnlySubagentTools()` — `agent/subagents.go:138-140`. Consumers: registry removal at depth>0 (`agent/session_init.go:531-535`); `isRootOnlySubagentTool` (`agent/subagents.go:146-148`) → grant rejection "cannot grant tool %q: root-only tools are top-level only" (`:418-419`) and `agentUsesRootOnlySubagentTools` (`:154-161`) → agent-type rejection "agent_type %q is top-level only" (`:310-312`), agent-type filtering out of `delegate`'s enum (`agent/session_tools.go:526-536`) and the prompt's available-agents list (`:503-509`); `removeRootOnlySubagentTools` (`:150-152`) → `defaultToolSummaryForAgent` (`agent/session_tools.go:489-501`); `baseSubagentToolPolicy` (`agent/subagents.go:163-174`): the default deny-list for unnamed subagents is exactly `rootOnlySubagentTools()`; `validateRestoredDelegateRequiredTools` (`agent/job_delegate.go:706-719`).
- `isRootOnlyJobPresenceTool` (`agent/subagents.go:142-144`) has **no non-test caller** — residue.

**Prompt-section gating.** Whole-template swap, not per-section conditionals: `system.md.tmpl` (root) includes delegation, background-jobs, git-safety, security, task-tracking, skills, available-agents, project-docs, etc.; `subagent.md.tmpl` (any depth>0) includes an inline "Delegated task limits" block and omits all of those. `delegation.md` opens: "Only you can call `delegate` and `job_watch`."

**Depth limit knob.** `MaxSubagentDepth` (`agent/session_config.go:37-39`), default 1 (`:240-242`), persisted in `schema.ConfigSnapshot` (`:262`, `:297`). Children inherit it wholesale via `subCfg := s.cfg` (`agent/subagents.go:340-342`).

## 2. Spawn seams

**`spawnConfig` (full field set), `agent/session_config.go:190-228`:** `parentSessionID`, `parentToolCallID`, `parentJobID`, `forwardJobEvent func(jobstore.Event) error`, `parentSteer func(string)` (= parent's `Steer`, queue append, `agent/session_queue.go:58-60`), `parentSteerDelivered func(string) bool` (= parent's `trySteer`), `subagentTask`, `depth`, `sharedTaskStore`, `rolePromptOverride`, `activatedSkillBodies`, `allowedToolNames`, `deniedToolNames`, `communicateOutputSchema`. **[working tree]** Phase 4 T2 added `parentGrantedJobRead`.

**Population sites.** Fresh spawn: `prepareSubagentRun` (`agent/subagents.go:340-367`); `parentJobID` + `forwardJobEvent` set **only** when `ctxParentJobID` is in ctx (`:360-364`) — the delegate path (`createDelegate` plants it, `agent/job_delegate.go:140-141`); `forwardJobEvent = s.jobManager.forwardEvent` (the **parent's** jm method). Terminal-delegate resume: `restoreTerminalDelegateChildClaimed` builds `RestoreSessionConfig.spawn` (`agent/job_delegate.go:563-576`), `depth: s.depth + 1`, frozen role/skills/tools from the `DelegateRestoreDescriptor`. MCP stripped for children (`agent/subagents.go:343-344`); delegate `MaxTurns` defaults 500 (`:369-372`).

**The subagents registry.** `s.subagents *subagentManager` (`agent/session.go:109`); type `agent/subagent_manager.go:23-36`. Lock order: manager mu OUTER, per-child `sub.mu` INNER, never held across `sub.sess.Close()` (`:19-22`). `maxRetainedTerminal = 128` per parent (`:39`); **running children never count toward that cap** (`countsTowardCap` `:206-208`). Children closed by parent's `Session.close` (`agent/session_lifecycle.go:114-116`) or evicted-on-spawn. `sub.closed`/`closeTimedOut` are **never set true in production** (only reset, `agent/subagents.go:636`) — residue still read by `ownerJobManagerFor` and retention.

**Identity lineage.** `encodeRef("", sessionID)` → `"local:<id>"` (`agent/transcript_ref.go:10-44`). Delegate job `TranscriptRef = encodeRef("", childID)` (`agent/job_delegate.go:1112`); resume mints a **fresh job_id** for the same child session (`attachDelegateJobFromWatch` `:1098-1100`) and repoints via `relinkDelegateChildToJob` (`:933-941`). `DelegateRestoreDescriptor` (`:1178-1223`) persists the respawn config incl. frozen role prompt, tools, skills, schema.

## 3. Nested-job forwarding (P5) — and depth 2

`ownerJobManagerFor` (`agent/jobs_nested.go:11-44`): **direct children only**. `nestedOrLocalJobManager` (`:46-67`) routes to live owner else local+forwarded record. `stopNestedOrLocal` (`:69-87`): dead owner + non-terminal → `not_controllable`. `stopChildren` (`:89-115`) iterates the local store for `ParentJobID == delegateJobID` — one generation.

`jm.forward` wired at construction (`agent/session_init.go:126-127`, restore `:333-334`) = parent jm's `forwardEvent` (`agent/jobs_nested.go:242-260`): stamps `VisibleToSession`, appends to the parent store, enqueues only for deliverable `EventJobNotificationPending`. Forwarded kinds: shell `EventJobStarted` (`agent/jobs.go:443-453`; deferred-promotion path `agent/job_shell.go:406-466`), `EventJobFinished` (`forwardFinishedJob`, `agent/jobs.go:867-883`), `EventJobNotificationPending` (`:1060-1076`). **Delegate-job creation forwards NOTHING and stamps no ParentJobID** (`agent/job_delegate.go:1110-1176`) — the /par rounds proved this is the root of the orphan-record cluster; v3 §2 fixes it. `terminal_generation` minted once at finalize (`agent/jobs.go:823`); dedupe key `{VisibleSessionID, JobID, TerminalGen}` (`agent/internal/jobstore/notify.go:12-26`); fold drops gen-mismatched notification events (`fold.go:140-156`). Crash recovery re-forwards owner-matching terminal records (`recoverForwardedTerminalEvents`, `agent/jobs_nested.go:135-193`).

**Forwarding is single-hop by construction**: `forwardEvent` never calls the receiver's own forward; `VisibleToSession` is overwritten per hop; the root never holds grandchild records; `reconcileLostJobs` skips non-owned records (`agent/jobs.go:674-679`); orphaned forwarded records finalize only when the owning child is resumed (`agent/session_init.go:737-758`).

## 4. The drain at depth

`drainPendingWatchSends` drains self + **exactly one level** of children (`agent/job_watch.go@d851bd10:2350-2395`); child caller pendings re-token onto the parent's rail; delegate-targeted pendings deliver via the **parent's** `sendDelegateMessage`. `watchSendToken.ChildSessionID` is one-level routing (`:204-243`). A child jm's `wake` = its own session's `notify` — a **no-op** unless `SetNotifyFunc` was wired, which only serve.go does for the served root (`cmd/serf/serve.go:304`; `agent/session.go:288-295`). Running delegates drain themselves at their own boundaries while processing (`agent/session_tool_round.go`, `agent/session_state.go:113-126`); **idle children drain nothing**. Idle-root zero-turn delivery: wake → `SubmitNotification` → `EntryNotification` → `acceptNotificationInput` (`agent/session_lifecycle.go:821-873`).

## 5. Watches and grants across trees

Concrete watch targets must be **locally owned**: child-owned records rejected `target_not_watchable` (`job_watch.go@d851bd10:567-568`; commit `ecafb94f`: triggers have only local plumbing) — contradicting the contract's ParentCanWatch flowchart (`docs/job-control.md:1085`, rule `:1054`) at dossier time; v3 amends the contract to own-jobs-only. Send-target validation accepts forwarded delegate records that delivery later can't reach (`:646-658` vs registry-local child lookup). Grants: jobstore kinds committed (`EventWatchReadGrant`, `FoldGrants`, `Store.LoadGrants`); **[working tree, since landed]** minting at `configureWatch` (fail-loud) + per-fire (diagnostic-and-proceed), observer resolved to **child session id** via transcript_ref (stable across resume; job_ids are not). Transcript tools (`read_session_transcript` etc.) registered for every session with a StateDir (`agent/session_tool_registry.go:235-244`); refs are machine-local, no per-ref authorization.

## 6. Close / interrupt tree

`Session.Close` (`agent/session_lifecycle.go:72-182`): locks taken then released BEFORE child closes; `drainForClose` under locks, children closed outside them; jm `closeRuntimeState` signals running jobs (`stopped_by_parent`), waits ≤5s, abandons (`agent/jobs.go:240-307`); children closed **sequentially, recursively** (`:114-116`) with the parent store held open one level above each closing child ("nested child jobs may still need to forward their terminal events", `:102-105`); then `closeStoreOnly`, env cleanup, hooks. **No cross-session lock recursion — the cost is stacked sequential latency, not deadlock.** The deadlock note's stage-4 rules (`docs/specs/2026-06-12-job-control-watch-deadlock-design.md:452-462`) call for command-tree close eventually. `job_stop` on a delegate cancels only its run context (`cancelDelegateSub`, `agent/job_delegate.go:1263-1274`; run contexts derive from `context.Background()`, `agent/subagents.go:520-524`) — it does NOT close the child session or stop its jobs (v3 §2 adds the cascade).

## 7. Notifications upward

One hop is the whole story: finalize enqueues at the owner (`agent/jobs.go:1046-1056`) AND forwards the pending to the owner's parent (`:1060-1076`) — so today TWO levels learn of a nested completion, and only the driven one (the root) can render. Nothing propagates twice. Caller-targeted watch sends ride the same rail with one-level ChildSessionID routing. Supervision extras to the owner session only: quiet-delegate watchdog (10m, `job_watch.go@d851bd10:53-72`), `last_activity` projection (`agent/session_tools_jobs.go:477-487`).

## 8. Governance hooks (what bounds a runaway tree)

| Bound | Value / location |
|---|---|
| `MaxSubagentDepth` | default 1; checked only in `prepareSubagentRun`; inherited wholesale |
| Hard depth gates independent of the knob | `agent/subagents.go:296-298`; `agent/job_delegate.go:252-254`; `agent/session_init.go:531-535` |
| Retained **terminal** children per parent | 128 (`agent/subagent_manager.go:39`); running children **uncapped** |
| Concurrent shell/delegate jobs | **No implementation cap** (contract requires bounds, `docs/job-control.md:1170-1179`) |
| Child turn budget | delegate `MaxTurns` 500; `MaxToolRoundsPerInput` 200 |
| Watch chatter | budget 50/auto-clear; pending cap 32; frame ≤4096 |
| Output retention | 8 MiB/job |
| Token/cost budget plumbing | **None** |

## 9. Capability-availability construction

`registerCoreTools` registers the full set for every session (`agent/session_tool_registry.go:219-246`); spawn shaping subtracts (allow/deny lists, then depth strip, `agent/session_init.go:521-535`); restored delegates re-validate frozen tools with root-only names excluded (`agent/job_delegate.go:695-733`). The prompt's tools section reflects the live registry; the root's available-agents section shows per-type `DefaultTools` with root-only stripped; `DefDelegate`'s enum is pre-filtered. The contract pins the principle at `docs/job-control.md:831`. No seam exists for a parent to ask "will my child be able to delegate?" — v3 §1's allowance makes it explicit at spawn. Legacy `spawnAgent`/`sendInput`/`cancelAgent` survive with no production callers (`agent/subagents.go:277,575,649`).

## 10. Open questions raised here (now decided in the spec)

Fifteen decisions were framed (forwarding topology; control routing at depth; descendant watchability; mid-tree draining; token routing; which guard is the depth law; intermediate prompting; send-message scope; grant walks; crash reconciliation at depth; notification fan-up; runaway bounds; per-depth agent types; close-as-command-tree; transcript confidentiality). All fifteen are resolved by `docs/superpowers/specs/2026-06-12-recursive-subagents-design.md` v3 (§0 decisions + sections); consult the spec, not this list.
