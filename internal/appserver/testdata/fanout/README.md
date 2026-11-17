# Fanout wire golden

`wire.json` is the wire bytes each content selection received for five recorded
notifications, captured from the tree before the hub fanout change that
introduced this directory. It is the independent oracle for
`TestFilteredFanoutWireBytesMatchPreChangeGolden`: re-deriving the expected value
from the fanout's own filter would prove nothing, so the bytes clients saw before
the change are recorded here instead.

Keys are `<record>/<selection>`:

- `history` — one `history/updated` carrying user, assistant, reasoning,
  steering (user and system), command-execution, and diagnostic items.
- `stream-upsert`, `tool-upsert` — `overlay/upserted` for a streaming agent
  message and a running tool call.
- `stream-delta`, `tool-delta` — `overlay/delta` targeting those two overlays.

Selections: `full` (no filter at all), `hide-thinking`, `hide-tools`,
`hide-intents`, `hide-conversation`, `hide-system`, `hide-diagnostics`,
`hide-thinking-tools`.

An empty string means that subscriber was sent nothing for that record. Three
entries are empty, each for its own reason:

- `stream-upsert/hide-conversation` — that view drops the agent message the
  upsert carries, so the upsert is not kept.
- `tool-delta/hide-tools` and `tool-delta/hide-thinking-tools` — a tool output
  delta reaches only a view that keeps tool calls. These two keep the intent
  form of the call, which carries no output, so the record is dropped for them.

The `hide-intents` rows are not empty, which is the distinction to keep in
mind: `hide-intents` excludes only tool call intents, so it still keeps the
tool call itself, with only its description stripped.
`tool-upsert/hide-intents` carries the call without `description`, and
`tool-delta/hide-intents` still delivers the output delta.

All subscribers were seeded with both overlay identities before the broadcast,
the way a live subscription seeds what its snapshot held. A delta therefore
needs a seeded identity to reach a reader, and a tool overlay's delta also
needs a selection that keeps tool calls.

The bytes are map-marshalled, so a filtered frame's members come out in
alphabetical order while a full view's passthrough frame keeps the producer's
order. That difference is part of the contract, not an accident.

Regenerate only by capturing from a tree whose fanout you intend to replace;
never by running the current implementation, which would make the check
circular.
