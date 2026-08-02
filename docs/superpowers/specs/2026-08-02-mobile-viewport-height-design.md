# Mobile Viewport Height Design

**Date:** 2026-08-02
**Status:** Approved

## Problem

On iOS, the WebUI can extend below the visible browser viewport. The strip beneath the session composer appears roughly one line too tall, and the same vertical overflow affects Spawn and other panes.

The shared app shell uses `height: 100vh`. Mobile Safari may resolve `vh` against a layout viewport taller than the currently visible area when browser chrome is present. `StackHost`, `PaneScaffold`, and the session footer then inherit that oversized height. The footer remains inside the shell, but its bottom edge falls below the visible screen.

The existing Spawn browser guard passes at 390 px because it renders the Spawn pane without the complete app-shell height chain. The viewport meta tag is present, and pane-local flex children already use the required `min-height: 0` rules.

## Design

Update the shared shell height in `cmd/serf-hub/frontend/src/shell/AppShell.module.css`:

1. Keep `height: 100vh` as a fallback for browsers without dynamic viewport units.
2. Add `height: 100dvh` after it so supported mobile browsers track the visible viewport as browser chrome expands and retracts.

Keep `StackHost`, `PaneScaffold`, and the composer footer unchanged. Their existing `height: 100%`, flex sizing, and internal scrolling should inherit the corrected shell height.

Do not hide document overflow. `overflow: hidden` would conceal the sizing error and could block legitimate pane scrolling.

## Behavior

- The mobile shell fits the currently visible viewport.
- The session composer and the area beneath it remain visible above the screen edge and safe area.
- Spawn and non-Spawn panes receive the same correction.
- Pane bodies continue to scroll internally.
- Desktop behavior remains unchanged.
- The shell retains a `100vh` fallback.

## Testing

Add deterministic coverage at two levels:

1. A source-level CSS contract test asserts that the shell declares `100vh` before `100dvh`.
2. A real-browser shell guard renders the complete mobile shell and asserts:
   - the document does not exceed the visible viewport height;
   - the shell bottom aligns with the visible viewport bottom;
   - the session footer/composer bottom does not extend past the viewport;
   - both a session pane and a non-session pane satisfy the invariant.

Mutation-check the browser guard by temporarily removing the `100dvh` declaration and confirming that the guard reports the overflowing shell or footer.

Run the focused tests and browser guard, then the frontend test suite, lint, and production build.

## Alternatives Rejected

- **`100svh`:** prevents overflow but leaves unused space when browser chrome retracts.
- **A JavaScript `visualViewport` height variable:** adds resize listeners and lifecycle complexity without a demonstrated need.
- **Document-level clipping:** masks the defect rather than fixing the shared height chain.
