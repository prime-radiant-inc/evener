# Additional SDK producer evidence — 8 September 2026

The retained fixture `/tmp/evener-session-producers-m1F18a/run/qualification`
provides direct SDK notification observations, an independent final read, and
provider HTTP evidence. It adds six distinct producer names to the fifteen
already documented in `sdk-notifications-evidence.md`, for 21 in this series.

- `turn/started` (raw event 0) and `turn/completed` (event 10) identify
  `turn_m2` through `params.turn.id` and the same thread/ref. These events have
  no top-level `turnId`.
- `thread/status/changed` (events 2 and 11) carries the same thread/ref and
  transitions `active` to `awaiting`. It is a thread-level status event and has
  no `turnId`.
- `item/started` (event 6), `item/agentMessage/delta` (event 7), and
  `item/completed` (event 8) share item ID `item_communicate_preview_29` and `turn_m2`. The started
  and completed items also share transcript key
  `apptranscript-item-v1:turn_m2:3:2`; the delta carries only the wire item ID. The delta
  and final item text are `streaming retry fixture complete`.

The independent final read reports completed `turn_m2` and the same completed
agent item/text. Provider HTTP request 4 is the qualification request; its
recorded authored tool is `communicate`, and the authored message corresponds
to that final text. Assertions were executed against the retained raw files;
no event was injected or synthesized.

The backend binaries are the retained `d2d5eedf9` pair and the SDK is the
independently installed `58d1b079f` package. The receipt retains binary, helper,
raw evidence and verifier hashes. The filtered session recipe observed the two
status events; lifecycle and delta checks used the SDK raw notification callback.

The SDK tarball comparison is distinct from the installed consumer inventory:
the tarball contains 139 regular files and the consumer tree contains 283.
The byte-equality assertion covers the tarball's 139 files only. Cleanup
assertions found no retained bearer/auth credential, private hub log, owned
listener, or fixture process; `hubLogRetained` is false. This evidence does
not establish global notification coverage, reconnect/replay, warning coverage,
native behavior, signing, or release readiness.
