# Content Pane Max Width Design

Date: 2026-06-25

## Goal

The Serf web UI workspace content should not stretch indefinitely on wide screens. When the available workspace area is wider than a sensible reading/composition width, the content column should cap its width and center itself. On narrower screens it should continue to use the available width.

## Context

The hub app shell lives in `cmd/serf-hub/templates/app.html`. Its `#workspace` element is the flex item between the sidebar and optional side panes. The workspace partial in `cmd/serf-hub/templates/partials/workspace.html` renders three top-level content sections:

- `.workspace-header`
- `.conversation`
- `.workspace-input`

Styles live in `cmd/serf-hub/assets/style.css`. Today `#workspace` fills all remaining horizontal space and each child section owns its own horizontal padding, so message content and the composer can become too wide on large displays.

## Design

Keep `#workspace` full-width so the app shell, sidebar resizing, side-pane splitter, and side panes continue to work normally. Add a shared content-column cap to the existing workspace child sections instead of changing the app shell flex item.

Introduce a named CSS custom property for the cap, for example:

```css
#workspace {
  --workspace-content-max-w: 1040px;
}
```

Apply a common sizing rule to the content sections:

```css
.workspace-header,
.conversation,
.workspace-input {
  width: min(100%, var(--workspace-content-max-w));
  margin-inline: auto;
}
```

This keeps the layout fluid below the cap and centers the workspace content once there is extra room.

## Behavior

- Narrow and medium workspaces: header, transcript, and composer continue to fill the available workspace width.
- Wide workspaces: those three sections stop growing past the configured max width and are horizontally centered.
- The sidebar and side panes remain anchored to the app shell edges because `#workspace` itself is not capped.
- Standalone thread documents inherit the same behavior because they reuse the workspace partial and stylesheet.
- Existing phone-specific rules continue to adjust padding and controls; the width rule remains `min(100%, cap)`, so it should not create horizontal overflow.

## Alternatives Considered

1. Cap `#workspace` itself.
   - Simpler CSS, but wrong layout boundary: it would interfere with the flex shell and leave awkward space between the sidebar/content/side panes.

2. Add a new wrapper around the workspace partial contents.
   - Clean conceptual model for future layout variants, but requires template changes for a narrow styling request.

3. Cap the existing child sections.
   - Minimal, preserves the current app-shell structure, and matches the actual content units that need readable width. This is the recommended approach.

## Verification

- Add or update a deterministic test only if there is an existing practical static/template test seam for the stylesheet contract.
- Run `go test ./cmd/serf-hub` to verify hub web tests still pass.
- Inspect the CSS diff to confirm no template or app-shell flex behavior changed unless implementation discovers a concrete need.

## Scope

In scope:

- Width cap and centering for the primary workspace content column.
- Keeping responsive/mobile and side-pane behavior intact.

Out of scope:

- Redesigning message bubbles, typography, or composer controls.
- Making the cap user-configurable.
- Refactoring the workspace partial into a new layout component unless required by implementation evidence.
