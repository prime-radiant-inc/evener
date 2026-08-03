# Mobile Root Overscroll Design

**Date:** 2026-08-03
**Status:** Approved

## Problem

The mobile WebUI uses an app-like shell. The shell owns the viewport, and panes own scrolling. The existing mobile root rule locks document scrolling with `overflow: hidden`, but it does not explicitly disable browser-level scroll chaining, pull-to-refresh, or rubber-band page bounce.

These gestures can move or refresh the browser surface when a user reaches a pane's scroll boundary. That behavior conflicts with the fixed StackHost top bar and pane footer.

## Design

In `cmd/serf-hub/frontend/src/styles/global.css`, add `overscroll-behavior: none` to `html` inside the existing `@media (max-width: 899px)` rule.

Keep the existing `overflow: hidden` declarations on `html, body`. Keep pane scroll containers unchanged.

The root declaration will:

- suppress mobile pull-to-refresh;
- suppress browser rubber-band and page bounce;
- prevent pane scroll gestures from chaining to the document;
- preserve scrolling inside pane-owned scroll containers;
- leave desktop behavior unchanged.

This rule complements the shared shell's `100dvh` height. It does not replace viewport sizing or hide layout overflow.

## Testing

Add a deterministic source-level CSS test that verifies:

1. the declaration appears inside `@media (max-width: 899px)`;
2. the selector is `html`, not a pane scroll container;
3. the value is `none`;
4. the existing mobile `html, body { overflow: hidden; }` contract remains intact.

Run the focused CSS contract test, the mobile full-shell layoutguard, frontend lint, and frontend build.

## Alternatives Rejected

- **`overscroll-behavior: contain`:** blocks scroll chaining but can preserve browser overscroll effects; it does not meet the requirement to disable pull-to-refresh and page bounce.
- **A global desktop-and-mobile rule:** changes desktop browser behavior without need.
- **Replacing `overflow: hidden`:** changes document scroll ownership and risks restoring page-level movement.
