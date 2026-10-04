# Remove scratch retention

Date: 2026-10-03. Approved direction: Jesse, 2026-10-03.

## Problem

A session's scratch directory outlives the session. Since 6190f9d469 (2026-08-02, "retain delegate scratch for handoff") every session end releases the scratch lease and keeps the directory. A later system of pins, manifests and consumer bindings carries it across resume, idle retirement and handoff. The startup sweep may not reclaim a pinned directory, and pins release only on terminal close, which long-lived sessions rarely reach.

On magic-kingdom on 2026-10-03 this left 1,475 scratch directories holding 134 GiB, 1,405 of them pinned. About 102 GiB was per-session Go module and build caches (the sandbox's session-private cache redirect). Another ~10 GiB was `$TMPDIR` litter, and the rest was agent-made clones, copies and logs. Agents treated scratch as storage because the prompt told them to "leave it in place".

Retention was added so a read-only delegate's files survive for its parent. The parent can read those files while the delegate's runtime is alive; retention keeps them after the runtime is gone. A resumed session whose scratch is missing already gets a fresh, empty one; this was checked on 2026-10-03, sandboxed (`workspace-write`) and unsandboxed.

## Goal

Scratch is scratch: a session's scratch lives as long as the environment that owns it, and no longer.

## Design

### An environment deletes its scratch when it ends

When an owning environment ends, it deletes what it owns: the sandbox scratch, the unsandboxed scratch, and the session `$TMPDIR` container. This uses the existing disposal (`DisposeUnadoptedScratch`, `disposeChildScratch`) where the code calls `Retain` today. The ends are:

- **root session close.** Deletion is the last step of close, after SessionEnd hooks run and MCP servers shut down. Both still use the scratch: hooks get `$TMPDIR` from the session tmp, and bubblewrap refuses a missing bind source. `LocalExecutionEnvironment.Cleanup` keeps killing processes; it stops being the place scratch is released.
- **idle retirement**, and capacity release of an idle resumable delegate (`delegate_tree_reclaim.go`).
- **a delegate runtime's teardown:** reclaim, GC eviction, lane disposal, parent close.
- **parked and abandoned worktree environments**, at close and at retirement (`session_lifecycle.go` `retainParkedWorktreeEnvironmentScratch`, `settleAbandonedEnvironmentScratch`).
- **a launch or restore that fails**, and a swap aborted by close.

A resumed or re-woken session gets a fresh scratch. Its environment block already names the new path.

Removal must handle read-only trees. Go writes its module cache with read-only directories, so a plain `os.RemoveAll` fails on `gomodcache/`. The disposal and the sweep share one removal helper that makes directories writable and retries, as `go clean -modcache` does. Disposal reports a removal failure instead of dropping it.

Unchanged:

- **Shared-scratch delegates never delete the parent's scratch.** Ownership already decides this: `ownsFresh` / `parentSharedEnv` in `agent/sandbox_delegate.go` and `agent/subagents.go`.
- **Live ownership moves keep their lease-only release.** These are `AdoptSessionScratch` in environment swaps and worktree resume, and `EnableSandbox`'s re-provision. A directory released there may still be used by a live clone, so the sweep's age rule reclaims it. That is today's behaviour, and these are not retention.

### Detached commands keep today's behaviour

`DetachCommand` runs only in an unsandboxed environment (`DetachSupported` requires `Wrapper == nil`, `agent/execenv/local.go:2671`). Its `$TMPDIR` is the session's world-usable temp container in `/tmp` (#495: a command that drops privileges must still be able to write it).

That stays. An environment that started a detached command releases the lease on its `$TMPDIR` container instead of deleting it, exactly as every close does today. The sweep reclaims the container once its top-level modification time is 24 hours old. Everything else the environment owns is deleted as above.

A detached command's environment drops `EVENER_SCRATCH_DIR`, because the scratch it names is deleted when the session ends.

Known limit, unchanged from today: a detached command running longer than a day after its last top-level temp write can lose its `$TMPDIR` to the sweep.

### The sweep runs at startup and at every root session end

`SweepCrashedSessionScratch` keeps its rules:

- a prefix and owner check;
- a top-level modification time at least 24 hours old;
- a lease that can be taken;
- a `SameFile` recheck;
- a tombstone rename, then removal.

It drops the pin, manifest and reference checks. It also runs, off the hot path, whenever a root session ends, so a long-running `evener serve` reclaims old directories without a periodic timer.

Old pinned directories become ordinary sweep candidates. The manifests in `<stateDir>/scratch-retention/`, a few KB each, are left in place, since nothing reads them once the code is gone.

### What is removed

The retention system goes:

- `agent/session_scratch_retention.go`, `agent/sandbox/scratch_retention.go` and `agent/execenv/scratch_retention.go`;
- the retention parts of `session_retirement_release.go`, `delegate_runtime.go`, `session_init.go`, `execenv/local.go`, `session_worktree_resume.go`, `session.go`, `session_config.go`, the hub's `project_delete.go`, and the sweep's pin and manifest checks in `sandbox/session_scratch.go`;
- the tests that exist only to pin retention behaviour.

That is about 5,100 production lines and 13,500 test lines.

### Visible changes

- **The delegate finish packet keeps `scratch_path`.** Its documented job, partial evidence after an externally cancelled run, still holds while the delegate's runtime is alive. Its note changes from "retained on disk regardless of outcome" to "exists until the delegate's runtime is torn down". `docs/job-control.md` says the same.
- **The sandbox prompt line** (`sandboxPromptLine`) drops "cleanup is manual" and says the scratch is deleted when the session's environment ends.
- **The system prompt's scratch paragraph** (from #3682) changes "except files your report points to" to say: put files you are keeping outside scratch, in the workspace or where the task names; scratch can be cleared when the session goes idle or ends. The new wording is measured with the scratch-cleanup eval tasks.
- **Docs and skills** stop describing retention:
  - `docs/daemon-idle-retirement.md`, `docs/job-control.md`, `docs/product/subsystems.md`, `docs/product/friction.md`;
  - `docs/subagent-management/11-delegate-resource-model.md`, `docs/developing-evener/testing.md`, `docs/developing-evener/environment.md` (#3667);
  - the doctoring-evener skill.

## Testing

- **Each end deletes:** root close, idle retirement, delegate teardown, the parked and abandoned worktree environments, and a failed launch each remove the owned scratch. A shared-scratch delegate's end leaves the parent's scratch in place.
- **Close ordering:** a sandboxed SessionEnd hook runs, and MCP servers shut down, before the scratch is removed.
- **Read-only trees:** scratch holding a read-only Go module cache is removed completely, both by disposal and by the sweep.
- **Resume:** a resumed and a re-woken session run with a fresh scratch. The 2026-10-03 manual check becomes a test.
- **Detached commands:**
  - after the starting session closes, the command's `$TMPDIR` container still exists;
  - the command's environment has no `EVENER_SCRATCH_DIR`;
  - the rest of the session's scratch is gone.
- **Sweep:** it runs at root session end and reclaims an old, unleased directory, including one that still carries a retention pin file.

## Delivery

A stack of small PRs, each one mechanism, each based on main:

1. **Removal helper:** one removal that handles read-only trees, used by disposal and the sweep.
2. **Ends delete:** every end disposes of its owned scratch, at the end of close. Detached-command environments keep their `$TMPDIR` container and drop `EVENER_SCRATCH_DIR`. The sweep runs at root session end.
3. **Remove retention:** the retention code, its tests, and the sweep's pin and manifest checks, in one PR, since the sweep calls into the retention files. This is deletion only. It runs past the usual 400-line limit, and Jesse approved large deletion PRs on 2026-10-03.
4. **Surfaces:** the `scratch_path` note, the sandbox prompt line, the system prompt paragraph, and docs and skills. This closes #3667 and #3681.

## Out of scope

- The sandbox's session-private cache redirect stays as it is. Its caches now go with the scratch.
- `/private/tmp/claude-*` belongs to Claude Code, not evener.
