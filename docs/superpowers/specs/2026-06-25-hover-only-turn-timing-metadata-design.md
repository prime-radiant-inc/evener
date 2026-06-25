# Hover-Only Turn Timing Metadata Design

Date: 2026-06-25

## Goal

Reduce visual noise in the Serf web transcript by hiding task/tool timing metadata until the user shows interest in the relevant row. Time and runtime details should remain available on hover and keyboard focus.

## Context

The web UI renders transcript annotation rows in `cmd/serf-hub/assets/renderer*.js` and styles them in `cmd/serf-hub/assets/style.css`. Tool and task-related rows can include clock/runtime metadata such as event time and duration. These details are useful for inspection but distract from the main transcript when always visible.

The existing formatting helpers in `cmd/serf-hub/assets/renderer-format.js` include:

- `toolEventTime`
- `toolDuration`
- `formatToolClock`
- `formatToolDuration`

The existing visible metadata styling includes selectors such as `.tool-call .tool-meta`.

## Design

Keep timing metadata in the rendered DOM, but make it visually quiet by default. Use CSS-only hover/focus reveal behavior instead of adding JavaScript state.

For rows that display time/runtime metadata:

- Default state: metadata is hidden visually with `opacity: 0` and `visibility: hidden`.
- Hover state: metadata becomes visible when the relevant row is hovered.
- Keyboard state: metadata becomes visible when the relevant row contains focus via `:focus-within`.
- Layout should remain stable: preserve the metadata element's space where practical so hovering does not shift row content.
- Motion should be subtle and use the existing motion token.

Primary target:

```css
.tool-call .tool-meta { ... }
.tool-call:hover .tool-meta,
.tool-call:focus-within .tool-meta { ... }
```

If implementation finds equivalent task timing metadata selectors, apply the same pattern there with the narrowest selector that matches the existing markup.

## Accessibility

The metadata remains in the DOM so assistive technology can still access it. Keyboard users get parity through `:focus-within`; if a row has no focusable descendant, the implementation should not add unnecessary tab stops just for this hover affordance unless testing shows keyboard access is otherwise impossible for interactive metadata.

## Verification

- Add or update a deterministic CSS contract test for the hover/focus reveal rules.
- Run the relevant web UI CSS/JS test command if available.
- Run `go test ./cmd/serf-hub -count=1` to ensure the hub package still passes.

## Scope

In scope:

- Time/runtime metadata visibility for transcript task/tool turn rows.
- CSS-only hover and focus reveal behavior.

Out of scope:

- Changing timing data calculation or formatting.
- Adding new runtime metadata.
- Redesigning the transcript rows beyond timing metadata visibility.
- Browser-only visual verification unless implementation exposes an issue that cannot be tested statically.
