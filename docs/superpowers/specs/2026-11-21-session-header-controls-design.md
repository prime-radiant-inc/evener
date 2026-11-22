# Session header controls

## Intent

Make Find in session and Overview available directly from the session header instead of hiding them in the composer menu.

## Approved behavior

- Desktop header actions read **Find**, then **Overview**, left to right.
- Tablet and phone use the same order with icon-only buttons and existing 44px touch targets.
- Verbosity remains in the composer `⋯` menu.
- Find and Overview leave the ordinary session's composer menu when their header controls are present. Rail menus and embedded cascade columns retain their existing access.
- Find targets the owning pane. Overview preserves the existing focus, scope, and same-session toggle behavior, with the same icon in both states and selected-state styling when open.
- A mobile session with no real Back destination gets Sessions as its only leading navigation control. A session with a retained parent, valid back-stack destination, or attention Back gets Back instead. The two controls never appear together on a session.
- Mobile session titles align left. Non-session mobile top bars keep their existing layout.
- The existing left-sidebar Show/Hide control already shares `SidebarIcon`; it does not change.

## Constraints

- Use the existing `Button`, `IconButton`, `Tooltip`, activity-sidebar store, and Find request bridge. Add no widget API.
- Keep the production change within a couple hundred lines and prefer the smallest composition that covers every breakpoint.
- Preserve all composer turn, recovery, organization, and shutdown actions.

## Approaches

1. **One shared inspection-action component, mounted by Session and StackHost.** Reuse the same Overview helper in the existing menu. This is the chosen approach because it changes only session surfaces and needs no new chrome-store channel.
2. **Publish generic pane actions through chromeStore.** Rejected: it would relocate other panes' actions and broaden the product change.
3. **Separate desktop and mobile action implementations.** Rejected: it would duplicate Find and Overview behavior.
