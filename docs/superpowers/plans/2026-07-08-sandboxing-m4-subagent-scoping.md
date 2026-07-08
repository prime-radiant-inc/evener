# Serf Sandboxing — M4: Subagent / Worktree Sandbox Scoping

> **For agentic workers:** Implement with superpowers:subagent-driven-development,
> task-by-task, red→green→adversarial-verify→commit. Follow the SDD protocol in
> `2026-07-08-sandboxing-m0-master.md`. Design source:
> `docs/superpowers/specs/2026-07-08-sandboxing-design.md` (v4) — seam #5
> (Subagents/worktrees) and the M4 milestone bullet.

**Goal:** Make the `SandboxPolicy`/`ResolvedPolicy` **follow a session across every
env re-root** — subagent spawn, delegate isolation lanes, delegate resume after a
serf restart, and interactive `manage_worktree` switches — **re-rooted to the
child's/target's worktree with fresh gitdir resolution**, exactly the way
`EnvPolicy` is carried today but with the one crucial difference that the sandbox's
resolved roots are worktree-dependent and must be *recomputed*, never copied. After
M4: a delegate in its own worktree is confined to THAT worktree (not its parent's,
not a sibling's); the policy is captured into the delegate descriptor, persisted,
and re-applied on resume; depth-N children re-root at every level; and the
`manage_worktree` control env carries a control-flavored policy rather than the
current worktree's tool policy.

**Why here:** M5 flips `--sandbox` live on Linux, but only "once M3 *and* M4 pass
e2e together (a sandboxed session can delegate the instant it's visible)"
(master plan). A sandboxed root that delegates into an unscoped child is an escape
hatch — the child would run with the parent's roots, or with none. M4 closes that
before the flag can ship. This is **novel surface** — no vendor publishes subagent
sandbox scoping — so it is carried by explicit tests, not by leaning on a
reference implementation.

**Architecture:** One re-rooting primitive, `(*sandbox.ResolvedPolicy).ReRoot(cwd)`,
is the single point of truth for "same policy, different worktree." It re-runs the
M1 root + gitdir resolution against `cwd` using the mode/net/host-facts/extensions
the policy was resolved from (retained on the resolved value), returning a fresh
`ResolvedPolicy` or a typed `*RefusalError` (fail-closed). Every env re-root funnels
through it: `LocalExecutionEnvironment.WithWorkingDirectory` re-roots the carried
policy (was an inert pointer-copy in M1); the subagent/delegate spawn path captures
the child's policy *inputs* into the prepared run and persists them in the delegate
descriptor beside `LocalEnvPolicy`; the resume path re-resolves those persisted
inputs against the restored worktree + freshly-probed host facts (never the
parent's possibly-drifted config — the session's opted-in policy is immutable across
a restart); and `manage_worktree`'s main-repo control env takes a
`ControlPolicy(mainRepoRoot)` variant (main repo + `.git/worktrees` registry
writable, config/hooks denied). The M1 contract harness is extended with re-root
cases so the child-lane grants are asserted, not assumed.

**Tech Stack:** Go 1.25, stdlib + `golang.org/x/sys/unix`. No new external dep. The
kernel backend (`github.com/landlock-lsm/go-landlock`, bwrap) arrives with M3, which
is already merged on this branch — so M4's **real-confinement e2e** runs against a
live backend, while the **plumbing** (capture/propagate/persist/restore/re-root) is
exercised against M1+M2 with a `FakeProber`. Table-driven `testing`; fuzz the
re-root primitive.

**Prerequisites & branch:** Needs **M3 (kernel enforcement) merged** so real
confinement is testable; the plumbing itself depends only on M1 (policy core +
contract harness) and M2 (file-tool layer). Cut **`wip/sandbox-m4`** from
`wip/sandboxing` *after M3 merges*. Consumes M1's `agent/sandbox` contract harness
(`ContractCase`/`AssertResolve`). Merge back to `wip/sandboxing`; nothing ships (M5
is the human-gated go-live).

**Anchors** (re-verified against the live tree 2026-07-08 — re-verify again before
editing; M1/M2/M3 will have shifted these):
- `agent/execenv/local.go:60` struct (M1 adds `Sandbox *sandbox.ResolvedPolicy`);
  `:92` constructor; **`:123` `WithWorkingDirectory`** — copies `EnvPolicy` at
  `:126`; M4 makes it *re-root* the sandbox policy here (M1 left it an inert copy).
- `agent/subagents.go:226`/`:243` `localEnvPolicyName`/`localEnvPolicyFromName`
  (the name↔enum round-trip to mirror). `prepareSubagentRun` **`:333`**; env
  re-root **`:528`** (`subEnv := s.currentEnv()`) → **`:531`**
  (`le.WithWorkingDirectory(workingDir)`); policy capture into the prepared run at
  **`:657`** (`workingDir`) / **`:658`** (`localEnvPolicy`). Mirror the capture
  here with the sandbox snapshot.
- `agent/job_delegate.go`: **`restoreDelegateChildEnvironment` `:932`**; the
  re-root+override pattern to mirror — `clone := le.WithWorkingDirectory(workDir)`
  **`:960`**, `clone.EnvPolicy = policy` **`:961`**; the restore-conversion helper
  `delegateRestoreLocalEnvPolicy` `:1027`→`localEnvPolicyFromName(desc.LocalEnvPolicy)`
  **`:1031`**; validity gates `hasValidDelegateRestoreLocalEnvPolicy` `:1017` /
  `hasValidDelegateRestoreWorkingDir` `:1022`, consumed by
  `validateDelegateRestoreState` at `:648`/`:651`.
- Descriptor persist — **TWO builders**: initial
  `desc.WorkingDir = prepared.workingDir` **`:1801`** /
  `desc.LocalEnvPolicy = prepared.localEnvPolicy` **`:1802`** /
  `desc.Isolation` `:1803`; **and** the resume-path
  `resumedDelegateRestoreDescriptor` at `:1818` which re-copies `WorkingDir`
  **`:1846`** / `LocalEnvPolicy` **`:1847`** from `previous`. Both must carry the
  sandbox field (the hint named only the initial `:1801-1802`).
- `agent/internal/jobstore/record.go:62` `DelegateRestoreDescriptor` —
  `LocalEnvPolicy string` at `:86`; `*Snapshot` house pattern at `:134`
  (`WatchConfigSnapshot`). Add the sandbox descriptor field here.
- `agent/session_env_swap.go:17` `swapEnvAndRefresh` (installs a
  `WithWorkingDirectory`-built env — inherits the re-root for free).
- `agent/session_tools_worktree.go`: **`worktreeControlEnv` `:434`** (re-root to
  main repo at `:439`; the doc comment the hint's `:429` points into) —
  needs the **control** policy, not the tool policy; **`enterWorktree` `:490`**
  (rides `WithWorkingDirectory` re-root); **`exitWorktree` `:519`** (restores the
  saved prior env — its policy is already correct, no re-root);
  `reacquireDelegateWorktreeLock` `:979` builds two control envs (`:984`, `:989`).
- Restore call site `job_delegate.go:839`; isolation-deny-survives-restore
  precedent `agent/session_init.go:735`.
- Validation e2e extends `cmd/serf-hub/sandbox_test.go`'s containment invariant.

## Global Constraints

- **Re-root, never copy.** The M1 `Sandbox` field on the env is a *resolved* policy
  whose roots are anchored at the parent's worktree. Copying that pointer to a child
  at a different worktree is a containment hole. Every env re-root must recompute the
  roots via `ReRoot(cwd)` (or `ControlPolicy` for the control env). `off` (nil
  policy) stays a copy of nil — byte-identical to today.
- **Fail closed on re-root.** A re-root that the host can't satisfy (e.g. a
  `restricted` policy re-rooted onto a main checkout on a Landlock-only host)
  returns a typed refusal that **surfaces** — a delegate spawn errors, a resume is
  marked not-resumable, a worktree switch is refused — it is never silently dropped
  or downgraded. `off` is exempt (no policy → no re-root → no refusal).
- **Immutable across restart.** Resume re-resolves the delegate's *persisted* policy
  inputs, not the parent's current config. A config that loosened between serf runs
  must not loosen a live delegate's confinement (spec: policy "immutable for the
  session's lifetime — no tool call can relax it").
- **Mirror `EnvPolicy` exactly** for the capture→propagate→persist→restore path
  (subagents.go `:658`, job_delegate.go `:1802`/`:1847`/`:961`), so the two travel
  the same seam and can't drift apart.
- **Plumbing vs. enforcement.** Tasks 1–5 assert *policy values* (roots/refusals)
  and are green on M1+M2 with a `FakeProber`. Task 6 asserts *real kernel
  confinement* and requires the M3 backend present on this branch.
- **snake_case** for the new descriptor JSON keys; `make lint` (serf-namingcheck) is
  a gate. Never `git add -A` without a prior `git status`.

## File Structure

- `agent/sandbox/reroot.go` (new) — `(*ResolvedPolicy).ReRoot(cwd string)
  (*ResolvedPolicy, error)`: re-runs root + `gitdir:` resolution against `cwd`
  (fresh linked-worktree main-`.git` read grant, submodule/worktree-config
  protection recomputed for the target lane) using the mode/net/host-facts/user-
  extensions **retained on the resolved policy** (add retained inputs to
  `ResolvedPolicy` if M1 did not keep them); returns a typed `*RefusalError` when
  the target+host can't satisfy the mode+net. Plus `ControlPolicy(mainRepoRoot
  string) (*ResolvedPolicy, error)` — the `manage_worktree` control variant (main
  repo + `.git/worktrees` registry writable; `.git/config`/hooks denied).
- `agent/sandbox/reroot_test.go` (new) — unit + contract extension: re-root each
  (mode × host row) cell to a linked worktree and assert the child-lane roots via
  the M1 harness; sibling lanes yield disjoint roots; `ControlPolicy` grants the
  registry but denies config/hooks; a `FuzzReRoot` (arbitrary cwd + retained
  inputs) never panics, never widens beyond the mode's grants, never returns a
  denylisted/pseudo-fs path, refusals are typed.
- `agent/sandbox/contract.go` (modify, small) — add `ReRootCase{From, ToCwd, Host,
  WantResolved, WantRefusal}` + `AssertReRoot(t, ...)` so M6 (Seatbelt) satisfies
  the same re-root semantics. Keep the cases data-only.
- `agent/execenv/local.go` (modify) — `WithWorkingDirectory` (`:123`) re-roots
  `Sandbox` via `ReRoot(dir)`; on refusal it stores a sticky
  `sandboxReRootErr` on the returned env (infallible signature preserved for the
  wide caller set) exposed via `SandboxReRootError() error`. nil policy → nil
  result, nil error (off no-op).
- `agent/subagents.go` (modify) — add `sandboxSnapshot *jobstore.SandboxSnapshot`
  (or `*sandbox.SandboxPolicy`) to `preparedSubagentRun`; capture it beside
  `localEnvPolicy` (`:658`) from `subEnv`'s re-rooted policy; after `:531` surface
  `subEnv.SandboxReRootError()` as a spawn error.
- `agent/job_delegate.go` (modify) — persist the snapshot in **both** descriptor
  builders (`:1802`, `:1847`); in `restoreDelegateChildEnvironment` (`:932`)
  re-resolve it against `workDir` + freshly-probed host facts and set
  `clone.Sandbox` (mirroring `clone.EnvPolicy = policy` at `:961`); add
  `delegateRestoreSandbox`/`hasValidDelegateRestoreSandbox` (mirror `:1017`/`:1027`);
  add `notResumableSandboxUnsatisfiable` and gate it in restore.
- `agent/internal/jobstore/record.go` (modify) — `Sandbox *SandboxSnapshot` on
  `DelegateRestoreDescriptor` (snake_case) + a `SandboxSnapshot` struct mirroring
  the child policy's resolved *inputs* (mode, net, extra writable/read roots,
  denylist add/remove) — the `WatchConfigSnapshot` house pattern, decoupling the
  durable schema from `sandbox.SandboxPolicy`.
- `agent/session_tools_worktree.go` (modify) — `worktreeControlEnv` (`:434`) and
  `reacquireDelegateWorktreeLock`'s control env (`:989`) set the `ControlPolicy`
  variant; `enterWorktree`/`exitWorktree` need no policy code (they ride
  `WithWorkingDirectory` / restore a saved env).
- Tests: `agent/sandbox_delegate_test.go` (new) — the plumbing suite (capture,
  re-root, persist, resume, depth, cross-lane); `cmd/serf-hub/sandbox_test.go`
  (extend) — the real-confinement escape suite.

## Task 1 — `ReRoot` + `ControlPolicy` primitives (agent/sandbox)

**Files:** `agent/sandbox/reroot.go` (new), `reroot_test.go` (new),
`contract.go` (modify).

- [ ] **Failing test** (`reroot_test.go`): using the M1 `FakeProber` rows and
  real temp git repos (a main checkout + two linked worktrees, no mocks) —
  `TestReRootToLinkedWorktree` (roots re-anchor to the target lane, gitdir read-
  grant points at the *target's* main `.git`, protected config set recomputed),
  `TestReRootSiblingLanesDisjoint` (two lanes → non-overlapping writable roots),
  `TestReRootRefusesUnsatisfiable` (`restricted` re-rooted onto a main checkout on
  the Landlock-only row → typed `*RefusalError`), `TestControlPolicyGrantsRegistry`
  (main repo + `.git/worktrees` writable; `.git/config`/hooks denied),
  `TestReRootOffIsNil` (nil resolved policy → nil, no error). Add the golden
  `[]ReRootCase` and call `AssertReRoot`.
- [ ] Implement `ReRoot` and `ControlPolicy` reusing the M1 root/gitdir resolver;
  retain the resolve inputs on `ResolvedPolicy` if absent.
- [ ] **Fuzz** `FuzzReRoot` (arbitrary cwd + retained inputs): never panics; a
  returned policy never lists a denylisted/pseudo-fs path and never grants beyond
  the mode's floor; refusals typed.
- [ ] Adversarial verify (does re-root actually recompute gitdir, or does it leak
  the source root's grants? does a sibling ever see another lane?). Fix, commit.

## Task 2 — `WithWorkingDirectory` re-roots the policy (execenv)

**Files:** `agent/execenv/local.go` (modify), `agent/execenv/local_sandbox_test.go`
(new or extend the M1 plumbing test).

- [ ] **Failing test:** an env carrying a `restricted` `ResolvedPolicy` rooted at
  worktree A, `.WithWorkingDirectory(B)` → the returned env's `Sandbox` roots are
  anchored at B (recomputed, not A's); `SandboxReRootError()` is nil.
  A re-root the host can't satisfy → `Sandbox` nil and `SandboxReRootError()`
  non-nil (sticky, legible). **Regression:** a nil-policy env (`off`) →
  `WithWorkingDirectory` byte-identical to today, `SandboxReRootError()` nil (assert
  an existing execenv test still passes unchanged).
- [ ] Implement the re-root in `WithWorkingDirectory` (`:123`) alongside the
  `EnvPolicy` copy at `:126`; add the sticky error field + `SandboxReRootError()`.
- [ ] Adversarial verify (grep every `WithWorkingDirectory` caller — does any
  sandbox-aware site now silently inherit a refusal? is `off` provably untouched?).
  Fix, commit.

## Task 3 — Live subagent / delegate propagation (capture + surface)

**Files:** `agent/subagents.go` (modify), `agent/sandbox_delegate_test.go` (new).

- [ ] **Failing test** (real temp git worktrees, `FakeProber`, no mocks): a
  sandboxed parent spawns a delegate into an isolation lane →
  `prepared.workingDir == lane` **and** the child session's env `Sandbox` is rooted
  at the lane, disjoint from the parent's roots; **cross-lane** — two sibling
  delegates get disjoint writable roots and neither's roots contain the other's
  lane; **depth inheritance** — a depth-2 grandchild delegate is re-rooted at its
  own lane (policy survives two `WithWorkingDirectory` hops);
  a re-root refusal at spawn returns a legible tool error (not a silent unscoped
  child).
- [ ] Capture the child's sandbox snapshot into `preparedSubagentRun` beside
  `localEnvPolicy` (`:658`), read from `subEnv`'s re-rooted policy; surface
  `subEnv.SandboxReRootError()` after the `WithWorkingDirectory` at `:531`.
- [ ] Adversarial verify (is the captured snapshot the child's *inputs*, so resume
  can re-resolve — not the parent's roots? does a non-sandboxed spawn stay a no-op?).
  Fix, commit.

## Task 4 — Descriptor persistence + resume re-resolution

**Files:** `agent/internal/jobstore/record.go` (modify), `agent/job_delegate.go`
(modify), `agent/sandbox_delegate_test.go` (extend).

- [ ] **Failing test:** spawn a sandboxed isolation delegate, marshal its
  `DelegateRestoreDescriptor`, drop the live env (simulated serf restart), rebuild
  a fresh parent env, `restoreDelegateChildEnvironment` → the restored child env's
  `Sandbox` is re-resolved and rooted at the persisted lane with fresh gitdir
  resolution; a JSON round-trip preserves the snapshot (snake_case keys). **Fail
  closed:** restore on a host that can no longer satisfy the mode →
  `notResumableSandboxUnsatisfiable`, not a downgraded/unscoped resume.
  **Immutability:** a snapshot with a tighter denylist than the parent's current
  config still restores tight (config drift ignored).
- [ ] Add `SandboxSnapshot` + the descriptor field; persist in both builders
  (`:1802`, `:1847`); re-resolve in `restoreDelegateChildEnvironment` and set
  `clone.Sandbox` after `clone.EnvPolicy = policy` (`:961`); add the validity gate
  (mirror `hasValidDelegateRestoreLocalEnvPolicy`) into `validateDelegateRestoreState`
  and the new not-resumable reason.
- [ ] Adversarial verify (does the resume path re-probe host facts fresh, or replay
  a frozen resolved blob? do BOTH descriptor builders carry it — the resumed-turn
  one at `:1847` is easy to miss? is the snapshot decoupled from the live type?).
  Fix, commit.

## Task 5 — Worktree swap + `manage_worktree` control policy

**Files:** `agent/session_tools_worktree.go` (modify),
`agent/session_env_swap.go` (assert-only), `agent/sandbox_delegate_test.go`
(extend).

- [ ] **Failing test:** a sandboxed root session enters a managed worktree via
  `enterWorktree` → its env `Sandbox` re-roots to that worktree; `exitWorktree`
  restores the pre-worktree env with its original roots (no re-root, no refusal);
  `worktreeControlEnv(mainRepoRoot)` yields an env whose `Sandbox` is the
  **control** policy — `.git/worktrees` registry writable, `.git/config`/hooks
  denied — *not* the current worktree's tool policy; assert
  `swapEnvAndRefresh` installs a re-rooted env (invariant guard).
- [ ] Point `worktreeControlEnv` (`:439`) and `reacquireDelegateWorktreeLock`'s
  control env (`:989`) at `ControlPolicy(mainRepoRoot)`; leave enter/exit riding
  `WithWorkingDirectory`/restore.
- [ ] Adversarial verify (can the control env write `.git/config` or a hook via the
  registry grant? does an in-worktree tool op stay confined to the worktree after a
  switch?). Fix, commit.

## Task 6 — Adversarial escape suite (real confinement, needs M3)

**Files:** `cmd/serf-hub/sandbox_test.go` (extend).

- [ ] **Failing test** (live M3 backend on this branch; skip-guarded if the host
  lacks bwrap, mirroring the M3 e2e guards): a **sandboxed parent delegates** →
  the child *process* cannot read or write the parent's worktree nor a sibling
  delegate's worktree (kernel-enforced, both file-tool and spawned-shell layers);
  **resume after restart** — a restored delegate re-applies confinement to its lane
  (read/write outside → denied); **net inheritance** — a `net=off` parent's delegate
  is also `net=off` (spawned egress denied); **depth** — a grandchild is confined to
  its own lane. These extend `sandbox_test.go`'s containment invariant and are the
  named-deliverable escape tests for M4 (spec Validation: "delegate resume after
  serf restart", cross-lane isolation).
- [ ] No implementation change expected — this validates Tasks 1–5 end to end. Any
  failure is a real hole; fix at its root, re-run.
- [ ] Adversarial verify (are these asserting *kernel* denial, not just policy
  values — i.e. would they pass on M1+M2 alone, which would mean they test nothing?).
  Fix, commit.

## Done criteria

- `cd <worktree> && make test-short && make vet && make lint` clean.
- `go test ./agent/sandbox/... ./agent/... ./cmd/serf-hub/...` green incl. the
  fuzz seed corpus and the skip-guarded e2e escape suite.
- The re-root contract cases are exported (`ReRootCase`/`AssertReRoot`) for M6.
- A sandboxed delegate is confined to its own worktree (not parent's/sibling's);
  the policy is captured, persisted in the descriptor, and re-resolved on resume
  after a simulated restart; depth-N children re-root at each level; the
  `manage_worktree` control env carries the control policy; `off` is a verified
  no-op across every re-root path.
- Merge `wip/sandbox-m4` → `wip/sandboxing`; tick M4 in the M0 status ledger; report.
