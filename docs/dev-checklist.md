# Manual dev-box checklist

Checks in this file run against **your own real dev hub and real
`~/.serf` / `~/.local/state/serf` session history** — not an isolated,
agent-runnable checkout the way every card under `test/scenarios/` is.
An agent asked to "run the scenario sweep" should skip this file
entirely: its Pre-state cannot be satisfied in a fresh, isolated `$HOME`
by design, because the whole point is proving the UI against a real,
long-lived project history that only exists on a dev box that's been
used for a while. See `test/scenarios/README.md` for the isolated-card
convention this file deliberately does not follow.

Moved here from `test/scenarios/index-sidebar-lists-projects.md` (kata
`e0ee`): it read like an automated sweep card living in the wrong
directory, hardcoded to Jesse's real `/home/jesse` path with no
isolation marker, which is exactly the hazard
`scenariohome_audit_test.go` (kata `93f5`) flags for cards actually
under `test/scenarios/`. It never spawns a session or writes anything,
so it was never the OAuth-footgun class of hazard (kata `keyb`) — just
filed in the wrong place.

## index-sidebar-lists-projects: hub home page sidebar enumerates projects + sessions

**What this covers**: regression baseline for the hub index view.
Validates that:
- The sidebar renders a `Live` group (current open daemons) +
  per-project folders.
- Each project shows its session count and a list of recent
  sessions with compact session-ID short codes + age (e.g. `2m`, `17h`).
- Past sessions surface even when their daemon is no longer
  running (the past-index projection works).
- Live count matches the actual running daemon count.

## Pre-state

- Hub running. At least one project with at least one past
  session under `~/.local/state/serf/projects/`. On this dev box
  there's plenty.
- Browser authed.

## Steps

1. Open `/` (root) in a browser. Confirm main shows `No session
   selected. ＋ new session` (no auto-redirect).
2. Read the sidebar. Confirm `nav` contains:
   - A top action row: `＋ new`, `search ⌘K`, `settings`.
   - A `Live` group with a count badge.
   - Project folders, each with a count badge.
3. For each project, count the listed sessions and confirm it
   matches the count badge.
4. Cross-check live count:
   `ls /home/jesse/.serf/run/*.json | wc -l` — minus orphans
   (rendezvous files whose PID isn't actually running). The
   sidebar's Live count should match the actual count of running
   serf-daemon processes.
5. Click into a session in the sidebar. Confirm the URL becomes
   `/s/<id>` and the workspace loads.
6. Click `＋ new` in the nav. Confirm URL is `/new` and the spawn
   form renders.

## Expected

- Sidebar renders without errors.
- Counts are internally consistent.
- Clicking a session navigates correctly.
- Falsification: a session disappears from the sidebar after a
  hub restart (past-index projection broken), or click navigates
  to a 404.

## Cleanup

- None.

## Sharp edges

- The Live count is computed from the in-memory roster of daemon
  sources. Rendezvous files in `~/.serf/run/` are the source of
  truth; orphan files (daemon dead, file lingers) inflate the
  count until the hub reaps. Worth filing a kata if you see a
  persistent stale count after a clean kill.
- Project IDs are readable canonical identifiers with a 10-character base62
  suffix. Renaming a working dir on disk creates a new project ID; old
  sessions stay under the old ID. Not a bug, but surprising.
- The sidebar pulls session titles from the first user input —
  long prompts get truncated in the UI. Some session entries
  show just `session <SHORT_ID>` because the first turn was
  empty or the session predates the title-from-prompt feature.
- 001 is a meta project — used by the kata daemon for storing
  test-mode state. If you see it without having created it,
  that's fine.
