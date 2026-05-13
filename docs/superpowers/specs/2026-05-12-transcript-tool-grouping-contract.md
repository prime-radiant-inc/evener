# Transcript Tool Grouping Contract

This contract applies to Hub AppWire transcript projection in the web UI and
serf-tui.

## Contract

- A tool invocation is identified by `callId` when present, otherwise by item
  `id`.
- `tool_call` start, streamed `item/toolOutput/delta`, and completed
  `tool_call` result items for the same invocation are one transcript unit.
- Live streaming and history replay must converge to the same final transcript
  shape.
- Interleaved assistant text remains in transcript order, but it must not cause
  the matching tool result to render as a second unrelated tool row.
- A completed or failed tool result without a previously seen start still
  renders as one completed tool unit.
- Tool output belongs inside the tool invocation UI, not as a separate
  conversation row.
- Failed tools stay grouped with their invocation and render an error state
  without collapsing into another tool call.

## Fixtures

`internal/appwire/testdata/tool-groups-thread.json` covers the required replay
cases: split start/result items, interleaved assistant text, and a failed tool.
