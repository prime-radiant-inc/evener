# Approved Serf Decisions Design

Date: 2026-08-02
Status: Approved implementation record

This document consolidates Jesse's approved decisions for the Serf-owned
portions of the open Kata triage. It supersedes conflicting behavior in older
design notes where the decisions below are more recent. SDD, Kata, and local
launcher behavior remain outside Serf proper.

## Delegate scratch and handoff

The existing sandbox session scratch facility remains the capability used by
read-only delegates. The environment prompt names the delegate's absolute
scratch path and says that read-only delegates may write only there. The prompt
also tells a delegate to report that absolute scratch path, plus absolute paths
for any artifacts it wants its parent to retain, in its final human-readable
handoff.

The handoff is prose. Serf does not add a structured scratch/artifact field,
parse report syntax, copy artifacts automatically, retry or resume
automatically, or know about SDD report locations. The parent decides whether
to copy, retry, resume, or recover an artifact. Scratch cleanup is manual for
now; an explicitly unadopted allocation may still be rolled back when setup
fails before a delegate can receive it.

## Job outcomes and worktree provenance

The durable job outcomes remain distinct: `completed`, `failed`, `cancelled`,
`stopped`, and `exhausted`. Teardown and worktree disposal are separate
operational facts, not replacement job outcomes. Existing cancellation reason,
phase, output, and delegate handoff prose remain the evidence for partial work;
Serf does not invent an automatic retry/resume decision.

An isolated delegate's worktree report includes its path, branch, ahead/dirty
state, and the exact observed `HEAD` revision used for that report. Shared
branches remain supported without a registry, lock protocol, or task-diff
ledger; callers own the session/job, branch, and revision provenance.

## Transcript surface

`read_transcript` is the sole model-facing transcript reader. It accepts both
session references and `job:<job_id>` references, including shell and delegate
jobs. `read_session_transcript` is removed from the callable tool surface.
Public transcript reads do not expose API-log selectors or body/attempt options;
API-log inspection remains an internal/doctor concern, and historical
transcript projection still redacts private API-log evidence.

The public reader has no `max_bytes` argument. Session turn expansions use the
existing fixed internal 16 KiB page size and returned `offset_bytes`
continuations. Job refs continue to reject session-only range and expansion
arguments. `find_session_transcripts` keeps its name because it discovers
sessions, not jobs.

## File-search patterns

`glob` and `grep_files` accept the existing `*`, `?`, `[]`, and `**` patterns,
plus bounded nested brace alternatives such as `*.{ts,tsx,css}`. A small local
helper expands only brace alternatives; it does not implement tilde,
parameter, command, arithmetic, or numeric-range shell expansion. Malformed
braces fail clearly, expansion count is capped, and a valid pattern with no
matches still returns an empty result.

## Environment boundaries

Serf inherits the caller's effective `PATH` and active working directory. The
local launcher owns PATH setup and tool installation; Serf does not bootstrap
dependencies or embed Kata behavior. Prompt guidance may note that a newly
created worktree may require copying or installing dependencies.

## Explicitly external

The following remain outside this implementation and must not enter Serf:

- SDD report persistence, validation, gate derivation, review-package commit
  selection, and report/control-byte policy;
- Kata installation, context/quickstart behavior, and the issue ledger;
- local launch-agent/launch-daemon configuration and tool installation;
- automatic ownership enforcement, retry/resume policy, or artifact materialization.

