# Serf Sandboxing — Design (v2)

Date: 2026-07-08 (v1: from-scratch recon; v2: post roborev design review job 2319 (Fail)
+ fresh-eyes pass — backend flipped to bwrap-preferred, enforcement floor made
fail-closed, `.git` protection narrowed, cache roots added, MCP/hook surfaces
covered, milestones restructured)
Status: Draft — awaiting re-review
Author: Bot. Prior session's spec was never found; this lineage starts at zero.

## Goal

Stop a poorly-behaved model — accidental or deliberate — from reading, writing,
or executing outside a boundary the user opted into at session start. Primary
threat: a *running-amok* agent (`rm -rf` outside the workspace, reading
`~/.ssh`, `curl | sh`, phoning secrets home). The sandbox is **opt-in per
session** and **immutable for the session's lifetime** — no tool call can relax
it.

Non-goals for v1: rich per-tool approval/allowlist policy engine (orthogonal,
deferred); domain-allowlist egress (network is all-or-nothing); confining serf's
own long-lived process; Windows (fail closed, below).

## Threat model — two layers

1. **Spawned processes run arbitrary, model-composed input** → **kernel
   enforcement**. String/path denylists are fundamentally bypassable (the Ona
   incident: `/proc/self/root/...` aliasing, `ld-linux.so` indirect exec,
   attempts to disable the sandbox itself). Kernel policy is inode/namespace
   based and inherited by all descendants — an `npm install` postinstall script
   is exactly as confined as the shell that launched it.
2. **Serf's own file tools run trusted serf code** → **in-process,
   fully-symlink-resolved path checks**, extending the containment
   `resolveWrite`/`ensureUnderRoot` already applies to writes. The model's only
   input is a path string; the code between it and the syscall is ours. NOTE:
   resolution must be strict, not best-effort — the model can create a symlink
   inside the worktree via shell, then read through it via `read_file`; checks
   run on the fully resolved path. (Kernel side is inode-based and immune.)

We deliberately diverge from Codex here (it routes even its own `apply_patch`
through a sandboxed helper subprocess). Serf's model-reachable filesystem routes
are enumerated in "Tool-surface inventory" below; each is covered by one of the
two layers or explicitly disabled.

### Persistence is part of the threat

A sandboxed process writing `.git/hooks/pre-commit` or `~/.bashrc` doesn't
escape *now* — it escapes **later**, when the user runs git or a shell
unsandboxed. Hence the protected-subpath rules below even inside writable roots.

## Modes

`--sandbox <mode>`, set once at session start. Network is an independent flag
(below). All sandboxed modes share: per-session tmp dir (writable), secrets
denylist on reads, protected git-metadata subpaths, runtime resource access
(`/dev/null`, `/dev/zero`, `/dev/urandom`, `/dev/tty`, `/proc` reads).

| Mode | file-tool reads | file-tool writes | spawned reads | spawned writes |
|---|---|---|---|---|
| `off` (default) | anywhere | root-confined (today) | anywhere | anywhere |
| `read-only` | anywhere − denylist | **denied** (session tmp only) | anywhere − denylist | session tmp only |
| `workspace-write` | anywhere − denylist | worktree + tmp + caches | anywhere − denylist | worktree + tmp + caches + gitdir |
| `restricted` | **worktree only** | worktree + tmp | worktree + system read roots + tmp + gitdir(ro+rw split) | worktree + tmp + gitdir(rw subset) |

- **`off` is exactly today's behavior.** No new code path runs; the change is a
  strict superset.
- **`read-only` cannot commit** — no worktree or `.git` writes of any kind.
  Session tmp stays writable because innumerable innocuous commands need scratch
  space; that does not weaken "look but don't touch."
- **System read roots** (`restricted` spawned procs): `/usr`, `/bin`, `/lib*`,
  `/etc`, `/opt`, `/nix/store`, toolchain roots — read-only, so binaries can
  exec at all. File tools in `restricted` stay worktree-only (in-process check);
  the asymmetry is intentional: tools answer "what may the *model* browse,"
  kernel policy answers "what does a *process* need to run."
- **Secrets denylist** (all sandboxed modes, both layers): `~/.ssh`, `~/.aws`,
  `~/.config/gcloud`, `~/.netrc`, `~/.config/serf`, `~/.gnupg`,
  `~/.docker/config.json`, `~/.kube`, `~/.git-credentials`. **User-extensible
  both directions** via config (add paths, or punch holes to opt secrets back
  in) — the human chooses their risk; the model can never change it
  mid-session.
- **Git metadata protection, narrowed (v2).** NOT blanket read-only `.git` —
  that breaks `git commit` (objects, refs, index, `index.lock` are all writes).
  Protected set: **`.git/hooks` and `.git/config`** (and their resolved
  `gitdir:` equivalents for linked worktrees — hooks/config live in the *main*
  repo's `.git`), write-denied in every writable mode. Everything else in the
  relevant gitdir is writable so git works.
- **Linked-worktree gitdir (v2).** A worktree-rooted session's git writes go
  *outside* the worktree: `<main>/.git/worktrees/<id>/`, `objects/`, `refs/`,
  `logs/`, `packed-refs`. The policy resolves the worktree's `gitdir:` pointer
  and grants those paths (minus hooks/config). Without this, subagent worktree
  lanes can't commit — a non-starter.
- **Cache roots (v2).** `workspace-write` grants writes to a default cache set —
  `~/.cache`, `~/go/pkg`, `~/.npm`, `~/.cargo` (user-extensible) — because
  `go build`/`npm`/`cargo` write caches outside the worktree and would fail on
  the first sandboxed session otherwise. Accepted, documented tradeoff: a
  sandboxed session can poison a cache that a later unsandboxed build consumes.
  `restricted` does not grant cache roots; it redirects redirectable caches into
  session tmp via env (`GOCACHE=...`) and eats cold-cache cost.
- **Session tmp**: per-session dir (not shared `/tmp` — cross-session side
  channel), exported as `TMPDIR` to spawned procs, cleaned on session end;
  stale dirs from crashed sessions swept age-based at next serf start.

## Network — one flag, honest semantics

`--sandbox-net=on|off`, default **on** when sandboxed (decision: FS containment
is the load-bearing protection; egress denial is the opt-in extra — diverges
from Codex/Claude Code default-deny, deliberately).

`network=off` governs the **tool plane** only — spawned processes (via
`--unshare-net`: no interfaces, no TCP/UDP/DNS) and serf-process tool egress:
`web_fetch`/`web_search` and **remote (HTTP/SSE) MCP servers are disabled with a
legible error** (they'd otherwise bypass the sandbox from inside the serf
process). **LLM provider traffic is unaffected** — the agent still talks to its
model; "network off" never promised otherwise, and now says so.

`network=off` requires bwrap (see floor). No Landlock-TCP-only degraded variant —
TCP-only denial leaves UDP/DNS exfiltration open and would be a silently weaker
contract.

## Backends and the enforcement floor

**Backend selection (Linux): bwrap-preferred.** Probe once at session start
(`bwrap --unshare-net --unshare-user true` style capability check, binary
resolved outside cwd). bwrap expresses everything: writable-root binds,
**subtractive masking** (denylist paths hidden via tmpfs/`/dev/null` binds,
hooks/config `--ro-bind`), `--unshare-net`, fresh `/proc` and minimal `/dev`.
One startup line always states the backend and enforcement set.

**Landlock fallback** (`landlock-lsm/go-landlock`, no userns dependency — immune
to Ubuntu 24.04's `apparmor_restrict_unprivileged_userns`): Landlock is
**allowlist-only — it cannot express "everything except X"** (roborev finding,
confirmed). Consequence under the floor rule below: Landlock serves exactly the
contracts it can fully express — **`restricted` sessions rooted in a linked
worktree** (pure allowlist; hooks/config live in the main `.git` and are simply
not granted; the gitdir rw-subset is additive grants). It cannot serve:
`workspace-write` or `read-only` (subtractive secrets denylist), `restricted`
on a main checkout (`.git/hooks` sits inside the writable root and can't be
subtracted), or `network=off`.

**Enforcement floor: full contract or refuse — no override flag (decision).**
If the host cannot fully enforce the requested mode+network, the session
**fails to start** with an error naming the missing capability and the fix
("install bwrap" / "use a worktree-rooted session" / "unsupported platform").
Matrix:

| Host capability | runs | refuses |
|---|---|---|
| bwrap capable | all modes, net on/off | — |
| Landlock only | `restricted` in a linked worktree, net=on | everything else |
| neither / Windows / other OS | — (`off` only) | any sandboxed mode |

No `BestEffort()` degradation, no `--sandbox-allow-degraded`. `off` always
works everywhere.

**Not used:** seccomp-bpf as a primary layer; Codex's ~2700-line bwrap
mount-ordering machinery (nested overlays, ripgrep-expanded glob deny) — flat
roots + the fixed protected-subpath/denylist sets.

## Backend (macOS)

`sandbox-exec` (Seatbelt) — deprecated a decade, still the only headless
option, used by both Codex and Claude Code. SBPL generated from the same policy
model (Seatbelt is deny-capable, so all modes are expressible); invoked as
`/usr/bin/sandbox-exec -p <policy> -- <cmd>` with the path hard-coded (PATH
injection defense, copied from Codex). Validated on **paradise-park** (ssh).

## Tool-surface inventory (v2)

Every model-reachable route to filesystem/network, and its coverage:

| Surface | Coverage |
|---|---|
| `shell` (fg + background jobs, `StreamCommand`) | kernel (backend wrapper) |
| `read_file`/`write_file`/`edit_file`/`apply_patch` | in-process checks |
| `glob`/`grep`/`list_dir` | in-process base resolution; `rg` subprocess additionally kernel-wrapped |
| `manage_worktree` (incl. its main-repo-rooted control env) | control env carries a control policy: main repo + `.git` worktree-registry writes allowed, hooks/config still denied |
| stdio MCP servers | **spawned under the session sandbox** (decision) — a server needing more shouldn't run in a sandboxed session |
| remote MCP servers | allowed net=on; disabled net=off |
| hook commands (PreToolUse etc.) | **spawned under the session sandbox** (decision); hooks needing broader access are incompatible with sandboxed sessions, documented |
| `web_fetch`/`web_search` | allowed net=on; disabled net=off |
| delegate/subagent sessions | inherit policy re-rooted to their worktree (below) |
| `compact_context`, `ask_user`, job-control, etc. | no fs/network surface |

Implementation-time check: verify `apply_patch` routes through
`WriteFile`/`EditFile` (recon says file ops all flow through execenv; confirm).

## Seams (recon-verified)

Everything flows through `execenv.ExecutionEnvironment` → 
`LocalExecutionEnvironment` (`agent/execenv/local.go:60`).

1. **Policy carrier**: `SandboxPolicy` (mode, resolved roots, denylist, net,
   backend) on `LocalExecutionEnvironment` beside `RootDir`/`EnvPolicy`;
   constructed at `cmd/serf/run.go:177` / `cmd/serf/serve.go:203`.
2. **Config/flags**: `Sandbox`+`SandboxNet` on `SessionConfig`
   (`agent/session_config.go:20`); `--sandbox`, `--sandbox-net`; denylist/root
   extensions in config file, human-only, resolved before session start.
3. **Kernel wrap**: prefix backend argv in `shellCommand`/`execPreparedCommand`
   (`local.go:768`) and `StreamCommand` (`local.go:866`); same wrapper at MCP
   stdio spawn and hook-command spawn sites.
4. **In-process**: extend `resolve` (`local.go:1121`) with the read-side check
   (strict symlink resolution) mirroring `resolveWrite`→`ensureUnderRoot`
   (`local.go:1136`/`:1158`); `Glob`/`Grep`/`ListDirectory` share it.
5. **Subagents/worktrees**: policy rides `WithWorkingDirectory` (`local.go:123`)
   like `EnvPolicy`, **re-rooted to the child worktree** with fresh gitdir
   resolution; persisted in the delegate descriptor beside `LocalEnvPolicy`;
   resumed delegates re-apply it. Novel surface (no vendor publishes this) —
   explicit tests.
6. **Denials**: typed, legible tool error ("sandbox: write to /etc/hosts denied
   (workspace-write; writable: <worktree>, <tmp>)"). In-process denials know the
   path exactly; kernel denials are attributed best-effort (exit status + stderr
   heuristics) and never silently retried. **Every denial is also audit-logged**
   to the session log (mode, tool, path/command). Non-interactive: denial is
   final.

## Milestones (v2 — restructured so no half-enforced mode is ever user-visible)

- **M1 — Policy core + cross-backend contract tests.** `SandboxPolicy` model,
  mode/flag/config parsing, backend probe, gitdir resolution, denylist/root
  computation. A backend-agnostic policy test suite (given mode+host facts →
  exact expected grants/denials/refusals) that every backend must satisfy —
  written before any backend, so macOS lands against the same contract
  (roborev's cross-platform concern). No user-visible flag yet.
- **M2 — File-tool in-process layer.** Read-side containment, strict symlink
  resolution, denylist, `read-only` write denial, denial errors + audit log.
- **M3 — Linux kernel layer; `--sandbox` goes live on Linux.** bwrap backend
  (+ Landlock worktree-restricted fallback), shell/jobs/rg wrapping, MCP/hook
  spawn wrapping, session tmp + caches, floor refusals, net=off, startup
  enforcement line. Modes ship only here, whole-contract.
- **M4 — Subagent/worktree scoping.** Re-rooting, descriptor persistence,
  resume, depth inheritance.
- **M5 — macOS Seatbelt** against the M1 contract suite; live on paradise-park.
- **M6 — In-UI escalation** (below), after M1–M5 validated e2e.

## M6 — In-UI escalation (specced now, built last)

Serf has no harness-level approval prompt (PreToolUse can only deny; `ask_user`
is model-initiated — the wrong trust direction for privilege escalation). New
primitive, human-gated over AppWire, interactive sessions only:

- **File-tool denials** (precise path, no partial side effects): "Agent tried to
  write /etc/hosts — allow this one action?" Approve → re-run that single
  invocation with that path added *for that invocation only* (gemini-cli's
  model). Deny → typed error to model.
- **Shell denials** (v2 grounding fix): the denied path is heuristic and the
  command may have **partially executed** before the denial. The approval card
  shows the full command, its output so far, and a "this command already
  partially ran; approving re-runs it start-to-finish" caveat. Approve → re-run
  whole command with the expanded grant for that invocation.
- The model cannot trigger, approve, observe, or replay approvals. No session-
  wide relaxation, no persistent allowlist (that's the deferred policy engine —
  this primitive is what it would later sit on).
- Non-interactive: unchanged (denial final).

## Validation

- **Adversarial escape suite** (named deliverable, extends the containment
  invariant of `cmd/serf-hub/sandbox_test.go`): symlink-out-of-worktree (file
  tools + shell), hardlink to denied file, `/proc/self/root` aliasing,
  `ld-linux.so <denied-binary>`, `.git/hooks` + `~/.bashrc` write attempts,
  postinstall-inherits-sandbox, `gitdir:` tamper, denylist read via every file
  tool, net=off egress attempts (TCP, UDP/DNS), worktree-lane cross-reads.
- Per-milestone unit + e2e; live feel-testing of the denial UX (models react to
  denial errors sanely — no retry loops); Landlock/bwrap paths each e2e'd; the
  M1 contract suite runs against both backends and Seatbelt.
- Edge cases pinned by tests: nested worktrees, submodules (gitdir resolution),
  resumed sessions re-applying policy, deleted-then-recreated roots, delegate
  resume after serf restart.

## Resolved decisions (Jesse, 2026-07-08)

1. Secrets denylist: default set above; user-extensible both directions; never
   model-changeable.
2. `web_fetch`/`web_search` (and remote MCP) disabled under net=off.
3. `read-only` mode kept; it cannot commit; session-tmp writes allowed.
4. Backend: **bwrap-preferred, Landlock fallback** (v2 flip — Landlock can't
   express subtraction; per-host variance made loud via the startup line).
5. MCP servers **and** hook commands run inside the session sandbox.
6. Network default **on** when sandboxed; single independent flag.
7. Floor: **fail closed, no override flag** — full contract or refuse; Windows
   and unknown platforms refuse all sandboxed modes.

## Open questions

None — v2 addresses roborev job 2319 and the fresh-eyes findings; ready for
re-review.
