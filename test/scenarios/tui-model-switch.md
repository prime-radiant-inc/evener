# tui-model-switch: serf-tui `/model` switches live, updates the header, and shows the switch marker after reload

**What this covers**: spec Acceptance criteria 5 (transcript shows the
switch marker after reload) and part of criterion 6 (both surfaces display
the current model for a cold-attached client) via the TUI path. Exercises
`/model` (`cmd/serf-tui/hub_command_registry.go:321-346`), the `(active)` tag fix in
the picker (item ids `provider/model` normalized against bare
`detail.Model`), the live header refresh from `thread/model/changed`
(`hub_session_view.go:51`'s `model` part), and the dashboard row Model
cell (`hub_dashboard_view.go:338`; the details drawer's `Model:` line is
`:503-504`).

## Pre-state

- Hub + serf-tui running against a real hub, tmux session attached (pattern:
  `tui-workspace-navigation.md`).
- A session spawned on model A (e.g. `openai/gpt-5.5`), idle.

## Steps

1. `tmux send-keys` to attach the TUI to the session. Capture-pane; read the
   session header's `model` part — confirm it shows model A.
2. Type `/model`, `Enter` (bare form). Capture-pane: confirm the picker
   opens and the row matching model A is tagged `(active)`.
3. Navigate to a different model B in the picker, `Enter` to select.
   Capture-pane.
4. Wait ~2s for the `thread/model/changed` notification. Capture-pane:
   confirm the header's `model` part now reads model B **without**
   re-attaching or re-opening the session.
5. Check the dashboard (session list) view for this session's Model column.
6. Detach and re-attach the TUI to the session (or restart serf-tui) — a
   cold attach with no prior notification.
7. Scroll/read the transcript for the switch marker line.

## Expected

- Step 2: exactly one row is tagged `(active)` and it is model A's — the
  fixed normalization comparing `provider/model` ids consistently
  (`modelIDMatchesActive`,
  `cmd/serf-tui/internal/tuipick/model_picker.go:171,207-218` — the picker
  moved into the `tuipick` subpackage), not the pre-fix bug where
  `activeModel` never
  matched because item ids were `provider/model` while `detail.Model` was
  bare.
- Step 4: header `model` part updates live to model B, driven by
  `thread/model/changed`, no manual refresh.
- Step 5 (AC dashboard convergence, N2): the dashboard's Model column for
  this session also reads model B.
- Step 6-7 (AC 5 + AC 6 cold-attach): on a fresh attach with zero prior
  notifications, the session header shows model B (from the thread
  snapshot, not a missed notification) — confirms `ModelProvider` is
  "switch-fresh" per the snapshot-surface decision. The transcript shows a
  `systemMessage` line reading exactly `Switched model: <A's
  provider/model> → <B's provider/model>` (schema.TurnModelSwitch, excluded
  from `expandHistory` but rendered by both the live projector and
  `apptranscript.ProjectTurn`).
- Falsification: the picker's active tag is missing/wrong; the header needs
  a manual refresh to show model B; the dashboard row stays stale; the
  marker line is absent, has the wrong text, or is missing after a fresh
  (non-live) attach.

## Cleanup

- Detach tmux / kill the TUI process.
- Shut down the session if spawned solely for this card.

## Sharp edges

- The marker text is exact: `"Switched model: %s/%s → %s/%s"` built from
  `oldProfile.ID()/oldProfile.Model()` and `nextProfile.ID()/nextProfile.Model()`
  (`buildModelSwitchMarkerText`, `agent/session.go#buildModelSwitchMarkerText`; `:775` is
  `SetModel`'s doc comment, 125 lines above) — assert that literal first
  line, not a paraphrase. The marker may carry further `Warning:` lines
  (`:902-908`), so match the first line rather than the whole entry.
- Step 6's "cold attach" must be a genuinely fresh client path (new
  `thread/read`/subscribe), not a resumed in-memory picker state — restart
  serf-tui or navigate away and back if detach/reattach doesn't force a
  re-hydration.
