# Group Skill Activation with `use_skill` Tool Calls

Date: 2026-06-24

## Problem

The web transcript currently shows a successful `use_skill` invocation and the resulting `Skill activated` lifecycle event as two separate transcript units:

1. a `use_skill <skill-name>` tool-call row, and
2. a separate collapsed `Skill activated` system disclosure.

For a user reading the transcript, these are one action: the assistant invoked `use_skill`, and the skill activation is the tool result. Rendering them separately makes the startup transcript look noisier and implies that an independent system event happened after the tool call.

## Goal

When a `Skill activated` event is clearly caused by a preceding `use_skill` tool call for the same skill in the same turn, render them as one transcript unit. The activation detail should live inside the `use_skill` tool card/disclosure rather than as a separate lifecycle/system disclosure.

Standalone skill activation events must still render as lifecycle/system messages.

## Non-goals

- Do not change the requirement that agents call `use_skill` when a skill applies.
- Do not suppress skill activation information entirely.
- Do not group unrelated lifecycle events with tool calls.
- Do not redesign general system-message coalescing.

## Current Behavior and Root Cause

Backend appwire projection handles these events separately:

- `EventToolCallStart` / `EventToolCallEnd` project `use_skill` as a `commandExecution` item keyed by `callId`.
- `EventSkillActivated` projects as a `systemMessage` with description `Skill activated` and text `Activated skill: <name>`.

The web renderer then does exactly what the projection says:

- `TOOL_CALL_START` / `TOOL_CALL_END` render the `use_skill` tool card.
- `SYSTEM_MESSAGE` renders `Skill activated` through `appendSystemMessage`, which creates a separate collapsed lifecycle disclosure.

The root cause is that the transcript model does not carry correlation between the `use_skill` tool invocation and the subsequent skill activation event. The browser can infer adjacency in simple cases, but the durable projection treats them as unrelated transcript items.

## Design

### Projection-level correlation

The appwire projector should correlate a skill activation with the closest preceding `use_skill` tool item in the same active turn when all of these are true:

- the tool name is `use_skill`,
- the tool arguments identify the same skill name as the activation event,
- the tool call is in the same turn, and
- no later non-correlatable transcript item has made the relationship ambiguous.

When correlated, the projector should attach the activation detail to the `use_skill` command-execution item rather than emit a separate `systemMessage`.

The projected item should preserve the existing tool-call identity (`callId` and item ID), and include enough structured data for clients to render an honest detail view without parsing prose. A suitable raw payload is:

```json
{
  "skillActivation": {
    "name": "superpowers:using-superpowers",
    "text": "Activated skill: superpowers:using-superpowers"
  }
}
```

The exact JSON field names should follow existing appwire raw-field conventions where practical.

### Frontend rendering

The web `use_skill` renderer should render correlated activation detail inside the existing tool card. The visible collapsed row remains concise, for example:

```text
✓ skill superpowers:using-superpowers
```

If expanded, the card shows the activation detail and any existing tool output that is useful to the reader. It must not also render a sibling `Skill activated` lifecycle disclosure for the same activation.

The terminal renderer should either render the same grouped detail if it consumes the raw metadata, or retain its current concise successful `use_skill` row. It must not regress into duplicate visible activation rows for grouped data.

### Fallback behavior

If `EventSkillActivated` cannot be correlated with a `use_skill` invocation, keep today’s standalone lifecycle rendering:

```text
Skill activated
  Activated skill: <name>
```

This covers startup/session-level activation, future activation sources, and malformed or incomplete event sequences.

## Data Flow

### Live event flow

1. `EventToolCallStart` for `use_skill` starts a `commandExecution` item.
2. `EventToolCallEnd` completes the item with the tool output.
3. `EventSkillActivated` arrives.
4. The projector checks the recent same-turn `use_skill` candidate.
5. If matching, it emits an update/completion for the same tool item carrying activation metadata.
6. The browser updates the existing `use_skill` card.
7. If not matching, the projector emits the existing standalone `systemMessage`.

### Replay/history flow

History replay must converge to the same final shape as live streaming. The durable transcript/appwire conversion path should either store the grouped shape directly or perform the same deterministic correlation when replaying events.

The existing transcript tool grouping contract still applies: a tool invocation and its output are one transcript unit. This design extends that intent to the skill activation event when the event is causally the result of `use_skill`.

## Error Handling and Edge Cases

- **Failed `use_skill`:** do not attach activation detail to a failed tool call unless the activation event still explicitly reports the same skill activation. Prefer normal tool failure rendering plus standalone activation only if both actually happened.
- **Multiple `use_skill` calls in one turn:** match by skill name and closest preceding candidate.
- **Legacy arguments:** support both `skill_name` and legacy `name`, matching the existing renderer behavior.
- **Out-of-order events:** if activation arrives before the tool call can be known, render as standalone rather than buffering indefinitely.
- **Repeated activation for the same skill:** only group the activation with the nearest matching call. Additional unmatched activations remain standalone.
- **Missing active turn:** keep standalone system-message behavior.

## Testing

### Backend projector tests

Add tests under `internal/appprojector` for:

1. `use_skill` followed by matching `EventSkillActivated` produces one grouped tool item and no extra `systemMessage`.
2. unmatched `EventSkillActivated` still produces a standalone `systemMessage`.
3. multiple `use_skill` calls match the closest preceding call with the same skill name.
4. legacy argument key `name` still correlates.

### Web renderer tests

Add or extend `cmd/serf-hub/jstest/test-tool-renderers.js` coverage for:

1. grouped activation renders inside `.tool-call.use_skill`,
2. no sibling `.system-message` with title `Skill activated` is rendered for the grouped case, and
3. standalone `SYSTEM_MESSAGE` activation still renders when no grouped metadata exists.

### Regression criteria

A fresh session startup that invokes `superpowers:using-superpowers` should display a single grouped `use_skill` disclosure/card for that action, not both a `use_skill` row and a separate `Skill activated` disclosure.
