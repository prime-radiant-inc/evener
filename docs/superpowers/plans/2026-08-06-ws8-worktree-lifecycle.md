# WS8: Worktree Lifecycle and Instruction Coherence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** manage_worktree stops failing at its own job — resumed sessions
own their lanes, force means the sanctioned cascade, unmanaged worktrees
get an explicit adoption path — and serf never instructs a tool the
session doesn't have.

**Architecture:** Implements the WS8 section of
`docs/superpowers/plans/2026-08-06-agentic-ux-remediation.md` as audited
against `docs/superpowers/specs/2026-07-02-native-worktree-tools-design.md`
and ruled with Jesse 2026-08-06. The record's ownership-scoping principle
("defaults to not destroying other sessions' work") is binding; every task
below works through it, never around it.

**Tech stack:** Go, `agent` module (session_tools_worktree*,
session_worktree_close, job_delegate descriptors, session_init registry,
session_self_compact) plus `internal/bundled` prompt audits. Anchors
verified 2026-08-06; trust symbol names.

## Global Constraints

- The worktree design record is normative; contradictions escalate to
  Jesse, never get coded around.
- Running jobs block removal, always — no force override for live
  execution.
- Nothing unmanaged is ever removed or pruned; adoption is explicit only.
- TDD; real git worktrees in tests (the package's existing fixtures), no
  mocks; multi-module gates per commit, exit codes only.

---

### Task 1: Lane ownership follows resumed identity

**Files:**
- Modify: the session-resume path that restores delegate lane descriptors (find where `DelegateRestoreDescriptor`s are loaded on resume; re-stamp there), `agent/session_tools_worktree_dispose.go` (`findDelegateLaneRecord` ~:506-531 unchanged in logic — the fix is descriptor state), `agent/session_worktree_close.go` (`ownedIsolationLanes` ~:33-52 benefits automatically)
- Test: resume-dispose integration test

- [ ] **Step 1 (failing test):** create a lane, simulate a session resume
  (new session id restoring the prior session's state), call
  `manage_worktree dispose` for the lane — currently rejected as "not a
  known isolation delegate"; assert it succeeds post-fix.
- [ ] **Step 2:** on resume, inherited lane descriptors re-stamp
  `ParentSessionID` to the resumed identity (durable, via the jobstore
  event path descriptors already use). A genuinely different session
  (not a resume of the creator) still gets the ownership refusal — pin
  with a second test.
- [ ] **Step 3:** close-time sweep (`disposeDelegateLanesAtClose`) now
  also covers resumed lanes — assert via a close-path test.
- [ ] **Step 4:** gates; commit
  (`fix(worktree): lane ownership follows resumed session identity`).

### Task 2: remove force:true disposes retained-idle lanes first

**Files:**
- Modify: `agent/session_tools_worktree.go` (`worktreeRemove` ~:1653, live-work guard ~:1748-1753, `liveWorkUnder` ~:749-807, `disposeHintForRetainedIdle` ~:830-869)
- Test: force-cascade tests

- [ ] **Step 1 (failing tests):**
  (a) tree whose only live-work entries are retained-idle delegate lanes:
  `remove force:true` succeeds by disposing each lane then removing;
  (b) each cascaded dispose keeps its own gates — a lane with unmerged
  work refuses unless the existing dispose force semantics say otherwise
  (follow the current dispose contract exactly);
  (c) a genuinely running job under the tree: `remove force:true` still
  refuses, naming the job.
- [ ] **Step 2:** implement the cascade inside `worktreeRemove`'s
  force path; the non-force behavior and the hint text are unchanged
  except the hint may now also mention `force` performs the cascade.
- [ ] **Step 3:** gates; commit
  (`feat(worktree): remove force disposes retained-idle lanes through the sanctioned path`).

### Task 3: Unmanaged-worktree visibility and explicit adopt

**Files:**
- Modify: `agent/session_tools_worktree.go` (`worktreeList` ~:2019, `managedPorcelainEntries`/`isUnderManagedDir` ~:1280-1321; new `adopt` operation in the op enum + dispatch)
- Modify: the manage_worktree tool definition (operation enum + description)
- Test: adopt round-trip tests

- [ ] **Step 1 (failing tests):**
  (a) a raw `git worktree add` under the managed root: `list` shows it in
  a labeled `unmanaged` section (id-less, path + branch);
  (b) `adopt` with its path creates the sidecar; it then appears managed
  and `switch`/`remove` work on it;
  (c) `adopt` on a worktree outside the managed root is refused;
  (d) nothing auto-adopts: `remove`/`prune` still skip unmanaged entries
  (pin the record's "remove stays managed-only" for un-adopted ones).
- [ ] **Step 2:** implement; the sidecar written by `adopt` records the
  adopting session as creator (ownership scoping then applies normally).
- [ ] **Step 3:** gates; commit
  (`feat(worktree): list surfaces unmanaged worktrees; explicit adopt converts them`).

### Task 4: Subagent compact_context + validate-before-instruct

**Files:**
- Investigate + modify: `agent/session_init.go` (~:999-1019 pruning paths), the subagent default tool surface (`agent/subagents.go`), `agent/session_self_compact.go` (~:81-118)
- Audit + modify: steering templates in `agent/` and role prompts in `internal/bundled` that name tools
- Test: registry-surface and nudge-gating tests

Ruled 2026-08-06: subagents should have `compact_context`; and any canned
instruction naming a tool validates availability first.
- [ ] **Step 1 (investigate):** determine which pruning path removed
  `compact_context` from the study's delegate sessions (denied-tools
  config? subagent default surface? typed-agent tool lists?). Record in
  the task report.
- [ ] **Step 2 (failing test):** a default-surface subagent's registry
  contains `compact_context`; fix the surface accordingly.
- [ ] **Step 3 (failing tests, guard):** with `compact_context` removed
  from a session's registry, the low-context nudge emits the tool-free
  fallback ("summarize and drop stale context in your next messages");
  with it present, the current wording. Implement the general helper
  (steering text builders check `s.reg.Get(name)`), convert the
  self-compact nudge, then sweep `agent/` steering templates and
  `internal/bundled` role prompts for tool mentions — each gets gated
  wording or a registry-conditional variant. List every converted site in
  the task report.
- [ ] **Step 4:** gates; commit series
  (`fix(subagents): compact_context in the default surface` /
  `fix(steering): never instruct a tool the session does not have`).

## Acceptance (whole workstream)

- A resumed session disposes and removes its own lanes end to end.
- `remove force:true` on an idle-lane-blocked tree succeeds; on a
  running-job tree, refuses.
- A raw-git worktree under the managed root is visible, adoptable, and
  afterwards fully managed.
- No steering or bundled prompt can name an unregistered tool (pinned by
  the guard tests + the audited-sites list).
