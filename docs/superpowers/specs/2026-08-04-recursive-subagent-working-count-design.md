# Recursive Subagent Working Count

## Goal

Show the number of currently working subagents in each session row in the web rail. The existing `working` status remains the session's own status; the new text reports active descendants.

## Design

`RailRow` will derive the count from the session's existing recursive `children` tree. A descendant counts when its wire state is `active`. The traversal includes every nested level and excludes the session row itself. No API or wire-model changes are needed.

The existing activity line will render:

- `1 subagent working` for one active descendant.
- `N subagents working` for multiple active descendants.

When no descendant is active, the current activity text and behavior remain unchanged. Existing branch and project text continues to render in its current order. The count will replace only the leading `working` label when the session itself is active and has active descendants; otherwise, an active-descendant count will be added as the activity description for rows that already display an activity line. The implementation will preserve existing signal styling and row layout.

## Testing

Add deterministic frontend unit tests for the pure recursive count and rendering behavior:

1. No descendants produces the existing activity text.
2. One direct active descendant produces `1 subagent working`.
3. Multiple direct active descendants use the plural form.
4. Active descendants nested below an inactive intermediate node are counted.
5. Non-active descendants are excluded.

Run `make test-web` and, when available, `make test-web-browser`. Run Biome formatting on touched frontend files before the gates.

## Scope and constraints

- Keep the change within the frontend rail status presentation and its tests.
- Do not change server-side tree serialization.
- Do not count the main session itself.
- Use the existing `active` wire state as the definition of “working.”
