# To-Ask-Jesse

Open design questions where the right call isn't obvious. Add new items at the
bottom with date + context.

## 2026-05-16 — Answered 2026-05-17

### w7j9 BLOCKER — model filtering / Responses API compatibility
**Answer**: Web-search for current correct handling. Then fall back to /v1/chat/completions for OpenAI models when /v1/responses fails. Never silent fail.

### ahtm — /new form state persistence
**Answer**: Persist both model and working dir (current model behavior, extend to working dir).

### y5bt — branch chip `(default)` meaning
**Answer**: Show the resolved branch name in the chip before spawn.

### mggf — stream-ended recovery UX
**Answer**: Retry first, Resume as fallback. Also: serf should have exponential backoff retries internally — copy what Codex does in `inspo/codex`.

### Credentials display when both env and file are set
**Answer**: Show both with priority indicator (effective env, file shadowed).

### `/settings/project` without `?cwd=`
**Answer**: Keep the 'No project selected' state but add a project picker inline so the user can choose without leaving the page.

### Codex launch tab parity
**Answer**: Make it writable, full parity with `/settings/launch-serf`.

### TOFU re-prompt cadence
**Answer**: Remember a SET of trusted hashes per project so branch-switching with different `.serf/launch.toml` content doesn't constantly re-prompt.

### Model name validation on spawn
**Answer**: Don't pre-validate or filter the picker. Trust the user's typed value. BUT failures must NEVER be silent — every failure surfaces with a clear error.

## 2026-05-17 — Open

### Turn count semantics for failed turns (kata k5t4)
For a turn that failed before any assistant response (e.g. stream-ended): should the turn count show 1 (user messages sent) or 0 (completed exchanges)?
Today live shows 1, ended shows 0. Pick one definition and apply consistently across live + persisted/past-index paths.
**Answered 2026-05-17**: Completed exchanges (committed in e9bc444).

### Resume-with-new-daemon UX (kata e465)
The hub's `MethodTurnStart` already auto-resumes when the source lookup fails or the turn returns `ErrorSessionUnavailable` (see `cmd/serf-hub/app_rpc.go:317-346`). So Layer 3 is largely in place on the server. The kata is really about the UX layer: when the user sees a diagnostic, when should the action button be "Retry turn" vs "Reconnect & retry" vs "Resume session"?

Today (`cmd/serf-hub/assets/renderer.js:932+`):
- `source=provider` (e.g. stream-ended) → "Retry turn" button. Calls `startTurn` with `lastUserText`. Hub auto-resumes if needed.
- `source=hub` (e.g. daemon died, rendezvous timeout) → no button at all.

Proposed default (if no objection): extend `buildDiagnosticActions` to ALSO show an action for `source=hub` errors, labelled **"Reconnect & retry"** when we have `lastUserText` (or **"Reconnect"** with no payload when we don't). Action: identical to Retry — call `startTurn` with `lastUserText`. The hub already does the spawn-fresh-daemon work transparently; the UI just needs to expose the button.

Questions for jesse:
1. **Single button or two?** Keep one button per diagnostic (Retry for provider, Reconnect & retry for hub) — or always show both when applicable?
2. **Discard option?** If Reconnect fails too (daemon won't come up), the user is currently stuck on a dead session with no graceful exit. Should we add a "Discard session" button after N failed reconnects?
3. **Label sensitivity:** Is "Reconnect & retry" clear enough, or would something more explicit like "Spawn fresh daemon & retry" land better?
4. **New appwire method?** Today everything funnels through `startTurn`. The kata speculates a new method may be needed — but as far as I can tell, the existing path is sufficient. Confirm we don't need a dedicated `thread/recover` or similar?
