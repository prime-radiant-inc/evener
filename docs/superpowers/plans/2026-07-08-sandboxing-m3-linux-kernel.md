# Serf Sandboxing — M3: Linux Kernel Layer (flag NOT live)

> **For agentic workers:** Implement with superpowers:subagent-driven-development,
> task-by-task, red→green→adversarial-verify→commit. Follow the SDD protocol in
> `2026-07-08-sandboxing-m0-master.md`. Design source:
> `docs/superpowers/specs/2026-07-08-sandboxing-design.md` (v4). Where a plan and
> the spec disagree, the spec wins — stop and reconcile.

**Goal:** Build the Linux **kernel enforcement layer** — the mechanism that
confines *spawned processes* (shell jobs, `rg`, stdio MCP servers, hook commands
and every descendant) to the boundary M1 resolved. bwrap-preferred with a
Landlock worktree-restricted fallback, both driven by a single re-exec'd helper
(`serf sandbox-exec`) that applies the sandbox to a process and then execs the
real command. After M3, an `execenv` carrying a non-nil `ResolvedPolicy` wraps
every command it spawns with the resolved backend; the M1 contract suite runs
green against the **real** bwrap and Landlock backends, and the adversarial
escape subset (proc aliasing, ld.so indirect exec, net=off, hook-persist,
cache-poison) holds.

**The `--sandbox` flag stays inert and unexposed in M3.** M3 wires and tests
enforcement through **internal/integration construction** of a `ResolvedPolicy`
(a test attaches one to an `execenv`). It does **not** connect the user-facing
flag to enforcement, does not document or advertise the flag, and does not
remove the hiding M1 left in place. A sandboxed session cannot safely *delegate*
until M4 (subagent scoping); making the flag live is M5, a human-gated review
after M3 **and** M4 pass e2e together. If you find yourself printing `--sandbox`
in help, announcing it live, or flipping a default, you've left M3 — stop.

**Why now:** M1 produced the backend-independent `ResolvedPolicy` and the
contract-test harness; M2 produced the in-process race-safe file-tool layer. M3
is the *other* enforcement layer of the spec's two-layer threat model — kernel
policy inherited by all descendants (an `npm install` postinstall is as confined
as its shell), including a fresh PID namespace + private `/proc` so host process
state is invisible. It is the load-bearing containment for arbitrary
model-composed subprocess input.

**Architecture:** A `Backend` abstraction in `agent/sandbox` turns a
`ResolvedPolicy` into a **wrapped argv**. Two backends: `bwrapBackend`
(namespaces + subtractive masking + `--unshare-net` + `--unshare-pid`/`--proc` +
minimal `/dev` + overlay) and `landlockBackend` (additive allowlist,
worktree-restricted-only). Both are reached the same way: serf re-execs **itself**
as `serf sandbox-exec` (arg0/subcommand-dispatched, like codex's
`codex-linux-sandbox`), passing a serialized policy; the helper either execs
`bwrap` with the built argv, or applies a go-landlock ruleset to its own thread
and then `execvp`s the command. The wrap site lives in `execenv` at command
construction; the same wrapper is threaded to the MCP-stdio and hook spawn sites,
which do **not** flow through `execenv` today. We adapt codex's argv-transform
pattern (`create_bwrap_command_args` → `["bwrap", …flags…, "--", cmd…]`, then
`--argv0` fix-up) but **not** its ~2700-line synthetic-mount / protected-create /
proxy-routing machinery: flat writable/read roots + fixed protected-subpath and
denylist sets, no seccomp.

**Tech Stack:** Go 1.25, stdlib + `golang.org/x/sys/unix` (already a dep). M3
adds one external dep, **`github.com/landlock-lsm/go-landlock`** (no userns
dependency — immune to Ubuntu 24.04 `apparmor_restrict_unprivileged_userns`).
Real backend integration tests are build-tagged / capability-gated so unit runs
never require bwrap or a specific kernel. Table-driven `testing`; the M1 golden
contract table is imported and re-satisfied per backend.

## Host facts (verified 2026-07-08 on this machine)

- **bwrap present:** `/usr/bin/bwrap`, bubblewrap **0.9.0**. Unprivileged userns
  works: `kernel.unprivileged_userns_clone=1`,
  `apparmor_restrict_unprivileged_userns=0`.
- **Kernel** `6.8.0-110-generic`; Landlock enabled (LSM list:
  `lockdown,capability,landlock,yama,apparmor`) → **ABI v4** (net-TCP hooks
  present; ioctl-v5 is 6.10, absent). Enough for the FS-only Landlock fallback.
- **Smoke-verified working here:** FS confinement + `--unshare-pid`/`--proc /proc`
  (inside, PID 1 is `bwrap`; host `/proc` invisible), `--dev /dev`,
  `--unshare-net` (DNS/TCP fail), tmpfs-over-secret masking (secret file →
  `No such file or directory`).
- **NOT available here:** `--overlay-src` / `--tmp-overlay` — this bwrap build
  was compiled **without** overlay support (`bwrap --overlay-src … ⇒ Unknown
  option`). So on this host `HostFacts.overlaySupported=false` and M3d's cache
  path **degrades to the session-private redirect** — exactly the spec's
  documented fallback. Overlay-path integration tests must be capability-gated
  and will **skip** here; the overlay path cannot be validated on this machine.

## Anchors (LIVE, re-verify before editing — they drift as M2 lands)

- `agent/execenv/local.go:60` struct `LocalExecutionEnvironment`; `:92`
  `NewLocalExecutionEnvironment`; `:123` `WithWorkingDirectory` (must copy the
  backend/wrapper alongside `Sandbox`).
- `:756` `ExecCommand` → `:757` `execPreparedCommand(shellCommand(command), …)`;
  `:763` `ExecArgv` (direct argv, uses `execCommandContext` `:216`); `:768`
  `execPreparedCommand` (**primary fg wrap site**); `:866` `StreamCommand`
  (**primary bg-job wrap site**). Both set `SysProcAttr{Setpgid:true}` (`:785`,
  `:891`) and `cmd.Env = filteredEnvWithPolicy(...)` (`:786`, `:892`) — the
  fd-hygiene + env-floor points.
- `:1110` `shellCommand` (returns the `bash -c`/`cmd.exe` `*exec.Cmd`; `:1114`
  bash path).
- rg-backed Grep: `:627` `Grep` → `:641` `e.ExecCommand(ctx, rg+" "+shellEscapeArgs(args...), …)`
  → flows through `execPreparedCommand`, so wrapping `execPreparedCommand`
  covers `rg` for free. (`:603` `buildRipgrepArgs` is the pure arg core — mirror
  its test style for the bwrap argv builder.)
- `:1209` `filteredEnvWithPolicy`; `:1244` `filteredEnv` (today's
  `*KEY*/*SECRET*/*TOKEN*/*PASSWORD*/*CREDENTIAL*` drop) — the sandbox env floor
  composes **on top of** this.
- **MCP stdio spawn:** `agent/internal/mcp/manager.go:711` `transportForConfig`,
  `:717` `exec.Command(cfg.Command, cfg.Args...)` wrapped in
  `mcpsdk.CommandTransport`. Reached via `NewManager` (`:136`) →
  `transportForConfig` closure (`:247`). **No policy handle today.**
- **Hook spawn:** `agent/internal/hooks/hooks.go:68` `executeCommandHook`, `:81`
  exec form / `:86` `bash -c` form; `:95` builds env from **`os.Environ()`
  directly** (not `filteredEnvWithPolicy` — see discrepancy #4). Reached from
  `Runner` (`:215`) / `:534`. **No policy handle today.**
- **Subcommand dispatch:** `cmd/serf/main.go:268` `dispatchCLICommand`, `:273`
  the `switch args[0]` (add `case "sandbox-exec"`). `:221` `printRunCommands`
  (do **not** list `sandbox-exec` — it is an internal re-exec target, not a
  user command).
- **Env construction (inert carrier already added by M1):** `cmd/serf/run.go:177`,
  `cmd/serf/serve.go:203`.
- **M1 deliverables consumed:** `agent/sandbox` — `ResolvedPolicy` (writable
  roots, read roots, masked paths incl. pseudo-fs, net decision, cache-strategy,
  backend-required), `Resolve`, `HostFacts`, `Prober`/`FakeProber`, `Mode`,
  `*RefusalError`, and `ContractCase`/`AssertResolve` (the golden table).
- **Escape suite base:** `cmd/serf-hub/sandbox_test.go` (existing containment
  invariant + above-CWD secret tripwire) — the M3 escape subset extends it.

## Global Constraints

- **Flag stays inert & unexposed.** Enforcement is reachable only when
  `execenv.Sandbox` is non-nil, and M3 populates it **only in tests / integration
  harnesses**, never from the CLI flag. A no-sandbox session (`Sandbox == nil`)
  must be **byte-identical** to today — assert it. Keep `--sandbox` out of
  `printRunCommands`/help; add a test that it is absent.
- **Full contract or refuse — no `BestEffort()`, no degrade flag.** The Landlock
  backend uses the exact ABI level the policy requires and **fails closed** if the
  kernel can't express it. bwrap likewise: if a required feature (e.g.
  `--unshare-net` for net=off) is unavailable, refuse — never silently drop a
  grant/denial.
- **Adapt, do not copy codex.** Take the argv-transform + arg0 re-exec + `--argv0`
  fix-up. Do **not** port synthetic-mount targets, protected-create monitors,
  proxy routing, or seccomp. Flat roots + fixed protected subpaths.
- **fd hygiene is load-bearing.** Every spawn: `cmd.ExtraFiles = nil`, serf's own
  long-lived fds `O_CLOEXEC`, bwrap passes no extra fds; agent sockets never
  bind-mounted. A spawned proc inherits nothing beyond stdio.
- **Pristine output.** Captured-and-asserted denials/errors are fine; no stray
  failures or logs. Real bwrap/Landlock in integration tests — no mocks.
- **snake_case** for any JSON/wire/config key (the serialized policy passed to
  `sandbox-exec` crosses a process boundary → `make lint`/serf-namingcheck gate).
- Never `git add -A` without a prior `git status`. Stage exact paths.

## File Structure

- `agent/sandbox/backend.go` (new) — `Backend` interface
  (`Wrap(argv []string, cwd string) ([]string, error)`), backend selection from
  `ResolvedPolicy.Backend` (`bwrap`/`landlock`/`none`), and the `Wrapper` value
  carried on `execenv` (holds the resolved policy + serf's own helper path,
  resolved once via `os.Executable` outside cwd — PATH-injection defense).
- `agent/sandbox/bwrap.go` (new) — pure argv builder: writable `--bind` for each
  writable root, `--ro-bind` for each read root, `--tmpfs`/`--ro-bind /dev/null`
  masks for each denylist/pseudo-fs path, `--unshare-pid --proc /proc`,
  `--dev /dev`, `--unshare-net` when net=off, `--overlay-src/--tmp-overlay` per
  cache root when overlay-capable, `--die-with-parent --new-session`, `--argv0`
  fix-up, `--` command. Mirrors `buildRipgrepArgs`' pure-core test style.
- `agent/sandbox/landlock.go` (new) — apply an additive ruleset via
  `go-landlock` (RW worktree+tmp+gitdir-rw-subset, RO system read roots + main
  `.git/config` read; never config/hooks, never `/proc`); fail closed below the
  required ABI. Applies to the current thread, then the helper `execvp`s.
- `cmd/serf/sandbox_exec.go` (new) — the `sandbox-exec` subcommand: deserialize
  the policy (from an inherited fd or arg), dispatch to bwrap-exec or
  landlock-apply-then-exec. `+build linux`.
- `agent/sandbox/session_tmp.go` (new) — per-session tmp dir (writable, `TMPDIR`),
  cleanup on end + age-sweep of crashed-session dirs at next start.
- `agent/sandbox/env_floor.go` (new) — the sandbox env floor (drop
  `SSH_AUTH_SOCK`, `AWS_*`, `GOOGLE_*`, `GCLOUD_*`, `VAULT_*`, worktree-external
  `KUBECONFIG`) + cache-redirect env (`GOCACHE`/`npm_config_cache`/`CARGO_HOME`
  → session tmp) composed on top of `filteredEnvWithPolicy`.
- `agent/sandbox/provider_web.go` (new) — provider-native web-capability registry;
  `WebEgress(provider) (enabled bool, known bool)`; **fails closed** (treated as
  egress-capable → refused under net=off) for unknown providers.
- `agent/execenv/local.go` (modify) — carry a `*sandbox.Wrapper` beside
  `Sandbox`; wrap argv in `execPreparedCommand` (`:768`) and `StreamCommand`
  (`:866`) when non-nil; `ExtraFiles = nil` at both; extend the env build with
  the sandbox floor; carry the wrapper in `WithWorkingDirectory` (`:123`).
- `agent/internal/mcp/manager.go`, `agent/internal/hooks/hooks.go` (modify) —
  accept and apply a `*sandbox.Wrapper` at their spawn sites (`manager.go:717`,
  `hooks.go:81/86`); route hook env through the sandbox floor.
- `cmd/serf/main.go` (modify, small) — add `case "sandbox-exec"` to
  `dispatchCLICommand` (`:273`); **not** listed in `printRunCommands`.
- Tests: `agent/sandbox/bwrap_test.go`, `landlock_test.go`, `backend_test.go`,
  `env_floor_test.go`, `provider_web_test.go`, `contract_backend_test.go`
  (re-runs `AssertResolve`-shaped assertions realized against each real backend),
  `agent/sandbox/escape_test.go` (the adversarial subset), plus `execenv` and
  MCP/hook wrap tests. Integration tests capability-gated (bwrap-present /
  landlock-ABI / overlay-supported).

## Task M3a — bwrap backend: FS confinement + `sandbox-exec` helper + pid-ns/`/proc`/`/dev`

**Files:** `agent/sandbox/backend.go`, `bwrap.go`, `cmd/serf/sandbox_exec.go`
(new); `bwrap_test.go`, `backend_test.go` (new); `cmd/serf/main.go` (modify).

- [ ] **Failing test:** `TestBuildBwrapArgv` (pure, no exec) — from a
  `ResolvedPolicy` fixture assert the exact flag vector: one `--bind` per writable
  root, `--ro-bind` per read root, `--tmpfs`/`--ro-bind /dev/null` per masked
  path (incl. `/proc`, `/sys`, `/dev/fd`, `/dev/mem`, `/run/user`), `--proc /proc`,
  `--dev /dev`, `--unshare-pid`, `--die-with-parent`, `--new-session`, the
  `--argv0` fix-up, and `--` before the command. `TestSandboxExecDispatch` — `serf
  sandbox-exec` is reached from `dispatchCLICommand` and is **absent** from user
  help. **Integration (bwrap-gated)** `TestBwrapConfinesAndMasks`: run a real
  command under the helper; assert a masked above-worktree secret reads as absent
  and `/proc/1/comm` is the sandbox init (host PID state invisible).
- [ ] Implement `Backend`/`Wrapper`, the bwrap argv builder, and the
  `sandbox-exec` subcommand (resolve serf's own path via `os.Executable`, refuse
  a cwd-relative arg0). Wire `execPreparedCommand`/`StreamCommand` to wrap when
  `e.Sandbox != nil` (behind the non-nil gate only — no flag path).
- [ ] **Adversarial verify:** does the masked set include every pseudo-fs the spec
  lists? is `/proc` a *fresh* mount (not a `--ro-bind` of host `/proc`)? does a
  cwd-relative helper path get rejected? Fix, commit.

## Task M3b — Landlock fallback backend (worktree-restricted only)

**Files:** `agent/sandbox/landlock.go` (new), `landlock_test.go` (new);
`cmd/serf/sandbox_exec.go`, `go.mod`/`go.sum` (modify — add `go-landlock`).

- [ ] **Failing test:** **Integration (landlock-ABI-gated)**
  `TestLandlockWorktreeRestricted`: apply the ruleset then exec; assert write
  inside the worktree/tmp allowed, write outside denied, read of a system root
  (`/usr`) allowed, read of `/proc` denied, `.git/config`/`.git/hooks` write
  denied. `TestLandlockFailsClosedBelowABI`: a HostFacts with an ABI too low to
  express the required rights **refuses** (no `BestEffort` downgrade).
  `TestLandlockOnlyServesRestricted`: re-run the M1 contract facts for a
  landlock-only host — only `restricted`+linked-worktree+net=on resolves; every
  other cell refuses naming bwrap.
- [ ] Add the dep (`go get github.com/landlock-lsm/go-landlock/landlock`; **verify
  the exact `RestrictPaths`/`RODirs`/`RWDirs`/ABI-constant API against the pinned
  version** — do not assume). Implement the additive ruleset + the helper's
  landlock-apply-then-`execvp` path.
- [ ] **Adversarial verify:** can the additive set ever grant config/hooks or
  `/proc`? does the main-repo `.git/config` get **read** (linked-worktree git
  needs it) but never **write**? is the refusal path truly no-BestEffort? Fix,
  commit.

## Task M3c — Wrap all spawn sites + fd hygiene

**Files:** `agent/execenv/local.go`, `agent/internal/mcp/manager.go`,
`agent/internal/hooks/hooks.go` (modify); wrap tests in each package.

- [ ] **Failing test:** `TestExecWrappedWhenSandboxed` / `TestStreamWrappedWhenSandboxed`
  — a sandboxed `execenv` runs `bash -c`/rg confined (a masked path is invisible
  to the spawned shell); `TestNoSandboxByteIdentical` — `Sandbox == nil` produces
  the exact argv/env as today. `TestMCPStdioServerConfined` and
  `TestHookCommandConfined` — the MCP stdio server and a PreToolUse hook run under
  the same wrapper. `TestNoInheritedFDs` — a spawned proc cannot read an inherited
  high fd or a `/run/user` agent socket (`ExtraFiles == nil`, serf fds
  `O_CLOEXEC`).
- [ ] Implement the wrap at `execPreparedCommand`/`StreamCommand`; thread a
  `*sandbox.Wrapper` to `transportForConfig`/`NewManager` and to
  `executeCommandHook`/`Runner` (these are free/package functions with **no policy
  handle today** — see discrepancy #3); set `ExtraFiles = nil` and confirm
  `O_CLOEXEC` at each site.
- [ ] **Adversarial verify:** is rg (`:641`) actually covered transitively? does a
  background `StreamCommand` job stay confined after `DetachAfterStart`? can any
  spawn path bypass the wrapper? Fix, commit.

## Task M3d — Session tmp + cache overlay/redirect + env floor

**Files:** `agent/sandbox/session_tmp.go`, `env_floor.go` (new);
`agent/sandbox/bwrap.go`, `agent/execenv/local.go`,
`agent/internal/hooks/hooks.go` (modify); tests.

- [ ] **Failing test:** `TestSessionTmpLifecycle` (per-session `TMPDIR`, cleaned on
  end, crashed dirs age-swept). `TestEnvFloorComposesOnPolicy` — on top of
  `filteredEnvWithPolicy`, `SSH_AUTH_SOCK`/`AWS_*`/`GOOGLE_*`/`GCLOUD_*`/`VAULT_*`
  and worktree-external `KUBECONFIG` are dropped from a spawned proc's environ,
  while the existing `*KEY*` scrub still holds — **including for hook commands**
  (which build env from `os.Environ()` today, discrepancy #4).
  `TestCacheRedirectWhenNoOverlay` — with `overlaySupported=false` (this host),
  `GOCACHE`/`npm_config_cache`/`CARGO_HOME` point into session tmp and the real
  cache is untouched. `TestCacheOverlayArgv` (**overlay-gated → SKIP here**) —
  when overlay-capable, the argv carries `--overlay-src <cache> --tmp-overlay`.
- [ ] Implement session tmp, the env floor + cache redirect, and the overlay argv
  branch (guarded by `HostFacts.overlaySupported`).
- [ ] **Adversarial verify:** can a write to a redirected/overlaid cache ever reach
  the real cache a later build consumes? is `restricted` always session-private
  (never overlay)? is the floor applied to **all** spawn sites incl. hooks/MCP?
  Fix, commit.

## Task M3e — net=off + provider-web registry + floor refusals + startup line

**Files:** `agent/sandbox/provider_web.go` (new); `agent/sandbox/bwrap.go`,
`agent/execenv/local.go` (or the web_fetch/web_search + remote-MCP tool sites),
`cmd/serf/run.go`/`serve.go` (startup line) (modify); tests.

- [ ] **Failing test:** **Integration (bwrap-gated)** `TestNetOffBlocksEgress` — a
  spawned proc under net=off cannot open TCP and cannot resolve DNS/UDP
  (`--unshare-net`). `TestNetOffDisablesToolEgress` — `web_fetch`/`web_search` and
  remote/SSE-HTTP MCP fail with a legible error under net=off.
  `TestProviderWebRegistryFailsClosed` — a **known** egress-capable provider is
  refused under net=off; an **unknown** provider is *also* refused (fail closed).
  `TestNetOffRequiresBwrap` — net=off on a landlock-only host refuses.
  `TestStartupEnforcementLine` — one line states backend + exact enforcement set
  (mode, net, warm-overlay vs cold-cache).
- [ ] Implement `--unshare-net`, the tool-plane egress gates, the provider-web
  registry (fail-closed default), the fail-closed floor surfaced at construction
  (M1 wired the refusal; M3's backend selection makes it real), and the startup
  line.
- [ ] **Adversarial verify:** can net=off be silently false through any path the
  user can't inspect (unknown provider capability, a remote MCP that slips the
  gate)? is LLM inference traffic correctly **unaffected**? Fix, commit.

## Task M3f — Adversarial escape suite (M3 subset)

**Files:** `agent/sandbox/escape_test.go` (new; may extend
`cmd/serf-hub/sandbox_test.go`'s containment invariant). All run against the
**real bwrap** backend (bwrap-gated), a few also under Landlock.

- [ ] **proc aliasing** — from a spawned proc, `read /proc/1/root/...`,
  `/proc/<host-pid>/root/...`, and `/proc/self/root` cannot reach a masked secret
  (fresh pid-ns `/proc` + masking; expect denied).
- [ ] **ld.so indirect exec** — `ld-linux.so <denied-binary>` cannot execute a
  binary outside the read roots (kernel FS view, not a string denylist; expect
  denied).
- [ ] **net=off egress** — TCP connect **and** UDP/DNS from a spawned proc fail;
  the provider-native web path fails closed.
- [ ] **hook-persist** — a spawned proc cannot write `.git/hooks/*` or a
  `core.hooksPath` redirect that survives (config/hooks write-denied in every
  writable mode; `$HOME` unwritable); assert the planted hook stays inert.
- [ ] **cache-poison isolation** — write into the cache root, then assert a later
  consumer sees the *real* cache unpoisoned (overlay private-upper, or
  session-private redirect on this host).
- [ ] **Adversarial verify:** does each escape assert the *mechanism* (masking /
  namespace / additive-only), not an incidental error? Are the residuals the spec
  documents (pre-existing hardlink) asserted-as-residual, not claimed closed?
  Fix, commit.

## Done criteria

- `cd <worktree> && make test-short && make vet && make lint` clean.
- `go test ./agent/sandbox/...` green incl. capability-gated integration tests on
  a bwrap+Landlock host (overlay tests SKIP where overlay is unavailable).
- The M1 contract suite runs green **realized against the real bwrap and Landlock
  backends**; the escape subset (M3f) passes against real bwrap.
- A no-sandbox session is verified byte-identical to today; `--sandbox` remains
  unexposed (absent from help) and enforcement is reachable only via a non-nil
  `ResolvedPolicy` attached in tests/integration.
- Merge `wip/sandbox-m3` → `wip/sandboxing`; update the M0 status ledger; report.
  (Flag goes live only at M5, after M4, under Jesse's review.)

## Discrepancies found (verify-before-build notes)

1. **bwrap overlay unavailable on this host.** bubblewrap 0.9.0 here was built
   without overlay support, so `--overlay-src`/`--tmp-overlay` are unknown
   options. Not a spec error — a host-capability fact — but M3d's overlay path
   **cannot be validated on this machine** and its integration test must be
   overlay-gated (SKIP here); the cache path degrades to session-private redirect
   exactly as the spec prescribes.
2. **Anchors verified accurate (2026-07-08).** `shellCommand:1110`,
   `execPreparedCommand:768`, `StreamCommand:866`, `ExecCommand:756`,
   `filteredEnvWithPolicy:1209` all match the master/M1 citations. rg-backed Grep
   confirmed to funnel through `ExecCommand` (`:641`), so wrapping
   `execPreparedCommand` covers it.
3. **MCP-stdio and hook sites have no policy handle today.** The spec's seam item
   3 ("same at MCP-stdio + hook spawn sites") glosses that
   `agent/internal/mcp/manager.go:717` (`transportForConfig`, a free function
   reached via a closure) and `agent/internal/hooks/hooks.go:81/86`
   (`executeCommandHook`, a package function) receive **no** `execenv` or policy
   today. M3c must add explicit plumbing to both — extra wiring weight the local.go
   sites don't have (those already hold `e`).
4. **Hook commands bypass the existing env scrub.** `hooks.go:95` builds child env
   from `os.Environ()` directly, **not** `filteredEnvWithPolicy` — so today's
   `*KEY*/*SECRET*/*TOKEN*` drop is **not** applied to hook commands, and the
   sandbox env floor wouldn't be either without a fix. M3c/M3d must route hook env
   through the sandbox floor; otherwise a hook leaks the provider API key even in a
   sandboxed session. Real gap, flagged for the M3c/M3d verifier.
