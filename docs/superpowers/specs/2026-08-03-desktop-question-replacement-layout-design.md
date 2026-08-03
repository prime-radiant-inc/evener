# Desktop question replacement layout

## Goal

When questions are open, the question surface replaces the composer in the active session pane. The shared parent fills the available pane height, and both the normal composer and the question surface are bottom anchored within that space.

## Layout behavior

The composer/footer parent must remain in the pane's flex chain and consume the available height with `flex: 1 1 auto` and `min-height: 0`. The normal composer is aligned to the bottom of that filled region.

Pending questions use the same replacement slot. The question surface is rendered instead of the composer, not above or alongside it. Its outer surface is bottom anchored within the filled parent. If the question content exceeds the available height, the question region owns the necessary vertical scrolling without changing the pane's outer scroll behavior. Existing question controls, card structure, and submission behavior remain unchanged.

## Responsive behavior

The implementation must preserve the existing mobile viewport-height and overscroll containment behavior. It must not use viewport-fixed positioning that can escape the active pane or overlap other panes. Desktop and mobile should share the replacement-slot semantics; only existing responsive sizing and overflow rules may differ.

## Testing

Add or update focused frontend tests to cover:

- the parent layout contract fills available space;
- the composer and question surface use the same replacement slot;
- pending questions do not render an additional composer;
- question content remains usable when it exceeds the available height;
- existing mobile layout contracts continue to pass.

Run the focused Composer and AskDock Vitest tests, then the repository web preflight/build checks.

## Scope

Change only the session composer/question layout and its focused tests. Do not refactor question state, controls, or unrelated pane layout.
