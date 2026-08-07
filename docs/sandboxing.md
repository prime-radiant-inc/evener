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
servers, hook commands) and its descendants. The kernel layer is bubblewrap on
Linux and Seatbelt (`sandbox-exec`) on macOS — see [macOS notes](#macos-notes).

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
what is enforced on this host — the backend, the mode, the network decision, that
credential/secret paths are masked, and how the language caches are served — for
example:

```
sandbox: bwrap enforcing restricted (network on, secrets masked, cache private)
sandbox: bwrap enforcing workspace-write (network on, secrets masked, cache shared-read/private-write)
```

This line is read from the *resolved* policy, so it never overstates. The `cache`
field is plain words for how the language caches (Go, npm, cargo) are served:
`shared-read/private-write` reads the real host caches but keeps writes in a
private overlay; `private` redirects the caches into a fully private per-session
directory (the fallback when the host's bubblewrap lacks overlay support); `none`
when the mode never writes caches (read-only). A degraded resolution (e.g. overlay
unavailable → `private`) is reflected honestly in the line.

## What the session is told

The model gets the same facts in its system prompt's `<environment>` block, as a
short capability preamble: the sandbox mode and network decision, a summary of
the writable roots, the masked-path count, the scratch directory behind
`$SERF_SCRATCH_DIR`/`$TMPDIR`, the cache strategy, the resolved `GOCACHE` /
`GOMODCACHE`, and a toolchain probe (the exit status of `git config --list`,
and which of `go`, `node`, `rg` are on PATH). An unsandboxed session gets the
same block minus the sandbox-derived lines. It exists so a session reads its box instead of discovering it by failing
at it.

The same rule governs it: every line is read from the resolved policy or
measured once at session start by a single short-timeout probe, run through the
session's own execution environment (so what it measures is what a real command
meets). A probe that cannot run, errors, or times out renders `unprobed` —
never a guess. Paths and counts only: no environment variable values that could
be credentials. Each probe is reported as the command it ran and that command's
exit status, never as a broader claim: `git config --list` exiting 0 says the
config chain is readable, not that every git operation succeeds.

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
  `/lib64`, `/etc`, `/opt`, `/nix/store`, and on macOS the developer-toolchain
  directories below — but not
  `/proc`. The distinction is deliberate: the file tools expose what the *model*
  may browse; the kernel layer grants what a *process* needs to execute.

  On macOS the developer-toolchain directories are part of that set, **read-only**
  (ruled 2026-08-06). They are the active developer directory reported by
  `xcode-select -p` — widened to its enclosing application bundle, typically
  `/Applications/Xcode.app` — plus `/Library/Developer/CommandLineTools`. Without
  them restricted mode could not run `git` at all: `/usr/bin/git` is an `xcrun`
  shim that execs the real binary out of one of those directories, and neither
  sits under the system roots above. A directory that is absent simply
  contributes nothing; a session never fails to start over one. The grant reaches
  the spawned layer only — the model's file tools cannot browse the toolchain —
  it adds nothing to any write surface, and the denylist still wins over it.

  This grant is **necessary but not sufficient**: on a host with a `~/.gitconfig`,
  `git` still fails under `restricted` with
  `fatal: unable to access '<home>/.gitconfig': Operation not permitted`, because
  the global git config sits under `$HOME` and restricted mode grants no home read.
  Reading it is a separate ruling — a global config can carry credential helpers
  and URL rewrites — and has not been made. Until it is, `git` works under
  `restricted` only for a session whose global config is absent or neutralized
  (`GIT_CONFIG_GLOBAL=/dev/null`). Two further residuals: every `git` call under
  `restricted` emits two `xcrun_db` cache-write denials to stderr and costs roughly
  3.5s, because the `xcrun` shim retries a cache write into the per-user temp
  directory that the mode does not grant.

## The fail-closed floor

A mode is either fully enforced or the session refuses to start. There is no
"best effort" and no override flag: serf will not run a session that claims a
sandbox mode it cannot actually enforce.

| Host capability | Modes that run | Otherwise |
|---|---|---|
| Linux with a working bubblewrap (unprivileged user namespaces usable) | all modes, `--sandbox-net` on or off | — |
| macOS with `/usr/bin/sandbox-exec` (present on every stock install) | all modes, `--sandbox-net` on or off | — |
| Linux without a usable bubblewrap, Windows, macOS without `sandbox-exec`, any other OS | `off` only | any non-off mode refuses to start |

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
succeed. On macOS/Seatbelt the packed-refs grant also covers the two fixed sibling
names git rewrites it through (`packed-refs.lock` and `packed-refs.new`, renamed
into place), so `git pack-refs` and the ref packing a commit triggers work too.
But every git **config and hook** surface is read-only:

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
  temp (a cold cache), never to a persistent-writable location. GOMODCACHE is
  redirected alongside GOCACHE: it defaults to `$GOPATH/pkg/mod`, which the
  granted cache root does not track when GOPATH is customized away from its
  default location, so the redirect applies regardless of GOPATH.
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
  strategy, redirects `GOCACHE`/`GOMODCACHE`/`npm_config_cache`/`CARGO_HOME`
  there.

**Known residual: Go telemetry noise is not suppressed.** Go's telemetry
counter/token file lives under the user's Go config directory (outside every
sandbox root by design), so a `go` invocation inside a sandboxed session logs
one denied-write line to stderr (`error acquiring upload token: ... operation
not permitted`) and continues; the command's exit code is unaffected.
`GOTELEMETRY` was considered as an env-floor fix but is a **report-only** `go
env` value — the Go toolchain does not read it from the process environment
(`go env -w GOTELEMETRY=off` itself fails with "GOTELEMETRY cannot be
modified"), so setting it in the floor would have been a no-op. The only ways
to actually silence the line are a per-session `HOME` redirect or granting a
new writable root for Go's config directory; both were rejected as broader
than the problem warrants. Ruled 2026-08-06: accept and document the noise
rather than ship an env var that does nothing.

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

In a non-interactive session a denial is final. In an interactive root session with a
UI client attached (subscribed to the thread), a `read_file`, `write_file`, or
`edit_file` denial for being outside the sandbox's roots can be escalated to a
human-gated approval card — "Allow" / "Deny" in the web UI, `ctrl+y` / `ctrl+g` in the
TUI hub — that blocks the tool-exec goroutine until answered
(`serf/sandbox/escalation/requested` → `serf/sandbox/escalation/resolve`, see
[docs/appwire-protocol.md](appwire-protocol.md)). Shell/kernel denials, `apply_patch`,
the browse tools, and a masked/git-protected/symlinked/escape denial all stay final,
exactly as a non-interactive session. The model can never trigger, approve, or
observe an approval: the escalation and its resolution never enter session history.

## Subagents and worktrees

A sandboxed session's delegates and subagents inherit the policy automatically,
re-rooted to their own worktree with fresh git-metadata resolution — a delegate in
lane A cannot read lane B. Because inheritance is automatic, the flag only goes live
on a build where subagent/worktree scoping already holds, so the first `delegate`
never punches a hole in the boundary you opted into. Resumed delegates re-resolve
their persisted policy the same way the root session does. See
[docs/worktrees.md](worktrees.md) for the worktree model itself.

## Hooks and MCP under a sandbox

Hooks and MCP servers are session **infrastructure**, so they work in every mode
(ruled 2026-08-06). The session's configured hook and MCP-server paths join the
spawned layer's **read/exec** surface in all modes — including `restricted`, whose
spawned processes otherwise read only the worktree. Without this, a hook script
installed in the plugin cache died with exit 126, "Operation not permitted", and
took the session's first steering input with it.

- **Hook commands** (SessionStart, PreToolUse, PostToolUse, …) run under the session
  sandbox, same as any spawned process, with their configured paths granted.
- **stdio MCP servers** are spawned under the session sandbox, with the directories
  holding their programs and script files granted.
- **Remote (HTTP/SSE) MCP servers** require `--sandbox-net on`; under `net=off`
  their egress is disabled.

The grant is tightly bounded:

- **Config-derived, never a glob.** The roots come from the plugin directories this
  session actually loads (`--plugin-dir` plus the enabled plugin registry) and the
  stdio MCP servers this session actually configures — not from a
  `~/.claude/plugins/*`-shaped pattern, which would both grant caches the session
  never loads and miss anything configured elsewhere.
- **Only inputs the model cannot write.** MCP servers are read from the *trusted*
  layers only: the global config, `--mcp-config` files, and `--mcp` inline specs.
  The per-project layer (`<git root>/.serf/mcp.json`) is **excluded**, because it
  sits inside the model's own write surface — a grant derived from it would let a
  session widen its own box (plant the file, spawn a delegate with
  `sandbox=restricted`, read whatever you named), breaking the rule that the policy
  is fixed for the life of the session. A project-declared MCP server still connects
  normally; it just cannot hand itself filesystem roots.
- **Read and exec only.** The write surface is unchanged; a hook's own directory
  stays unwritable in every mode.
- **Spawned layer only.** File tools do not gain a browse grant over the plugin
  cache, so `restricted` still holds the model's own reads to the worktree.
- **The denylist still wins.** A hook or MCP path at or under a masked path is not
  granted, and a denylisted subtree inside a granted path stays masked — the
  pseudo-filesystem floor and the credential denylist are authoritative over this
  grant as over every other.
- **Never a shared, multi-tenant tree.** A candidate root is refused when it is at
  or *above* the user's home directory, the session's worktree, or a temp root, or
  when it is fewer than two path components deep. So `/`, `/Users`, `/home`,
  `/private`, `/var`, `/Volumes` and `/tmp` can never be granted. Paths are
  symlink-resolved *and* stripped of their `/System/Volumes/Data` firmlink alias
  before this check, so neither spelling of a path can slip past it. Note that the
  denylist alone cannot do this job: it drops roots at or *beneath* a masked path,
  and these are *above* them.

An MCP `command` or argument contributes only when it is a **regular file**, and it
contributes the directory holding it (an interpreter resolves a script's
neighbours). A directory named directly as an argument grants nothing.

A hook or server path the sandbox cannot safely serve fails that hook or server, not
the session — the same outcome as before this grant existed. A failed SessionStart
hook surfaces as one summarized warning line naming the hook and its exit code; its
raw stderr never becomes the model's opening context.

## Known residuals

Three boundary edges are deliberately documented as open rather than claimed closed:

- **A pre-existing hardlink** inside the worktree to an out-of-tree secret is
  *readable* through the worktree (path-based masking cannot see that two names share
  an inode). A *write* through such a hardlink does not propagate to the original —
  the file tools write atomically via temp-plus-rename, which replaces the name with
  a fresh inode. This read residual is out of the running-amok threat model.
- **Daemon-socket masking is a targeted list** (docker, podman, containerd, dbus).
  An exotic or custom daemon socket under `/run` that is not on the list could still
  be reached; a broader `/run` mask is deferred because it would also hide
  legitimate runtime state and break DNS/daemons.
- **On Linux/bubblewrap, a linked worktree's common `.git` grants only the
  metadata entries that already exist.** That common dir sits outside the worktree
  and is granted entry by entry, and bubblewrap grants by bind-mounting, which
  requires the target to exist. Any granted entry absent when the sandbox starts —
  `packed-refs` and its `packed-refs.lock` / `packed-refs.new` siblings, `logs`,
  `index` — is therefore skipped, and git cannot create it. (A main checkout is
  unaffected: its whole `.git` sits under the worktree write root, as does a linked
  worktree's own per-worktree git dir.) This is structural, not an oversight:
  permission to *create* a name belongs to the parent directory, so no mount can
  express "may create exactly this filename in a read-only directory". macOS/
  Seatbelt matches path strings instead of mounting and grants those entries
  exactly. Closing the gap on Linux needs a directory-level decision about the
  common dir, which is open.

## macOS notes

The macOS backend is Seatbelt (`/usr/bin/sandbox-exec`), driven from the same policy
model as bubblewrap so all modes are expressible; caches are always served
session-private (there is no overlay on macOS), and the `sandbox-exec` path is
hard-coded as a PATH-injection defense. A host without `/usr/bin/sandbox-exec`
refuses any sandboxed mode — the same fail-closed floor as a Linux host without a
usable bubblewrap.

Seatbelt policies canonicalize every granted and denied root through symlink and
firmlink resolution (`/tmp` → `/private/tmp` is pinned by test), and a live parity
suite runs the generated profiles under the real `sandbox-exec` when
`SERF_SEATBELT_LIVE=1` is set.

## Related

- [docs/worktrees.md](worktrees.md) — the worktree model that backs delegate
  isolation.
- [docs/environment.md](environment.md) — the environment variables serf reads.
- [docs/subagent-runtime-contracts.md](subagent-runtime-contracts.md) — the runtime
  contracts subagents, plugins, and hooks operate under.
