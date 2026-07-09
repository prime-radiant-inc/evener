# Serf Sandboxing — Design (v4)

Date: 2026-07-08 — lineage: v1 recon; v2 (job 2319: Landlock allowlist-only →
bwrap-preferred, fail-closed floor); v3 (job 2321: apply_patch bypass, git-config
persistence, `/proc` PID-ns, rollout ordering); v4 (job 2323: in-process `/proc`
read via file tools, TOCTOU path race, cache poisoning containment, fd hygiene,
provider-web registry).
Status: Draft — implementation-ready pending Jesse's sign-off. See "Review
stopping point" at end.
Author: Bot. Prior session's spec was never found; this lineage starts at zero.

## Goal

Stop a poorly-behaved model — accidental or deliberate — from reading, writing,
or executing outside a boundary the user opted into at session start, **and**
from planting anything that runs later outside the sandbox. Opt-in per session;
**immutable for the session's lifetime** — no tool call can relax it.

Non-goals for v1: rich per-tool approval/allowlist engine (orthogonal,
deferred); domain-allowlist egress (network is all-or-nothing); confining serf's
own long-lived process; Windows (fail closed).

## Threat model — two layers, one adversary

The model is treated as an **adversarial reasoner that can spawn concurrent
processes**, not a fuzzer. Both layers must hold against deliberate evasion.

1. **Spawned processes run arbitrary model-composed input** → **kernel
   enforcement** (bwrap namespaces+mounts / Seatbelt). String denylists are
   bypassable (Ona: `/proc/self/root` aliasing, `ld-linux.so` indirect exec).
   Kernel policy is inode/namespace based, inherited by all descendants (an
   `npm install` postinstall is as confined as its shell), and includes a fresh
   PID namespace + private `/proc` so host process state is invisible.
2. **Serf's own file tools run in the unsandboxed serf process** → **in-process
   enforcement that is both path-correct and race-safe** (see below). This is
   the *privileged* path — a file tool that follows an attacker-planted symlink
   writes with serf's full authority. It gets the most scrutiny.

### Race-safe path enforcement (v4 — the load-bearing in-process requirement)

Resolve-then-open is a TOCTOU hole: a model-spawned background job can swap a
path component for an out-of-worktree symlink between serf's check and its
`open`/`write`/`rename`. Requirements:

- **Linux**: `openat2(2)` with `RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS` (via
  `golang.org/x/sys/unix.Openat2`), anchored at a base fd for the allowed root;
  all reads/writes operate on the returned fd, never re-opening by path.
- **macOS** (no `openat2`): open each path component with
  `openat(O_NOFOLLOW | O_DIRECTORY)` walking beneath a base-root fd, then operate
  on the final fd. (This is the fd-walk that path-string secure-join can't
  provide.)
- Writes are atomic temp-file-plus-`renameat` beneath the checked fd; never a
  path-based re-open after checking.
- **Implementation note**: `LocalExecutionEnvironment` currently uses an `afero`
  path-based fs. The race-safe path needs real fds, so sandboxed file ops bypass
  afero's path API for OS-backed fd operations (afero stays for `off` mode and
  tests). Flagged because it's non-trivial refactor weight in M2.
- Race tests: concurrent symlink/component replacement during read, write,
  rename, apply_patch.

### Denylist by masking; pseudo-filesystems included (v4)

- **Spawned procs**: denied dirs made **invisible** (bwrap tmpfs/`/dev/null`
  over-mounts) + fresh PID-ns `/proc` — defeats symlink, `/proc/pid/root`, and
  hardlink *creation* against secrets (can't `ln`/read a source you can't see).
- **File tools**: after race-safe resolution, denied paths refused. The
  in-process denylist **must include host pseudo-filesystems**, not just
  credential dirs: `/proc`, `/sys`, `/dev/fd`, `/dev/mem`, `/run/user/*` (agent
  sockets). Rationale (v4 High): file-tool reads are "anywhere − denylist," and
  `read_file("/proc/<serf-pid>/environ")` would otherwise read **serf's own
  environment — including the provider API key**. The spawned-proc `/proc` fix
  does not cover the file-tool path; this does.
- **Residuals, documented not claimed-closed**: a *pre-existing* hardlink to a
  secret (read) or to an outside file (write-through) already sitting inside a
  readable/writable root — a hardlink shares an inode with no backlink to
  resolve. Masking closes the *create-then-use* path (can't reference an
  invisible source); pre-planted links need inode-preflight scanning, deferred.
  Out of the running-amok threat model.

### Persistence — closed on all known in-writable-set surfaces

A sandboxed write that executes *later, unsandboxed* is an escape. Coverage:

- `$HOME` is never writable → `~/.bashrc`, `~/.profile`, `~/.gitconfig`,
  `~/.config/autostart`, user systemd units already write-denied.
- git: all config + hook surfaces read-only (below) → `core.hooksPath` redirect
  can't be *persisted*, planted hooks stay inert.
- caches: contained (read-real/write-private or session-private) → **no cache
  poisoning in any sandboxed mode** (v4, below).

## Modes

`--sandbox <mode>`, set once. Network is an independent flag. All sandboxed modes
share: per-session tmp (writable, `TMPDIR`), secrets+pseudo-fs denylist (masked),
git-metadata protection, `--unshare-pid`+fresh `--proc`, minimal `/dev`, env
floor, no inherited serf fds/sockets, race-safe file-tool enforcement.

| Mode | file-tool reads | file-tool writes | spawned reads | spawned writes |
|---|---|---|---|---|
| `off` (default) | anywhere | root-confined (today) | anywhere | anywhere |
| `read-only` | anywhere − denylist | **denied** (tmp only) | anywhere − denylist | tmp only |
| `workspace-write` | anywhere − denylist | worktree + tmp + caches* | anywhere − denylist | worktree + tmp + caches* + gitdir(−config/hooks) |
| `restricted` | **worktree only** | worktree + tmp | worktree + system read roots + tmp | worktree + tmp + gitdir(−config/hooks) |

\* caches = read-real/write-private overlay (below), not persistent-writable.

- **`off` is exactly today's behavior** — no new code path; strict superset.
- **`read-only` cannot commit**; session-tmp writes allowed for scratch.
- **System read roots** (`restricted` spawned procs): `/usr`, `/bin`, `/lib*`,
  `/etc`, `/opt`, `/nix/store`, toolchain roots — read-only, excludes `/proc`.
  File tools stay worktree-only (tools = what the *model* may browse; kernel =
  what a *process* needs to run).
- **Secrets denylist** (masked, both layers): `~/.ssh`, `~/.aws`,
  `~/.config/gcloud`, `~/.netrc`, `~/.config/serf`, `~/.gnupg`,
  `~/.docker/config.json`, `~/.kube`, `~/.git-credentials`, **+ pseudo-fs
  (`/proc`, `/sys`, `/dev/fd`, `/dev/mem`, `/run/user/*`)**. User-extensible both
  directions; never model-changeable mid-session.
- **Git-metadata protection (complete).** Writable: objects, refs, index, logs,
  packed-refs (so commit/add/checkout work). Read-only in every writable mode:
  **all config + hook surfaces** — `.git/config`, per-worktree config
  (`config.worktree` + `extensions.worktreeConfig`), submodule configs
  (`.git/modules/*/config`), `.git/hooks` — resolved through `gitdir:`. Because
  every config file git reads for the repo is read-only **and** `$HOME` configs
  are unwritable, a `core.hooksPath` redirect can't persist. Cost: `git config
  --local` fails in-sandbox (legible denial) — that write is exactly the vector.
- **Linked-worktree gitdir.** git writes land in `<main>/.git/worktrees/<id>/`,
  `objects/`, `refs/`, `logs/`, `packed-refs` (granted, minus config/hooks). The
  **main repo's `.git/config` and common config are read-granted (write-denied)**
  — git must *read* common config even from a linked worktree (v4 fix: not
  "never granted", which would break linked-worktree git).
- **Cache roots (v4 — contained, not just warned).** Invariant: **a sandboxed
  session can never poison a cache a later build consumes.** `workspace-write`
  serves cache roots (`~/.cache`, `~/go/pkg`, `~/.npm`, `~/.cargo`,
  user-extensible) as **read-only-lower + private-upper overlay** (`bwrap
  --overlay-src <cache> --tmp-overlay`): warm reads from the real cache, writes
  land in a per-session tmpfs discarded at session end. Where overlay is
  unavailable (Seatbelt/macOS, bwrap < 0.5): **degrade to session-private
  redirect** (`GOCACHE`/`npm_config_cache`/`CARGO_HOME` → session tmp; cold) —
  never to persistent-writable. `restricted`: session-private redirect always.
  The overlay is a *performance* optimization; the security floor (no poisoning)
  holds regardless of backend.
- **Session tmp**: per-session (not shared `/tmp`), `TMPDIR`, cleaned on end;
  crashed-session dirs age-swept at next serf start.

## Environment & descriptor floor

- Serf already drops `*KEY*/*SECRET*/*TOKEN*/*PASSWORD*/*CREDENTIAL*` env.
  Sandbox raises a floor **on top of** the user's `EnvPolicy` (composes; scrub
  stays an independent knob): also drop `SSH_AUTH_SOCK`, cloud cred-agent vars
  (`AWS_*`, `GOOGLE_*`, `GCLOUD_*`, `VAULT_*`, worktree-external `KUBECONFIG`).
- ssh-agent / gpg-agent Unix sockets are **not** bind-mounted in (a live agent
  socket is sign-anything/exfil even with `~/.ssh` masked).
- **Spawned commands inherit no serf fds/sockets** beyond stdio: `exec.Cmd`
  runs with empty `ExtraFiles`, serf's fds are `O_CLOEXEC`, and bwrap passes no
  extra fds. Prevents inheriting serf's live LLM-API connection or credential
  fds.

## Network — one flag, honest semantics

`--sandbox-net=on|off`, default **on** when sandboxed (FS containment is
load-bearing; egress denial is opt-in — deliberately diverges from
Codex/Claude-Code default-deny).

`network=off` governs the **tool plane**: spawned procs (`--unshare-net`: no
interfaces — no TCP/UDP/DNS); serf-process tool egress (`web_fetch`/`web_search`,
remote HTTP/SSE MCP servers) disabled with a legible error; and **provider-native
egress** (server-side web-search / fetch the provider runs for the model) — see
the provider-capability registry (M-task) which **fails closed for unknown
provider capabilities**, so `net=off` can't be silently false through a path the
user can't inspect. **LLM provider inference traffic is unaffected** — the agent
still talks to its model; net=off never promised otherwise.

`network=off` requires bwrap (no Landlock TCP-only variant — leaves UDP/DNS
open).

## Backends and the enforcement floor

**Linux: bwrap-preferred.** Probe once at start (capability check, binary
resolved outside cwd; overlay support probed separately for the cache path).
bwrap expresses the full model: writable binds, subtractive masking,
`--unshare-net`, `--unshare-pid`+`--proc`, minimal `/dev`, `--overlay`. One
startup line states backend + exact enforcement set (incl. warm-overlay vs
cold-cache).

**Landlock fallback** (`go-landlock`, no userns dep — immune to Ubuntu 24.04
`apparmor_restrict_unprivileged_userns`). Allowlist-only, can't subtract → under
fail-closed it serves exactly **`restricted` in a linked worktree, net=on**
(pure additive grants: worktree + system read roots + tmp + gitdir rw-subset;
main `.git/config` read-granted; config/hooks never granted; `/proc` never
granted). It cannot serve `workspace-write`, `read-only` (subtractive denylist),
`restricted` on a main checkout, or `net=off`.

**Floor: full contract or refuse — no override flag.**

| Host capability | runs | refuses |
|---|---|---|
| bwrap capable | all modes, net on/off | — |
| Landlock only | `restricted` in a linked worktree, net=on | everything else |
| neither / Windows / other OS | `off` only | any sandboxed mode |

No `BestEffort()`, no `--sandbox-allow-degraded`.

**Not used:** seccomp-bpf as primary; Codex's ~2700-line bwrap mount-ordering
machinery — flat roots + fixed protected-subpath/denylist sets.

## Backend (macOS)

`sandbox-exec` (Seatbelt) — deprecated a decade, only headless option; used by
Codex + Claude Code. SBPL from the same policy model (deny-capable → all modes
expressible), `/usr/bin/sandbox-exec -p <policy> -- <cmd>`, path hard-coded
(PATH-injection defense). Cache path degrades to session-private (no overlay).
**Explicit parity tests** (v4): network denial, pseudo-fs/process exposure, and
path-race behavior must match the Linux contract. Validated on **paradise-park**.

## Tool-surface inventory

| Surface | Coverage |
|---|---|
| `shell` (fg + background jobs, `StreamCommand`) | kernel wrapper |
| `read_file`/`write_file`/`edit_file` | in-process, race-safe (openat2/fd-walk) |
| **`apply_patch`** | in-process — **does NOT flow through execenv today** (`session_tools_shell.go:233` → `tool.ApplyPatch(env.WorkingDirectory(),patch)` → `os.ReadFile/WriteFile/Remove/Rename`, `apply_patch.go`). **Decision (v4): must be refactored to route through the race-safe execenv layer before `--sandbox` ships — NOT disabled.** Disabling a core edit tool to pass a milestone is rejected. |
| `glob`/`grep`/`list_dir` | in-process base resolution; `rg` subprocess kernel-wrapped |
| `manage_worktree` (main-repo control env) | control policy: main repo + `.git/worktrees` registry writes; config/hooks denied |
| stdio MCP servers | spawned under session sandbox |
| remote MCP servers | net=on only |
| hook commands (PreToolUse etc.) | spawned under session sandbox; broader-access hooks incompatible with sandboxed sessions (documented) |
| `web_fetch`/`web_search` + provider-native web | net=on only; registry fails closed on unknown provider capabilities |
| delegate/subagent sessions | inherit policy re-rooted to their worktree |
| `compact_context`, `ask_user`, job-control | no fs/network surface |

## Seams (recon-verified)

Everything flows through `execenv.ExecutionEnvironment` →
`LocalExecutionEnvironment` (`agent/execenv/local.go:60`).

1. **Policy carrier**: `SandboxPolicy` (mode, resolved roots, denylist incl.
   pseudo-fs, net, backend, env-floor, cache-strategy) on
   `LocalExecutionEnvironment`; constructed at `cmd/serf/run.go:177` /
   `serve.go:203`.
2. **Config/flags**: `Sandbox`+`SandboxNet` on `SessionConfig`
   (`session_config.go:20`); `--sandbox`/`--sandbox-net`; denylist/root
   extensions in config, human-only, resolved before start.
3. **Kernel wrap**: prefix backend argv in `shellCommand`/`execPreparedCommand`
   (`local.go:768`) + `StreamCommand` (`local.go:866`); same at MCP-stdio + hook
   spawn sites; empty `ExtraFiles` + `O_CLOEXEC`.
4. **In-process race-safe layer**: replace `resolve`/`resolveWrite`
   (`local.go:1121`/`:1136`) path-string checks with openat2/fd-walk beneath a
   base-root fd; `Glob`/`Grep`/`ListDirectory` and the **refactored
   `apply_patch`** route through it; pseudo-fs denylist enforced here.
5. **Subagents/worktrees**: policy rides `WithWorkingDirectory` (`local.go:123`)
   like `EnvPolicy`, re-rooted to the child worktree with fresh gitdir
   resolution; persisted in the delegate descriptor beside `LocalEnvPolicy`;
   resumed delegates re-apply. Novel surface — explicit tests.
6. **Denials**: typed, legible tool error; in-process denials know the path,
   kernel denials attributed best-effort, never silently retried. Every denial
   audit-logged with a **redaction contract** (v4): log mode + tool + a
   redacted path (basename or `<denied>` token for denylisted/secret paths) +
   truncated command; never the file *contents* or full secret path.
   Non-interactive: final.

## Milestones (v4 — M1 sliced; flag ships only after subagent inheritance holds)

- **M1 — Policy core + contract tests** (sliced):
  M1a policy/config/flag parsing; M1b root + gitdir/submodule/config-surface
  resolution; M1c backend + overlay capability probing; M1d backend-agnostic
  contract-test harness (mode + host facts → exact grants/denials/refusals),
  which every backend must satisfy. No user-visible flag.
- **M2 — File-tool race-safe in-process layer.** openat2/fd-walk; strict
  resolution; secrets+pseudo-fs denylist; `read-only` write denial;
  **`apply_patch` refactored** (per-tool checklist: `read_file`, `write_file`,
  `edit_file`, `apply_patch`, `glob`, `grep`, `list_dir`, each with acceptance +
  race tests); denial errors + redacted audit log.
- **M3 — Linux kernel layer** (internal slices, flag NOT live): M3a bwrap FS +
  `--unshare-pid`/`--proc`/`/dev`; M3b Landlock worktree-restricted fallback;
  M3c jobs/`rg`/MCP-stdio/hook wrapping + fd hygiene; M3d session tmp + cache
  overlay/redirect + env floor; M3e net=off + provider registry + floor refusals
  + startup line. Each slice vs. the M1 contract suite.
- **M4 — Subagent/worktree scoping.** Re-rooting, descriptor persistence,
  resume, depth inheritance, cross-lane isolation.
- **M5 — `--sandbox` goes live on Linux** — ships only once **M3 *and* M4** pass
  e2e together (a sandboxed session can delegate the instant it's visible).
- **M6 — macOS Seatbelt** vs. the M1 contract suite + parity tests; live on
  paradise-park; flag live on macOS.
- **M7 — In-UI escalation** (below), after M1–M6 validated e2e.

## M7 — In-UI escalation (specced now, built last)

Serf has no harness approval prompt (PreToolUse only denies; `ask_user` is
model-initiated — wrong trust direction). New human-gated AppWire primitive,
interactive only:

- **File-tool denials** (precise path, no partial effects): approve → re-run that
  single invocation with that path added *for that invocation only*
  (gemini-cli model). Deny → typed error to model. **This is what v1 ships** —
  escalation is allowlisted to the single-file tools (`read_file`/`write_file`/
  `edit_file`); `apply_patch` (multi-file) and the browse tools
  (`glob`/`grep`/`list_dir`, which walk a directory subtree) stay final, so one
  grant can never widen more than one leaf. The grant opens the one path from `/`
  with **every symlink refused** (parent and leaf), so it widens root-containment
  only — never symlink resolution, masking, or git-protection.
- **Shell denials — OUT OF SCOPE (v1), architecturally unbuildable on the current
  backend.** bwrap confines by **masking** (a tmpfs / `/dev/null` bind over denied
  paths) and **not-mounting**, not by an attributable syscall refusal. A sandboxed
  shell command that hits a denied path therefore either reads *empty content with
  exit 0* (a masked secret — no error at all), gets *ENOENT indistinguishable from
  a genuinely missing file*, or *EROFS on a write*. There is **no signal** to
  attribute a denied path to, so no honest escalation card can be built: it would
  never fire for the common (masked-read) case and would false-positive on every
  ordinary missing-file error. `DeniedError.Command`/`OutputSoFar` are therefore
  **reserved** (never populated by any current path) for a future seccomp-notify
  design that could intercept and attribute the syscall; the card and the wire
  type keep the shell shape dormant so it is a drop-in when such a backend exists.
- Model cannot trigger/approve/observe/replay approvals. No session-wide
  relaxation, no persistent allowlist (the deferred policy engine sits here
  later).
- Non-interactive: unchanged (denial final). Zero live subscribers → deny
  immediately (do not block a card no one can answer).

## Validation

- **Adversarial escape suite** (named deliverable, extends
  `cmd/serf-hub/sandbox_test.go`'s containment invariant): symlink-out (file
  tools + shell), **TOCTOU symlink-swap race during read/write/rename/apply_patch
  (concurrent job)**, **`read_file("/proc/<serf-pid>/environ")` + `/proc/1/root`
  + `/proc/<pid>/root` aliasing** (expect denied both layers), fd/root aliasing,
  `ld-linux.so <denied-binary>`, `.git/hooks` write + `core.hooksPath` redirect
  persist attempt, `config.worktree`/submodule-config tamper,
  `~/.bashrc`/`~/.gitconfig` write (expect write-confinement denial),
  denylist read via every file tool incl. `apply_patch`,
  hardlink-create-then-use vs. masked secret (blocked by masking) +
  pre-existing-hardlink read/write-through (documented residual, asserted),
  inherited-fd/agent-socket egress, cache-poison-then-consume (expect
  overlay/private isolation), net=off egress (TCP + UDP/DNS + provider-native).
- Per-milestone unit + e2e; live denial-UX feel-testing (no model retry loops);
  bwrap + Landlock paths each e2e'd; M1 contract suite runs against both Linux
  backends and Seatbelt (parity).
- Edge cases pinned: nested worktrees, submodules, resumed sessions re-applying
  policy, deleted-then-recreated roots, delegate resume after serf restart,
  overlay-unavailable degradation.

## Resolved decisions (Jesse, 2026-07-08)

1. Secrets denylist: default set (+ pseudo-fs); user-extensible both directions;
   never model-changeable.
2. `web_fetch`/`web_search`, remote MCP, provider-native web disabled under
   net=off (registry fails closed).
3. `read-only` kept; cannot commit; session-tmp writes allowed.
4. Backend: bwrap-preferred, Landlock fallback (worktree-restricted only);
   per-host enforcement stated loudly at startup.
5. MCP servers and hook commands run inside the session sandbox.
6. Network default on when sandboxed; single independent flag.
7. Floor: fail closed, no override — full contract or refuse; Windows/unknown
   refuse all sandboxed modes.
8. Cache: read-real/write-private overlay where supported, else session-private;
   never persistent-writable — **no cache poisoning in any sandboxed mode**.
9. `apply_patch`: refactored through the race-safe execenv layer, not disabled.

## Review stopping point

Three design-review rounds hardened this from "solid split, wrong primary
mechanism" (v1) to implementation-ready (v4). Round 3 found two genuine
containment holes (in-process `/proc` read; TOCTOU race) — worth the round.
Beyond v4 the expected findings are judgment calls or diminishing returns; a
security design asymptotes and never earns a clean "Pass." **Recommendation:
treat v4 as buildable and let each milestone's own code review (esp. M2's
race-safety and M3's bwrap policy) catch implementation-level issues, rather than
iterating the design doc further.** M1 is the next action.

## Open questions

None — awaiting Jesse's go/no-go on starting M1.
