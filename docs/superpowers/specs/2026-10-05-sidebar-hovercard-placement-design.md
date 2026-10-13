# Sidebar session hover-card placement

## Intent and scope

Jesse wants sidebar session context to float beside the sidebar instead of crowding
the current row. He chose the beside-sidebar direction with a 12px gap and
row-centered vertical alignment. This change affects browser session rows in the
desktop sidebar and the browser's sessions drawer. Native and terminal clients,
ordinary tooltips, and other rich hover cards keep their existing behavior.

The main checkout's unrelated untracked files remain untouched. Work proceeds in
`wip-sidebar-hovercard-float`, based on `abd240265411bb1a35f10d1761a56cf0685339ea`.

## Placement

- Prefer the card's left edge 12px beyond the rendered sidebar's right edge.
  Measure the sidebar boundary, not the title width, indentation, timestamp slot,
  or pointer position. A resized sidebar and different title lengths must produce
  the same separation from that boundary.
- Center the card vertically on the owning session row. Shift it vertically when
  necessary to preserve the existing 8px viewport-edge clearance.
- When the card cannot fit to the right, fall back above or below the session row,
  with a 12px gap. Prefer above when it fits, otherwise below, then shift into the
  viewport when neither fits. Horizontal fallback remains centered on the row and
  uses the existing viewport clamp.
- These containment guarantees apply when the card's existing dimensions fit in
  the viewport. Keep existing card sizing and full content; this change introduces
  no truncation, new scrolling surface, or content removal.
- Recompute placement when the card's measured size changes. Measure its
  untransformed layout dimensions so the existing scale-in animation does not
  distort collision calculations.

## Interaction and preservation

Keep the body portal and non-interactive overlay surface. Opening the card must
not move rows, take focus, activate a session, or change navigation or session
state. Preserve hover delay, keyboard/assistive focus and description association,
long-press reveal, ordinary touch activation, click suppression after a long press,
and outside-pointer dismissal. Scroll and viewport resize dismiss the card and
cancel pending reveal; a fresh hover, focus or long press can reveal it at the new
position. Reduced-motion and theme behavior remain unchanged.

## Implementation boundary

Use a narrowly scoped positioning option on the existing HoverCard/floating-bubble
path. Rail owns the sidebar and session-row geometry; shared widgets own portal,
measurement and lifecycle. Reuse the existing viewport clamp rather than copying
collision arithmetic. Keep default tooltip and HoverCard placement unchanged.
No backend, protocol, shared-client state, navigation read or recovery-owner change
is needed.

Update the sidebar placement contract in `docs/web-ui/design-system.md` and the
browser/session-navigation references in `docs/product/subsystems.md` and
`docs/product/session-activity.md`.

## Behavior evidence

1. Red-first placement tests cover the 12px sidebar separation, row-center
   alignment, top and bottom collisions, narrow-screen above/below fallback,
   changed card size, and unchanged default tooltip/card placement.
2. Production RailRow/component coverage proves correct sidebar and row geometry
   is supplied without changing content, focus descriptions, activation, or touch
   behavior. Cover more than one title length and sidebar width.
3. A real Chrome journey renders production Rail/session rows, hovers the title,
   checks actual card and sidebar rectangles and unchanged row layout, and checks
   keyboard reveal, dismissal on scroll/resize, fresh reveal, and the narrow
   sessions-drawer/touch path. Use fixture session data, no model calls or mocked
   widget/placement behavior. Retain meaningful browser regression assertions.
4. Run focused hover-card, tooltip and RailRow tests, `make test-web`, and
   `make test-web-browser`. Read each runner before trusting its reported result.
   Full repository CI establishes the remaining required gates.

The production hub currently rejects this browser as unauthorized. Browser
verification uses an isolated production-component harness rather than reading or
exposing the hub's auth token, and does not restart or deploy the running hub.
