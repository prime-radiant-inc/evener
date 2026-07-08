# Serf Sandboxing — Design (v1)

Date: 2026-07-08
Status: Draft — awaiting Jesse's spec review
Author: Bot, from a from-scratch recon (the prior session's spec was never found; starting at zero)

## Goal

Stop a poorly-behaved model — whether it goes off the rails accidentally or is
driven off them by prompt injection or misalignment — from reading, writing, or
executing outside a boundary the user opted into at session start. The primary
threat is a *running-amok* agent: `rm -rf` outside the workspace, reading
`~/.ssh`, `curl | sh`-ing a payload, phoning secrets home. The sandbox is
**opt-in per session** and **immutable for the session's lifetime** — nothing
the model can do, including any tool call, may relax it.

Non-goals for v1: a rich per-tool approval/allowlist/denylist policy engine (à la
Claude Code) — that is orthogonal and deferred. Domain-allowlist network egress —
network is all-or-nothing. Confining serf's own long-lived process.

## Threat model (what each layer is for)

Two failure classes, two enforcement layers, per the literature convergence
(Anthropic's containment guidance; the Ona escape incident):

1. **The shell tool runs arbitrary, model-composed input.** A denylist of command
   strings or paths is *fundamentally* bypassable — the Ona incident showed
   Claude Code reasoning its way around a string denylist via
   `/proc/self/root/usr/bin/npx` and `ld-linux.so`, then trying to disable the
   sandbox outright. Therefore **spawned processes must be confined at the kernel
   layer**, not by string inspection.
2. **Serf's own file tools (`read_file`/`write_file`/`edit_file`/`glob`/`grep`/
   `list_dir`) run trusted serf code.** The model's only inputs are path strings;
   containment is a symlink-resolved path check in our own code — the same
   mechanism `resolveWrite`/`ensureUnderRoot` already applies to writes today.
   These do **not** need kernel enforcement because the code between the model's
   input and the syscall is ours, not arbitrary.

This split is the core design decision and where we deliberately diverge from
Codex (which runs even its own `apply_patch` through a sandboxed helper
subprocess). We have exactly two routes from model to filesystem — our file
tools and the shell — so kernel-confining the shell plus in-process-confining our
tools closes both without the helper-subprocess machinery.

## Modes

`--sandbox <mode>`, a mode string, set once at session start. Four modes:

| Mode | serf file-tool reads | serf file-tool writes | spawned-process reads | spawned-process writes | network |
|---|---|---|---|---|---|
| `off` (default) | anywhere | root-confined (today's behavior) | anywhere | anywhere | on |
| `read-only` | anywhere | **denied** | anywhere (system roots) | **denied** | flag |
| `workspace-write` | anywhere (minus secrets denylist) | worktree + session tmp | anywhere (system roots) | worktree + session tmp | flag |
| `restricted` | **worktree only** | worktree + session tmp | worktree + system read roots + session tmp | worktree + session tmp | flag |

Notes that the table can't hold:

- **`off` is exactly today's behavior** — writes are already root-confined via
  `ensureUnderRoot`; reads are already unconfined. No new code path executes. This
  keeps the change a strict superset and the default a no-op.
- **"System read roots"** are a curated bind/allow set so binaries can run at all:
  `/usr`, `/bin`, `/lib`, `/lib64`, `/etc`, `/nix/store`, the Go/toolchain paths,
  read-only. Without these, `restricted` mode can't exec `/bin/bash`. This is the
  point Jesse's "deny reads outside the worktree" resolves to: **serf's file
  tools** are worktree-confined in `restricted` mode (in-process check), while
  **spawned processes** still get system read roots (kernel policy) — otherwise
  nothing executes. The asymmetry is intentional and load-bearing.
- **Secrets denylist** (`workspace-write` reads): blocked from both file tools and
  spawned processes even though the rest of the disk is readable. Default set:
  `~/.ssh`, `~/.aws`, `~/.config/gcloud`, `~/.netrc`, `~/.config/serf`, `~/.gnupg`,
  `~/.docker/config.json`, `~/.kube`, `~/.git-credentials`. **User-extensible via
  config, both directions**: config can add paths to the denylist *and* punch
  holes in it (opt a specific secret path back into readability) — the human
  chooses their own risk. The hard invariant is only that the **model cannot
  change the denylist mid-session** (the whole policy is immutable for the session
  lifetime); config is set by the human at/before session start.
- **`.git` protection**: the worktree's `.git` (hooks dir especially) is
  **read-only even inside the writable root** in every restricted mode, because a
  writable git-hooks dir turns a benign `git commit` into arbitrary code
  execution. Mirrors Codex's `.git`/`.codex` protection.
- **Session tmp**: each session gets its **own** temp dir (not shared `/tmp`),
  writable in every restricted mode. Shared `/tmp` is a cross-session side channel;
  a per-session dir also gives builds a scratch space without widening the
  workspace. Cleaned up on session end.

## Network — all or nothing, honestly

`network=off` means: deny the spawned process's network access. The Linux
mechanism catch (surfaced in recon): **Landlock only restricts TCP, and only on
kernel 6.7+** — a Landlock "network-off" sandbox still permits UDP, so DNS-based
exfiltration stays open. Resolution:

- When `network=off` is requested, **probe for bwrap** and prefer
  `bwrap --unshare-net` (true no-interface network-off).
- If bwrap is unavailable or unprivileged userns is blocked (Ubuntu 24.04+
  AppArmor default, see Mechanism section), **fall back to Landlock TCP-deny with
  a loud startup warning** naming exactly what's not covered (UDP/DNS still
  reachable). Honest degradation, never silent.

File tools make no network calls, so network policy is a spawned-process concern
only. `web_fetch`/`web_search` are serf-process tools, not spawned processes; in
`network=off` they are **disabled with a legible error** (they'd otherwise be an
egress bypass around the sandbox). Stated so it isn't a surprise gap.

## Mechanism (Linux)

**Primary: Landlock**, via `github.com/landlock-lsm/go-landlock`.

- Self-restricting, unprivileged, **no user-namespace dependency** — which
  sidesteps the Ubuntu 23.10+/24.04 `apparmor_restrict_unprivileged_userns=1`
  default that breaks bare `bwrap`. This is the deciding factor over
  bwrap-as-primary.
- Applied via a **re-exec'd helper**: serf forks/execs `serf sandbox-exec`
  (arg0-dispatched, like Codex's `codex-linux-sandbox`), which applies Landlock
  to *itself* (path rules for the mode's read/write roots; TCP-deny when
  `network=off`) and then execs the real command. The agent's own long-lived
  process is never Landlock-restricted (Landlock is irreversible per-process;
  restricting the agent process would be a one-way door).
- `BestEffort()` so an older kernel degrades to the strongest ruleset it supports
  rather than failing the session — with a startup warning stating the actual
  enforcement level.

**Stronger opt-in / network-off path: bubblewrap.** Used for genuine
`--unshare-net` and for full filesystem-invisibility (not just deny). Probed at
session start; its userns requirement is the reason it's not primary. A bwrap
capability probe (`bwrap --unshare-user true`) runs once at startup when a mode
needs it; failure → fall back + warn, never silently run unsandboxed.

**Not used:** seccomp-bpf as a primary layer (syscall granularity doesn't map to
"worktree" semantics, and it's fragile for a Go binary shelling out to arbitrary
tools). Legacy-Landlock-only and Codex's ~2700-line bwrap mount-ordering
edge-case machinery (nested overlays, glob deny-read via ripgrep) are explicitly
de-scoped — flat writable roots + fixed protected subpaths.

## Mechanism (macOS)

`sandbox-exec` (Seatbelt) — deprecated in its man page for ~a decade, still the
only headless option, and what both Codex and Claude Code use. Generate an SBPL
policy from the mode (read/write roots, deny network, protected `.git`), invoke
`/usr/bin/sandbox-exec -p <policy> -- <cmd>` with the path **hard-coded to
`/usr/bin`** to defeat PATH injection (copied verbatim from Codex). Same
policy model as Linux — the mode → policy mapping is shared; only the
enforcement backend differs.

## Where it attaches in serf (seams)

The recon confirmed one architectural fact that makes this tractable: **every
filesystem/shell tool flows through `execenv.ExecutionEnvironment`**, one
interface with one concrete impl, `LocalExecutionEnvironment`
(`agent/execenv/local.go:60`). Tool handlers never call `os`/`exec` directly.

1. **Policy carrier.** Add a `SandboxPolicy` (mode + resolved roots + network bool)
   field to `LocalExecutionEnvironment` alongside `RootDir`/`EnvPolicy`. Threaded
   into new envs at the two construction sites, `cmd/serf/run.go:177` and
   `cmd/serf/serve.go:203`.
2. **Config + flag.** New `Sandbox` field on `SessionConfig`
   (`agent/session_config.go:20`), populated from a `--sandbox <mode>` flag in the
   `run`/`serve` flag sets, defaulting to `off`. Round-trips through config the
   way `EnvPolicy` does (name ↔ enum helpers).
3. **Spawned-process confinement (kernel).** Wrap command construction in
   `shellCommand` / `execPreparedCommand` (`local.go:768`) and `StreamCommand`
   (`local.go:866`) to prefix the helper/`bwrap`/`sandbox-exec` argv per policy.
   Covers `grep` too — it shells out to `rg` through `ExecCommand`
   (`local.go:756`).
4. **File-tool confinement (in-process).** Extend the existing `resolveWrite` →
   `ensureUnderRoot` (`local.go:1136`/`:1158`) confinement to the **read** side:
   `resolve` (`local.go:1121`) currently passes absolute paths through unchanged.
   Under `restricted`, reads route through the same symlink-resolved check;
   under `workspace-write`/`read-only`, reads apply the secrets denylist. `Glob`,
   `Grep`, `ListDirectory` share the resolved base.
5. **Subagent / worktree scoping.** Child envs derive via
   `WithWorkingDirectory` (`local.go:123`), which already copies `EnvPolicy`. The
   `SandboxPolicy` rides the same path, **re-rooted to the child's worktree** — a
   delegate in its own worktree is confined to *that* worktree, not the parent's.
   Persisted in the delegate descriptor next to `LocalEnvPolicy` so a resumed
   delegate restores its sandbox. This is novel surface — no surveyed vendor
   publishes subagent sandbox scoping — so it gets explicit tests.
6. **Denial surfacing.** A sandbox denial (kernel `EACCES` from a wrapped command,
   or an in-process containment rejection) returns a **typed, legible tool error**
   to the model — "sandbox: write to /etc/hosts denied (workspace-write mode;
   writable roots: <worktree>, <tmp>)". Never a silent failure, never an
   auto-retry-unsandboxed. Mirrors Codex `on-request` semantics. In a
   non-interactive session this is terminal.

## Milestones

Sequential; each fully validated (unit + e2e where it has runtime surface) before
the next.

- **M1 — Linux spawned-process confinement.** Landlock helper + the four modes +
  `--sandbox` flag + `SessionConfig`/env plumbing. Shell/grep confined; denials
  legible. bwrap network-off path + capability probe + degradation warnings.
  This is the load-bearing milestone against the running-amok threat.
- **M2 — File-tool in-process confinement.** Read-side `ensureUnderRoot`
  extension, secrets denylist, `.git` read-only, `read-only` mode's write denial.
- **M3 — Subagent / worktree scoping.** Policy re-rooting across
  `WithWorkingDirectory`; delegate-descriptor persistence + resume; the novel-
  surface tests.
- **M4 — macOS (`sandbox-exec`).** Shared mode→policy mapping, Seatbelt backend.
- **M5 — In-UI sandbox-exemption approval** (specced now, built last, after M1–M4
  are validated e2e). See below.

## M5 — In-UI escalation (specced now, built last)

**The cost center of this feature is not the sandbox — it's this.** Serf today has
**no harness-level approval prompt at all**: PreToolUse hooks can *deny*, and
`ask_user` is *model-initiated* (the wrong trust direction — the model must never
approve its own escalation). So "approve this out-of-worktree read/write/exec in
the UI" requires a **new primitive**:

- On a sandbox denial *in an interactive session*, instead of returning the error
  to the model, emit a **sandbox-escalation approval request** over AppWire to the
  human: "The agent tried to write `/etc/hosts` (outside the workspace). Allow this
  one action?" — approve / deny.
- On approve, **re-run that single invocation** with that one path added to the
  policy for that invocation only (gemini-cli's per-invocation expansion model —
  *not* a session-wide relaxation, *not* a persistent allowlist). On deny, the
  typed error goes to the model as in the non-interactive path.
- The escalation principal is the **human via the UI**, out-of-band from the model
  loop. The model cannot trigger, approve, or observe-then-replay the approval.
- Non-interactive sessions: no escalation, denial is final (M1 behavior unchanged).

This primitive is deliberately narrow (single-action, single-invocation,
human-gated) but it is the *same* primitive a future general tool-approval engine
would sit on. Building it narrow now is fine; the rich approve/deny/allowlist/
denylist policy engine stays out of scope and orthogonal, exactly as Jesse
scoped it.

## Resolved decisions (Jesse, 2026-07-08)

1. **Secrets denylist** — default set fixed in the spec above; **user-extensible
   both directions** (add paths, or punch holes to opt specific secrets back into
   readability). Model can never change it mid-session.
2. **`web_fetch`/`web_search` under `network=off`** — **disabled with a legible
   error.** The toggle means the agent can't reach the internet at all, not just
   its shell. No serf-process egress exemption.
3. **`read-only` mode** — **kept.** "Look but don't touch" is a distinct, near-free
   contract (empty writable-root set).
4. **Backend default: Landlock-primary** (option 1). Always Landlock, falling back
   to bwrap **only** when a mode requests `network=off` (Landlock can't deliver
   true network-off — TCP-only, kernel 6.7+). Rejected bwrap-primary because
   enforcement strength would vary silently per host. macOS testing host:
   **paradise-park** (ssh).

## Open questions for review

None outstanding — ready for design review.
