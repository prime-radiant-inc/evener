# Session Scratch and Orchestration Posture Design

Date: 2026-07-15
Status: Approved
Tracker: #20

## Purpose

Agents need a private place for temporary diagnostics and intermediate reports
that does not pollute the product worktree or collide with another delegate.
They also need concise Serf-owned guidance for choosing existing isolation,
verification, and compaction controls well.

Serf already provisions an owned temporary directory for sandboxed delegates.
This design promotes that lifecycle to every agent session and exposes it as an
intentional facility. It also tightens Serf's own orchestration posture without
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

Promote/rename the existing sandbox `SessionTmp` ownership mechanism rather than
building a parallel cleanup system.

### Environment

Serf sets both:

- `TMPDIR=<scratch-path>`;
- `SERF_SCRATCH_DIR=<scratch-path>`.

The exact path appears in the session's dynamic environment/system-prompt
section. `HOME` is not redirected. This feature does not newly redirect
`GOCACHE`, npm, Cargo, or other durable build caches; sandbox cache policy remains
a separate concern and must preserve safe shared caches where supported.

### System-prompt contract

Every agent receives this semantic instruction with its actual path:

> Your session-scoped scratch directory is `<path>`. Use it for temporary files,
> generated diagnostics, intermediate reports, and disposable working data. It
> is private to this session and may be deleted when the session closes or Serf
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
- Cleanup terminates tracked processes before removing scratch.
- Normal close/dispose removes the directory recursively.
- Spawn failure cleans a newly provisioned directory.
- Startup/provisioning sweeps Serf-owned crash leftovers older than 24 hours.
- Cleanup recognizes only Serf's reserved directory prefix and never removes
  unrelated operating-system temporary files.

## Worktree-Isolation Posture

Isolation remains the parent agent's decision. Serf does not automatically put
every writable delegate in a worktree.

Serf's delegation guidance explains:

- worktree isolation is recommended for independent writable tasks, especially
  concurrent subagents;
- shared workspaces are appropriate for deliberate collaboration on the same
  uncommitted state or for read-only work;
- the parent should consider file, report, branch, and Git-state collisions;
- existing sandbox deny paths can protect specific control artifacts when a
  shared delegate must not modify them.

When a second concurrent delegate is launched with shared isolation into the
same working directory, Serf returns one advisory warning identifying the shared
workspace and suggesting worktree isolation. The warning does not block creation
or override the agent's choice.

No new `protected_paths` parameter is added.

## Verification Posture

Serf's parent and delegate instructions state:

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

Serf's orchestration instructions state:

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
- `TMPDIR`, `SERF_SCRATCH_DIR`, environment info, and prompt use the same path;
- sandboxed tools/processes can use only their own scratch;
- worktree re-root keeps the same live-session scratch;
- close, spawn failure, and parent teardown clean scratch after process shutdown;
- restore gets a new empty scratch;
- 24-hour sweep removes only stale Serf-owned directories;
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
