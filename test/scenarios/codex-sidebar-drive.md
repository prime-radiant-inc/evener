# codex-sidebar-drive: Codex workspace actions stay bound to the exact source thread

**What this covers**: docs/superpowers/specs/2026-07-13-codex-sidebar-session-navigation-design.md, row `codex-sidebar-drive`; proves an opened Codex workspace exposes the source-advertised action and routes it through the controlled Codex AppWire source to the same thread.

## Pre-state

- Fresh `serf-hub` build under test, started in an isolated state dir.
- Browser authenticated to the test hub.
- A controlled local Codex-compatible AppWire source is running, exposes one thread in the sidebar, and advertises at least one enabled action in that thread's workspace.

## Steps

1. Open the Codex sidebar row and confirm the browser route is source-qualified for that thread. Note the exact `source:thread-id` identity shown by the row or URL.
2. In the opened Codex workspace, locate the visible control the UI advertises as available for that thread (the enabled action/button/menu item). Use the label the page shows, not a DOM selector.
3. Activate that control once.
4. Read the authoritative logs from the controlled source process and the serf-hub request/route logs for the resulting call.

## Expected

- The workspace exposes the advertised action and it is enabled for the selected Codex thread.
- The log evidence shows the action was routed to the same exact source-qualified thread ref captured in step 1.
- The log evidence names the action that was clicked and does not show a fallback to a bare local Serf session or a different thread.
- Sending an available action targets a local Serf session, returns not found because the source was lost, or reaches a different thread.

## Cleanup

- Return to the sidebar home route and close any browser tabs opened for this scenario. Leave only harness-managed state behind.

## Sharp edges

- This card depends on the controlled source advertising at least one action that is visible in the workspace; if the control is hidden, the source fixture is wrong.
- The authoritative evidence is the source/hub log pair, not the DOM alone; the UI can look correct while routing the wrong identity.
