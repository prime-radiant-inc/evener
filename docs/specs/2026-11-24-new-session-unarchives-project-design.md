# New session unarchives its project

Date: 2026-11-24
Status: accepted

## Problem

`hubcore.SessionArchived` files a session as archived when its owning project
has an explicit archive decision, whatever the session's own decision says
([`tree.go`](../../cmd/evener-hub/internal/hubcore/tree.go)). That is the rule
that keeps an archived project's population cleared. It also means a session
launched in the project after the archive inherits the decision at birth: with
the project's decision at T, every later top-level session appears only in the
rail's Archived section, even though that session has never been archived.

The reported case was session `0352vT2QWCgJpFJ199Sa7y` in `/Users/jesse`
(project `Users-jesse-SOa9pWpsc7`). The project was archived 2026-08-19, the
session was launched 2026-11-24, and the session's own unarchive decision was
ignored.

## Rule

A top-level session created after a project's manual archive decision clears
that decision for the session's owning source. Starting a session in the
project is the user using it again, so the project returns to the rail's active
catalog and its sessions return to their activity tiers. An individually
archived session stays archived.

The clearing is narrow:

- Only sessions whose `created_at` is strictly after the decision's
  `decided_at` count. An existing, resumed, or re-indexed session does not
  unarchive a project the user archived while it existed.
- Only top-level sessions count; a subagent launched into the project, or a
  fork original superseded by its continuation, does not. A fork does count:
  it is a new top-level session.
- The decision is per owning source. A new remote session clears that host's
  project decision only. The project ID is the one the owning host reported
  for that session: two hosts can name the same working directory, so the
  hub's path-keyed project map cannot stand in for the host's own identity.
- The session's own archive decision is untouched. An explicit session
  unarchive still does not override a project archive; the project decision is
  the one cleared.

## Where it runs

The hub owns archive decisions, so the hub observes the session rather than
requiring each launcher (CLI, hub spawn, remote host) to call it.

The reconciliation runs on the hub's existing snapshot wake
(`watchHubSnapshotWake`), which already reads the same local and remote metas
and live entries navigation uses. It runs once at startup, then on the 5 s tick
and on every poke (a roster change, a remote-cache change, an
archive/favorite/pin decision). A live local session the past index has not yet
folded is read directly from `<state_dir>/sessions/<id>.meta.json`, so a launch
is caught as soon as its daemon registers rather than waiting for the past
index rebuild.

`ArchiveStore.DecisionStates` returns each decision with its decision time;
`hubcore.ProjectsToUnarchive` is the pure rule. Clearing is
`ArchiveStore.Set(source, "project", id, false, now)`, the same write the
unarchive action makes, so it invalidates navigation through the existing
change hook. The rule compares at the decision row's one-second precision: a
session created in the decision's own second counts as covered.

## Acceptance

- Project archived at T; top-level session in it created after T: project
  unarchived, project in the active catalog, session in its activity tier.
- Session created at or before T, a subagent, a superseded fork original, a
  different project, or a different source: project stays archived.
- A live local session missing from the past index is read from its state dir
  and counts.
- Remote session after T in a source-archived project: only that source's
  project is unarchived.
- No behavior change to session-level archive/unarchive or age auto-archive.
