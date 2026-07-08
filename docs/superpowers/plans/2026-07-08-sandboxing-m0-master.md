# Serf Sandboxing — Master Implementation Plan (sequencing, worktrees, SDD protocol)

> **For agentic workers:** This is the umbrella plan. Each milestone Mn has its
> own `2026-07-08-sandboxing-mN-*.md` plan. Implement a milestone with
> superpowers:subagent-driven-development, task-by-task, red→green→verify.

Design source of truth: `docs/superpowers/specs/2026-07-08-sandboxing-design.md`
(v4). Where a plan and the spec disagree, the spec wins — stop and reconcile.

## Why the order is what it is (dependency graph)

```
M1 (policy core + cross-backend contract tests)   ← gates EVERYTHING
        │
        ├── M2 (file-tool race-safe in-process layer)   ┐  parallel worktrees
        ├── M3 (Linux kernel layer, flag NOT live)       �│  once M1 lands
        └── M6 (macOS Seatbelt backend)                  ┘  (M6 needs a Mac; runs on paradise-park)
        │
        M4 (subagent/worktree scoping)   ← needs M3
        │
        M5 (--sandbox goes LIVE on Linux)   ← GATE: needs M3 AND M4 both green + Jesse review
        │
        M7 (in-UI escalation)   ← last, needs everything
```

**M1 is a hard prerequisite.** M2/M3/M6 all code against the `SandboxPolicy`
type and the contract-test harness M1 produces. Do not start them before M1's
branch is merged into the integration branch. Fanning them out earlier just
produces incompatible copies of the core type.

**Enforcement never goes live implicitly.** The `--sandbox` flag is wired but
inert until M5, and M5 is a human-gated review point, not an autonomous step.
M1–M4 land as reviewable branches; nothing ships.

## Branch & worktree strategy

Integration branch: **`wip/sandboxing`** (cut from `wip/sandboxing-spec`, which
holds the spec + these plans). Each milestone builds on its own branch in its
own worktree, then merges into `wip/sandboxing` after its own review passes.

```
wip/sandboxing-spec   (spec + plans; current)
  └─ wip/sandboxing   (integration; cut from spec branch)
       ├─ wip/sandbox-m1   .worktrees/sandbox-m1   (first; merges to integration)
       ├─ wip/sandbox-m2   .worktrees/sandbox-m2   ┐ cut from integration
       ├─ wip/sandbox-m3   .worktrees/sandbox-m3   �│ AFTER m1 merges
       └─ wip/sandbox-m6   .worktrees/sandbox-m6   ┘
```

A worktree agent operates entirely via **absolute paths** (Read/Write/Edit take
them) and **`git -C <worktree>`** for git — no `cd` needed for file ops. Build
and test run as `cd <worktree> && go test ...` in a single compound command.

Nothing is pushed or merged to `main` in this campaign. Jesse merges to main.

## SDD protocol (every milestone follows this)

1. Read the milestone plan + the spec sections it cites. Re-verify every
   `file:line` anchor against the live file before using it (anchors drift).
2. For each task, in order:
   a. Write the failing test first (red). Test names and intent are given in the
      plan. Real dependencies, no mocks in e2e (Jesse's rules).
   b. Implement the smallest change that makes it green.
   c. **Spawn an adversarial verifier subagent** (Opus) to review the diff for
      correctness, the specific escape/bypass the task defends against, and
      test-quality (does the test actually exercise real logic, not a mock?).
      Fix what it finds; re-run. This is the "subagent-driven" in SDD.
   d. `git -C <worktree> add <exact paths>` (never `git add -A` without a prior
      `git status`) and commit with a Claude-Session trailer.
3. Test output must be **pristine** — captured-and-asserted errors are fine, but
   no stray failures/logs. `cd <worktree> && make test-short` (and `make vet`,
   `make lint`) must be clean before the milestone is declared done.
4. A milestone is done when: all its tasks are green, its adversarial escape
   tests (from the spec's Validation section) pass, and `make test`/`vet`/`lint`
   are clean. Then it merges to `wip/sandboxing` and reports.

## Shared conventions (all milestones)

- Module `primeradiant.com/serf`, Go 1.25. `golang.org/x/sys v0.42` is a direct
  dep (use `unix.Openat2` for M2; no new dep needed there). M3 adds
  `github.com/landlock-lsm/go-landlock`.
- Commands: `make test-short` (fast gate), `make test`, `make test-race`,
  `make vet`, `make lint` (runs `serf-namingcheck` — **all new JSON/wire keys
  MUST be snake_case**), `make build-all`.
- New Go code matches surrounding style; JSON tags snake_case; no backward-compat
  shims without Jesse's explicit OK.
- Every commit ends with:
  `Claude-Session: https://claude.ai/code/session_01ECS898PJinC6he5jBfoUxX`

## The seam (verified 2026-07-08 against the live tree)

Everything funnels through `execenv.ExecutionEnvironment` →
`LocalExecutionEnvironment` (`agent/execenv/local.go:60`). Key anchors (re-verify
before editing — they will drift as milestones land):
- `NewLocalExecutionEnvironment` `local.go:92`; `WithWorkingDirectory` `:123`
  (copies `EnvPolicy` — `SandboxPolicy` rides here for M4).
- `ExecCommand` `:756`; `execPreparedCommand` `:768`; `StreamCommand` `:866`;
  `shellCommand` `:1110` — kernel-wrap sites (M3).
- `resolve` `:1121` (read; passes absolute paths through today — M2 tightens);
  `resolveWrite` `:1136` → `ensureUnderRoot` `:1158` (write confinement today).
- `SessionConfig` `agent/session_config.go:20` (add `Sandbox`/`SandboxNet` — M1).
- Env construction sites: `cmd/serf/run.go:177`, `cmd/serf/serve.go:203` (M1).
- `apply_patch` bypasses execenv today: `agent/session_tools_shell.go:233` →
  `tool.ApplyPatch(env.WorkingDirectory(), patch)` → `os.*` in
  `agent/internal/tool/apply_patch.go` (M2 refactors this).

## Status ledger (update as milestones land)

- [ ] M1 — policy core + contract tests
- [ ] M2 — file-tool race-safe in-process layer
- [ ] M3 — Linux kernel layer (flag inert)
- [ ] M4 — subagent/worktree scoping
- [ ] M5 — flag goes live on Linux (GATE: Jesse review)
- [ ] M6 — macOS Seatbelt (paradise-park)
- [ ] M7 — in-UI escalation
