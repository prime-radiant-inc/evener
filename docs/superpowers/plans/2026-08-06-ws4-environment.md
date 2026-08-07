# WS4: Environment and Sandbox Provisioning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Sessions stop discovering the sandbox by failing at it: developer
PATH is preserved, scratch vars always exist, go/git work in every mode,
hooks and MCP servers run everywhere, and the session prompt states the
resolved capabilities up front.

**Architecture:** All items implement rulings recorded in
`docs/superpowers/plans/2026-08-06-agentic-ux-remediation.md` §WS4 (audited
against `docs/sandboxing.md` and `docs/environment.md`; Jesse ruled the two
open questions 2026-08-06). The sandbox contract's invariants are binding:
caches never poisoned, git config/hooks read-only, the prompt banner never
overstates.

**Tech stack:** Go. Modules: `agent` (execenv, sandbox, session_prompts).
Anchor symbols were verified 2026-08-06 — trust symbol names over line
numbers and read the real code first.

## Global Constraints

- The sandbox invariants are untouchable: a sandboxed session can never
  poison a cache a later build consumes; git config and hooks stay
  read-only; the prompt banner renders only resolved facts.
- Secret-name filtering (`sandbox.IsSecretEnvName`) and the env floor's
  credential drops are unchanged by every task.
- TDD per behavior; integration tests use real sandboxed execution (both
  `workspace-write` and `restricted` where the behavior differs), no mocked
  sandbox.
- Multi-module gates before every commit: `go build ./...` and
  `go test ./...` in the agent module and root, exit codes only — never
  read `$?` after a pipe; redirect to a file.
- Smallest reasonable change; match surrounding style; no drive-by
  refactors.

---

### Task 1: Developer PATH and always-on scratch vars

**Files:**
- Modify: `agent/execenv/local.go` (`commandEnvironment` ~:257-263, `filteredEnvWithSource`/`filteredEnvFrom` ~:1684-1743)
- Modify: the session/daemon launch path that constructs `LocalExecutionEnvironment` (locate the construction site; add login-PATH resolution there)
- Modify: `agent/sandbox/env_floor.go` (`ApplySessionScratchEnv` ~:83-99)
- Test: `agent/execenv/local_env_test.go` (new or extend existing env tests)

Two behaviors (kata 31gh plus the scratch contract):
- [ ] **Step 1 (failing test):** construct the exec env from a parent
  process whose PATH lacks `/opt/homebrew/bin` while the login shell
  provides it; assert the spawned command env's PATH contains the
  login-shell entries. Second test: `SERF_SCRATCH_DIR` and `TMPDIR` are
  present in the command env for an UNSANDBOXED session.
- [ ] **Step 2 (implement PATH):** resolve the user's login-shell PATH once
  per daemon/session launch — `exec.Command(os.Getenv("SHELL"), "-lc",
  "echo $PATH")` with a short timeout, cached; on failure fall back to the
  process PATH unchanged (never block launch on this). Merge: login-shell
  PATH wins for the PATH variable only; everything else flows through the
  existing filter untouched.
- [ ] **Step 3 (implement scratch):** provision a per-session scratch dir
  unconditionally (reuse the sandbox scratch location convention) and
  export `SERF_SCRATCH_DIR`/`TMPDIR` in `commandEnvironment` for
  unsandboxed sessions too, matching `docs/environment.md`'s contract
  ("Serf-provided private scratch directory for one live session" — no
  sandbox-only caveat). Sandboxed behavior unchanged.
- [ ] **Step 4:** tests green; gates; commit
  (`feat(execenv): preserve developer PATH and always export session scratch vars`).

### Task 2: GOMODCACHE redirection (telemetry: accept + document)

**AMENDED 2026-08-06.** The original task ordered `GOTELEMETRY=off` in
`ApplyEnvFloor`. Implementation proved the premise false: `GOTELEMETRY` is
**not a settable Go environment variable** — the Go toolchain only *reports*
it from the persisted telemetry-mode file (written by `go telemetry <mode>`),
so setting it in the child environment changes nothing. Verified empirically
in a real sandbox, not by reading docs.

**Jesse's ruling (2026-08-06): accept and document. Ship no telemetry
change.** Rationale: the remaining artifact is one harmless stderr line, and
every mechanism that would actually silence it (a per-session `HOME`, a
writable grant for the Go config dir, or running `go telemetry off` on the
user's behalf) either mutates user state serf does not own or widens the
sandbox — both worse than the noise.

**Files:**
- Modify: `agent/sandbox/env_floor.go` (`isRedirectedCacheVar` ~:116-118)
- Test: `agent/sandbox/env_floor_test.go`

- [ ] **Step 1 (verification, may produce a fix):** with a custom GOPATH,
  confirm `GOMODCACHE` resolves inside a granted cache root under both
  cache strategies; if not, extend `isRedirectedCacheVar` and pin with a
  test. *(Result: a real gap was found and fixed — this is now the whole
  code change for this task.)*
- [ ] **Step 2 (no telemetry change):** do not set `GOTELEMETRY` anywhere.
  Remove any `GOTELEMETRY` row from the `envvars` registry and from
  `docs/environment.md`, and any claim in `docs/sandboxing.md` that the
  environment floor sets it.
- [ ] **Step 3:** sandboxed integration test: `go test` on a trivial module
  in a `restricted` sandbox exits 0. Stderr is pristine **except** for the
  one known Go telemetry line (`error acquiring upload token ... operation
  not permitted`), which the test pins as expected-and-explained — asserted
  against, with a comment naming the ruling, never silenced or globbed away.
- [ ] **Step 4:** gates; commit
  (`fix(sandbox): redirect GOMODCACHE into the granted cache root`).

Task 6's capability preamble states the residue as a resolved fact (a short
line such as `go: telemetry writes denied (harmless stderr noise)`) so a
session reads it instead of discovering it.

### Task 3: packed-refs.lock grant granularity

**Files:**
- Investigate then modify: `agent/sandbox/seatbelt_darwin.go`, `agent/sandbox/bwrap.go`, `agent/sandbox/gitdir.go` (`GitLayout.WritablePaths` ~:58-78)
- Test: sandboxed git integration test (extend the existing sandbox git tests)

- [ ] **Step 1 (reproduce):** in a sandboxed scratch repo, drive git into
  packed-refs maintenance (e.g. `git pack-refs --all` or a commit after
  packing) and capture the `packed-refs.lock: Operation not permitted`
  stderr as a failing assertion (test asserts pristine stderr).
- [ ] **Step 2 (diagnose + fix):** determine whether the grant is
  file-exact where git needs the `.lock` sibling created via
  rename-into-place; grant exactly the `.lock` sibling (or the minimal
  pattern covering git's tmp+rename dance for packed-refs). The contract
  (`docs/sandboxing.md`) promises packed-refs writable; config/hooks
  protection must be provably unchanged — add an explicit test that
  `.git/config` and `.git/hooks` writes still fail.
- [ ] **Step 3:** gates; commit
  (`fix(sandbox): allow git packed-refs lock churn per the sandbox contract`).

### Task 4: Hooks and MCP servers work in every sandbox mode

**Files:**
- Modify: `agent/sandbox/resolve.go` (read/exec roots), possibly the hook/MCP launch paths for the summarized-failure line
- Test: restricted-mode integration test executing a script from the plugin-cache path; hook-failure rendering test

Ruling (2026-08-06): hooks and MCP servers are session infrastructure and
must work in all modes.
- [ ] **Step 1 (failing test):** in `restricted` mode, exec a script
  living under the hook/plugin path (test fixture standing in for
  `~/.claude/plugins/...`) — currently denied.
- [ ] **Step 2:** include the session's hook and MCP-server paths
  (resolved from the session's actual hook/MCP config, not a hardcoded
  home glob) in the read/exec surface for all modes. Write surface
  unchanged.
- [ ] **Step 3 (independent UX fix):** a hook that still fails for any
  reason surfaces as ONE summarized warning line ("SessionStart hook
  <name> failed (exit N)") — never raw shell stderr as the session's
  first steering. Locate the hook-failure surfacing path and pin with a
  rendering test.
- [ ] **Step 4:** gates; commit
  (`fix(sandbox): hook and MCP-server paths executable in every sandbox mode`).

### Task 5: Developer toolchain readable in restricted mode

**Files:**
- Modify: `agent/sandbox/resolve.go` (system read roots)
- Test: restricted-mode integration test running `git --version` and a `git commit`

Ruling (2026-08-06): add developer-tools directories read-only to
restricted mode's read roots.
- [ ] **Step 1 (confirm mechanism first — shrunken investigation):** in a
  `restricted` sandbox on this host, run `git --version`; confirm the
  xcrun/CLT failure reproduces and note the exact paths git needs
  (`xcode-select -p`, `/Library/Developer/CommandLineTools`,
  `/Applications/Xcode.app` where present). Record findings in the task
  report. If it does NOT reproduce this way, STOP and report — the
  study sessions' git failures then need a different explanation before
  any grant lands.
- [ ] **Step 2 (failing test):** restricted-mode test asserting
  `git --version` exits 0.
- [ ] **Step 3:** add the developer-tools directories (resolved via
  `xcode-select -p` plus the standard CLT path, read-only) to restricted
  read roots. Then a full `git commit` in a sandboxed scratch repo exits
  0 with pristine stderr.
- [ ] **Step 4:** gates; commit
  (`fix(sandbox): developer toolchain readable in restricted mode`).

### Task 6: Capability preamble

**Files:**
- Modify: `agent/session_prompts.go` (`sandboxPromptLine` ~:223-244 and the environment section)
- Test: snapshot tests rendering ResolvedPolicy variants

- [ ] **Step 1 (failing snapshot test):** render the extended preamble for
  a workspace-write policy and a restricted policy: sandbox mode +
  network, writable roots (`Spawned.WriteRoots`, `Git.WritablePaths`
  summarized), masked-path count, scratch vars, cache mode, and toolchain
  probe results (git works?; go/node/rg on PATH?) as short factual lines.
- [ ] **Step 2:** implement. Probes run once at session start with tight
  timeouts; a probe that cannot run renders as `unprobed`, never as a
  guess — the banner's "never overstates" principle is binding. Values,
  not prose.
- [ ] **Step 3:** unsandboxed sessions get the same section minus the
  sandbox line (PATH source, scratch vars, probes).
- [ ] **Step 4:** gates; commit
  (`feat(session): capability preamble — resolved sandbox facts and toolchain probes`).

### Task 7: GOCACHE wedge investigation (report-only unless trivially fixable)

**Files:**
- Create: report at the SDD workspace (not in docs/) summarizing findings; a plan amendment only if a serf change is warranted

- [ ] **Step 1:** for the wedged sessions (033zIWu0M97TPEmlte5j45,
  0341OD339bdFXqO2JkqNyK, 0340x0n1NdMduPrOuCT1DS), use `serf-doctor`
  (sessions/transcript/jobs) plus their meta to classify: sandboxed or
  not, and the resolved cache strategy at the time.
- [ ] **Step 2:** reproduce the wedge shape if possible (concurrent go
  builds against the identified cache configuration).
- [ ] **Step 3:** report: root cause, whether any serf change is needed
  (any proposal must preserve the never-poison invariant), or whether
  this was host configuration (external-volume GOCACHE) that the Task 6
  preamble now at least surfaces. BLOCKED-on-Jesse if a design change
  seems warranted — do not implement one unilaterally.

## Acceptance (whole workstream)

- A restricted-sandbox session on this host can: run git (version +
  commit, pristine stderr), run `go test` on a trivial module, execute its
  SessionStart hook, and read its own capability preamble stating all of
  that — with zero trial-and-error discovery.
- `SERF_SCRATCH_DIR`/`TMPDIR` present in every session's exec env.
- Config/hooks write-protection provably unchanged.

### Amendment 2026-08-07 — global git config is readable in restricted mode

The first delivery left the git criterion only partly met: the
developer-toolchain grant made the `xcrun` shim resolvable, but `git` still
failed outright on any host with a global config, because that config lives
under `$HOME` and `restricted` granted no home read. The acceptance test passed
only by neutralizing it with `GIT_CONFIG_GLOBAL=/dev/null`, which is not the
criterion — a test that arranges the user's real configuration out of existence
is not evidence that git works for the user.

**Jesse ruled 2026-08-07: grant read-only.** `~/.gitconfig` and
`~/.config/git/config` — the paths git actually consults for global config —
are read-only members of restricted mode's read roots. The
`GIT_CONFIG_GLOBAL` neutralization comes out of the acceptance test, and the
criterion becomes the real one:

- **git works in `restricted` mode with the user's actual global config** —
  `git --version` and a full `git commit`, against the config the developer
  really has, not a blanked-out one.

Unchanged by this amendment: git config and hook **write** protection (the
grant is read-only, and the anti-hook-planting argument depends on the write
denial, not on unreadability), the credential denylist (`~/.git-credentials`
stays masked, so a `credential.helper` line remains readable while the secret
it points at does not), and every other invariant. The capability preamble
reflects the grant rather than the old failure.

### Amendment 2026-08-07 — bwrap parity, and a shipped corruption bug

Task 3 fixed git's packed-refs rewrite on Seatbelt but left the Linux backend
untouched, because bubblewrap grants by bind-mounting and a bind target must
exist: any granted metadata entry absent at sandbox start is skipped, so a
linked worktree's common dir keeps the gap. Attempting the ruled shape (grant
the common dir writable, re-bind `ProtectedPaths` read-only on top) surfaced a
worse problem underneath.

**The shipped bug.** `commondir` and `gitdir` are in `gitProtectedLeaves`. For a
**main checkout** `<cwd>/.git/commondir` does not exist *and* sits under the
worktree write root, so `maskReadOnly` pins it with `--ro-bind /dev/null`.
Bubblewrap materializes that mountpoint on the real filesystem, leaving an empty
`commondir` behind after the sandbox exits — and git treats an empty `commondir`
as fatal, permanently, for every later command in that repo. macOS is
unaffected: Seatbelt matches path strings and creates nothing.

**Empirically confirmed 2026-08-07** on a real Linux host (magic-kingdom, git
2.43), after being reasoned out on macOS where no bubblewrap exists:

- A `--ro-bind /dev/null` over an absent `.git/commondir` materializes a real
  empty file that **persists after the sandbox exits**, and git in that repo is
  then fatal (`failed to read .../commondir`).
- The linked-worktree case is safe: `commondir` exists, is pinned over itself,
  git works inside the sandbox, writes are denied, and the file is intact
  afterwards.

Neither live bwrap test caught it — one never checks git's exit code, the other
expects failure — and CI never installs bubblewrap, so there is no automated
real-bwrap coverage at all.

**Peer behaviour** (for the record, none of them do what serf did): Claude
Code's srt skips absent files, a documented gap; Codex ro-binds the whole
existing `.git` directory; nobody materializes absent files.

**Jesse ruled 2026-08-07: SHAPE C.** Serf pre-creates an absent protected
surface with **inert content** before pinning it — `.` for `commondir`, which is
exactly what a main checkout's common dir means and is the only content git
accepts (empty and directory are both fatal). That fixes the corruption bug and
unblocks the parity grant, at the cost of a side-effecting prep step at sandbox
setup. Generalize only as far as the real protected-surface list requires, and
document why each inert value is what it is.

Rejected: pinning as-is (corrupts repos); not pinning `commondir` (a repointed
`commondir` makes git read an attacker-controlled common dir's config, including
`core.hooksPath` — the exact persistence vector the write denial exists to
close).
