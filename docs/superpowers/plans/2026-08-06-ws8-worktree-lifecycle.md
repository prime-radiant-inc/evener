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
- Nothing unmanaged is ever *implicitly* removed or pruned; adoption is
  explicit only. **Ruled 2026-08-07 (Jesse):** the design record's
  "refuse *without* force" governs — `remove force:true` may remove an
  unmanaged worktree, because explicit `force` is the anti-implicit-
  destruction line. Shipped behaviour is correct; no code change. `prune`
  never touches an unmanaged worktree.
- TDD; real git worktrees in tests (the package's existing fixtures), no
  mocks; multi-module gates per commit, exit codes only. Gates must include
  `make lint` — build+test alone does not compile `//go:build serffuzz`
  sources, and a broken compile floor once survived a full review round.

---

### Task 1 (RE-SCOPED 2026-08-07): A resumed session can dispose its own idle lane

**The original task's premise was false.** It assumed resume mints a new
session id and that lane ownership therefore breaks. It does not: resume
preserves `meta.ID` (`agent/session_init.go`, `id: meta.ID`), and
`cmd/serf-hub/spawn.go` documents that as load-bearing — a fresh id is
minted only by `/clear`. The ownership gate already passes for a resumed
session, so the prescribed Step-1 failing test could not be written. The
prescribed fix — re-stamping `ParentSessionID` on inherited descriptors —
would have rewritten *forwarded descendant* descriptors
(`agent/jobs_nested.go`), letting an ancestor dispose a live descendant's
lane, contradicting the record's ownership scoping. Reported
BLOCKED_ON_DECISION rather than coded around; the final whole-branch
reviewer concurred the refusal was correct.

**Ruled 2026-08-07 (Jesse): fix the real defect instead.** What actually
blocks a resumed session from disposing its own lane is the QUIESCENCE
gate, not ownership: `armPendingTerminalNotifications` re-arms terminal
delegate records to `NotifyPending` on resume, and `delegateRecordQuiescent`
then refuses with "still has running or undelivered work". The gate must
recognize resumed-session ownership rather than blocking unconditionally.

This is not cosmetic: it sits directly under the Task-2 cascade. In a
resumed session, `remove force:true` finds the lanes *eligible* at step 4
(ownership passes), so no dispose hint is emitted; steps 5-7 run, including
the env swap and unlock; then the cascade fails on quiescence — a refusal
that has already moved the session out of the worktree and unlocked it.

**Binding:** the ownership-scoping record still governs. The fix must NOT
let an ancestor dispose a live descendant's lane, and must not weaken the
"running jobs block removal, always" rule. A genuinely un-quiesced lane —
real running work, or a terminal event a *different* live session has not
consumed — must still refuse.

**Files:**
- Investigate + modify: `agent/jobs.go`
  (`armPendingTerminalNotifications`, `rearmTerminalNotificationDecision`
  ~:1918), the `delegateRecordQuiescent` predicate, and the dispose path in
  `agent/session_tools_worktree_dispose.go`
- Test: resume-then-dispose integration test using real git worktrees

- [ ] **Step 1 (investigate):** establish precisely why a resumed session's
  own terminal delegate records are non-quiescent, and whether the state
  clears on its own after the resumed session's first turn (the earlier
  investigation left this unverified). Record the finding in the report —
  if it self-clears, the fix may be narrower than expected.
- [ ] **Step 2 (failing test):** create a lane, drive its delegate to
  terminal, resume the session, call `manage_worktree dispose` for that
  lane — currently refused as "still has running or undelivered work";
  assert it succeeds post-fix.
- [ ] **Step 3:** make the quiescence predicate recognize that the
  resumed owner is the session being asked to dispose. Pin the negatives
  with tests: a lane with genuinely running work still refuses; a
  descendant's live lane is still not disposable by an ancestor; another
  session's undelivered terminal event is still respected.
- [ ] **Step 4:** confirm the Task-2 force cascade now succeeds end to end
  in a resumed session, and that a cascade which still refuses does so
  *before* the step-7 env swap and unlock — no partial mutation on a
  refusing call.
- [ ] **Step 5:** gates (including `make lint`); commit
  (`fix(worktree): a resumed session can dispose its own quiesced lane`).

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

- A resumed session disposes and removes its own lanes end to end (Task 1
  re-scoped 2026-08-07 to the quiescence gate; see the task for why).
- `remove force:true` on an idle-lane-blocked tree succeeds; on a
  running-job tree, refuses.
- A raw-git worktree under the managed root is visible, adoptable, and
  afterwards fully managed.
- No steering or bundled prompt can name an unregistered tool (pinned by
  the guard tests + the audited-sites list).
