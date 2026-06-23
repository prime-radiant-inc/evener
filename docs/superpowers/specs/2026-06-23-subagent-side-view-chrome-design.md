# Subagent Side-View Chrome — Shared Thread Surface Design

Date: 2026-06-23
Status: Approved for implementation planning
Scope: Serf web hub (`cmd/serf-hub`)

## Problem

Subagents can be opened beside a parent session in the web UI. Today, the side view can render the full session app shell, which brings along top-level workspace chrome such as the global sidebar, app-level navigation, search/settings controls, and duplicate sidebar affordances. That is wrong inside a side pane: a side pane should show the selected subagent/thread, not a second copy of the whole application.

At the same time, a side-view subagent should not be reduced to a read-only transcript. It should remain an interactive thread view: the input composer should stay available, and the user should be able to open additional subagents/documents further to the right.

## Design Goal

Separate the **global app shell** from a reusable **individual thread surface**.

The app shell owns global UI:

- primary sidebar;
- global search/settings/new-session actions;
- mobile sidebar drawer/hamburger behavior;
- the top-level workspace container.

The thread surface owns one session/thread:

- thread header/title/status;
- parent breadcrumb for subagents;
- conversation transcript and live renderer;
- input composer;
- per-thread controls such as details/status/actions;
- open-beside affordances that can ask the host to open more panes to the right.

Main workspace and side panes should both reuse the same thread surface. The side pane should not load a full `/s/<id>` app-shell page with hidden chrome.

## Recommended Architecture

Introduce or formalize an individual thread endpoint, conceptually:

- `/s/<id>`: public full-page session route. It renders `app.html` and points its workspace region at the individual thread endpoint.
- `/_partials/s/<id>/workspace` or a clearer alias such as `/_partials/thread/<id>`: the reusable individual thread surface.
- Side panes open the individual thread endpoint directly, not `/s/<id>`.

The existing `templates/partials/workspace.html` is already close to the desired reusable thread surface. It contains the workspace header, parent breadcrumb, conversation, composer, and input status. The design is therefore an extraction/renaming/route-boundary cleanup rather than a new UI.

For the side-pane implementation, continue using iframe-per-pane for now. The renderer is currently a document-global singleton; iframe isolation keeps each thread's renderer, composer wiring, scrolling, and websocket lifecycle independent. The important correction is that the iframe should frame the individual thread surface, not the full app shell.

## Data Flow

### Main session navigation

1. User navigates to `/s/<id>` or clicks a sidebar session row.
2. The full app shell remains responsible for the global sidebar and top-level layout.
3. The shell loads/swaps the individual thread surface into `#workspace`.
4. Browser URL remains `/s/<id>` for normal top-level navigation.

### Side-pane subagent navigation

1. User clicks an open-beside affordance on a subagent row.
2. Renderer/sidebar code calls the pane host with the thread-surface URL, for example `/_partials/thread/<encoded-ref>` or the chosen canonical equivalent.
3. The side pane iframe loads only the individual thread surface.
4. The pane shows thread header, parent breadcrumb, transcript, composer, and per-thread actions.
5. The pane does not show global sidebar/search/settings/new-session chrome.
6. If the pane's thread opens another subagent/document beside it, the request is routed to the host pane manager, which opens another pane to the right.

## UI Contract

### The individual thread surface must include

- Session/subagent title and status.
- Parent breadcrumb/banner when the thread is a subagent.
- Conversation transcript.
- Live update behavior.
- Composer/input field.
- Input status/queue controls already owned by the thread.
- Per-thread details controls.
- Open-beside controls for nested panes.

### The individual thread surface must exclude

- `#sidebar` and all primary-sidebar markup.
- Global search trigger/dialog chrome.
- Global settings links.
- Global new-session controls.
- Mobile sidebar hamburger/drawer controls.
- Any duplicated top-level app navigation.

## Error Handling and Fallbacks

- If `/s/<id>` is loaded directly, it should continue to render the full app shell.
- If the individual thread surface cannot load a thread, it should render the existing workspace/session error state inside the thread region or pane, not fall back to full app chrome.
- If a pane fails to load, the pane host should show an inline pane error instead of navigating the parent workspace away.
- If nested open-beside is unavailable, normal navigation can remain a fallback, but the preferred behavior is opening another pane to the right.

## Security and Isolation

The side-pane iframe path must remain same-origin and authenticated through the existing hub auth path. Because previous multi-pane design notes identified CSP framing as a possible blocker, implementation must verify that same-origin pane framing is permitted while preserving protection against third-party framing.

The thread endpoint should not expose private partials to unauthenticated callers. It should follow the existing partial-route auth and HX-gating policy unless implementation intentionally introduces a standalone minimal thread document for panes. If a standalone thread document is needed for iframe loading, it must still omit app chrome and pass through the same auth guard.

## Testing Plan

Add regression tests at both server/template and JS levels.

Server/template tests:

- `/s/<id>` renders the full app shell with `#sidebar` and a workspace loader targeting the individual thread endpoint.
- The individual thread endpoint renders conversation/composer/header content.
- The individual thread endpoint does not render `#sidebar`, global search/settings/new-session controls, or sidebar-toggle/hamburger controls.
- A subagent thread endpoint includes the parent breadcrumb/banner.

JS tests:

- Subagent open-beside uses the individual thread endpoint rather than `/s/<id>`.
- Sidebar subagent open-beside, if present, uses the same endpoint.
- Open-beside remains available from inside a thread surface so nested panes can open to the right.
- A pane-loaded thread surface does not expose a duplicate sidebar toggle.

Regression scenario:

- Open a real subagent beside the parent.
- Verify the side pane has transcript + composer + parent breadcrumb.
- Verify the side pane has no global sidebar/top-level nav.
- Verify opening another subagent/document from that pane targets a new pane to the right.

## Non-Goals

- Refactoring `SerfRenderer` into a multi-instance in-page renderer.
- Building a new global navigation model.
- Making pane layouts shareable/restorable in the URL.
- Redesigning the transcript or composer UI.
- Removing normal full-page `/s/<id>` navigation.

## Implementation Notes

- Prefer keeping `workspace.html` as the shared thread-surface template and improving route names/aliases around it.
- Avoid duplicating thread markup between full workspace and pane view.
- Avoid client-only hiding of full app chrome as the primary solution. The server should render the correct surface for the context.
- Preserve existing top-level behavior for sidebar clicks and direct `/s/<id>` visits.
- Keep changes narrow: route/template boundary, pane URL construction, and tests.

## Self-Review

- No placeholders or TBDs remain.
- The design is internally consistent: app shell and thread surface are separate, and both main workspace and side panes reuse the thread surface.
- Scope is focused on side-view subagent chrome and does not require a renderer singleton refactor.
- Ambiguous `?pane=1` behavior is explicitly rejected in favor of a reusable thread endpoint.
