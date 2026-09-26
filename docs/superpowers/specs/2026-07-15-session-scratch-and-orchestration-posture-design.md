# Session Scratch and Orchestration Posture Design

Date: 2026-07-15
Status: Approved
Tracker: #20
Program order: Project 5 of 6; implement after the budget, transcript/API-log,
and job-supervision projects. Project 4 model-selection correctness was canceled
by Jesse on 2026-07-17 and is not a prerequisite.

## Purpose

Agents need a private place for temporary diagnostics and intermediate reports
that does not pollute the product worktree or collide with another delegate.
They also need concise Evener-owned guidance for choosing existing isolation,
verification, and compaction controls well.

Evener already provisions an owned temporary directory for sandboxed delegates.
This design promotes that lifecycle to every agent session and exposes it as an
intentional facility. It also tightens Evener's own orchestration posture without
changing Superpowers.

## Universal Session Scratch

Every root and child session gets one unique scratch directory.

Properties:

- mode `0700`;
- owned by exactly one live session execution environment;
- distinct for parent, child, sibling, and forked sessions;
- available whether sandbox mode is on or off;
- writable inside the session's sandbox when confinement is enabled;
- not placed inside the Git worktree;
- not a durable artifact or cross-session communication channel.

Allocation enforces the worktree exclusion even when ambient `TMPDIR` points
inside the checkout. Evener selects a safe operating-system temporary/cache base
instead of accepting an in-worktree scratch path.

Promote/rename the existing sandbox `SessionTmp` ownership mechanism rather than
building a parallel cleanup system.

### Environment

Amended 2026-09-20 (Jesse; #495): the `TMPDIR` half of the original "Evener sets
both" rule is replaced by the shape-dependent rule below. `EVENER_SCRATCH_DIR`
and the file-tool root are unchanged.

`EVENER_SCRATCH_DIR=<scratch-path>` is set for every spawned process, and the
exact path appears in the session's dynamic environment/system-prompt section.

`TMPDIR` is set per spawn shape, because a temp directory has to be usable by the
process that inherits it and a private scratch is not:

- `TMPDIR=<scratch-path>` for every **sandboxed** spawn and for every unsandboxed
  spawn whose **file tools are confined** (`FileToolConfined()` — the
  write-blocked off policy). In those shapes the scratch is the only writable
  temp the process has, so `TMPDIR` and `EVENER_SCRATCH_DIR` name one directory
  and a `mktemp`-then-`write_file` workflow stays inside the file tools'
  writable root.
- `TMPDIR=<temp-container>/tmp` for an unsandboxed spawn whose file tools are
  **unconfined**. The container sits in a world-usable host temp (`/tmp` or
  `/var/tmp`, chosen OS-aware and deliberately *not* `os.TempDir()`, which on
  macOS is the per-user `0700` directory under `/var/folders/...`), is named
  under Evener's session-scratch prefix so the existing 24-hour reclaim covers
  it, and holds a `1777` sticky leaf any uid may create temp files in. An
  unsandboxed command may spawn a descendant that deliberately becomes another
  user (`su`, an `env_keep += "TMPDIR"`, a setuid binary); such a descendant
  cannot write a directory owned by the session user at `0700`, which is exactly
  why it no longer receives one as `TMPDIR`.
  This rule applies where such a container can exist. The container is defined by
  POSIX mode bits and by a descendant being able to become another user; on
  Windows it cannot, so `TMPDIR` keeps the session scratch there for every shape
  rather than being left inherited.

  The container's leaf is world-writable and world-readable, exactly like `/tmp`
  itself, because a directory an arbitrary uid can use cannot also be private.
  **That is a disclosed cost, not an oversight:** a session's temp files are
  readable by other local users of the host, as they would be in `/tmp`. Secrets
  and other session-private intermediates therefore belong in
  `EVENER_SCRATCH_DIR`, which stays `0700` and is never world-readable. The cost
  is inherent to the requirement — a directory both private to the session owner
  and writable by an arbitrary other uid does not exist — and this design chooses
  usability by the child over the privacy of throwaway temp.

`EVENER_SCRATCH_DIR` and the file-tool root do not vary with the shape: the
model's file tools and its shell still name the same writable scratch, and that
scratch stays private to the session. `HOME` is not redirected. This feature does
not newly redirect `GOCACHE`, npm, Cargo, or other durable build caches; sandbox
cache policy remains a separate concern and must preserve safe shared caches where
supported.
Existing sandbox environment filters for credentials, agents, and external
configuration remain unchanged. Short-lived execution-environment clones and
invocation grants preserve the same scratch path as their owning session.

### System-prompt contract

Every agent receives this semantic instruction with its actual path:

> Your session-scoped scratch directory is `<path>`. Use it for temporary files,
> generated diagnostics, intermediate reports, and disposable working data. It
> is private to this session and may be deleted when the session closes or Evener
> restarts. Move anything needed after handoff into the workspace or another
> durable location.

The posture also tells agents not to force-add ignored temporary reports. Such
reports belong in scratch unless they are an explicitly requested durable product
artifact.

The wording may follow surrounding prompt style, but every stated lifecycle and
durability fact is normative.

### Lifecycle

- Scratch survives turns and worktree re-rooting within one live session.
- It does not survive daemon/session restoration; a restored session receives a
  new empty scratch directory.
- Cleanup terminates tracked processes, finishes SessionEnd hooks, and closes
  stdio MCP processes before removing scratch.
- Normal close/dispose removes the directory recursively.
- Spawn, restore, and abandoned-candidate failure paths clean a newly
  provisioned directory.
- Startup/provisioning sweeps Evener-owned crash leftovers older than 24 hours,
  but an operating-system-released liveness lease prevents an old yet active
  session directory from being swept.
- Cleanup recognizes only Evener's reserved directory prefix and never removes
  unrelated operating-system temporary files.

The session temp container a `TMPDIR` export uses (see "Environment") is not the
scratch and does not follow these bullets. It is retained when the session closes
— its lease released, its directory kept — and reclaimed by the same 24-hour
sweep, because a detached command deliberately outlives the session and keeps the
`TMPDIR` it was spawned with; removing the directory at close would strand it.
Only a mint being discarded (a launch that failed before a session adopted it) is
removed outright.

## Worktree-Isolation Posture

Isolation remains the parent agent's decision. Evener does not automatically put
every writable delegate in a worktree.

Evener's delegation guidance explains:

- worktree isolation is recommended for independent writable tasks, especially
  concurrent subagents;
- shared workspaces are appropriate for deliberate collaboration on the same
  uncommitted state or for read-only work;
- the parent should consider file, report, branch, and Git-state collisions;
- existing sandbox deny paths can protect specific control artifacts when a
  shared delegate must not modify them.

When a second concurrent delegate is launched with shared isolation into the
same working directory, Evener returns one advisory warning identifying the shared
workspace and suggesting worktree isolation. The warning does not block creation
or override the agent's choice.

No new `protected_paths` parameter is added.

## Verification Posture

Evener's parent and delegate instructions state:

- a required gate counts as passed only when it actually ran and exited zero;
- timeout, launch failure, sandbox denial, or environmental blockage leaves
  verification incomplete;
- agents report the exact condition rather than broad green status;
- agents prove fixture/environment failure versus product failure before changing
  production behavior;
- a parent with the needed environment reruns decisive incomplete gates.

Existing shell/job exit-code and timeout metadata remains the evidence source.
This spec adds no gate service or result schema.

## Compaction and Review-Loop Posture

Evener's orchestration instructions state:

- after completing and reporting a task, consider the existing
  `compact_context` tool before starting unrelated work, especially after a
  large implementation or review;
- after two incomplete implement/review/fix cycles on the same task, stop
  repeating the loop and report evidence, reslice, or ask for direction.

These are agent judgments. This spec adds no automatic task-boundary compaction,
semantic cycle detector, or forced stop.

## Prompt Composition

The new guidance belongs in focused prompt components with clear ownership, not
one duplicated prose block across personas. Root and child prompts receive the
parts relevant to their capabilities. Prompt caching must remain stable within a
session; the scratch path is part of the environment-dependent prompt content.

Tests exercise prompt-component behavior and runtime outcomes. They must not
snapshot or regex-match a large rendered system prompt.

## Testing

Cover:

- unique scratch for root, child, sibling, and fork;
- `EVENER_SCRATCH_DIR`, environment info, and prompt use the same path, and
  `TMPDIR` names that path exactly when the spawn is sandboxed or its file tools
  are confined (see "Environment");
- sandboxed tools/processes can use only their own scratch;
- worktree re-root keeps the same live-session scratch;
- close, spawn failure, and parent teardown clean scratch after process shutdown;
- restore gets a new empty scratch;
- 24-hour sweep removes only stale Evener-owned directories;
- build-cache variables and `HOME` are not newly redirected;
- second shared concurrent delegate returns an advisory but still launches;
- isolated or non-overlapping delegates do not warn;
- focused prompt components carry the approved worktree, verification, and
  compaction/review-loop posture.

## Scope Lock

This spec does not:

- modify Superpowers code, skills, plans, or prompts;
- automatically select worktree isolation;
- block shared delegates;
- add a protected-path mechanism or gate service;
- persist scratch across restart;
- make scratch a handoff/artifact store;
- automatically compact context or terminate semantic review loops;
- redirect `HOME` or durable build caches as part of this feature.
