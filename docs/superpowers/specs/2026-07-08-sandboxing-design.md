# Serf Sandboxing — Design (v3)

Date: 2026-07-08 — lineage: v1 from-scratch recon; v2 addressed roborev job 2319
(Landlock allowlist-only → bwrap-preferred; fail-closed floor); v3 addresses
roborev job 2321 (apply_patch execenv bypass, hardlink aliasing, git-config
persistence surfaces, `/proc` host-PID leak, subagent rollout ordering) + the
env-exposure / provider-web / cache-warning missing-considerations.
Status: Draft — awaiting re-review
Author: Bot. Prior session's spec was never found; this lineage starts at zero.

## Goal

Stop a poorly-behaved model — accidental or deliberate — from reading, writing,
or executing outside a boundary the user opted into at session start. Primary
threat: a *running-amok* agent (`rm -rf` outside the workspace, reading
`~/.ssh`, `curl | sh`, phoning secrets home, planting something that runs later
outside the sandbox). Opt-in per session; **immutable for the session's
lifetime** — no tool call can relax it.

Non-goals for v1: rich per-tool approval/allowlist engine (orthogonal,
deferred); domain-allowlist egress (network is all-or-nothing); confining serf's
own long-lived process; Windows (fail closed).

## Threat model — two layers

1. **Spawned processes run arbitrary, model-composed input** → **kernel
   enforcement** (bwrap namespaces + mounts). String/path denylists are
   bypassable (Ona incident: `/proc/self/root` aliasing, `ld-linux.so` indirect
   exec, self-disable attempts). Kernel policy is inode/namespace based and
   inherited by all descendants — an `npm install` postinstall is exactly as
   confined as the shell that launched it.
2. **Serf's own file tools run trusted serf code** → **in-process,
   fully-symlink-resolved path checks**. The model's only input is a path
   string; the code between it and the syscall is ours. Resolution is strict,
   not best-effort (a model can `ln -s` inside the worktree via shell, then
   `read_file` through it — checks run on the fully resolved path).

**Denylist enforcement is by masking, not string-refusal, wherever possible.**
Spawned procs: denied dirs are made **invisible** (bwrap tmpfs/`/dev/null`
over-mounts), so a process cannot even reference them — this is what defeats
symlink, `/proc/pid/root`, and hardlink *creation* against secrets (you can't
`ln` a source you can't see). File tools: denied paths refused after strict
resolution. Residual (documented, not claimed-closed): a **pre-existing
hardlink** to a secret already sitting inside a readable root is reachable via
file tools — a hardlink shares the inode with no backlink to resolve, so
path-based checks can't see its origin. Out of the running-amok threat model
(requires a pre-planted link); closing it would need an inode-preflight scan,
deferred.

### Persistence is part of the threat (and mostly closed by write-confinement)

A sandboxed write to something that executes *later, unsandboxed* is an escape.
In every sandboxed mode the writable set is **only** worktree + session tmp
(+ caches + gitdir in `workspace-write`). `$HOME` is never writable — so
`~/.bashrc`, `~/.profile`, `~/.gitconfig`, `~/.config/autostart`, user systemd
units, etc. are already write-denied. The one execute-later surface *inside* the
writable set is git: see git-metadata protection below.

## Modes

`--sandbox <mode>`, set once. Network is an independent flag (below). All
sandboxed modes share: per-session tmp (writable, exported as `TMPDIR`), secrets
denylist (masked), git-metadata protection, `--unshare-pid` + fresh `--proc`
(so `/proc` shows only sandbox PIDs — no host `/proc/<pid>/root` aliasing or
`environ` leak), minimal `/dev` (`null`, `zero`, `full`, `random`, `urandom`,
`tty`), env floor (below).

| Mode | file-tool reads | file-tool writes | spawned reads | spawned writes |
|---|---|---|---|---|
| `off` (default) | anywhere | root-confined (today) | anywhere | anywhere |
| `read-only` | anywhere − denylist | **denied** (tmp only) | anywhere − denylist | tmp only |
| `workspace-write` | anywhere − denylist | worktree + tmp + caches | anywhere − denylist | worktree + tmp + caches + gitdir(−config/hooks) |
| `restricted` | **worktree only** | worktree + tmp | worktree + system read roots + tmp | worktree + tmp + gitdir(−config/hooks) |

- **`off` is exactly today's behavior** — no new code path; strict superset.
- **`read-only` cannot commit** (no worktree or `.git` writes); session-tmp
  writes allowed so ordinary commands have scratch space.
- **System read roots** (`restricted` spawned procs): `/usr`, `/bin`, `/lib*`,
  `/etc`, `/opt`, `/nix/store`, toolchain roots — read-only, so binaries can
  exec. File tools stay worktree-only; the asymmetry is intentional (tools =
  what the *model* may browse; kernel policy = what a *process* needs to run).
- **Secrets denylist** (all sandboxed modes, both layers, masked): `~/.ssh`,
  `~/.aws`, `~/.config/gcloud`, `~/.netrc`, `~/.config/serf`, `~/.gnupg`,
  `~/.docker/config.json`, `~/.kube`, `~/.git-credentials`. User-extensible both
  directions via config (add / punch holes); never model-changeable mid-session.
- **Git-metadata protection (v3 — complete, not just `.git/hooks`).** Objects,
  refs, index, logs, packed-refs are **writable** (so `git commit`/`add`/
  `checkout` work). Read-only in every writable mode: **all config surfaces and
  hook surfaces** — `.git/config`, per-worktree config (`config.worktree` +
  `extensions.worktreeConfig`), submodule configs (`.git/modules/*/config`), and
  `.git/hooks`, resolved through `gitdir:` for linked worktrees. Rationale: hook
  poisoning executes later, unsandboxed. A raw `git config core.hooksPath
  <writable-dir>` redirect is neutralized because **every config file git would
  read for this repo is read-only and `$HOME`-level configs aren't writable** —
  so the redirect can't be *persisted*, and a hook file dropped in the worktree
  stays inert. Cost: `git config --local ...` fails in a sandboxed session
  (legible denial); accepted — persisting repo config is exactly the vector.
- **Linked-worktree gitdir.** A worktree session's git writes land *outside* the
  worktree in `<main>/.git/worktrees/<id>/`, `objects/`, `refs/`, `logs/`,
  `packed-refs`. Policy resolves the `gitdir:` pointer and grants those (minus
  config/hooks). Without this, subagent worktree lanes can't commit.
- **Cache roots.** `workspace-write` grants writes to `~/.cache`, `~/go/pkg`,
  `~/.npm`, `~/.cargo` (user-extensible) — else `go build`/`npm`/`cargo` fail on
  the first sandboxed session. **A startup warning states cache roots are
  writable and that a sandboxed session can poison a cache a later unsandboxed
  build consumes** (mitigation is user awareness in v1). `restricted` grants no
  cache roots; it redirects redirectable caches into session tmp via env
  (`GOCACHE`, `npm_config_cache`, `CARGO_HOME`) and eats cold-cache cost.
- **Session tmp**: per-session (not shared `/tmp`), `TMPDIR`, cleaned on session
  end; crashed-session dirs age-swept at next serf start.

## Environment floor (v3)

Serf already drops `*KEY*/*SECRET*/*TOKEN*/*PASSWORD*/*CREDENTIAL*` env by
default. Sandboxed sessions raise a **floor on top of** the user's `EnvPolicy`
(composes, doesn't replace — env-scrubbing stays an independent knob):
additionally drop `SSH_AUTH_SOCK` (ssh-agent access is credential access without
touching `~/.ssh`), and cloud credential-agent vars (`AWS_*`, `GOOGLE_*`,
`GCLOUD_*`, `VAULT_*`, `KUBECONFIG` pointing outside the worktree). The
ssh-agent / gpg-agent Unix sockets are **not** bind-mounted into the sandbox.
Stated because otherwise net-off + FS-masking would still leave a live agent
socket as an exfil/sign-anything channel.

## Network — one flag, honest semantics

`--sandbox-net=on|off`, default **on** when sandboxed (FS containment is
load-bearing; egress denial is the opt-in extra — deliberately diverges from
Codex/Claude Code default-deny).

`network=off` governs the **tool plane**: spawned procs (`--unshare-net`: no
interfaces — no TCP/UDP/DNS) and serf-process tool egress —
`web_fetch`/`web_search` and remote (HTTP/SSE) MCP servers disabled with a
legible error. **Provider-native egress is also disabled (v3):** any
provider-side web-search / server-tool feature that fetches on the model's
behalf is turned off for the turn under net=off — otherwise "network off" is a
lie told through the provider. **LLM provider *inference* traffic is
unaffected** — the agent still talks to its model; net=off never promised
otherwise, and now says so explicitly.

`network=off` requires bwrap (no Landlock TCP-only variant — TCP-only leaves
UDP/DNS exfiltration open, a silently weaker contract).

## Backends and the enforcement floor

**Linux: bwrap-preferred.** Probe once at session start (capability check,
binary resolved outside cwd). bwrap expresses the full model: writable-root
binds, subtractive masking (denylist/config/hooks hidden via tmpfs/`--ro-bind`),
`--unshare-net`, `--unshare-pid` + fresh `--proc`, minimal `/dev`. One startup
line always states backend + the exact enforcement set.

**Landlock fallback** (`landlock-lsm/go-landlock`, no userns dependency — immune
to Ubuntu 24.04 `apparmor_restrict_unprivileged_userns`). Landlock is
**allowlist-only — cannot express subtraction**. Under the fail-closed floor it
therefore serves exactly one contract it can fully express: **`restricted` in a
linked worktree, net=on** (pure additive grants; config/hooks live in the main
`.git` and are simply never granted; no `/proc` PID-ns, so it also cannot
deliver the `/proc` guarantee → this is folded into the "cannot serve" set
unless the worktree-restricted grant set excludes `/proc` entirely, which it
does — `restricted` grants only worktree + system read roots, and system read
roots exclude `/proc`). It cannot serve `workspace-write`, `read-only`
(subtractive denylist), `restricted` on a main checkout, or `net=off`.

**Floor: full contract or refuse — no override flag.** If the host can't fully
enforce the requested mode+net, the session **fails to start** with an error
naming the missing capability and fix.

| Host capability | runs | refuses |
|---|---|---|
| bwrap capable | all modes, net on/off | — |
| Landlock only | `restricted` in a linked worktree, net=on | everything else |
| neither / Windows / other OS | `off` only | any sandboxed mode |

No `BestEffort()` degradation, no `--sandbox-allow-degraded`.

**Not used:** seccomp-bpf as primary; Codex's ~2700-line bwrap mount-ordering
machinery (nested overlays, ripgrep glob-deny) — flat roots + fixed
protected-subpath/denylist sets.

## Backend (macOS)

`sandbox-exec` (Seatbelt) — deprecated a decade, still the only headless option;
used by Codex and Claude Code. SBPL from the same policy model (Seatbelt is
deny-capable → all modes expressible), invoked `/usr/bin/sandbox-exec -p
<policy> -- <cmd>` with the path hard-coded (PATH-injection defense, from Codex).
Validated on **paradise-park** (ssh).

## Tool-surface inventory

Every model-reachable route to filesystem/network and its coverage:

| Surface | Coverage |
|---|---|
| `shell` (fg + background jobs, `StreamCommand`) | kernel wrapper |
| `read_file`/`write_file`/`edit_file` | in-process checks |
| **`apply_patch`** | **in-process — but does NOT flow through execenv today** (`session_tools_shell.go:233` → `tool.ApplyPatch(env.WorkingDirectory(), patch)` → `os.ReadFile/WriteFile/Remove/Rename` directly, `apply_patch.go`). **M2 must route every op through policy-aware execenv methods, or disable `apply_patch` in sandboxed modes until refactored.** Explicit tests: `read-only`, protected `.git/config`+`hooks`, symlink escape. |
| `glob`/`grep`/`list_dir` | in-process base resolution; `rg` subprocess also kernel-wrapped |
| `manage_worktree` (main-repo control env) | control policy: main repo + `.git/worktrees` registry writes allowed; config/hooks denied |
| stdio MCP servers | spawned under session sandbox |
| remote MCP servers | net=on only; disabled net=off |
| hook commands (PreToolUse etc.) | spawned under session sandbox; hooks needing broader access are incompatible with sandboxed sessions (documented) |
| `web_fetch`/`web_search` + provider-native web | net=on only; disabled net=off |
| delegate/subagent sessions | inherit policy re-rooted to their worktree |
| `compact_context`, `ask_user`, job-control | no fs/network surface |

## Seams (recon-verified)

Everything flows through `execenv.ExecutionEnvironment` →
`LocalExecutionEnvironment` (`agent/execenv/local.go:60`).

1. **Policy carrier**: `SandboxPolicy` (mode, resolved roots, denylist, net,
   backend, env-floor) on `LocalExecutionEnvironment`; constructed at
   `cmd/serf/run.go:177` / `serve.go:203`.
2. **Config/flags**: `Sandbox`+`SandboxNet` on `SessionConfig`
   (`session_config.go:20`); `--sandbox`, `--sandbox-net`; denylist/root
   extensions in config, human-only, resolved before session start.
3. **Kernel wrap**: prefix backend argv in `shellCommand`/`execPreparedCommand`
   (`local.go:768`) + `StreamCommand` (`local.go:866`); same wrapper at MCP
   stdio-spawn and hook-command-spawn sites.
4. **In-process**: extend `resolve` (`local.go:1121`) with the strict read-side
   check mirroring `resolveWrite`→`ensureUnderRoot` (`local.go:1136`/`:1158`);
   `Glob`/`Grep`/`ListDirectory` share it; **`apply_patch` refactored to route
   through the same checks** (see inventory).
5. **Subagents/worktrees**: policy rides `WithWorkingDirectory` (`local.go:123`)
   like `EnvPolicy`, re-rooted to the child worktree with fresh gitdir
   resolution; persisted in the delegate descriptor beside `LocalEnvPolicy`;
   resumed delegates re-apply. Novel surface — explicit tests.
6. **Denials**: typed, legible tool error. In-process denials know the path;
   kernel denials attributed best-effort (exit + stderr), never silently
   retried. Every denial audit-logged to the session log (mode, tool,
   path/command). Non-interactive: final.

## Milestones (v3 — no half-enforced mode ever user-visible; subagent inheritance ships WITH the flag)

- **M1 — Policy core + cross-backend contract tests.** `SandboxPolicy` model,
  mode/flag/config parsing, backend probe, gitdir resolution (incl. worktree +
  submodule config surfaces), denylist/root/env-floor computation. A
  backend-agnostic contract suite (mode + host facts → exact grants / denials /
  refusals) every backend must satisfy — written first, so macOS lands against
  the same contract. No user-visible flag.
- **M2 — File-tool in-process layer.** Read-side containment + strict symlink
  resolution; denylist; `read-only` write denial; **`apply_patch` refactored (or
  disabled) — per-tool checklist: `read_file`, `write_file`, `edit_file`,
  `apply_patch`, `glob`, `grep`, `list_dir`, each with acceptance tests**;
  denial errors + audit log.
- **M3 — Linux kernel layer (internal slices, flag NOT yet live).**
  M3a bwrap FS confinement for `shell` (+ `--unshare-pid`/`--proc`, `/dev`);
  M3b Landlock worktree-restricted fallback; M3c jobs/`rg`/MCP-stdio/hook-spawn
  wrapping; M3d session tmp + caches + env floor; M3e net=off + floor refusals +
  startup line. Each slice reviewed against the M1 contract suite.
- **M4 — Subagent/worktree scoping.** Re-rooting, descriptor persistence,
  resume, depth inheritance, cross-lane isolation tests.
- **M5 — `--sandbox` goes live on Linux.** The flag ships only once **M3 *and*
  M4** pass end-to-end together — a sandboxed session can delegate the instant
  it's user-visible, so subagent inheritance must already hold. (Fixes the v2
  ordering gap where the flag preceded inheritance.)
- **M6 — macOS Seatbelt** against the M1 contract suite; live on paradise-park;
  flag goes live on macOS.
- **M7 — In-UI escalation** (below), after M1–M6 validated e2e.

## M7 — In-UI escalation (specced now, built last)

Serf has no harness-level approval prompt (PreToolUse can only deny; `ask_user`
is model-initiated — wrong trust direction). New human-gated primitive over
AppWire, interactive sessions only:

- **File-tool denials** (precise path, no partial side effects): "Agent tried to
  write /etc/hosts — allow this one action?" Approve → re-run that single
  invocation with that path added *for that invocation only* (gemini-cli model).
  Deny → typed error to model.
- **Shell denials**: denied path is heuristic and the command may have
  **partially executed**. The card shows the full command, output so far, and a
  "already partially ran; approving re-runs start-to-finish" caveat. Approve →
  re-run whole command with the expanded grant for that invocation.
- Model cannot trigger, approve, observe, or replay approvals. No session-wide
  relaxation, no persistent allowlist (that's the deferred policy engine — this
  primitive is what it would sit on).
- Non-interactive: unchanged (denial final).

## Validation

- **Adversarial escape suite** (named deliverable, extends the containment
  invariant of `cmd/serf-hub/sandbox_test.go`): symlink-out (file tools + shell),
  `/proc/1/root` + `/proc/<pid>/root` aliasing, `/proc/<pid>/environ` read,
  fd/root aliasing, `ld-linux.so <denied-binary>`, `.git/hooks` write,
  `core.hooksPath` redirect + `git config` persist attempt, `config.worktree` /
  submodule-config tamper, `~/.bashrc`/`~/.gitconfig` write (expect denied by
  write-confinement), denylist read via **every** file tool incl. `apply_patch`,
  hardlink-create-then-read against a masked secret (expect blocked by masking),
  pre-existing-hardlink read (documented residual — asserted, not claimed
  closed), `SSH_AUTH_SOCK`/agent-socket egress, net=off egress (TCP + UDP/DNS),
  worktree-lane cross-reads.
- Per-milestone unit + e2e; live feel-testing of denial UX (no model retry
  loops); bwrap and Landlock paths each e2e'd; M1 contract suite runs against
  both Linux backends and Seatbelt.
- Edge cases pinned: nested worktrees, submodules, resumed sessions re-applying
  policy, deleted-then-recreated roots, delegate resume after serf restart.

## Resolved decisions (Jesse, 2026-07-08)

1. Secrets denylist: default set above; user-extensible both directions; never
   model-changeable.
2. `web_fetch`/`web_search`, remote MCP, and provider-native web disabled under
   net=off.
3. `read-only` kept; cannot commit; session-tmp writes allowed.
4. Backend: bwrap-preferred, Landlock fallback (worktree-restricted only);
   per-host enforcement stated loudly at startup.
5. MCP servers and hook commands run inside the session sandbox.
6. Network default on when sandboxed; single independent flag.
7. Floor: fail closed, no override — full contract or refuse; Windows/unknown
   refuse all sandboxed modes.

## Round-2 findings dispositioned (roborev job 2321)

- apply_patch execenv bypass → **fixed** (M2 refactor/disable + tests; inventory
  now states the bypass explicitly).
- hardlink aliasing → **narrowed honestly**: masking closes create-then-read for
  spawned procs; pre-existing-hardlink-to-secret is a documented residual, not a
  claimed guarantee.
- git persistence (`core.hooksPath`, `config.worktree`, submodule configs) →
  **fixed** by protecting all config surfaces (persist-blocked; `$HOME` configs
  already unwritable).
- `/proc` host-PID leak → **fixed**: `--unshare-pid` + fresh `--proc` required.
- M-ordering (flag before subagent inheritance) → **fixed**: flag goes live (M5)
  only after subagent scoping (M4).
- M3 too broad → **fixed**: split into internal slices M3a–e.
- env/credential exposure (`SSH_AUTH_SOCK`, agent sockets) → **added** (env
  floor).
- cache poisoning → **startup warning added**.

## Open questions

None — ready for re-review.
