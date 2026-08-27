# Vision Model Configuration — Design

Date: 2026-08-27
Status: Approved
Branch: `main`

## Summary

Evener describes every image a tool returns through a vision side-channel: a
separate, tool-free completion that asks the session's own model to describe the
image, then injects the result as steering. This call fires even when the
session model is vision-capable and already receives the same bytes inline in
the tool result.

This design makes the side-channel configurable through one per-session
setting, `vision_model`, with three states:

| Setting | Behavior |
|---|---|
| unset (default) | Side-channel on, using the session's active model — today's behavior |
| `off` | Side-channel skipped; the model still receives image bytes inline in the tool result; no vision steering is emitted |
| `provider/model` or bare `model` | Side-channel on, routed to that model; cross-provider allowed; provider refusal falls back to the session model |

The setting is a launch-time CLI flag (`--vision-model`), a persisted session
config field, a runtime mutation (`thread/vision-model/set`), and a picker in
the hub web UI whose entries are **Current model**, **Off**, and the
vision-capable models from the catalog. Picking the current model reproduces
today's same-model behavior, so one control is the whole user surface.

## Goals

- Let a user disable the image-description side-channel for a session.
- Let a user route the side-channel to a specific model, including a model on
  a different registered provider.
- Preserve current behavior when the setting is unset.
- Persist the setting so resumes and spawned children inherit it.
- Allow mid-session changes from the hub web UI without restarting the session.
- Reuse the existing cheap-model routing machinery (refusal learning, fallback
  to the session model) rather than building a parallel path.

## Non-goals

- An `auto` mode that infers the side-channel from catalog vision capability.
  The setting is explicit; capability metadata only filters picker entries.
- A spawn-pane field in the launch UI. The CLI flag covers launch-time
  selection; the picker covers running sessions.
- An `evener-tui` command. Per the product decision, `--vision-model=` is the
  entire terminal surface.
- A `hubapi` Go client method. Only the web frontend consumes the new wire
  method in this wave; the TUI has no command that would need it.
- Validating that a configured vision model advertises `supports_vision`. The
  catalog is incomplete by nature; users may deliberately route to an
  uncatalogued model. Capability is picker metadata, not a gate.
- Changing how image bytes flow inline to the session model in tool results.

## Current behavior

`Session.persistToolResults` (agent/session_tool_round.go) calls
`Session.describeImageCall` (agent/session_tools.go) for every tool result
carrying image data, except in explorer sessions. The side-channel builds a
tool-free request addressed to the session's own provider and model
(`profile.ID()`, `profile.Model()`), with the caller's stated purpose as the
prompt, a fixed low reasoning-effort cap, and a two-minute timeout. The
description arrives as steering ("Image description (from vision)"); timeouts
and provider failures arrive as "vision unavailable" steering.

The image bytes also travel inline to the session model: the Anthropic and
OpenAI Responses adapters embed them in the tool result itself. A
vision-capable session model therefore sees every image twice today — once
inline, once through the forced description.

Auxiliary calls that need a different model (naming, summarization, web fetch)
already route through `agent/internal/cheapmodel.Caller`: the profile carries a
configured cheap ref, the caller validates model compatibility, learns
provider refusals for the session, and falls back to the session model. The
`--fast-cheap-model` CLI flag sets that ref at launch, with cross-provider refs
validated against the client's registered providers.

The mid-session model switch shows the full UI mutation path this design
mirrors: `ModelSwitch.tsx` → `threadsStore.setModel` → appwire
`thread/model/set` → hub `setThreadModelWithResume` (resumes cold sessions) →
daemon `handleAppThreadModelSet` (rejects mid-turn) → `Session.SetModel` →
`thread/model/changed` notification → frontend reducer.

## Product decisions

### One knob, three states

The setting `vision_model` is a single string. Empty means "describe with the
session's active model" and preserves current behavior by default. The bare
word `off` disables the side-channel. Anything else is a model ref:
`provider/model` pins a provider instance, while a bare `model` resolves
against the session's active provider at call time, so it follows `SetModel`
switches the same way the cheap-model ref does.

`off` is reserved only as a bare word. A ref containing a slash parses as
`provider/model` first, so a provider instance literally named `off` stays
reachable as `off/some-model`.

### Disabled means silent

In the `off` state Evener makes no vision call and emits no steering — neither
descriptions nor "vision unavailable" notices. The session model keeps
receiving image bytes inline in tool results, so a vision-capable model loses
nothing but the forced second description.

### Cross-provider with refusal fallback

A configured vision model resolves through the cheap-model caller machinery:
model-compatibility validation, per-session refusal learning, and one fallback
to the session model. If both models refuse, the existing "vision unavailable"
steering fires. At launch and at runtime-set time, a cross-provider ref is
valid only when that provider is registered and credentialed in the client;
an unregistered provider is an immediate error, never a silent fallback.

### Runtime mutation is per-session and immediate

`Session.SetVisionModel` updates the setting under the session mutex, taking
effect on the next image read. The daemon rejects a set while a turn is in
flight (Conflict), matching `thread/model/set`. A successful set persists with
the session meta and propagates to the UI through a change notification.
Changing the session model does not touch `vision_model`: an explicit ref
stays pinned, an unset value follows the new model.

### Wire naming follows the flat-setting convention

Appwire names per-thread settings as flat siblings:
`thread/model/set`/`thread/model/changed`,
`thread/reasoning-effort/set`/`thread/reasoning-effort/changed`. The new pair
is `thread/vision-model/set` and `thread/vision-model/changed`.

## Technical design

### Agent layer

**Configuration.** `agent.SessionConfig` gains
`VisionModel string `json:"vision_model,omitempty"``, carried through
`toSnapshot`/`configFromSnapshot` into `schema.ConfigSnapshot`. Persistence,
resume, and child inheritance ride the existing snapshot plumbing; children
copy the parent's config at spawn and keep the value current at that moment.

**Runtime mutation.** `Session.SetVisionModel(ref string) error` mirrors
`SetModel`: it rejects on a closed session, validates the ref (`off`, bare
model, or `provider/model` with a registered cross-provider), stores it in
`cfg.VisionModel` under `s.mu`, and returns a validation error without
changing state. A new `Session.VisionModel()` getter reads the value under
`s.mu` for the daemon's thread-read payload.

**Side-channel.** `describeImageCall` reads the setting inside its existing
`s.mu` profile-snapshot block:

- `off` → return an empty success result; no call, no steering.
- otherwise → resolve the route (explicit ref, or the session route when
  unset) and execute through `cheapmodel.Caller` instead of a direct
  `client.Complete`, so every non-off state shares refusal learning and
  fallback. Prompt construction, media-part selection, timeout, effort clamp,
  and request metadata stay as they are.

**Routing.** `cheapmodel.Caller` gains an exported routed form,
`CompleteRouted(ctx, profile, provider, model, req)`: the existing `Complete`
and `CompleteConfigured` resolution logic refactors to route through it, and
the vision side-channel calls it with the configured route. The refusal map
and probe flights already key on route, so vision and cheap routes share one
session-scoped caller without interfering.

### CLI

`evener run` and `evener serve` accept `--vision-model`. Launch-time handling
mirrors `applyFastCheapModel`: an empty value leaves the field unset, `off`
stores the sentinel, a bare model stores as-is, and `provider/model` with a
provider different from the active one requires that provider to be registered
in the client, else launch fails with an error.

### Appwire protocol

- `MethodThreadVisionModelSet = "thread/vision-model/set"` with
  `ThreadVisionModelSetParams{Ref string, VisionModel string}`. The string
  carries the full setting — `""` for current-model, `"off"`, or a ref — so
  the wire has one unambiguous field rather than a provider/model split that
  cannot express two of the three states.
- `NotifyThreadVisionModelChanged = "thread/vision-model/changed"` with
  `ThreadVisionModelChangedParams{ThreadID, Ref, VisionModel string}`.
- `ThreadCapabilities` gains `ChangeVisionModel bool `json:"changeVisionModel"``.
- The thread payload gains `VisionModel string `json:"visionModel"`` beside
  the model fields, so a client reads the current setting without a separate
  round trip.

### Server (daemon)

The daemon registers `handleAppThreadVisionModelSet` mirroring the model-set
handler: it answers Conflict while a turn is processing or reserved, delegates
to a new `SetVisionModelFunc` hook wired to `Session.SetVisionModel`, surfaces
hook errors as wire errors, and emits `thread/vision-model/changed` after a
successful set. Thread reads populate `visionModel` from the session getter
and advertise `changeVisionModel` alongside `changeModel`.

### Hub

`app_rpc.go` registers the method and forwards to a new
`setThreadVisionModelWithResume` (in an `app_vision_model.go` mirroring
`app_model.go`): attempt the set, and when the session is unavailable but the
ref is known, resume the thread and retry once. Hub thread-read construction
carries `visionModel` and `changeVisionModel` for live threads from the daemon
payload and for cold, exited threads from the persisted session meta, matching
how the model fields reach `pastEntryThread` today.

### Frontend (hub web UI)

- `protocol/model.ts`: `ThreadModel` gains `visionModel: string` and the
  capability flag.
- `stores/threads.ts`: a `setVisionModel(ref, visionModel)` action as a
  fire-and-report mutation like `setModel`, plus a reducer case for
  `thread/vision-model/changed` that updates the stored value.
- A new `VisionModelSwitch.tsx` in `panes/session/chrome/`, rendered beside
  `ModelSwitch` and reusing `ModelSwitchTrigger` and the
  `listModels()` + `/api/models` catalog plumbing. Entries: **Current model**
  (sends `""`), **Off** (sends `"off"`), and catalog entries where
  `supports_vision === true` (send `provider/model`). The trigger labels the
  current state: the active model's label for unset, "Off" for `off`, and the
  ref for an explicit model. The control disables when
  `changeVisionModel` is false; mid-turn Conflicts surface as toasts, same as
  the model switch.
- Touched files pass `npx biome check --write` before the frontend gates.

## Error handling

- **Launch with an unregistered cross-provider ref**: fail launch with a
  descriptive error (same as `--fast-cheap-model`).
- **Runtime set to an invalid or unregistered ref**: the set call fails; the
  session keeps its previous value; the UI shows a toast.
- **Vision provider refuses the configured model**: the refusal is learned for
  the session and the call falls back to the session model; if both refuse,
  the existing "vision unavailable" steering fires.
- **Configured model cannot accept images**: the provider error follows the
  ordinary provider-failure path — warning event plus "vision unavailable"
  steering. Evener does not pre-flight capability checks.
- **Set during a turn**: the daemon answers Conflict; the UI surfaces it.

## Testing

All gates run per AGENTS.md: `make lint`, `make vet`, `make test`, plus
`make test-web` and `make test-web-browser` for the frontend.

- **agent**: with a scripted adapter — `off` makes no call and emits no
  steering while the tool result keeps its image bytes; a configured ref
  reaches the adapter with the vision provider/model; a refusal falls back to
  the session model; a double refusal yields "vision unavailable" steering;
  `SetVisionModel` validation and state transitions; `VisionModel()` getter;
  ConfigSnapshot round-trip (the mirror converter test guards the new field);
  child spawn inherits the value; resume restores it.
- **cheapmodel**: `CompleteRouted` resolution, refusal learning per route, and
  fallback, proving the cheap and vision routes share one caller without
  cross-contaminating refusal state.
- **appwire**: params/notification decode coverage and golden updates for the
  new method and notification.
- **server**: handler tests mirroring `model_set_test.go` — mid-turn Conflict,
  reserved-turn Conflict, hook error surfaces, success path emits the changed
  notification.
- **hub**: set-with-resume retries once after resume; unregistered or unknown
  refs error; thread read carries `visionModel` and `changeVisionModel`.
- **frontend**: store action and reducer tests; `VisionModelSwitch` renders
  the three entry kinds, filters by `supports_vision`, sends the right wire
  values, and disables on capability.
- **cmd/evener**: flag parsing; cross-provider launch validation accepted and
  rejected cases.

## Rollout and compatibility

Default behavior is unchanged: an unset `vision_model` describes with the
session model exactly as today. Persisted sessions without the field load as
unset. The new wire method and notification are additive; older frontends
ignore the capability and never render the picker.
