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

Questions for jesse — **Answered 2026-05-17:**
1. **Single button or two?** → **Single button, label depends on source.** Provider error → "Retry turn"; hub error → "Reconnect & retry". Each diagnostic offers one action.
2. **Discard option?** → **No discard button.** User navigates away manually when reconnect can't recover.
3. **Label sensitivity:** → **"Reconnect & retry".**
4. **New appwire method?** → **Investigate first.** Trace startTurn under daemon-death scenarios and report whether reuse is safe before implementing a dedicated `thread/recover` method.

Implementation landed in `aecf225` (xcas), `d02d386` (ws5f), `d2b5102` (t65c), `b46d0de` (e465 UI button).

### Legacy `/send` fallback in Reconnect button (kata 05vb)
The Reconnect & retry button has two paths: `window.SerfAppwire.startTurn` (works — hub auto-resumes) and `fetch /s/<id>/send` (does NOT trigger hub auto-resume, so for source=hub errors it re-issues against a dead daemon and fails the same way).

Options:
- (a) Drop the `/send` legacy fallback entirely from `makeRetryTurnHandler`. Appwire is the default; the fallback path is dead code for any modern client.
- (b) Detect `SerfAppwire` absence and hide the Reconnect button (but keep Retry) when the source is hub. Lets the legacy path survive for provider errors where auto-resume isn't strictly needed.

**Answered 2026-05-17**: Option (a) — dropped `/send` fallback. Fix landed in kata 05vb commit `62d0ff9`: `makeRetryTurnHandler` now unconditionally calls `SerfAppwire.startTurn`; if SerfAppwire is undefined at click time, renders a clear "reconnect failed: appwire unavailable" banner. No silent failure.

### meta.json `turn_count` drifts behind transcript reality (kata 3tgv)
Session `01KRSCTTEY3176G22YNWQW847Z` (2026-05-16) shows `turn_count: 0` in meta.json even though the transcript records two USER_INPUTs (both followed by failed api_calls). `agent.maybeAutoSave` only fires from happy-path turn boundaries; if the LLM call errors out before the assistant turn is appended, `turn_count` never advances. The past-index hubapi surface backfills from meta.TurnCount when the live count is 0, so the UI ends up showing 0 turns for sessions that have user input but no committed assistant exchange — same condition as kata k5t4 but on the persistence side, not the projection side.

Two paths to consider:
- (a) Bump `turn_count` from `s.turns` (post-increment in session.go line 1575) so a USER_INPUT alone counts. Aligns with the live-view interpretation but conflicts with k5t4's "completed exchanges only" answer.
- (b) Define turn_count consistently as "completed exchanges" and ensure meta.json is flushed in the error exit paths so the count never lags transcript reality. Requires a SaveSessionMeta call alongside the SessionIdle flip in session.go.

**Answered 2026-05-17**: Option (b) — same definition as k5t4. Fix landed in kata 3tgv: `agent/session.go` error-exit path now calls `maybeAutoSave` alongside the `SessionIdle` flip from r6y9. Sibling error-exit paths (empty-response-exhausted, bare-text-exhausted, MaxToolRoundsPerInput, ctx.Done, panic recovery) still don't flush — filed as adjacent katas below.

### Other error-exit paths that skip meta.json flush (adjacent to 3tgv)
While fixing kata 3tgv, found that the LLM-error path is not the only error exit that skips `maybeAutoSave`. Same-shape issue in:
- `emptyResponseExhaustedError` exit (`session.go:1966` area) — empty-response retries increment `modelResponses` then return without flushing.
- `bareTextWithoutResultToolError` exit (`session.go:1996` area) — same.
- `MaxToolRoundsPerInput` exit (end of for-loop, ~line 2229) — runs through tool rounds and updates `modelResponses` but `maybeAutoSave` after the loop is missing.
- `ctx.Done()` exit (`session.go:1607`) — Ctrl-C / cancellation returns before any persistence.
- Panic recovery — there is no top-level recover in `processOneInput`, so a panic would skip flush entirely.

None of these are blockers; filed for future cleanup once a single deferred-flush pattern is agreed.

### communicate inbox doesn't show image content from image-bearing steers (kata gnmv)
After t5j6 + re91 + 65mm, the agent loop persists image-bearing steers correctly: model sees the bytes via TurnSteering, transcript records a ContentImage part. BUT the communicate tool's `inbox` payload that surfaces to the AGENT shows only the text portion — not even an `[image attached]` hint.

Two interpretations:
- (a) **Intentional**: inbox is the "what new user messages did I miss" pane, and image content can't be summarized as text. Skipping it keeps the pane terse.
- (b) **Papercut**: agent should know an image was attached, even if it has to look at transcript to see it. A minimal `[1 image attached: photo.png]` hint would be enough.

Refs: agent/session.go after t5j6 (3b9ae14).
