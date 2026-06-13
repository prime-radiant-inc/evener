# Recursive Subagents — Implementation Plan (from spec v3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. TDD per task: write the red test FIRST, watch it fail for the stated reason, implement the minimum, watch it pass, run the task gate, commit.

**Source spec:** `docs/superpowers/specs/2026-06-12-recursive-subagents-design.md` (v3 — "recursion-minimal, drive-down", signed off, implementation gated on the job-control e2e matrix being green — it is, 14/14).

**Goal:** General recursive delegation, dark behind a double opt-in, with attention delivered by *drive-down* (a parent never renders a non-owned job's notification; it runs the child whose own loop delivers). Six capability seams flip from depth-keyed to allowance-keyed; a tree-wide running counter bounds width; `job_list(include_descendants=true)` resolves the live subtree on demand; `job_stop` cascades.

**Branch:** `job-control-spec`. Never push. Never merge.

---

## CRITICAL — surface-change dependency (read before any task)

`docs/superpowers/specs/2026-06-13-max-wait-unification.md` (v3) is being implemented **on this same branch right now** and is **sequenced BEFORE** this campaign. It deletes the `background`/`block` booleans and `block_timeout_ms` from all five job tools and replaces them with **`max_wait_ms`** (per-tool decode table in its §2; complete-or-handle in its §3). **As of this plan's writing, max_wait has NOT landed in code** (verified: `grep -rn max_wait_ms agent/` is empty; `definitions.go` still carries `background`/`block_timeout_ms`).

**Hard rule for every task below:** reference the **post-max_wait** surface. The recursion spec was written against the OLD vocabulary; this plan translates:

| Recursion spec says (old) | This plan's tasks use (post-max_wait) |
|---|---|
| `delegate` has `background`/`block_timeout_ms` | `delegate` has `max_wait_ms` (unset = no wait, return `job_id` now; >0 = wait inline up to N) — max_wait §2 delegate row |
| §3 drive-down's "no delegate job record is minted; the §4 counter is reserved for the turn's duration" — a *notification turn*, not a tool wait | unchanged: drive turns are internal, no tool surface; no translation needed |
| §5 "the background-jobs section gains one drive-down sentence" | the prompt edit must use post-max_wait vocabulary ("you are notified when YOUR delegates finish") — the `## Background jobs` section is itself reworded by the max_wait sweep first; this campaign edits the reworded text |
| spec §0.3 contract `:981` "never resumed solely to deliver a notification" | now contract **`:990`** (drift — see Drift Findings); the clause text is unchanged, only the line moved |

**If max_wait has NOT landed when a task here begins:** STOP and surface it (see Questions Q1). This campaign must build on the final tool surface. Do not author `max_wait_ms` plumbing here — that is the other spec's job — but every prompt-text / schema-description / scenario-card edit this plan makes MUST be written in the post-max_wait vocabulary, and where this plan's code reads a delegate's wait knob it reads `max_wait_ms`, never `background`.

---

## Drift findings against the dossier (anchors re-verified at HEAD = `3826df54`)

The dossier (`docs/superpowers/research/2026-06-12-recursion-dossier.md`) was captured at `d851bd10`; the branch has moved **27 commits** since. Every anchor this plan relies on was re-verified. Drift:

1. **`createDelegate` already exists and uses the prepared-run path.** Dossier §2 cited `spawnAgent`/`sendInput`/`cancelAgent` as the delegate runtime. The live path is `createDelegate` (`agent/job_delegate.go:122`) → `prepareSubagentRun` (`agent/subagents.go:292`) → `attachDelegateJobWithPrepared` → `bridgeDelegateFinalization`. The dossier's `spawnAgent`/`sendInput`/`cancelAgent` are still present as residue (no production callers) per spec §6. **Plan uses the live `createDelegate`/`prepareSubagentRun`/`attachDelegateJobWithRestore` symbols.**
2. **Depth gates moved.** Dossier `subagents.go:296-298` (hard `depth>0`) → now `agent/subagents.go:297`; `:299-301` (maxDepth) → now `agent/subagents.go:300`. Both inside `prepareSubagentRun` (`agent/subagents.go:292`). The `s.mu`-guarded `depth`/`maxDepth` read is at `:294-296`.
3. **Registry strip moved.** Dossier `session_init.go:531-535` → now `agent/session_init.go:531-533` (`for _, name := range rootOnlySubagentTools() { s.reg.Remove(name) }`).
4. **`sendDelegateMessage` depth guard moved.** Dossier `job_delegate.go:249-254` → now `agent/job_delegate.go:253` ("not_controllable: concrete delegate job targets are root-only"); `sendDelegateMessage` is at `:195`.
5. **`cancelDelegateSub` moved.** Dossier `:1263-1274` → now `agent/job_delegate.go:1264`. Wired as `signal: func() { cancelDelegateSub(sub) }` at `:1138`.
6. **`validateRestoredDelegateRequiredTools` (seam 5) moved.** Dossier `:706-719` → now `agent/job_delegate.go:707`; sibling `validateRestoredDelegateTools` (seam-3-self-heals) at `:696`.
7. **`agentUsesRootOnlySubagentTools` rejection moved.** Dossier `:310-312` → now `agent/subagents.go:310-311` ("agent_type %q is top-level only: it requires root-only tools"). Grant rejection (`:418-420`) → now `agent/subagents.go:419-420` ("cannot grant tool %q: root-only tools are top-level only").
8. **Prompt filters moved.** Dossier `session_tools.go:506/529/499` → `defaultToolSummaryForAgent` at `agent/session_tools.go:489`, `availableAgentEntries` at `:503` (filter `agentUsesRootOnlySubagentTools` at `:506`), `delegateAgentTypeNames` at `:526` (filter at `:529`), delegate-def injection in `rebuildToolDefsCache` at `:576`.
9. **Delegate-start forwarding gap CONFIRMED (spec §2's premise holds).** The delegate record (`attachDelegateJobWithRestore`, `agent/job_delegate.go:1126-1138`) does **NOT** set `ParentJobID`, and its `EventJobStarted` (`:1151-1163`) carries no `ParentJobID` and is **not** forwarded — contrast `createShell` (`agent/jobs.go:445-447`) which stamps `ParentJobID: rec.ParentJobID` and calls `jm.forwardLocked(started)`. The `DelegateRestoreDescriptor` *does* carry `ParentJobID` (`:1185`) and `createDelegate` plants `ctxParentJobID` (`:141`), but neither reaches the durable record or a forward. **Spec §2's "delegate-job creation forwards NOTHING and stamps no ParentJobID" is accurate at the record/event level.**
10. **Contract line drift (spec §3/§8 anchors).** The contract grew; spec §3's named anchors moved:
    - spec `:981` "resume-to-deliver" → **`docs/job-control.md:990`** ("must not be resumed solely to deliver a notification").
    - spec `:1056` "parent-renders / nested-delegate-future" → **`docs/job-control.md:1054`** (prose) and **`:90`** (design-principle 9). Until-nested-delegate guidance is at both.
    - spec `:988-990` "no-loss / durable pending" → **`docs/job-control.md:999`** ("must remain in a durable `pending` state").
    - spec guidance `:40` → the `caller` runtime-alias bullet is now at **`docs/job-control.md:38`** ("Runtime alias `caller` is available for runtime-originated delivery").
    Tasks cite the **current** lines; re-grep at edit time since the max_wait sweep rewrites the contract first and will shift them again.
11. **`drainPendingWatchSends` is the live drive seam.** Dossier §4 cited `drainPendingWatchSends` at `job_watch.go@d851bd10:2350-2395`; now at `agent/job_watch.go:2566`, with `drainJobManagerWatchSends` (child-jm traversal) at `:2582` and `hasPendingWatchSends` at `:2720`. Loop-owned call sites: `agent/session_tool_round.go:327`, `agent/session_state.go:122`, `agent/history_repair.go:126`. **This is the existing traversal §3 re-purposes as the drive-signal reader.**
12. **None of the new symbols exist yet** (verified): no `treeCounter`, `tree_at_capacity`, `delegation_allowance`, `CanDelegate`, `include_descendants`, or `owner_session_id` row in `job_list` output. All net-new.
13. **"agenttest trees" (spec §9) = the existing Go table tests** using `fakeAdapter` + `MaxSubagentDepth: N` (e.g. `agent/job_delegate_test.go`, `agent/job_nested_test.go`). No separate `agent/agenttest/` package exists. Depth-3 trees are built by nesting `fakeAdapter` scripts; the plan's red tests live in `agent/*_test.go`.

---

## Conventions for every task

- Work in the `agent` Go module: run Go commands from `/home/jesse/git/prime-radiant/serf/agent`.
- TDD: red test first, watch it fail for the named reason, minimal implementation, watch it pass.
- Commit style: `type(scope): subject`. Scope is `recursion` or `job-control` per task.
- Contract amendments (spec §8) ride **in the same commit** as the code that implements them (the mailbox-design precedent, `docs/superpowers/specs/2026-06-11-job-control-watch-mailbox-design.md:203`). The carrying task is named per clause below.
- **Per-task gate:** `cd agent && go test ./... -run <TaskTests>` green.
- **Per-phase gate (hard):** from repo root — `make test`, `make lint` (`~/go/bin/golangci-lint` via `make lint-golangci`, plus naming/internal/docs checks), and `cd agent && go test ./... -race`. Never start the next phase on a red gate.
- Re-grep every cited anchor before editing (max_wait lands first and shifts lines).

---

## Phase / Task list (titles)

**Phase 0 — Foundation: the allowance carrier and the counter (no behavior flip yet)**
- [x] Task 1: `delegation_allowance` on `spawnConfig`, transcript header, and `DelegateRestoreDescriptor`
- [x] Task 2: the tree-wide running counter (`treeCounter`, atomic, cap 16) — created by root, handed down

**Phase 1 — The headline red test (drive-down regression, red against today)**
- Task 3: deaf-coordinator drive-down regression test (unskipped RED run captured, lands `t.Skip`-tracked; Task 14 unskips first — mandatory mechanism, see task)

**Phase 2 — Flip the six capability seams to allowance-keyed**
- Task 4: seams 1+2 — depth/maxDepth gates become allowance checks (`prepareSubagentRun`)
- Task 5: seam 6 — agent-type spawn rejection + prompt-side filters key on grantable allowance
- Task 6: seam 3 — registry stripping at child init becomes allowance-aware
- Task 7: seam 4 — `baseSubagentToolPolicy` deny-list becomes allowance-aware; default child gets `delegate`+`job_watch` at allowance>0
- Task 8: seam 5 — `validateRestoredDelegateRequiredTools` allowance-aware (coordinator-type resume); grant-rejection text allowance-truthful
- Task 9: `delegate` schema gains `delegation_allowance`; grant rejection `< own allowance`; prompt template `{{ if .CanDelegate }}` sections + allowance stated

**Phase 3 — Visibility and control across the subtree**
- Task 10: delegate-start forwarding + `ParentJobID` stamp + the dedupe rule
- Task 11: `job_list(include_descendants=true)` — live subtree walk with `owner_session_id`+`depth`, dedupe, live-only
- Task 12: depth ≥ 2 reads — `job_read_output` resolves descendant rows through the recursive owner path; `max_wait_ms>0` rejected at depth ≥ 2
- Task 13: `job_stop` cascade — stop a coordinator's delegate stops its workers recursively; non-direct descendant → `not_controllable` with guidance

**Phase 4 — Drive-down delivery (the core mechanism)**
- Task 14: drive signals + the drive action (resume child for one `EntryNotification` turn; counter reserved; no job record minted)
- Task 15: settle-at-handoff + restore re-arm filtering + stop-gating (no resurrection) + failure fallback (`child unreachable:`) + delete the mid-owner caller re-route
- Task 16: counter wiring — reserve on spawn/resume/drive, release on finalize AND abandon; `tree_at_capacity` text; restart rebuild

**Phase 5 — Docs, rollout disclosure, and e2e cards**
- Task 17: contract §8 sweep residue + `docs/architecture.md` drive-down section + rollout disclosure
- Task 18: live e2e coordinator-pattern scenario cards (authoring only; running is the orchestrator's job)

(18 tasks — within the ticket's 17-19 estimate.)

---

## Seam → Task coverage (spec §1; a seam with no task is a plan bug)

| # | Seam (spec §1) | Current anchor | Task |
|---|---|---|---|
| 1 | Hard `depth > 0` error | `agent/subagents.go:297` | **Task 4** |
| 2 | `MaxSubagentDepth` check beside it | `agent/subagents.go:300` | **Task 4** |
| 3 | Registry stripping at child init | `agent/session_init.go:531-533` | **Task 6** |
| 4 | Default subagent tool-policy deny-list (`baseSubagentToolPolicy`) | `agent/subagents.go:163-174`, applied `session_init.go:528-530` | **Task 7** |
| 5 | Restored-delegate tool validation (`validateRestoredDelegateRequiredTools`) | `agent/job_delegate.go:707` (sibling `:696`) | **Task 8** |
| 6 | Agent-type spawn rejection (`agentUsesRootOnlySubagentTools`) + prompt filters | `agent/subagents.go:310-311`; `session_tools.go:506,529,499` | **Task 5** (+ enum/desc in **Task 9**) |
| + | `sendDelegateMessage` root-only guard → "own direct delegates" | `agent/job_delegate.go:253` | **Task 13** (control scoping) |
| + | grant_tools rejection text → allowance-truthful | `agent/subagents.go:419-420` | **Task 8** |

All six seams + the two "plus" items are covered.

---

## Test matrix → Task coverage (spec §9; every item maps to a task)

| Matrix item (spec §9) | Task |
|---|---|
| Allowance enforcement per level (depth-3 trees) | **Task 4, 9** |
| **Deaf-coordinator regression, drive-down form** (headline, RED against today) | **Task 3** (authored early) → made green by **Task 14** |
| Drive at depth 3 with idle middle (root drives mid; mid drives its child) | **Task 14** |
| Handoff settle + crash-between-handoff-and-render (re-arm; no wake-storm) | **Task 15** |
| Stop-cascade (workers stop; no resurrection — gated drive) | **Task 13** (cascade) + **Task 15** (stop-gating) |
| Fallback render for non-resumable child (`child unreachable:`) | **Task 15** |
| Counter (reserve spawn/resume/drive, release finalize+abandon, idle frees, 17th fails, restart rebuild) | **Task 16** (with Task 2 unit base) |
| Live-walk (annotations, dedupe, depth-2 read resolution, live-only limit) | **Task 11, 12** |
| Coordinator-type delegate resume (seam 5 — RED today) | **Task 8** |
| Mid-owner caller frames render mid-side | **Task 15** |
| Allowance persistence across resume | **Task 1** |
| E2e coordinator-pattern cards (raised config) | **Task 18** (authoring); running left to orchestrator |

---

## Contract amendment → carrying task (spec §8; each rides its code commit)

| Amendment clause (spec §8) | Current contract anchor | Carried by |
|---|---|---|
| Availability matrix (allowance-gated, six seams) + `delegation_allowance` schema + double opt-in | new §, near `docs/job-control.md:90` design-principles | **Task 9** |
| Delegate-start forwarding + dedupe rule | `docs/job-control.md:1056-1066` (Nested jobs) | **Task 10** |
| `include_descendants` + depth-read resolution | `docs/job-control.md:1062-1065`, read rules near `:534`/`:744` | **Task 11, 12** |
| Stop-cascade + its guidance error | `docs/job-control.md:1071` (currently `include_children`) | **Task 13** |
| Drive-down (`:981`→**`:990`**, `:1056`→**`:1054`**, `:988-990`→**`:999`**, guidance `:40`→**`:38`**) | see Drift #10 | **Task 14, 15** |
| `ParentCanWatch` → own-jobs + delegate-the-watching | `docs/job-control.md:1096` flowchart, rule `:534` | **Task 13** |
| Caps (`tree_at_capacity`, counter semantics, shell + retention gaps acknowledged) | new §, near `docs/job-control.md:1073-1078` | **Task 16** |
| `caller`/`job_send_message` per-level scope | `docs/job-control.md:38` (caller alias) | **Task 13** |

---

# Phase 0 — Foundation

## Task 1: `delegation_allowance` carrier (spawnConfig + transcript header + restore descriptor)

**Implements:** spec §1 (the allowance), persistence clause ("rides `spawnConfig`, the transcript header beside `Depth`, and the `DelegateRestoreDescriptor`").

**Files:**
- `agent/session_config.go` (add `delegationAllowance int` to `spawnConfig` near `depth` at `:222-223`, `json:"-"`; add `MaxSubagentDepth` is already the root knob at `:37-39`)
- `agent/session_init.go` (read/write transcript `Header.Depth` neighbor — `Header.Depth` at `:178`/`:390`; add `Header.DelegationAllowance`; set `s.delegationAllowance` from `cfg.spawn` at NewSession `:104`-region and Restore `:299`-region, mirroring `depth`)
- `agent/internal/jobstore/record.go` (add `DelegationAllowance int` to `DelegateRestoreDescriptor`)
- `agent/job_delegate.go` (`delegateRestoreDescriptor` `:1179` + `resumedDelegateRestoreDescriptor` `:1226` carry it; restore re-injects into `RestoreSessionConfig.spawn` at `:566-567`-region)
- `agent/session.go` (add `delegationAllowance int` field beside `depth` at `:108`, `s.mu`-guarded; accessor)
- Header schema wherever `Header.Depth` is declared (`agent/internal/.../schema` — grep `Depth ` in the transcript header struct)

**Red test (write FIRST), `agent/session_recursion_test.go` (new):**
- `TestDelegationAllowancePersistsAcrossResume`: spawn a delegate with `delegation_allowance=2` (via a direct `spawnConfig` for now — the tool param lands in Task 9), restore the child from its transcript, assert the restored child's `delegationAllowance == 2`. **Fails today:** no such field; the value is dropped on restore exactly as the dossier warns spawn fields are (`session_config.go:184-188`).
- `TestRootAllowanceFromConfig`: a root session with `MaxSubagentDepth: 2` reports root `delegationAllowance == 2`; with default (1) reports 1. **Fails today:** no `delegationAllowance` plumbing.

**Why red:** the field does not exist; `spawnConfig` spawn fields are `json:"-"` and reconstructed from the transcript header on restore — the header has no allowance slot, so any in-memory value vanishes. This task adds the header slot (the one persistence path that survives restore, like `Depth`).

**Note (do NOT repopulate spawn on restore naively):** `session_config.go:184-188` warns that repopulating `spawn` on restore gives restored subagents non-zero depth and breaks ATIF root-export gating. Allowance must ride the **transcript header** (durable) and be read into `s.delegationAllowance`, NOT reconstructed by faking `cfg.spawn`. Follow the `Header.Depth` pattern exactly.

**Gate:** `cd agent && go test ./... -run 'TestDelegationAllowance|TestRootAllowance'`
**Commit:** `feat(recursion): carry delegation_allowance on spawnConfig, transcript header, restore descriptor`

---

## Task 2: tree-wide running counter (`treeCounter`, atomic, cap 16)

**Implements:** spec §4 (governance, structural only — wiring into the create/resume/drive paths is Task 16).

**Files:**
- `agent/tree_counter.go` (new): `type treeCounter struct { n atomic.Int64; cap int64 }`; `func newTreeCounter() *treeCounter` (cap 16); `func (c *treeCounter) reserve() bool` (check-and-reserve, returns false at cap); `func (c *treeCounter) release()`. Created by the root; carried down via `spawnConfig`.
- `agent/session_config.go` (add `treeCounter *treeCounter` to `spawnConfig`, `json:"-"`; root mints in NewSession when `spawn.parentSessionID == ""`, children inherit the pointer)
- `agent/session.go` (hold the `*treeCounter` pointer on the session)
- `agent/tree_counter_test.go` (new)

**Red test (FIRST):**
- `TestTreeCounterReserveRelease`: reserve 16 succeed, 17th fails; release one, reserve succeeds again. **Fails today:** type does not exist.
- `TestTreeCounterSharedAcrossTree`: a child session's inherited `treeCounter` is the **same pointer** as the root's (reservations on the child decrement the root's budget). **Fails today:** no such field.

**Why red:** net-new; verified no `treeCounter` symbol exists anywhere.

**Note:** this task only builds the counter and threads the pointer. It does NOT yet call `reserve`/`release` on the spawn/resume/drive paths — that integration (and the `tree_at_capacity` error) is **Task 16**, after the drive turns exist (Phase 4) so the counter has all three reservation paths to wire at once. Keeping it inert here avoids the day-one fan-out bind landing before the disclosure (Task 17). **The counter is dormant until Task 16.**

**Gate:** `cd agent && go test ./... -run TestTreeCounter`
**Commit:** `feat(recursion): tree-wide running counter scaffold (dormant until wiring)`

---

# Phase 1 — The headline red test

## Task 3: deaf-coordinator drive-down regression (RED against today's code)

**Implements:** spec §9 headline ("the deaf-coordinator regression, drive-down form") — authored EARLY per the campaign brief; it is red against today and stays the north star through Phase 4.

**Files:**
- `agent/job_delegate_drivedown_test.go` (new)

**The test (this is the spec's headline shape):**
`TestDriveDownDeafCoordinator`: a depth-3 `fakeAdapter` tree. Root spawns a **coordinator** delegate (allowance ≥ 1) which backgrounds two **worker** delegates (`max_wait_ms` unset = no wait) and ends its turn. The workers finish. Assert:
1. The **parent drives the coordinator** — the coordinator's model gets a notification turn for its workers' completions (its own queue, in its own turn).
2. The **root's model is told ONLY when the coordinator itself finishes** — the root never receives a worker's terminal notification (drive-down: the root renders its OWN jobs' terminals, i.e. its direct delegate the coordinator, never grandchildren).

**Why red TODAY:** today a mid-level (depth ≥ 1) delegate that backgrounds children and ends its turn is **never driven** — idle children drain nothing (dossier §4: "idle children drain nothing"; `child jm wake = no-op unless SetNotifyFunc, which only serve.go wires for the root"). The coordinator's queued worker-completions sit undelivered; the worker terminals either vanish or (via today's single-hop forward) surface on the **root's** rail as type-less phantoms. Either way the assertions fail.

**Landing discipline (MANDATORY — resolved decision #4; no option remains):**
the implementer RUNS the test unskipped, captures the red output verbatim in
the task report, then lands it with
`t.Skip("RED until Task 14 — drive-down; tracks spec §9 headline")` above the
compiled assertions. Task 14's FIRST act: remove the skip, re-show red,
implement, green. Build tags / `-run` exclusions are NOT acceptable — they
hide the test from output; a visible skip is tracked. The mailbox-design
precedent ("regression target must stay covered forever; re-anchor, never
delete", `2026-06-11-...-mailbox-design.md:216`) governs.

**Gate:** `cd agent && go test ./... -run TestDriveDownDeafCoordinator` (must compile; reported as SKIP, with the captured unskipped-red evidence in the task report)
**Commit:** `test(recursion): deaf-coordinator drive-down regression (red until Phase 4)`

---

# Phase 2 — Flip the six capability seams

> Sequencing within Phase 2: seams are interdependent (a child with allowance but a stripped registry ships broken). Land Tasks 4-8 as the seam flips, then Task 9 exposes the tool param + prompt + grant rule that makes the whole thing reachable. **Until Task 9, allowance is set only via direct `spawnConfig` in tests** — the model cannot grant it, so production is unchanged (dark).

## Task 4: seams 1+2 — depth/maxDepth gates become allowance checks

**Implements:** spec §1 seams 1, 2.

**Files:** `agent/subagents.go` (`prepareSubagentRun` `:292`, gates at `:297` and `:300`); test `agent/subagents_test.go`.

**Red test (FIRST):**
- `TestPrepareSubagentRunAllowsRecursionWithAllowance`: a depth-1 session with `delegationAllowance == 1` calls `prepareSubagentRun` and succeeds (today: `depth > 0` → "subagent management is top-level only"). **Fails today** at `subagents.go:297`.
- `TestPrepareSubagentRunRejectsZeroAllowance`: a session with `delegationAllowance == 0` is rejected (the leaf case). **Passes-by-accident today** via the depth gate, so assert the **error message** is the allowance one, not the depth one — that distinguishes the new behavior.

**Implementation:** replace the two depth-keyed gates with one allowance check: `if s.delegationAllowance <= 0 { return nil, errors.New("delegation not permitted: your delegation_allowance is 0") }`. The `MaxSubagentDepth` config still bounds the **root's** allowance (Task 1); the per-call gate is now allowance, not depth. Keep the `s.mu`-guarded read shape at `:294-296`.

**Gate:** `cd agent && go test ./... -run TestPrepareSubagentRun`
**Commit:** `feat(recursion): depth gates become delegation_allowance checks (seams 1,2)`

---

## Task 5: seam 6 — agent-type spawn rejection + prompt-side filters key on grantable allowance

**Implements:** spec §1 seam 6, typed-agent rule.

**Files:** `agent/subagents.go` (`agentUsesRootOnlySubagentTools` rejection `:310-311`); `agent/session_tools.go` (`availableAgentEntries` filter `:506`, `defaultToolSummaryForAgent` `:499`, `delegateAgentTypeNames` filter `:529`); tests `agent/session_tools_test.go`.

**Red test (FIRST):** `TestAgentTypeRosterKeyedOnAllowance`: a session with `delegationAllowance == 1` advertises agent types that list `delegate` in their tool set (today they're filtered out by `agentUsesRootOnlySubagentTools` regardless). A session with allowance 0 still filters them. **Fails today** — the filter is unconditional.

**Implementation (typed-agent rule, decided in spec §1):** a type listing `delegate`/`job_watch` is spawnable iff **grantable allowance > 0** (i.e. `s.delegationAllowance - 1 >= 0` for the would-be child, equivalently `s.delegationAllowance > 0`). The filters (`agentUsesRootOnlySubagentTools`-based) become allowance-gated: when grantable allowance > 0, these types are NOT filtered. Allowance never *injects* tools into a type that doesn't list them.

**Gate:** `cd agent && go test ./... -run 'TestAgentType|TestAvailableAgent|TestDelegateAdvertises'`
**Commit:** `feat(recursion): agent-type roster + prompt filters key on grantable allowance (seam 6)`

---

## Task 6: seam 3 — registry stripping at child init becomes allowance-aware

**Implements:** spec §1 seam 3.

**Files:** `agent/session_init.go` (`:531-533`, the `s.reg.Remove(name)` loop over `rootOnlySubagentTools()`); test `agent/session_init_test.go` or `agent/subagents_test.go`.

**Red test (FIRST):** `TestChildRegistryKeepsDelegateWithAllowance`: a child session initialized with `delegationAllowance > 0` retains `delegate` and `job_watch` in its registry (today: stripped because `s.depth > 0`). **Fails today** at `session_init.go:531`.

**Implementation:** gate the strip on allowance, not depth: `if s.delegationAllowance <= 0 { for _, name := range rootOnlySubagentTools() { s.reg.Remove(name) } }`. A child with allowance keeps the tools registered (the default-child surface-add in Task 7 handles the *untyped* child's tool list).

**Why this and Task 7 together:** seam 3 (registry) and seam 4 (policy) both gate the child's tool surface; flipping one without the other ships a child that has the tool registered but denied by policy (or vice versa). Land 6 then 7 back-to-back; the phase gate runs after 7.

**Gate:** `cd agent && go test ./... -run TestChildRegistry`
**Commit:** `feat(recursion): registry strip at child init is allowance-aware (seam 3)`

---

## Task 7: seam 4 — `baseSubagentToolPolicy` deny-list allowance-aware; default child gains delegate+job_watch

**Implements:** spec §1 seam 4, typed-agent rule's "default (untyped) child with allowance > 0 gets `delegate` + `job_watch` added to today's default surface."

**Files:** `agent/subagents.go` (`baseSubagentToolPolicy` `:163-174`, applied at `session_init.go:528-530`); test `agent/subagents_test.go`.

**Red test (FIRST):** `TestBaseSubagentPolicyAllowsDelegateWithAllowance`: with grantable allowance > 0 and no agent type (default child), `baseSubagentToolPolicy` does NOT deny `delegate`/`job_watch` (today the deny-list is exactly `rootOnlySubagentTools()`). **Fails today** at `subagents.go:172`.

**Implementation:** `baseSubagentToolPolicy` takes (or reads) the spawning session's grantable allowance; when > 0, the default deny-list excludes `delegate`/`job_watch` so the untyped child gets them on top of today's default surface. A type's explicit tool list still governs typed children (Task 5).

**Gate:** `cd agent && go test ./... -run 'TestBaseSubagentPolicy|TestChildRegistry'`
**Commit:** `feat(recursion): default subagent tool-policy allowance-aware; untyped child gains delegate+job_watch (seam 4)`

---

## Task 8: seam 5 — restored-delegate tool validation allowance-aware; grant-rejection text allowance-truthful

**Implements:** spec §1 seam 5 (the /par-A coordinator-resume bug), the grant_tools rejection-text amendment.

**Files:** `agent/job_delegate.go` (`validateRestoredDelegateRequiredTools` `:707`; sibling `validateRestoredDelegateTools` `:696` self-heals once seam 3 is allowance-aware); `agent/subagents.go` (grant rejection `:419-420`); tests `agent/job_delegate_test.go`.

**Red test (FIRST):** `TestCoordinatorTypeDelegateResumes`: a coordinator agent type whose frozen required-tools include `delegate` is spawned (allowance > 0), terminated, and resumed via `job_send_message`. Assert it resumes (today: `validateRestoredDelegateRequiredTools` deletes root-only names from the registered set, so the frozen requirement *fails* validation and the delegate cannot resume — dossier §1 seam 5 / /par-A). **Fails today** at `job_delegate.go:707`.

**Implementation:** `validateRestoredDelegateRequiredTools` must not strip `delegate`/`job_watch` from the validation set when the restored child's `delegationAllowance > 0` (carried by the descriptor, Task 1). The grant-rejection text at `subagents.go:419-420` becomes allowance-truthful: instead of "root-only tools are top-level only", it states the allowance rule (e.g. "cannot grant `delegate`: requires delegation_allowance > 0").

**Gate:** `cd agent && go test ./... -run 'TestCoordinatorTypeDelegate|TestRestoredDelegate|TestGrant'`
**Commit:** `feat(recursion): restored-delegate validation + grant text allowance-aware (seam 5)`

---

## Task 9: `delegate` schema gains `delegation_allowance`; grant rule; prompt template conditionals

**Implements:** spec §1 (the grant rule + rejection message), spec §5 (prompt surface), spec §8 (availability matrix + `delegation_allowance` schema + double opt-in contract amendment).

**Files:**
- `agent/internal/tool/definitions.go` (`DefDelegate` — add `delegation_allowance` integer property, default 0; description per spec §5; **post-max_wait surface**: `max_wait_ms` is already present from the max_wait campaign — this task only adds `delegation_allowance`)
- `agent/job_delegate.go` (`createDelegate` `:122` parses `delegation_allowance`, validates `< s.delegationAllowance` else `invalid_request: delegation_allowance must be less than your own allowance (<A>)`; sets `prepared`'s child `spawnConfig.delegationAllowance`)
- `agent/prompts/templates/subagent.md.tmpl` + `agent/prompts/sections/delegation.md` (conditional `{{ if .CanDelegate }}` sections; "Only you can call `delegate`" dies; allowance stated in prompt)
- `agent/session_prompts.go` (`:154-160` template data: add `CanDelegate` (allowance > 0) and the allowance integer to the template data; the binary depth>0 swap stays but the subagent template gains conditional sections)
- `docs/job-control.md` — **carries the spec §8 availability-matrix + `delegation_allowance` + double-opt-in amendment** (near design-principle 9, `:90`)
- tests: `agent/internal/tool/definitions_test.go`, `agent/job_delegate_test.go`, `agent/plugin_prompt_test.go`

**Red tests (FIRST):**
- `TestDefDelegateHasDelegationAllowance`: `DefDelegate(...)` props include `delegation_allowance`. **Fails today.**
- `TestDelegateRejectsAllowanceGEOwn`: a session with allowance 2 calling `delegate(delegation_allowance=2)` is rejected with the exact message; `=1` succeeds. **Fails today.**
- `TestSubagentPromptStatesAllowance`: a child rendered with `CanDelegate` shows the delegation section + its allowance; an allowance-0 child shows the leaf limits block and no delegation text. **Fails today** (template is a binary swap, no conditional).

**Translation note (max_wait):** the spec §5 sentence "your delegates handle their own children's completions; you are told when YOUR delegates finish" goes into the `## Background jobs` prompt section. That section is **reworded first by the max_wait sweep** (max_wait §4 lists it). This task edits the *reworded* text; if it still says `background`/`block_timeout_ms`, STOP (Q1).

**Double opt-in disclosure (spec §1, §10):** the contract amendment states plainly — under defaults (`MaxSubagentDepth=1`) the root may only grant 0; recursion requires raising the config AND granting per spawn.

**Gate:** `cd agent && go test ./... -run 'TestDefDelegate|TestDelegateRejectsAllowance|TestSubagentPrompt'`
**Commit:** `feat(recursion): delegate gains delegation_allowance, grant rule, prompt conditionals; contract availability matrix`

**Haiku comprehension gate (spec §5):** before this lands, run the reworded delegation/background-jobs prompt text through a Haiku read for comprehension (spec §5: "Haiku comprehension gate before landing"). Surface a failed gate to Jesse rather than shipping confusing prompt text.

**Phase 2 gate:** `make test && make lint` from repo root; `cd agent && go test ./... -race`.

---

# Phase 3 — Visibility and control

## Task 10: delegate-start forwarding + `ParentJobID` stamp + dedupe rule

**Implements:** spec §2 (delegate jobs join one-hop forwarding), spec §8 (delegate-start forwarding + dedupe contract amendment).

**Files:**
- `agent/job_delegate.go` (`attachDelegateJobWithRestore` `:1111` — stamp `ParentJobID` on the record `:1126-1138` from the ambient parent job (`jm.currentParentJobID()`, mirroring `createShell` `jobs.go:392/401`); add `ParentJobID` to the `EventJobStarted` `:1151-1163`; call `jm.forwardLocked(started)` exactly as `createShell` does at `jobs.go:445-447`, including the start-forward-failure handling)
- `agent/jobs_nested.go` (the dedupe rule in the walk — but the walk itself is Task 11; here ensure forwarded delegate-start records carry owner/type identity)
- `docs/job-control.md` — **carries the delegate-start-forwarding + dedupe amendment** (Nested jobs §, `:1056-1066`)
- tests: `agent/job_nested_test.go`, `agent/job_delegate_test.go`

**Red test (FIRST):** `TestDelegateStartForwardsToParent`: a coordinator (depth 1) spawns a worker delegate; the coordinator's `job_started` for the worker forwards one hop to the root's store carrying owner/type identity and `ParentJobID`. Assert the root's store (default list excludes it; query the forwarded record directly) carries the worker's owner = coordinator, type = delegate, `ParentJobID` = the coordinator's own delegate job. **Fails today** — delegate-start forwards NOTHING and stamps no `ParentJobID` (Drift #9).

**Why red:** verified at `job_delegate.go:1126-1163` — no `ParentJobID`, no `forwardLocked`. This is the gap spec §2 says is "the root of the orphan-record cluster."

**Note:** strictly one hop, the existing mechanism (`createShell`'s), applied to the delegate job type it skipped. NOT the deferred visibility chains.

**Gate:** `cd agent && go test ./... -run 'TestDelegateStartForwards|TestNested'`
**Commit:** `feat(recursion): delegate-start joins one-hop forwarding with ParentJobID + dedupe`

---

## Task 11: `job_list(include_descendants=true)` — live subtree walk

**Implements:** spec §2 (`include_descendants`), spec §0 decision 5, spec §8 (`include_descendants` contract amendment).

**Files:**
- `agent/session_tools_jobs.go` (`jobListFilterFromArgs` `:847`, add `include_descendants` bool; the list handler walks the live tree: own records, then recurse each live direct child's `jobManager` store via `s.subagents` + `ownerJobManagerFor` (`jobs_nested.go:11`), leaf-lock reads)
- output struct (`:528`) gains `owner_session_id` (already present at `:528`) + `depth` row annotations
- `agent/internal/tool/definitions.go` (`DefJobList` description + `include_descendants` param)
- `docs/job-control.md` — **carries the `include_descendants` amendment**
- tests: `agent/session_tools_jobs_test.go`

**Red test (FIRST):** `TestJobListIncludeDescendantsWalksLiveTree`: a depth-3 tree with live coordinator + workers; root's `job_list(include_descendants=true)` returns rows for own jobs + the coordinator's jobs + the workers' jobs, each annotated with `owner_session_id` + `depth`; the **dedupe rule** suppresses a forwarded copy of a `job_id` whose owner appears live in the walk; a **dead** coordinator contributes only its own terminal record (no deeper recursion). Default `job_list` and `include_nested` semantics unchanged. **Fails today** — no `include_descendants`, walk does not cross into child stores recursively.

**Gate:** `cd agent && go test ./... -run 'TestJobListIncludeDescendants|TestJobList'`
**Commit:** `feat(recursion): job_list include_descendants walks the live subtree with dedupe`

---

## Task 12: depth ≥ 2 reads — recursive owner resolution; `max_wait_ms>0` rejected

**Implements:** spec §2 (reads at depth ≥ 2), spec §8 (depth-read resolution amendment).

**Files:**
- `agent/session_tools_jobs.go` / `agent/jobs_nested.go` (`job_read_output` accepts descendant rows by resolving through the walk's recorded path — recursive `ownerJobManagerFor` hops; snapshot-only)
- **post-max_wait surface:** the read's wait knob is `max_wait_ms` (not `block`); a depth ≥ 2 read with `max_wait_ms > 0` is rejected **like granted cross-session reads already are** (max_wait §3: `grantedReadBlockUnsupportedErr` reworded — verify it exists post-max-wait). Translation: spec §2 says "`block=true` on a depth ≥ 2 read is rejected like granted reads" → reads as "`max_wait_ms > 0` rejected."
- `docs/job-control.md` — carries the depth-read amendment (with Task 11)
- tests: `agent/job_nested_test.go`

**Red test (FIRST):** `TestJobReadOutputDepth2Resolves`: root reads a grandchild's (worker's) output through the recursive owner path; snapshot returns the worker's bytes; `max_wait_ms > 0` on that read is rejected with the standard message. **Fails today** — `ownerJobManagerFor` is direct-children-only (`jobs_nested.go:11`, dossier §3).

**Gate:** `cd agent && go test ./... -run 'TestJobReadOutputDepth2|TestNested'`
**Commit:** `feat(recursion): depth>=2 reads resolve through recursive owner path; max_wait rejected`

---

## Task 13: `job_stop` cascade + `ParentCanWatch` own-jobs + caller per-level scope

**Implements:** spec §2 (stop cascade), spec §3 (`job_send_message` own-direct-delegates-at-every-level; `sendDelegateMessage` guard → "own direct delegates"), spec §8 (stop-cascade + ParentCanWatch + caller-scope amendments).

**Files:**
- `agent/jobs_nested.go` (`stopChildren` `:89` — make `job_stop` on a coordinator's delegate **cascade**: cancel-finalize also stops the coordinator's own running jobs (workers' delegate + shell jobs), recursively. Today `cancelDelegateSub` (`job_delegate.go:1264`) cancels only the coordinator's turn; workers survive orphaned — dossier §6.)
- `agent/job_delegate.go` (`sendDelegateMessage` depth guard `:253` → "own direct delegates" at every level instead of root-only; `caller` = immediate parent, already handled before the guard per dossier §1)
- `agent/job_watch.go` (`target_not_watchable` for child-owned records → own-jobs-only is already the post-mailbox behavior; here align contract: ParentCanWatch → own jobs + delegate-the-watching)
- non-direct descendant `job_stop` → `not_controllable` with the named coordinator + cascade guidance
- `docs/job-control.md` — **carries stop-cascade (`:1071`), ParentCanWatch (`:534`, `:1096`), caller/job_send_message per-level scope (`:38`)**
- tests: `agent/job_nested_test.go`, `agent/job_watch_test.go`, `agent/job_delegate_test.go`

**Red tests (FIRST):**
- `TestJobStopCascadesToWorkers`: stop a coordinator's delegate → its workers' delegate + shell jobs actually stop, recursively. **Fails today** — workers orphaned.
- `TestJobStopNonDirectDescendant`: `job_stop` on a grandchild row → `not_controllable` naming the coordinator + cascade guidance. **Fails today.**
- `TestSendDelegateMessageOwnDirectDelegatesAtDepth`: a depth-1 coordinator messages its own direct worker by `job_id` (today: `depth > 0` → root-only rejection at `:253`). **Fails today.**

**Translation note:** `job_send_message`'s wait knob is `max_wait_ms` post-max_wait; this task does not add waiting, only widens the *target* scope.

**Gate:** `cd agent && go test ./... -run 'TestJobStopCascades|TestJobStopNonDirect|TestSendDelegateMessageOwnDirect'`
**Commit:** `feat(recursion): job_stop cascade, own-direct-delegate control at every level, ParentCanWatch own-jobs`

**Phase 3 gate:** `make test && make lint`; `cd agent && go test ./... -race`.

---

# Phase 4 — Drive-down delivery (the core)

## Task 14: drive signals + the drive action

**Implements:** spec §3 (the rule, drive signals, the drive action). **Makes Task 3's headline test GREEN.**

**Files:**
- `agent/job_watch.go` (`drainPendingWatchSends` `:2566` + `drainJobManagerWatchSends` `:2582` — the existing child-jm traversal becomes the **drive-signal reader**: (a) a forwarded pending in the parent's store for a child-owned job (owner-identifiable now via Task 10); (b) the child jm reports `hasPendingWatchSends()` (`:2720`) or queued notifications (a queue peek))
- `agent/session_lifecycle.go` (`acceptNotificationInput` `:821` / `EntryNotification` `:242` — a **new launch mode** on the subagent run machinery that runs the child's own drain loop for ONE notification turn: the `EntryNotification` path the root runs at serve-wake, but driven by the parent at the parent's loop boundaries)
- `agent/subagents.go` (the launch-the-child's-drain-for-one-turn machinery; **no delegate job record minted** — this is the child processing its own queue; the §4 counter is reserved for the turn's duration via Task 16's hook)
- tests: remove Task 3's skip; `agent/job_delegate_drivedown_test.go`

**Red → green:** Task 3's `TestDriveDownDeafCoordinator` un-skips here and must pass. Add `TestDriveAtDepth3WithIdleMiddle`: root drives mid; mid, once driven, drives its child at its own boundary. **Fails before this task** (no drive machinery); **passes after.**

**The drive action (spec §3):** launch the child's own drain loop for one notification turn. No job record. If the turn needs to tell the parent something, `job_send_message(caller)` exists and is already legal for children. The drive happens at "the same boundaries that drain watch sends today" — the loop-owned call sites at `session_tool_round.go:327`, `session_state.go:122` (verified Drift #11).

**Gate:** `cd agent && go test ./... -run 'TestDriveDown|TestDriveAtDepth3'`
**Commit:** `feat(recursion): drive-down delivery — parents drive children with undelivered attention`

---

## Task 15: settle, restore re-arm, stop-gating, fallback, delete mid-owner re-route

**Implements:** spec §3 (settle at handoff; restore re-arm filtering; stop-gating no-resurrection; failure fallback; mid-owner caller sends; delete the v2-contradicting child-iteration re-route), spec §8 (drive-down `:990`/`:1054`/`:999`/`:38` amendments).

**Files:**
- `agent/job_watch.go` (mark the parent's forwarded pending **delivered at successful drive handoff**; delete the child-caller-token re-route onto the parent's rail — dossier §4 / spec §3 names `job_watch.go:2580-2592` region, the `ChildSessionID` re-token at `:251-257`)
- restore re-arm filters to owned + direct-child-owned records (closing the /par restart-wake-storm) — `agent/session_init.go` restore path (`:407`, `:747` drain points)
- stop-gating: a child whose latest delegate record terminated by deliberate stop (`stopped_by_parent` family) is not driven for attention predating the stop; new work clears the gate ("latest record for that child session")
- failure fallback: child non-resumable → parent renders the pending itself, prefixed `child unreachable:`; at `tree_at_capacity` the drive retries at subsequent boundaries
- mid-owner caller sends render in the mid's own drive turn (the deleted re-route's replacement)
- `docs/job-control.md` — **carries the drive-down amendments** (`:990` resume-to-deliver now parent-loop-driven; `:1054` parent-drives-for-delegate-owned-jobs but still renders its OWN direct delegates' terminals; `:999` receiver-copy handoff settle; guidance `:38`)
- tests: `agent/job_delegate_drivedown_test.go`, `agent/job_watch_test.go`, restore tests

**Red tests (FIRST):**
- `TestDriveHandoffSettleAndCrashReArm`: handoff settles the parent's copy; crash between handoff and render → the child's own durable queue re-arms; nothing lost; no restart wake-storm. **Fails before** — no settle-at-handoff, restore re-arms everything.
- `TestStopGatingNoResurrection`: a stopped child is not driven for pre-stop attention; a fresh send clears the gate. **Fails before** — no gate.
- `TestFallbackRenderNonResumableChild`: non-resumable child → parent renders `child unreachable:`. **Fails before.**
- `TestMidOwnerCallerFramesRenderMidSide`: a mid-level watch owner's `send.to="caller"` renders in the mid's own drive turn, not re-routed to the parent's rail. **Fails before** — the re-route at `job_watch.go:2580-2592` puts it on the parent.

**Contract clause translation (the `:981` amendment):** spec §3 says the contract's `:981` ("never resumed solely to deliver a notification") is amended — drive-down means the child IS resumed to deliver, parent-loop-driven. That clause is now at `docs/job-control.md:990` (Drift #10). This task rewrites it: "A child/delegate session with undelivered attention is driven by its parent at the parent's loop boundaries; the root is driven by serve.go."

**Gate:** `cd agent && go test ./... -run 'TestDriveHandoff|TestStopGating|TestFallbackRender|TestMidOwnerCaller'`
**Commit:** `feat(recursion): drive-down settle, stop-gating, fallback, mid-owner caller frames; delete child re-route`

---

## Task 16: counter wiring — reserve/release on all three paths; `tree_at_capacity`; restart rebuild

**Implements:** spec §4 (the running counter, fully wired). Activates the dormant Task 2 counter.

**Files:**
- `agent/subagents.go` (`prepareSubagentRun` — `treeCounter.reserve()` on spawn; release path)
- `agent/job_delegate.go` (`attachDelegateJobFromWatch` + resume siblings — reserve on resume)
- `agent/job_watch.go` / `agent/session_lifecycle.go` (Task 14's drive turn — reserve for the turn's duration)
- release on terminal finalize **and on the abandon path** (`abandonRunningJob`, `agent/jobs.go:357` — the /par-caught leak)
- `tree_at_capacity` error: `"16 delegate jobs running across this session tree. Wait for completions to free slots, job_stop work you no longer need, or narrow your fan-out and retry."` (and the cap text drops "you are notified automatically" per spec §3 — at saturation that's a /par-caught untruth; replaced with "completions free slots; retry")
- restart rebuild: root rebuilds from post-reconciliation state (zero); descendants re-reserve as they re-attach; detached orphan subtree uncounted until resumed (documented v1 looseness)
- `docs/job-control.md` — **carries the caps amendment** (`tree_at_capacity`, counter semantics, shell + retention gaps acknowledged)
- tests: `agent/tree_counter_test.go`, `agent/job_delegate_test.go`

**Red tests (FIRST):**
- `TestCounterReservesOnSpawnResumeDrive`: each of the three paths reserves; finalize and **abandon** release. **Fails before** — counter is dormant (Task 2).
- `TestCounter17thFails`: 17th concurrent running delegate across the tree → `tree_at_capacity`. **Fails before.**
- `TestCounterIdleFreesAndRestartRebuild`: idle (turn-ended) delegate holds no reservation; restart rebuilds from zero. **Fails before.**

**Rollout note (spec §10):** wiring this is the day-one bind — 16 concurrent root delegates now fail loudly even with no grants. This is **disclosed** in Task 17. Land the counter wiring here, the disclosure rides Task 17's docs.

**Gate:** `cd agent && go test ./... -run TestCounter`
**Commit:** `feat(recursion): wire tree counter (spawn/resume/drive reserve, finalize+abandon release); tree_at_capacity`

**Phase 4 gate:** `make test && make lint`; `cd agent && go test ./... -race`.

---

# Phase 5 — Docs, rollout disclosure, e2e cards

## Task 17: contract §8 residue sweep + architecture.md drive-down section + rollout disclosure

**Implements:** spec §8 (any amendment clause not carried by a code task), spec §10 (rollout disclosure).

**Files:**
- `docs/job-control.md` — sweep for any §8 clause not already landed by Tasks 9-16; verify the UNION of spec §8's amendment list against the contract as-is (the max_wait sweep rewrote it first — re-grep)
- `docs/architecture.md` — extend the "Ownership and mailboxes" section (added by the mailbox design, `2026-06-11-...-mailbox-design.md:207`) with drive-down: a parent drives children with undelivered attention; the tree is eventually-driven level by level; the root is driven by serve.go. State the invariant holds at every depth.
- rollout disclosure (spec §10): the counter binds existing single-level fan-out (16 concurrent root delegates fail loudly); delegate-start forwarding changes nested-store contents (invisible to default list); drive-down changes the existing nested-shell case (owner driven instead of parent's model interrupted). Recursion stays dark behind the double opt-in. **Disclose the width-counter day-one bind** where spec §10 says (the contract caps section + a note in the changelog/rollout doc).

**Red/verification:** this is a docs task — the gate is `make lint-docs` (`serf-docscheck`) green + the auditing-documentation pass. Use the `auditing-documentation` skill to confirm the contract matches the landed code. No new Go test, but **assert the doc claims against the code** (grep-verify each amended clause names a real symbol/behavior).

**Gate:** `make lint-docs`; manual auditing-documentation pass.
**Commit:** `docs(recursion): contract §8 residue, architecture drive-down section, rollout disclosure`

---

## Task 18: live e2e coordinator-pattern scenario cards (authoring only)

**Implements:** spec §9 e2e ("coordinator-pattern cards — the 2026-06-11 overnight shape inside serf — with the raised config"), spec §10.

**Files:**
- `test/scenarios/recursion-coordinator-fanout.md` (new) — a root with `MaxSubagentDepth: 2+` grants a coordinator allowance 1; coordinator fans out workers; assertions: workers complete, coordinator is driven (gets its workers' completions in its own turns), root is told only when the coordinator finishes, `job_list(include_descendants=true)` shows the live tree, `job_stop` on the coordinator cascades.
- `test/scenarios/recursion-deaf-coordinator-drivedown.md` (new) — the headline shape end-to-end through the real interface.
- Possibly amend `test/scenarios/subagent-cancel-runaway.md` / `subagent-list-and-output.md` for the cascade/descendant-list behavior (these already exist).

**post-max_wait surface:** every card uses `max_wait_ms` vocabulary, NOT `background`/`block_timeout_ms` (the max_wait sweep, max_wait §6.8, re-runs the full 14-card matrix live first; these new cards must match that vocabulary). Use the `e2e-scenario-testing` skill's card format (falsifiable assertions, freshly-built instance).

**Explicitly out of scope for the implementers:** *running* these cards live. Per the campaign brief, running the coordinator-pattern cards live is left to the **orchestrating session**, not the implementers. This task **authors** the cards with falsifiable assertions and verifies they parse/build; it does not execute them.

**Gate:** card files parse; `make build` green (cards run live by the orchestrator).
**Commit:** `test(recursion): author live coordinator-pattern e2e scenario cards`

**Phase 5 gate:** `make test && make lint`; `cd agent && go test ./... -race`. Then hand off to the orchestrator for the live e2e run.

---

# Questions — resolved by the orchestrating session (2026-06-13, Jesse-vetoable)

Decisions recorded before execution; each is an implementation choice within
the spec's semantics, with rationale. Veto any of these and the affected tasks
re-plan.

1. **max_wait sequencing → split rule** (amended 2026-06-13 after Jesse asked
   for the maximum safe early start; supersedes the original "Phase 0 BLOCKS"):
   - **Tasks 1-8 MAY run pre-max_wait** in the isolated branch
     `wip/recursion-early`, because they are dark by design and touch no
     tool-boundary surface. Conditions, all binding: (a) ZERO edits to
     `agent/internal/tool/definitions.go`, `docs/job-control.md`,
     `test/scenarios/`, or prompt text — a task that seems to need one is
     mis-scoped: stop and surface; (b) the branch REBASES onto the
     post-max_wait, fully-gated `job-control-spec` HEAD before merging
     (T1's `job_delegate.go` allowance-carrier diff stays minimal and
     region-confined for this reason); (c) tests in this tranche may
     construct internal structs whose fields (`delegateArgs.Background`
     and kin) the post-merge dead-field sweep may delete — the sweep
     commit updates those tests; this is expected friction, not breakage.
   - **Tasks 9-18 BLOCK** on max_wait merged + independently verified +
     live matrix green. The planner's CRITICAL stop-rule stays in force
     for any edit that would touch the tool surface.
2. **Counter reservation → AFTER the deliverable-attention check.** Reserve
   immediately before launching an actual render/resume of the child, never
   for the signal-read traversal or a no-op pass. At capacity the child's
   ledger simply stays queued and the next boundary retries — the ledger is
   durable, boundaries recur, so no starvation and no capacity burned on
   stale tokens. (Spec §4's counter counts "concurrently running delegate
   jobs"; a no-op pass runs nothing.)
3. **"Latest record" ordering key → durable append order.** The jobstore is
   append-only and the mailbox §3 invariant already reasons in append order;
   wall-clock keys invite skew bugs and terminal-generation identifies
   terminal events, not total order. Resume races resolve to the record
   appended last.
4. **Headline red mechanism → skip-then-unskip with evidence.** Task 3's
   implementer must RUN the test unskipped, capture the red output in its
   task report, then land it with `t.Skip("RED until drive-down — plan T14")`.
   Task 14's first act: unskip, re-show red, implement, green. Skips are
   visible in test output and tracked by the plan checkbox — explicit,
   never silent. Build tags hide too well.
5. **Per-level retention → document-only for v1.** The disclosure rides
   Task 17 next to the counter-bind disclosure. Per-level reduction is
   deferred-by-design with the trigger: observed retention pressure in the
   coordinator e2e cards or live use.
6. **ParentCanWatch → mostly contract-text catch-up + one small code
   deliverable.** No `ParentCanWatch` code symbol exists; one-hop watch
   resolution already enforces the narrow behavior. Task 13's watch portion
   shrinks to: the contract flowchart/text amendment PLUS a guidance error
   for non-visible deep watch targets ("delegate the watching") replacing the
   bare `target_not_found`.

## Original questions (verbatim, for the record)

### Questions as raised by the planner (spec genuinely leaves open / surface contradictions)

1. **Sequencing hard-dependency on max_wait.** This plan assumes the `max_wait_ms` unification (`2026-06-13-max-wait-unification.md`) lands on `job-control-spec` **before** Task 1 begins (it's "sequenced before PRI-2204"). As of this plan it has NOT landed (no `max_wait_ms` in code). **Should the orchestrator block Phase 0 until max_wait is green, or is there a planned overlap window?** Every prompt/schema/card edit here is written in post-max_wait vocabulary; if max_wait slips, Tasks 9, 12, 18 collide with the old surface.

2. **Drive-turn counter reservation vs. the `EntryNotification` no-op-pass.** Spec §3 says the drive turn reserves the §4 counter "for the turn's duration" and §4 says reserve on "drive turns." But the drain-tail's `EntryNotification` gate (`session_lifecycle.go:403,421`) bounds drive to "one no-op pass" when there's nothing to render. **If a drive turn reserves a counter slot but then renders nothing (stale token), is that a wasted reservation that could starve the tree under churn?** The spec doesn't say whether the reservation is taken before or after the "is there anything to deliver?" check. I read it as: reserve only when actually launching a render turn, not for the signal-read traversal — but this is load-bearing and I won't decide it.

3. **"Latest record for that child session" under one-session-many-records.** Spec §3 stop-gating pins "latest record for that child session" to resolve the one-session-many-records ambiguity (resume mints a fresh job_id for the same child, dossier §2 `:1098-1100`). **Is "latest" by `StartedAt`, by terminal-generation, or by record append order?** These can disagree across a resume race. The spec names the rule but not the ordering key.

4. **Task 3 RED-test landing mechanism.** The headline regression must be red against today but the campaign runs phase gates green between phases. I propose `t.Skip(...)` with the body compiled (Task 14 un-skips). **Is the skip-then-unskip pattern acceptable, or do you want a build-tagged genuinely-failing test excluded from the gate?** (The mailbox precedent says "re-anchor, never delete" but doesn't pick the green-CI mechanism.)

5. **Memory bound at depth (per-level retention multiplication).** Spec §4 acknowledges `maxRetainedTerminal=128` is per-level, so deep trees multiply retained live Sessions (~128^depth worst case), documented as a bound-gap. **Under the double opt-in with `MaxSubagentDepth` typically ≤ 2-3, is documenting-only acceptable for v1, or do you want a per-level retention reduction (the named cheap fix) in scope?** The spec defers it; I'm flagging that a depth-3 e2e card (Task 18) could surface it under real fan-out.

6. **`ParentCanWatch` contract amendment vs. already-landed mailbox behavior.** The mailbox design already made concrete watch targets own-jobs-only (`target_not_watchable` for child-owned, dossier §5). The recursion spec §8 amends the contract's ParentCanWatch flowchart (`:1096`) to "own-jobs + delegate-the-watching." **Is there code left to change here (Task 13), or is this purely a contract-text catch-up to behavior the mailbox campaign already shipped?** If purely text, Task 13's watch portion is docs-only and I've over-scoped it; please confirm.
