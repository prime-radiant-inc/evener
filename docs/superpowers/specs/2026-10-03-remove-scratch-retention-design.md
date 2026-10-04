# Remove scratch retention

Date: 2026-10-03. Approved direction: Jesse, 2026-10-03.

## Problem

A session's scratch directory outlives the session. Since 6190f9d469 (2026-08-02, "retain delegate scratch for handoff") every session end releases the scratch lease and keeps the directory. A later system of pins, manifests and consumer bindings carries it across resume, idle retirement and handoff. The startup sweep may not reclaim a pinned directory, and pins release only on terminal close, which long-lived sessions rarely reach.

On magic-kingdom on 2026-10-03 this left 1,475 scratch directories holding 134 GiB, 1,405 of them pinned. About 102 GiB was per-session Go module and build caches (the sandbox's session-private cache redirect). Another ~10 GiB was `$TMPDIR` litter, and the rest was agent-made clones, copies and logs. Agents treated scratch as storage because the prompt told them to "leave it in place".

The reason for retention is gone. Delegates have a durable artifacts directory, `<stateDir>/sessions/<child>/artifacts` (`agent/delegate_artifacts.go`). A resumed session whose scratch is missing already gets a fresh, empty one; this was checked on 2026-10-03, sandboxed (`workspace-write`) and unsandboxed.

## Goal

Scratch is scratch: no session scratch directory outlives the session that owns it.

## Design

### A session deletes its scratch when it ends

When an owning environment ends, it deletes its scratch: the sandbox scratch, the unsandboxed scratch, and the session `$TMPDIR` container. This uses the existing disposal (`DisposeUnadoptedScratch`, `disposeChildScratch`) instead of `Retain`. The ends are:

- root session close, including `LocalExecutionEnvironment.Cleanup`;
- idle retirement;
- a delegate runtime's teardown (reclaim, GC eviction, lane disposal, parent close);
- a launch or restore that fails;
- a swap aborted by close.

A resumed or re-woken session gets a fresh scratch.

Unchanged:

- **Shared-scratch delegates never delete the parent's scratch.** Ownership already decides this: `ownsFresh` / `parentSharedEnv` in `agent/sandbox_delegate.go` and `agent/subagents.go`.
- **Live ownership moves keep their lease-only handoff.** That means `AdoptSessionScratch` in environment swaps and worktree resume. Nothing has ended there; ownership passes between live environments.

### Detached commands get their own temp directory, held by the command

`DetachCommand` (Linux and macOS only) starts a process meant to outlive the session. It no longer uses session scratch. At spawn:

1. Evener creates a fresh `evener-sandbox-*` directory in the scratch base and points the command's `$TMPDIR` at it. Under the sandbox, the command may write there, and that is its only writable temp location.
2. Evener takes the directory's lease (`flock` on `.evener-session.lock`) on a new open file. It passes that file to the command as its one inherited descriptor, then closes its own copy. This is a deliberate, documented exception to "spawned processes inherit no evener descriptors". The lease is then held for exactly as long as the command or any descendant keeps the descriptor.
3. After the command exits, the lease is free and the sweep reclaims the directory.

Known limit: a program that closes every inherited descriptor releases the lease early. Its directory then becomes reclaimable 24 hours after it was created, even if the program is still running.

### The sweep runs at startup and at every root session end

`SweepCrashedSessionScratch` keeps its rules:

- a prefix and owner check;
- an age of at least 24 hours;
- a lease that can be taken;
- a `SameFile` recheck;
- a tombstone rename, then removal.

It loses the pin, manifest and reference checks, which become dead code. It also runs, off the hot path, whenever a root session ends. A long-running `evener serve` therefore reclaims exited detached commands' directories and crash leftovers without a periodic timer.

Old pinned directories and `<stateDir>/scratch-retention/*.json` become ordinary leftovers. The sweep removes the directories. The manifests, a few KB each, are left in place: once the code is gone, nothing reads them.

### What is removed

The retention system goes, whole:

- `agent/session_scratch_retention.go`, `agent/sandbox/scratch_retention.go` and `agent/execenv/scratch_retention.go`;
- the retention parts of `session_retirement_release.go`, `delegate_runtime.go`, `session_init.go`, `execenv/local.go`, `session_worktree_resume.go`, `session.go`, `session_config.go`, and the hub's `project_delete.go`;
- the tests that exist only to pin retention behaviour.

That is about 5,100 production lines and 13,500 test lines.

### Visible changes

- **The delegate finish packet drops `scratch_path`** and its "retained on disk regardless of outcome" note. So do job stop output and the phone's evidence view. A delegate whose sandbox lets it write only to its scratch returns its report in its result.
- **The sandbox prompt line** (`sandboxPromptLine`) stops asking for the scratch path in the handoff, and stops saying "cleanup is manual".
- **The system prompt's scratch paragraph** (from #3682) changes "except files your report points to" to: put files you are keeping outside scratch, in the workspace or where the task names, because scratch is deleted when the session ends.
- **Docs and skills** stop describing retention:
  - `docs/daemon-idle-retirement.md`, `docs/job-control.md`, `docs/product/subsystems.md`, `docs/product/friction.md`;
  - `docs/subagent-management/11-delegate-resource-model.md`, `docs/developing-evener/testing.md`, `docs/developing-evener/environment.md` (#3667);
  - the doctoring-evener skill.

  `docs/sandboxing.md` gains the detached-command temp directory.

## Testing

- **Scratch removed at each end:** root close, idle retirement, delegate teardown and failed launch each remove the owned scratch. A shared-scratch delegate's end leaves the parent's scratch in place.
- **Resume:** a resumed and a re-woken session run with a fresh scratch. The 2026-10-03 manual check becomes a test.
- **Detached commands:** the directory exists, is the command's `$TMPDIR`, and is writable under the sandbox. Its lease is held while the command runs, and the sweep cannot reclaim it then. Once the command exits, the sweep reclaims it.
- **Sweep timing:** the sweep runs at root session end and reclaims an old, unleased directory, including one that still carries an old retention pin file.

## Delivery

A stack of small PRs, each one mechanism, each based on main:

1. **Detached commands** get their own held temp directory.
2. **Ends delete:** every session end disposes of its owned scratch, and the sweep runs at root session end.
3. **Remove the retention code and its tests.** This is deletion only. It runs past the usual 400-line limit, and Jesse approved large deletion PRs on 2026-10-03.
4. **Simplify the sweep:** drop the pin, manifest and reference checks.
5. **Surfaces:** `scratch_path`, the sandbox prompt line, the system prompt paragraph (measured with the scratch-cleanup eval tasks), and docs and skills. This closes #3667 and #3681.

## Out of scope

- The sandbox's session-private cache redirect stays as it is. Its caches now die with the scratch.
- `/private/tmp/claude-*` belongs to Claude Code, not evener.
