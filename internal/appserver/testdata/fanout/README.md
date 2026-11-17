# Fanout wire golden

`wire.json` is the wire bytes each content selection received for one recorded
notification, captured from the tree before the hub fanout change that introduced
this directory. It is the independent oracle for
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
entries are empty: the two `hide-intents` rows for the tool records, whose
command-execution item is outside an intent-only view, and `hide-thinking` for
the streaming upsert, whose agent message that view drops.

All subscribers were seeded with both overlay identities before the broadcast,
the way a live subscription seeds what its snapshot held, so a delta reaches
every reader that kept one.

The bytes are map-marshalled, so a filtered frame's members come out in
alphabetical order while a full view's passthrough frame keeps the producer's
order. That difference is part of the contract, not an accident.

Regenerate only by capturing from a tree whose fanout you intend to replace;
never by running the current implementation, which would make the check
circular.
