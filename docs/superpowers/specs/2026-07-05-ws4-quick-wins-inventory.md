# WS4 — quick wins: inventory (not a design spec)

From the 2026-07-03 UX diagnostic; unblocked since ask-user landed.

- **Send-key setting (web):** ⌘/Ctrl-Enter stays the default send; add a
  setting for Enter-to-send (Jesse's OS steals ⌘-Enter). Hardcoded at
  renderer.js `handleComposerKeydown`; no keybinding config exists yet.
- **Font-size setting (web):** user-adjustable base text size; scale tokens at
  style.css `--text-*`; never designed in docs/web-ui/design-system.md.
- **Copy fixes:** "tasks no tasks"; eternal "tasks loading…" on ended
  sessions; settings copy staleness (see WS6 inventory overlaps — take the
  quick ones here, leave systemic vocabulary work to WS6).
