# Sandboxing

Status: Evergreen guide to the `--sandbox` flag and what each mode enforces.

## Summary

`--sandbox <mode>` confines a session's file access, spawned processes, and
(optionally) network egress to a policy chosen once at session start. It is
**off by default**: an unsandboxed session behaves exactly as it always has. A
sandboxed session's boundary is enforced by two cooperating layers — an
in-process, race-safe layer for the file tools (`read_file`, `write_file`,
`edit_file`, `apply_patch`, `glob`, `grep`, `list_dir`) and a kernel layer
(bubblewrap on Linux) for every spawned process (shell jobs, `rg`, stdio MCP
servers, hook commands) and its descendants. The flag is currently live on Linux;
the macOS backend (Seatbelt) is being finalized — see [macOS notes](#macos-notes).

The policy is fixed for the life of the session. Nothing the model does mid-session
can relax it — the mode, the network decision, and the denylist are set from the
command line (or a resumed session's persisted config), never from a tool call.

Sandboxing is opt-in on purpose. The default stays off so existing workflows are
unchanged; turn it on when you want a session's blast radius contained.

## Flags

| Flag | Values | Default | Meaning |
|---|---|---|---|
| `--sandbox` | `off`, `read-only`, `workspace-write`, `restricted` | `off` | The enforcement mode. |
| `--sandbox-net` | `on`, `off` | `on` | Network egress for the tool plane. Only applies with a non-off mode. |

Both flags are available on `serf` and `serf serve`. `--sandbox-net` is meaningful
only when a non-off `--sandbox` mode is set; with `--sandbox off` it is ignored.

When a session starts sandboxed, serf prints one line to stderr stating exactly
what is enforced on this host — the backend, the mode, the network decision, and
how caches are served — for example:

```
sandbox: bwrap enforcing --sandbox restricted (network on, cache cold session-private)
```

This line is read from the *resolved* policy, so it never overstates: if a mode
degraded (e.g. the cache fell back from a warm overlay to a cold session-private
redirect because the host's bubblewrap lacks overlay support), the line says so.

## Modes

All sandboxed modes share a common floor: a per-session writable temp directory
(exported as `TMPDIR`), the secrets + pseudo-filesystem denylist (masked in both
layers), git config/hook protection, a fresh PID namespace with its own `/proc`, a
minimal `/dev`, the environment floor, and no inherited serf file descriptors or
sockets beyond stdio.

| Mode | File-tool reads | File-tool writes | Spawned-process reads | Spawned-process writes |
|---|---|---|---|---|
| `off` (default) | anywhere | working root only (today's behavior) | anywhere | anywhere |
| `read-only` | anywhere minus the denylist | denied (temp only) | anywhere minus the denylist | temp only |
| `workspace-write` | anywhere minus the denylist | worktree + temp + contained caches | anywhere minus the denylist | worktree + temp + caches + git metadata (not config/hooks) |
| `restricted` | worktree only | worktree + temp | worktree + system read roots + temp | worktree + temp + git metadata (not config/hooks) |

- **`off`** is exactly today's behavior — a strict superset of every sandboxed
  mode, with no new code path engaged.
- **`read-only`** cannot commit or write project files; only the session temp is
  writable, for scratch. Use it for review, exploration, and analysis.
- **`workspace-write`** grants writes to the worktree, the session temp, and
  contained caches. This is the natural mode for coding work that must not touch
  anything outside the project.
- **`restricted`** is the tightest mode: the model's file tools can only browse and
  write inside the worktree. Spawned processes additionally get read-only access to
  the system roots a process needs to run — `/usr`, `/bin`, `/sbin`, `/lib`,
  `/lib64`, `/etc`, `/opt`, `/nix/store` — but not
  `/proc`. The distinction is deliberate: the file tools expose what the *model*
  may browse; the kernel layer grants what a *process* needs to execute.

## The fail-closed floor

A mode is either fully enforced or the session refuses to start. There is no
"best effort" and no override flag: serf will not run a session that claims a
sandbox mode it cannot actually enforce.

| Host capability | Modes that run | Otherwise |
|---|---|---|
| Linux with a working bubblewrap (unprivileged user namespaces usable) | all modes, `--sandbox-net` on or off | — |
| Linux without a usable bubblewrap, Windows, macOS (for now), any other OS | `off` only | any non-off mode refuses to start |

macOS enforcement (Seatbelt) is designed and implemented but not yet live in this
release, so a non-off mode currently refuses to start on macOS with the same
fail-closed message; see [macOS notes](#macos-notes).

On Linux, "bubblewrap usable" means more than the binary being present: serf runs a
real unprivileged-user-namespace probe at startup, so a host that ships `bwrap` but
blocks unprivileged user namespaces (for example Ubuntu 24.04 with
`apparmor_restrict_unprivileged_userns=1`) reports *not capable* and refuses a
sandboxed mode rather than half-enforcing it.

When the host cannot satisfy a requested mode, the refusal is a legible start-time
error naming the mode and the reason — the session never silently runs unconfined.
This holds on resume too: a session persisted with a sandbox mode re-resolves its
policy against the *current* host at resume time, so a host that can no longer
enforce the mode fails the resume closed instead of resuming unconfined. The
persisted mode is authoritative on resume (immutable across restart); the flag
governs only fresh sessions.

## The denylist: secrets and pseudo-filesystems

Every sandboxed mode masks a default set of paths in **both** layers, so neither a
file tool nor a spawned process can read them:

- **Credential directories and files** (resolved against `$HOME`): `~/.ssh`,
  `~/.aws`, `~/.config/gcloud`, `~/.netrc`, `~/.config/serf`, `~/.gnupg`,
  `~/.docker/config.json`, `~/.kube`, `~/.git-credentials`.
- **Pseudo-filesystems and runtime sockets**: `/proc`, `/sys`, `/dev/fd`,
  `/dev/mem`, `/run/user`, and the well-known privileged daemon control sockets
  (`/run/docker.sock`, `/var/run/docker.sock`, `/run/podman/podman.sock`,
  `/run/containerd/containerd.sock`, `/run/dbus/system_bus_socket`).

Masking `/proc` is load-bearing: without it a `read_file("/proc/<serf-pid>/environ")`
would leak serf's own environment, including the provider API key. Masking the
daemon sockets turns a `connect()` into `ECONNREFUSED`, so a session cannot drive a
container daemon straight to host root even with `--sandbox-net off` (a read-only
bind of `/` does not block a Unix-socket `connect()`, and `--unshare-net` does not
affect `AF_UNIX`).

The denylist is **user-extensible in both directions** and never model-changeable
mid-session:

- **Add** paths to mask (absolute, `~/`-relative, or bare-relative to `$HOME`).
- **Remove** paths from the *credential* set to un-mask them.

The pseudo-filesystem floor (`/proc`, `/sys`, …) is **not removable** — a stray or
malicious removal can never re-open `/proc`. Only the credential set and your own
additions are removable.

## Git config and hooks are read-only

In the writable modes, git's object store works normally — objects, refs, the
index, logs, and packed-refs are writable, so `commit`, `add`, and `checkout`
succeed. But every git **config and hook** surface is read-only:

- `.git/config`, per-worktree config (`config.worktree`), submodule configs
  (`.git/modules/*/config`), and `.git/hooks`.
- In a linked worktree, git's own writes land under
  `<main>/.git/worktrees/<id>/`; the main repo's `.git/config` is read-granted
  (git must read common config even from a linked worktree) but never writable.

Because every config file git reads is read-only **and** `$HOME` config files are
unwritable, a `core.hooksPath` redirect cannot persist and no hook can be planted to
run later, unsandboxed. The visible cost is that `git config --local ...` fails
inside a sandboxed session — that write is exactly the persistence vector, so its
denial is by design, not a bug.

## Caches are contained, never poisoned

Invariant: a sandboxed session can never poison a cache that a later build consumes.

- `workspace-write` serves the language cache roots (`~/.cache`, `~/go/pkg`,
  `~/.npm`, `~/.cargo`, plus any you add) as a **read-real, write-private overlay**
  where the host's bubblewrap supports it: builds read warm from the real cache, but
  writes land in a per-session tmpfs that is discarded at session end.
- Where overlay is unavailable (macOS/Seatbelt, or a bubblewrap without overlay
  support — including bubblewrap 0.9.0), the cache **degrades to a session-private
  redirect**: `GOCACHE`, `npm_config_cache`, and `CARGO_HOME` point into the session
  temp (a cold cache), never to a persistent-writable location.
- `restricted` always uses the session-private redirect.

The overlay is a performance optimization (warm vs cold reads); the no-poisoning
floor holds either way.

## The environment floor

On top of serf's existing scrub of `*KEY*`/`*SECRET*`/`*TOKEN*`/`*PASSWORD*`/
`*CREDENTIAL*` variables, a sandboxed session raises an additional floor on every
spawned process:

- Drops `SSH_AUTH_SOCK` (a live ssh-agent socket is sign-anything even with `~/.ssh`
  masked), `GNUPGHOME`, and `DOCKER_HOST`.
- Drops the cloud credential-agent variable families `AWS_*`, `GOOGLE_*`,
  `GCLOUD_*`, and `VAULT_*`.
- Drops a `KUBECONFIG` that points outside every granted root (an external cluster
  config the session should not reach).
- Points `TMPDIR` at the per-session temp and, under the session-private cache
  strategy, redirects `GOCACHE`/`npm_config_cache`/`CARGO_HOME` there.

ssh-agent and gpg-agent Unix sockets are not bind-mounted into the sandbox, and
spawned commands inherit no serf file descriptors beyond stdio — not serf's live
LLM-API connection, not a credential fd.

## Network

`--sandbox-net` defaults to **on** when sandboxed: filesystem containment is the
load-bearing guarantee, and egress denial is opt-in. (This deliberately differs from
tools that default to deny.)

`--sandbox-net off` governs the **tool plane**:

- Spawned processes get `--unshare-net` — no network interfaces at all, so no TCP,
  UDP, or DNS.
- Serf-process tool egress — `web_fetch`, `web_search`, and remote HTTP/SSE MCP
  servers — is disabled with a legible error.
- Provider-native web egress (server-side web search or fetch the provider runs for
  the model) is disabled, and the provider-capability registry **fails closed** for
  unknown capabilities, so `net=off` can't be silently false through a path you
  cannot inspect.

What `--sandbox-net off` does **not** touch: the LLM inference traffic itself. The
agent still talks to its model — `net=off` never promised otherwise.

## Denials

A denied operation returns a **typed, legible error** the model can reason about
(the tool, the mode, and the offending path by basename), not a silent failure or a
retry loop. In-process file-tool denials know the exact path; kernel denials are
attributed best-effort from the command and its output-so-far. Denials are
audit-logged with a redaction contract — the log records the mode, tool, a redacted
path, and a truncated command, never the file contents or a full secret path.

In a non-interactive session a denial is final. (In-UI escalation — a human-gated
approval card for a specific denied invocation — is specified but not yet built; the
model can never trigger, approve, or observe an approval.)

## Subagents and worktrees

A sandboxed session's delegates and subagents inherit the policy automatically,
re-rooted to their own worktree with fresh git-metadata resolution — a delegate in
lane A cannot read lane B. Because inheritance is automatic, the flag only goes live
on a build where subagent/worktree scoping already holds, so the first `delegate`
never punches a hole in the boundary you opted into. Resumed delegates re-resolve
their persisted policy the same way the root session does. See
[docs/worktrees.md](worktrees.md) for the worktree model itself.

## Hooks and MCP under a sandbox

- **Hook commands** (PreToolUse, PostToolUse, etc.) run under the session sandbox,
  same as any spawned process. A hook that needs broader access than the sandbox
  grants is incompatible with a sandboxed session — run such hooks unsandboxed, or
  widen the policy's roots/denylist from the command line.
- **stdio MCP servers** are spawned under the session sandbox.
- **Remote (HTTP/SSE) MCP servers** require `--sandbox-net on`; under `net=off`
  their egress is disabled.

## Known residuals

Two boundary edges are deliberately documented as open rather than claimed closed:

- **A pre-existing hardlink** inside the worktree to an out-of-tree secret is
  *readable* through the worktree (path-based masking cannot see that two names share
  an inode). A *write* through such a hardlink does not propagate to the original —
  the file tools write atomically via temp-plus-rename, which replaces the name with
  a fresh inode. This read residual is out of the running-amok threat model.
- **Daemon-socket masking is a targeted list** (docker, podman, containerd, dbus).
  An exotic or custom daemon socket under `/run` that is not on the list could still
  be reached; a broader `/run` mask is deferred because it would also hide
  legitimate runtime state and break DNS/daemons.

## macOS notes

macOS enforcement is **not yet live in this release**: a non-off `--sandbox` mode
currently refuses to start on macOS (the fail-closed floor), because the kernel-layer
wiring is finalized on Linux first. Turn the flag on only on Linux for now.

The macOS backend is Seatbelt (`/usr/bin/sandbox-exec`), driven from the same policy
model so all modes are expressible; caches are always served session-private (there
is no overlay on macOS), and the `sandbox-exec` path is hard-coded as a
PATH-injection defense. When macOS goes live, a host without `/usr/bin/sandbox-exec`
will still refuse any sandboxed mode.

## Related

- [docs/worktrees.md](worktrees.md) — the worktree model that backs delegate
  isolation.
- [docs/environment.md](environment.md) — the environment variables serf reads.
- [docs/subagent-runtime-contracts.md](subagent-runtime-contracts.md) — the runtime
  contracts subagents, plugins, and hooks operate under.
