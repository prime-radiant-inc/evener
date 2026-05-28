# AppWire Agent Event Announcements

Agent-only session events that have no Codex AppWire primitive are projected as
`systemMessage` transcript items. This keeps the web UI, TUI, notification
replay, and optimistic transcript reducer on one rendering path.

Projection rules:

- If a turn is active, append the announcement to that turn with
  `item/completed`.
- If no turn is active, emit a completed synthetic turn containing one
  `systemMessage` item.
- Do not create one-off AppWire methods unless a client needs structured state
  outside the transcript.
- Keep announcement text concise and deterministic so notification replay is
  stable.

Covered agent events:

- `TURN_LIMIT`: turn or tool-round limit reached.
- `LOOP_DETECTION`: loop detection warning.
- `SKILL_ACTIVATED`: a skill was activated.
- `CONTEXT_COMPACTION`: compaction layer/count/token metadata.
- `PLUGIN_LOADED`: plugin load summary.
- `HOOK_START`: hook execution started.
- `HOOK_END`: hook execution finished.
- `FORK_SUMMARY`: fork summary captured.
- `PROMPT_LOADED`: prompt source loaded.
- `ROUND_TIMINGS`: round-level timing telemetry.
