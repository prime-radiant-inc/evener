# codex-sidebar-open: qualified Codex sidebar row opens the source workspace

**What this covers**: docs/superpowers/specs/2026-07-13-codex-sidebar-session-navigation-design.md, row `codex-sidebar-open`; proves the sidebar uses the source-qualified AppWire identity for a Codex thread instead of collapsing to a bare local route.

## Pre-state

- Fresh `serf-hub` build under test, started in an isolated state dir.
- Browser authenticated to the test hub.
- A controlled local Codex-compatible AppWire source is running and exposes one known thread in the sidebar; keep at least one local Serf session visible as a contrast case.

## Steps

1. Open the hub home / sidebar view that lists both local and Codex sessions. Find the Codex row by its visible thread title and source badge/label.
2. Click the row itself, not the row menu. Watch the browser location and workspace load.
3. Record the exact route the app navigates to and the thread identity rendered in the workspace header/body.

## Expected

- Step 2 opens the thread through the source-qualified workspace route, and the workspace displays the same thread the sidebar row identified.
- The browser path is `/s/<source>:<thread-id>` rather than a bare `/s/<session-id>`.
- The workspace shows the selected Codex thread instead of a local-session not-found page or a different source's thread.
- The click requests a bare local session URL, shows not found, or opens a different source's thread.

## Cleanup

- Return to the sidebar home route and close any browser tabs opened for this scenario. No backend state needs teardown beyond the harness-managed fresh hub/source.

## Sharp edges

- The row title alone is not enough if multiple sources share similar thread names; use the visible source badge/label plus the resulting URL.
- If the Codex row is collapsed behind a project header, expand that header before clicking the row itself.
