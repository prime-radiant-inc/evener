# Delegate Message Display Design

**Date:** 2026-08-03
**Status:** Approved

## Problem

The Web UI summarizes a `delegate_send` call as `Messaged <delegate>` and shows the tool output when the row expands. The expanded row does not show the message sent to the delegate. Its raw output may also combine the delegate's response with delivery metadata.

A reader should see the full message and any response when expanding the activity row. The compact summary should remain concise.

## Scope

This change affects the Web UI renderers for `delegate_send` and its historical `job_send_message` alias. It does not change agent behavior, tool schemas, transcript storage, wire formats, or delegate lifecycle state.

## User Interface

The collapsed row retains its current summary:

```text
Messaged <delegate> · <delivery/status>
```

The expanded row contains:

1. A **Message** section with the complete `message` argument.
2. A **Response** section when the tool output contains response text.

The UI preserves whitespace and line breaks in both sections. It renders their contents as plain text, not Markdown.

A call that is in flight, returns only delivery metadata, or has no response omits the Response section. The interface does not show an empty response placeholder.

## Architecture

Keep the change inside the existing job-tool renderer module:

- `cmd/serf-hub/frontend/src/panes/session/transcript/tools/jobTools.tsx`
- `cmd/serf-hub/frontend/src/panes/session/transcript/tools/jobTools.test.tsx`

Extract the subagent-row correlation effect from `CorrelatingBody` into a small hook. `CorrelatingBody` continues to serve `job_status` and `job_stop` without visual changes.

Add a dedicated `DelegateSendBody`. It uses the shared correlation hook, reads the sent message and response, and renders the two sections. The registered descriptor continues to match both `delegate_send` and `job_send_message`.

This boundary gives delegate messaging a focused presentation component without adding a general rendering abstraction for one exceptional tool.

## Data Flow

### Sent message

`DelegateSendBody` parses `item.argumentsJSON` and reads its `message` string. This field is the original tool input and survives in settled transcript items.

When `message` is absent, empty, or unavailable because the arguments are malformed, the body omits the Message section. It does not expose raw argument JSON or throw an error.

### Delegate response

`DelegateSendBody` reads `item.output`. The current output may end with the bracketed delivery/status footer that the compact summary already presents. The body removes only a recognized trailing footer, then trims the separator around it.

If non-whitespace text remains, the body renders it as the Response section. If only the footer remains, the body omits the section.

An unrecognized output remains visible as response text. This fallback prevents new or historical result shapes from hiding useful content.

### Correlated delegate row

The body preserves current correlation behavior. It resolves the delegate row from the `to` argument, or the historical `target` argument, and updates an existing row from the trailing footer's status. It never creates a delegate row.

## Error and Compatibility Behavior

- Malformed arguments do not crash the renderer.
- Missing message text does not produce an empty section.
- Unknown output remains visible.
- Only a recognized trailing status footer is removed from the response.
- The legacy `job_send_message` alias receives the same display behavior.
- The compact summary and delegate-row lifecycle updates remain unchanged.

## Testing

Add focused deterministic tests to `jobTools.test.tsx` for:

- full sent-message display;
- multiline whitespace preservation;
- response display without the trailing status footer;
- footer-only and in-flight output omitting Response;
- unrecognized output remaining visible;
- malformed or missing message arguments degrading safely;
- `job_send_message` parity; and
- unchanged correlation updates.

Run:

```sh
cd cmd/serf-hub/frontend
npm test -- --run src/panes/session/transcript/tools/jobTools.test.tsx
npm run typecheck
npm run lint
npm test
```

The implementation should reuse existing transcript body styles and add no layout-specific CSS. If implementation requires new layout CSS, also run the relevant browser layout and overflow guards described in `docs/testing.md`.

## Acceptance Criteria

1. Expanding a `delegate_send` or `job_send_message` row shows its complete sent message.
2. The expanded row shows the delegate's response when response text exists.
3. Delivery/status metadata remains in the compact summary and does not repeat in the Response section.
4. A call without response text shows no empty Response section.
5. Compact summaries and correlated delegate-row updates behave as before.
6. Malformed or historical transcript data degrades without crashing or hiding unrecognized output.
7. Focused and broader frontend checks pass.
