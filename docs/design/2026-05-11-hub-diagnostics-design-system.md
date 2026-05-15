# Hub Diagnostics Design System

Serf Hub diagnostics are inline transcript elements for failures or warnings that affect a session. They are not generic log lines. A diagnostic must say where the problem came from, what happened, and where the operator should look next.

## Sources

Use exactly one source label per diagnostic.

- `Provider`: the upstream model provider rejected or failed a request. Examples: HTTP 401 from OpenAI, rate limits, quota, provider outage, invalid API key.
- `Serf`: the Serf daemon, session runtime, model selection, transcript, or local configuration failed. Example: `configuration error: unknown provider: openrouter`.
- `Hub`: the hub process, AppWire connection, spawn/resume flow, rendezvous state, or hub proxy failed.
- `UI`: the browser-side app failed before or after a hub request. Examples: attachment read failure, clipboard/browser API failure, renderer failure.

`unknown provider` is a Serf configuration error, not a provider runtime error. In the current hub flow it usually means Hub launched Serf with a provider/model shape or Serf binary that Serf does not recognize, so the recovery hint should point at Hub launch configuration.

## Component

Diagnostics render as compact inline blocks in the conversation stream:

- source badge: `Provider error`, `Serf warning`, `Hub error`, `UI error`
- title: short noun phrase, for example `Serf configuration error`
- message: cleaned raw message with transport prefixes removed
- hint: one sentence naming the likely owner or next inspection point

The component is intentionally not a modal or toast. The failure is part of session history and should remain visible on refresh/replay.

## Visual Rules

- Use a left rail and small badge color to identify source.
- Keep the background quiet and aligned with existing transcript annotation styling.
- Do not render raw `[error]` or JSON payloads as user-facing text.
- Do not color all diagnostic text red. Red is reserved for source/severity accents and badges.
- Keep radius at 6px or less and avoid nested cards.
- On mobile, diagnostics use the full conversation width.

## Copy Rules

- Prefer specific source text over generic `error`.
- Preserve the useful raw message, but remove protocol noise.
- If classification is inferred, choose the layer where the operator can take action.
- Use `Provider` only for provider API/runtime failures, not for Serf rejecting a configured provider name.
- Hints should be actionable and short:
  - Provider: check credentials, account access, rate limits, and selected model.
  - Serf config: check Hub launch provider/model and the Serf binary Hub is using.
  - Hub: check hub process, AppWire connection, spawn arguments, and rendezvous state.
  - UI: check the browser console and refresh stale local UI state.

## Implementation

- Browser taxonomy and rendering live in `cmd/serf-hub/assets/diagnostics.js`.
- Transcript rendering calls the reusable component from `cmd/serf-hub/assets/renderer.js`.
- Backend classification for appwire/replay payloads lives in `internal/diagnostic`.
- Failed appwire turns can carry `error.source`, `error.title`, and `error.hint`.
- SSE warning/error payloads should include `source`, `title`, and `hint` when available.
