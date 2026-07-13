# local-sidebar-url-stability: local sidebar rows keep the canonical short URL

**What this covers**: docs/superpowers/specs/2026-07-13-codex-sidebar-session-navigation-design.md, row `local-sidebar-url-stability`; proves local Serf sessions keep their existing `/s/<session-id>` route and still open the local workspace.

## Pre-state

- Fresh `serf-hub` build under test, started in an isolated state dir.
- Browser authenticated to the test hub.
- At least one local Serf session row is visible in the sidebar.

## Steps

1. Find a local Serf session row whose visible identity is a bare session id, not a source-qualified Codex ref.
2. Click the row itself and watch the browser URL and workspace content.
3. Verify the opened workspace is the same local session that appeared in the row.

## Expected

- The browser path remains `/s/<session-id>` and not a qualified source route.
- The local workspace loads for the same session id that appeared in the row.
- A local sidebar row changes to a qualified URL or no longer opens its existing workspace.

## Cleanup

- None; this scenario is read-only apart from browser navigation.

## Sharp edges

- Use the row's visible local badge/label, not an inferred project name.
- Local rows may share titles with Codex rows; only the route identity distinguishes them.
