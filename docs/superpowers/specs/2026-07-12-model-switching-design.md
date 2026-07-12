# Mid-Session Model Switching (Web + TUI) — Completion Design

Date: 2026-07-12
Status: Draft (rev 2 — two-reviewer adversarial pass applied: 18 majors/minors
merged from both reviewers; all normative mechanisms now verified to exist)
Ticket: PRI-2574
Branch: `model-switching` (worktree off main @ f4ec5267)

## Summary

Serf already contains a skeleton of mid-session model switching: the engine
setter (`Session.SetModel`), the wire method (`thread/model/set`), a hub relay
with a capability gate, a TUI `/model` command with a picker, a web
command-palette entry, synchronous persistence, and a per-delegate `model`
override. The skeleton is real but unfinished, and the unfinished parts are
exactly the ones a user touches: nothing announces a completed switch (chips go
stale on every other client — and on reload, because the server's cached
session info only refreshes on `EventSessionStart`), the web header's model
chip renders as a button with no click handler, effort is invisible on the
wire, a switch requested mid-turn applies between tool rounds (so a
cross-provider swap can land mid-tool-loop), and none of it has test coverage.

Worse: the operation lies. `Session.SetModel` swallows resolver errors
(`agent/session.go:659-662`) and returns nothing, and both `POST /model` and
`thread/model/set` report success unconditionally — switching to a nonsense
ref "succeeds" and the session only fails at its next model call.

This spec finishes the feature. Ten deltas:

1. **Validated, acknowledged switching.** `Session.SetModel` returns an
   error; unknown instances and (for enumerable instances) unknown models are
   rejected; a history containing content the target provider cannot carry is
   rejected up front; the RPC propagates all of it.
2. **Change notifications.** New session events and AppWire notifications for
   model and reasoning-effort changes, so every attached client converges
   instantly (`serf/thread/name/changed` is the template).
3. **Snapshot freshness.** A switch updates the daemon's session info
   synchronously; `thread/read` and hub hydration reflect the new model
   immediately. The thread snapshot also gains the current reasoning effort.
4. **Turn-boundary rule.** `thread/model/set` is rejected while a turn is
   processing (the `thread/clear` precedent). Clients disable the control
   during a run.
5. **Cross-model replay provenance rules.** Thinking blocks replay only to
   the exact (provider, model) that produced them; `web_search` raw blocks
   replay only within their producing behavior-tag family; both are dropped
   otherwise — normative, enforced at projection, tested per target tag.
6. **Transcript marker.** A successful switch emits a `systemMessage` divider
   ("Switched model: X → Y") in both UIs via the existing announcement hook.
7. **Web header chip becomes the picker.** Wire `data-model-trigger` to a
   model picker (the palette entry stays); the chip live-updates from the new
   notification.
8. **Effort parity.** A TUI `/effort` command (today effort is web-only), and
   the live web effort picker offers the current model's supported levels
   instead of a hardcoded vocabulary (port the spawn form's
   `reasoning_effort_levels` pattern).
9. **Prompt freshness + delegate echo.** The system prompt's knowledge-cutoff
   line recomputes on switch (today it keeps the launch model's value), and
   `delegateResult` reports the model the child actually ran, mirroring the
   sandbox echo.
10. **Tests.** Unit + JSDOM + TUI coverage, deterministic scenario cards for
    both surfaces, and a live cross-provider switch ladder.

## Current state (verified inventory)

| Layer | What exists | Where |
|---|---|---|
| Engine setter | `Session.SetModel` resolves a new profile (cross-provider aware via injected resolver), swaps it under lock, rebuilds tool defs + system prompt, applies on next request | `agent/session.go:651` |
| Effort setter | `Session.SetReasoningEffort` normalizes and stores | `agent/session.go:584` |
| Persistence | Both setters flush `meta.json` synchronously (`maybeAutoSave`) — crash-safe | `agent/session.go:690,598` |
| Wire method | `thread/model/set` (`ThreadModelSetParams{ref, modelProvider, model}`), scope both | `appwire/protocol.go:96`, `appwire/types.go:707` |
| Daemon handler | `handleAppThreadModelSet` validates non-empty, prefixes provider, calls hook | `server/appwire_runtime.go:382` |
| Daemon hook | `SetModelFunc` → `getSession().SetModel(model)` | `cmd/serf/serve.go:406` |
| Hub relay | `MethodThreadModelSet` → capability gate (`ensureThreadActionAvailable(..., "model")`) → `source.SetThreadModel` | `cmd/serf-hub/app_rpc.go:525` |
| Capability | `ThreadCapabilities.ChangeModel` = `modelFunc != nil && !closed`; surfaced to web + TUI | `server/appwire_runtime.go:639`, `appwire/types.go:347` |
| TUI command | `/model` — no args opens picker (`fetchHubSessionModels` → `model/list`), arg form sends directly | `cmd/serf-tui/hub_command_registry.go:303-329` |
| TUI picker | `tuipick.ModelPicker`, already used for spawn/live/transcript-target | `cmd/serf-tui/internal/tuipick/model_picker.go:27` |
| Web switch | Command palette "Switch model" → `fetchModels()` (`model/list`) → `SerfAppwire.setModel` | `cmd/serf-hub/assets/search.js:337` |
| Web chip | Header `.composer-model` renders `button[data-model-trigger]` when `ChangeModel` | `cmd/serf-hub/templates/partials/workspace.html:71-78` |
| Model catalog on the wire | `model/list` (scope both) returns `ModelDescriptor`s; spawn path also carries `reasoning_effort_levels` | `cmd/serf-hub/web_spawn.go:382` |
| Delegate override | `delegate` tool `model` param, "default: parent model"; child inherits parent's *current* profile at spawn | `agent/internal/tool/definitions.go:128`, `agent/subagents.go:361-383` |
| Delegate restore | Child re-resolves its own persisted `ResolvedProfileID`/`ResolvedModel`, not the parent's current | `agent/job_delegate.go:806-844`, `agent/internal/jobstore/record.go:76-78` |
| Resume | `ResolveResumeModelRef` (persisted meta beats `SERF_MODEL`); `RestoreSessionFromMetaWithConfig` reattaches the profile resolver | `cmdutil/cmdutil.go:203`, `agent/session_init.go:309` |
| Cross-provider switching seam | `resolveProfileForRef` swaps profiles via resolver, preserves overrides, re-runs provider-conditional tool registration | `agent/session.go:605` (the `:1283` in `docs/llm-providers.md` has drifted) |

## Defects and gaps this spec closes

| # | Defect | Evidence |
|---|---|---|
| G1 | No `thread/model/changed` or effort-changed notification; the only setting push is `serf/thread/name/changed` | `appwire/types.go:82`; grep of notification catalog |
| G2 | Server session info (`status.Model`, source of `Thread.ModelProvider`) updates only on `EventSessionStart`; `SetModelFunc` never refreshes it → stale hydration for reloading clients | `server/bridge.go:28-30`, `server/appwire_runtime.go:558`, `cmd/serf/serve.go:406` |
| G3 | Web header model chip (`data-model-trigger`) has no JS handler — a dead affordance | `workspace.html:74`; no handler in `cmd/serf-hub/assets/` |
| G4 | Reasoning effort absent from the thread snapshot — clients cannot display the live session's effort | `server/appwire_runtime.go:545-565` |
| G5 | A switch requested mid-turn applies at the next tool round (`prepareModelRequest` re-reads the profile each round), so a cross-provider swap can land mid-tool-loop | `agent/session_model_call.go:168-191` |
| G6 | No transcript marker for a switch | `internal/appprojector/appwire_projection.go:852` (unused for this) |
| G7 | No TUI `/effort` command (effort is settable from the web palette only) | `cmd/serf-tui/hub_command_registry.go` |
| G8 | Live web effort picker hardcodes the full vocabulary instead of the model's levels (spawn form already does per-model) | `cmd/serf-hub/assets/search.js:361-369` vs `spawn.js:1609` |
| G9 | `delegateResult` echoes the enforced sandbox but not the resolved model | `agent/job_delegate.go:111-149` |
| G10 | Zero scenario/E2E coverage of mid-session switching | `test/scenarios/INDEX.md` |
| G11 | `SetModel` swallows resolver errors and returns void; `POST /model` and `thread/model/set` report success unconditionally; an unknown bare model builds a default-shaped profile that fails only at the next API call | `agent/session.go:659-662`, `server/server_handlers.go:215-217`, `server/appwire_runtime.go:396-398` |
| G12 | Anthropic-family builder replays cryptographic thinking signatures across anthropic→anthropic *model* changes (only openai-compat signatures are stripped) — unverified whether model B accepts model A's signed blocks | `llm/providers/anthropic/request.go:476-495` |
| G13 | `web_search` raw blocks replay verbatim into whichever builder runs next — Anthropic `server_tool_use` JSON lands untranslated in an OpenAI Responses `input` array and vice versa | `anthropic/request.go:504-511`, `openai/responses.go:976-983` |
| G14 | A history containing a document (or audio) hard-errors the Anthropic-family builder — switching into anthropic with a document in history bricks every subsequent request | `anthropic/request.go:512-513` |
| G15 | `envInfo.KnowledgeCutoff` is computed once at init and rendered into the system prompt; `SetModel` never recomputes it | `agent/session_init.go:652`, `agent/session_prompts.go:73` |

## Goals

- A user on either surface can see the session's current model and effort,
  change both between turns, and trust what they see: every attached client
  reflects a change within one notification round-trip, and reload/resume
  agree.
- A switch is visible in the transcript history afterward.
- Cross-provider (cross-instance) switches work at turn boundaries with the
  same fidelity as launch-time selection: prompt sections, tool surface, and
  effort clamping all re-key to the new profile.
- Every behavior above is covered by deterministic tests; the cross-provider
  path additionally by a live-gated ladder.

## Non-goals

- **No allowed-models restriction.** No such concept exists anywhere in serf
  today (verified); inventing one is YAGNI.
- **No agent-initiated self-switch tool.** User surfaces only.
- **No deferred "switch after this turn" queue.** Mid-turn requests are
  rejected, not queued. A pending-switch queue needs persisted crash-safe
  state (the compaction `pendingInstructions` template is in-memory and
  lossy); future work if the rejection UX proves annoying.
- **No change to delegate inheritance semantics.** Running children keep
  their captured profile; post-switch children inherit the new model; explicit
  `model`/plugin-agent overrides pin. This spec documents and tests that
  behavior; it does not alter it.
- **No per-message model badges in the transcript body.** The `systemMessage`
  divider carries attribution at switch points. (Per-turn `ResponseModel`
  already persists on disk — `agent/schema/turn.go:45` — surfacing it in
  hover metadata is a possible follow-up, not this spec.)
- **Codex sources stay unsupported.** `codex_source.go` returns `Unavailable`
  for `SetThreadModel`; unchanged.
- **No `providers.toml` hot-reload.** The frozen-registry limitation is
  documented and produces a clear error (decisions table); reloading adapters
  in a live daemon is its own project.
- **No document/audio support added to provider builders.** The N1 preflight
  refuses switches that would hit the Anthropic builder's hard error; teaching
  that builder documents is a separate follow-up (worth a ticket).
- **No translation of foreign `web_search` blocks.** Cross-tag they drop;
  converting them to text summaries is possible future work.

## Design decisions

| Decision | Choice | Rationale |
|---|---|---|
| What is pickable | Whatever `model/list` returns for the session (grouped by instance), plus a freeform `/model <ref>` arg | Same catalog the spawn picker trusts; server-side resolver already rejects unknown refs with a names-listing error |
| Cross-provider switches | Allowed, turn-boundary only | `resolveProfileForRef` (`agent/session.go:605`) + `reapplyProviderSpecificTools` + per-request projection already handle the swap; the boundary rule removes the mid-loop hazard for user switches |
| Mid-turn request | Reject with a structured error while the daemon is processing **or** holds a reserved turn (`processing \|\| appReservedTurnID != ""`, the same compound `appCapabilities` treats as active at `server/appwire_runtime.go:629`); the error names the active turn id when one exists. Clients disable the control while `Status.Type == "active"` / a non-empty `ActiveTurnID` — the signal `workspace.html:84-87` already uses. **Not** `ActiveFlags`: serf daemons never populate it (only the codex mapping does, `cmd/serf-hub/internal/appsource/codex_mapping.go:12-20`) | Matches `thread/clear` ("rejected while a turn is processing", `appwire_runtime.go:365-371`) but capability-consistent; simplest crash-safe semantics. Note: with queued input there is no reachable boundary until the queue drains — see Failure modes |
| Internal switches (fallbacks) | Untouched | `model_fallbacks` are same-behavior-tag by `validateModelFallbacks` (`agent/session_init.go:775-805`) and may continue swapping the request model between rounds inside `callModelWithFallback` (`agent/session_model_call.go:797-858`). N4's provenance rule explicitly exempts them |
| Model-fallbacks after a switch | Every successful switch re-runs fallback validation against the new profile; entries that no longer validate (cross-tag after the switch, or unresolvable) are dropped and named in the switch marker's warning line | The launch-time guarantee (`validateModelFallbacks` ran once against the launch profile) silently breaks after a cross-provider switch — a stale entry would trigger the exact mid-loop cross-provider swap N1 forbids |
| Ref grammar (slashed model ids) | When `ThreadModelSetParams.modelProvider` is non-empty the daemon joins `modelProvider + "/" + model` **unconditionally**; the model half may itself contain slashes (openrouter vendor-prefixed ids). Clients keep splitting picker items on the first slash (instance names cannot contain `/`) | Today the daemon skips the join when the model already contains a slash (`server/appwire_runtime.go:387-388`), so picking `openrouter/anthropic/claude-x` resolves `anthropic/claude-x` — routing to the anthropic instance and silently discarding openrouter |
| Effort across a switch | The requested level persists; per-call clamping to the new model's ladder already happens at request build (`agent/session_model_call.go:750`); UIs display the model's supported levels | Zero new clamp machinery; the user's intent survives round-trips through weaker models |
| Change announcement | New session events → projector → new notifications `thread/model/changed`, `thread/reasoning-effort/changed`; plus synchronous `UpdateSessionInfo` so hydration agrees | `serf/thread/name/changed` is the proven template (`appwire_projection.go:652`); hydration freshness fixes G2 for clients that missed the push |
| Transcript marker | `systemMessage` item via `systemAnnouncementWithRaw` on successful model switch (not for effort) | Both renderers already display `systemMessage`; effort changes are low-stakes and chip-visible — a divider per effort tweak is noise |
| Snapshot surface | Thread snapshot gains `reasoningEffort`; `ModelProvider` stays the model field and becomes switch-fresh | Smallest wire addition that lets clients render both settings without a side channel |
| Web picker | Reuse the existing picker pattern (`settings-pickers.js` / `spawn.js`) wired to `data-model-trigger`; palette entry retained | The chip is where users look; the dead button is worse than no button |
| TUI `/effort` | New command mirroring `/model`: bare form opens a level picker for the current model, arg form sends directly | Surface parity; reuses `tuipick` primitives and the existing `thread/reasoning-effort/set` method |
| Delegate echo | `delegateResult` gains the resolved `provider/model` (mirror of the sandbox echo) | Parent-side verifiability; the restore descriptor already persists the same fields |
| Context-window shrink | Switch succeeds; when estimated usage exceeds the new profile's compaction threshold the switch marker carries a warning line; existing context manager handles the rest next turn | `contextmgr` reads the window from the *current* profile per estimate (`SetModel` already calls `contextMgr.SetProfile`), so budgets self-correct; blocking the switch would strand users on expensive models |
| Model validation | Reject unknown instances (resolver error). Model membership reuses the launch policy verbatim — live `client.ListModels` with the launchcheck timeout plus catalog visibility (`cmd/serf/internal/launchcheck/launchcheck.go:217`), with the behavior-tag unreported-models allowance (`cmd/serf-hub/app_models.go:143`) — with one amendment: an enumeration failure of **any** error class fails open (accept) | Today an unknown bare model "succeeds" and fails one turn later with an opaque provider error (G11). Launchcheck fails open only for an error-message allowlist; that would make dead credentials block a switch, contradicting the failure-modes row — so the switch path fails open unconditionally on enumeration errors |
| Thinking replay provenance | A `thinking`/`redacted_thinking` block from a **completed prior turn** replays only when the outgoing request's `(req.Provider, req.Model)` — instance id + requested model — matches the producing turn's `(ResponseProvider, ResponseRequestModel)` (`agent/schema/turn.go:44-46`; fall back to catalog-canonicalized `ResponseModel` when `ResponseRequestModel` is empty). **Empty provenance (legacy transcripts) is replay-eligible.** Turns of the in-flight turn are exempt (fallback rounds), as are requests built by the fallback path itself. Per-tag scope: anthropic-family enforces exact-model; google enforces same-provider only (its builder must replay thought signatures for prior tool calls); Responses and compat targets already provider-scope thinking via their existing guards — the rule adds tests there, not behavior | The G12 risk is anthropic-family signature acceptance across models. Matching on `ResponseModel` (the provider-reported dated id) against the requested alias would strip *same-model* thinking every turn; matching in requested-model space avoids that. The intra-turn/fallback exemption keeps the "fallbacks untouched" decision true and avoids stripping the thinking Anthropic requires mid-tool-loop (`anthropic/request.go:476-483`) |
| web_search replay provenance | Raw blocks replay verbatim only within the producing behavior-tag family; cross-tag they are dropped | The raw payload is foreign JSON to any other wire protocol (G13); translation is possible future work, silence is not |
| Unrepresentable history | `SetModel` preflight-scans canonical history content kinds against an **explicit per-tag policy table** (not derived from builder errors): `document` and `audio` are unrepresentable for anthropic-family AND google targets (hard errors: `anthropic/request.go:512-513`, `google/request.go:280-281,328-329`) and for openai-compat targets (the compat builder silently **drops** them — `openaicompat/request.go:299-329` has no cases — and silently dropping user content counts as unrepresentable by policy); `audio` is unrepresentable for openai Responses (`responses.go:913-938`; documents are carried). Rejection names the kinds and the target | Silently dropping user content is worse than refusing; today the switch "succeeds" and every subsequent request errors (G14) — or silently loses content on compat. Real fix (document support in the builders) is a separate follow-up |
| Error propagation | `Session.SetModel` returns `error`; the daemon handler maps it to a structured AppWire error; no partial application on any rejection path | `ResolveProfileFromConfig` already produces good errors listing configured instances; they are currently thrown away (G11) |
| Knowledge cutoff | `SetModel` recomputes `envInfo.KnowledgeCutoff` alongside the existing prompt-cache refresh | The prompt otherwise claims the launch model's cutoff forever (G15) |
| Stale-registry limitation | Documented, not fixed: the daemon's adapter registry and resolver closure are frozen at process start, so an instance added to `providers.toml` after launch is unreachable until restart. The picker may legitimately offer it (hub enumerates fresh); the switch then fails with the resolver's instance-listing error. Related: the daemon's own `model/list` closure is launch-pinned (`cmd/serf/serve.go:409` binds `profile.ID()` at startup), so daemon-direct listings label models with the launch instance after a switch — documented; pickers use the hub's listing | Hot-reloading `providers.toml` is its own project; a clear error is the honest v1 |
| Effort control gating | TUI `/effort` and the web effort control gate on `ChangeModel` (documented reuse — there is no effort capability and the hub explicitly says so, `cmd/serf-hub/app_rpc.go:550-552`); no new wire capability is added | `ChangeModel` is `modelFunc set && !closed` — a session-liveness proxy with the right lifecycle; inventing `ChangeEffort` is YAGNI |
| Current-setting visibility | Both surfaces render the current model **and** current effort from the snapshot: web shows an effort chip beside the header model chip; the TUI adds an `effort` part beside the `model` part in the session header (`hub_session_view.go:53`) | The Goals promise visibility of both; today effort renders nowhere on a live session (G4) |
| Marker persistence | The switch marker is a **persisted turn**: a new `schema.Turn` kind (named consistently with `TurnCheckpoint`/`TurnSummary`), appended by the `SetModel` success path, projected to a `systemMessage` item by BOTH the live projector and `apptranscript.ProjectTurn`, and **excluded from `expandHistory`** (never sent to the model) | `systemAnnouncementWithRaw` alone emits a live notification backed by a bounded in-memory ring (`internal/appserver/notifier.go:15-44`); `thread/read` prefers transcript-file turns (`server/appwire_turns.go:13-24`) and the replay projection synthesizes `systemMessage` only from persisted kinds (`apptranscript.go:216-230`) — the 2026-06-26 dual-projection lesson, again |

## Normative behavior

### N1. Validated, turn-boundary application

`thread/model/set` against a session whose status shows an active turn MUST be
rejected with a structured AppWire error (message names the active turn; the
web and TUI map it to a friendly notice). When no turn is active the switch
validates (instance known; model known where enumerable; history representable
— see the decisions table), then applies immediately and fully: profile swap,
tool-def and system-prompt rebuild (including knowledge cutoff),
provider-conditional tool re-registration, `contextMgr.SetProfile`,
synchronous `meta.json` flush. The next `turn/start` runs entirely on the new
model. Any rejection leaves the session byte-identical.

`Session.SetModel` gains an `error` return carrying every validation and
resolution failure; the daemon handler propagates it. The setter's sole
production caller today is the daemon hook (`cmd/serf/serve.go:406` — the
fallback path and delegate spawn use `resolveProfileForRef` directly), but the
turn-active gate still lives in the daemon RPC layer: the setter stays
boundary-agnostic and reusable, and interaction policy belongs at the protocol
boundary. A successful switch also re-validates `cfg.ModelFallbacks` against
the new profile and drops entries that no longer validate, naming them in the
marker's warning line (decisions table).

### N2. Convergence

After a successful switch every subscribed client MUST learn the new model
without re-reading the thread: the daemon emits `thread/model/changed`
(payload: `threadId`, `ref`, `modelProvider`, `model`,
`reasoningEffortLevels`, `supportsReasoning` — so pickers re-key without a
`model/list` round trip). The daemon ALSO updates its cached session info
synchronously, so a client that missed the notification (reconnect, cold
load) hydrates the new model from `thread/read`. The same pair of guarantees
applies to `thread/reasoning-effort/set` via `thread/reasoning-effort/changed`.

Hub-side, the relay forwards daemon notifications to subscribed clients as-is
(verified generic broadcast, `cmd/serf-hub/app_rpc.go:225`). Index/list
surfaces converge too: the web sidebar's resync whitelist
(`cmd/serf-hub/assets/sidebar.js:914 QUALIFYING`) gains
`thread/model/changed`, and the TUI applies the notification to its session
detail and dashboard row (Model column, `hub_dashboard_view.go:348`). Resume
agreement comes from persisted meta (`detail.Model` reads it —
`cmd/serf-hub/web_api_tree.go:765`).

### N3. Persistence and resume

A switch survives: (a) daemon crash immediately after the RPC returns
(synchronous meta flush), (b) clean shutdown + hub resume (resume passes
persisted `ProfileID`/`Model`; `SERF_MODEL` must not override — already the
contract per `ResolveResumeModelRef`), (c) CLI resume. Acceptance: a session
switched from A to B, killed, and resumed starts its next turn on B with B's
context window and effort ladder.

### N4. Cross-model transcript replay (provenance matrix)

History is canonical (`[]schema.Turn` → `llm.Message`/`ContentPart`) and
re-projected per request by the target adapter, so a switch needs no
translation pass — but three content kinds carry provider- or model-scoped
state. Normative rules, enforced during history expansion/projection and
tested per destination behavior tag:

| Content kind | Rule after a switch |
|---|---|
| `thinking` / `redacted_thinking` | From **completed prior turns** only: replay when the outgoing `(req.Provider, req.Model)` (instance id, requested model) matches the producing turn's `(ResponseProvider, ResponseRequestModel)` — falling back to catalog-canonicalized `ResponseModel` when the request-model field is empty, and treating **empty provenance as replay-eligible** (legacy transcripts keep today's behavior). In-flight-turn rounds and fallback-built requests are exempt (fallbacks keep today's semantics). Tag scope: anthropic-family enforces exact-model (closes G12); google enforces same-provider only (its builder must replay thought signatures for prior tool calls, `google/request.go:~324-326`); Responses and compat keep their existing guards (`responses.go:951-966`, `reasoningReplayField`) — the rule pins them with tests |
| `web_search` raw blocks | Replay verbatim only when the target behavior tag matches the producing family (anthropic-family ↔ anthropic-family, openai ↔ openai); otherwise drop (G13). Compat already drops them |
| `tool_call` / `tool_result` | Replay verbatim with ids untouched (all three builders already do); no provenance restriction — ids are opaque strings to every wire format |
| `text`, `image` | Replay everywhere (existing behavior) |
| `document`, `audio` | No replay rule change; instead the N1 preflight rejects a switch whose target builder hard-errors on a kind present in history (G14) |
| OpenAI Responses continuation | Nothing to do: the continuation anchor is reused only when the stored request fingerprint matches, and the fingerprint hashes the model (`openai/responses_continuation_fingerprint.go:32-42`), so any model change falls back to full-history replay. Pin with a test |

Dropped content stays in the stored transcript untouched — the rules govern
outgoing request projection only, so switching back restores full replay.

### N5. Transcript marker

A successful model switch appends a **persisted marker turn** — a new
`schema.Turn` kind alongside `TurnCheckpoint`/`TurnSummary` — rendering as a
`systemMessage` item: `Switched model: <old provider/model> → <new
provider/model>`, with warning lines appended when the context-window-shrink
or fallback-drop decisions trigger. Both projection paths render it: the live
projector (new event → `systemMessage` notification) and the replay
projection (`apptranscript.ProjectTurn` case), so it survives reload and
daemon restart. `expandHistory` excludes the marker turn — it is
presentational and never sent to the model. Effort changes do not emit a
marker.

### N6. Surfaces

**TUI.** `/model` (existing) gains: the `(active)` tag actually rendering in
the picker — `activeModel` is already passed (`hub_update.go:451`) but never
matches because item ids are `provider/model` while `detail.Model` is bare
(`model_picker.go:180,190` compare exactly); fix the normalization, not the
plumbing. Plus a rejected-switch notice path (server error rendered), live
header refresh from `thread/model/changed`, and a dashboard-row Model update.
New `/effort` command: bare form opens a picker of the *current model's*
levels (snapshot-first, notification-on-update), arg form validates
client-side against the same list and sends. The session header renders an
`effort` part beside `model`. Both commands gate on `ChangeModel` (decisions
table).

**Web.** The header model chip opens a picker (anchored dropdown, reusing the
settings-picker pattern); the palette entry remains. An effort chip renders
beside it from the snapshot. Both controls live-update from the new
notifications and disable while `Status.Type == "active"` / `ActiveTurnID`
is set. The live effort picker offers the current model's levels
(snapshot-first; spawn-form port for the option rendering). Level-list
semantics follow the spawn pattern (`spawn.js:1608-1633`): `supportsReasoning
=== false` means a KNOWN empty ladder (no options); an absent/unknown ladder
falls back to the full vocabulary — the snapshot/notification payload carries
`supportsReasoning` to make the two distinguishable. A failed model-list
fetch renders an error state in the picker, never a silent empty list.

### N7. Delegates

Unchanged semantics, now tested and echoed: children capture the parent's
profile at spawn (post-switch children inherit the new model), explicit
`model` args and plugin agent models pin, restore re-resolves the child's own
persisted model. `delegateResult` gains `model: <provider/model actually
resolved>` mirroring the sandbox echo, and the delegate docs state the
inheritance rule.

## Protocol changes (AppWire)

Additive only; no changes to existing methods or params.

- New notifications in `appwire/protocol.go` + `types.go` (+ regenerate
  `docs/appwire-protocol.md` via `make generate`):
  - `thread/model/changed` — `{threadId, ref, modelProvider, model,
    reasoningEffortLevels []string, supportsReasoning bool}`
  - `thread/reasoning-effort/changed` — `{threadId, ref, reasoningEffort}`
- New agent-side session events (names per the `agent/events` package
  convention, e.g. `EventModelChanged`, `EventReasoningEffortChanged`)
  emitted by the setters; projector cases in
  `internal/appprojector/appwire_projection.go` mapping them to the
  notifications (template: `EventSessionNameChanged` at `:652`).
- Thread snapshot: `SerfThread` gains `reasoningEffort`,
  `reasoningEffortLevels`, and `supportsReasoning` — cold-attached clients
  must be able to render both settings and populate pickers with no prior
  notification (the appwire `model/list` carries provider+model only,
  `appwire/types.go:852-855`, and the hub strips the rest,
  `app_models.go:202-213`). Populated in `Server.appThread()` from live
  session state; `status.Model` refreshed synchronously on switch (fixes G2).
- New persisted marker turn kind in `agent/schema` + its
  `apptranscript.ProjectTurn` and live-projector cases (N5).
- Clients: `cmd/serf-hub/assets/appwire.js` `eventsFromNotification` maps the
  new notifications; `cmd/serf-tui` `applyHubNotification` likewise.

## Failure modes

| Case | Behavior |
|---|---|
| Switch while a turn is processing (or a turn id is reserved) | AppWire error; no state change; clients show notice and keep control disabled until `turn/completed` |
| Switch while messages are queued | Rejected until the queue drains: the daemon's `processing` covers the whole drain loop (`agent/session_lifecycle.go:432-438`), so no boundary is reachable — documented, tested |
| `model/list` fetch fails in the picker | Picker renders an error state (web today swallows into an empty list, `search.js:245-253`); TUI uses its existing notice path |
| Unknown instance | Resolver error (lists configured instances) relayed verbatim; no state change |
| Unknown model on an enumerable instance | Rejected with the enumeration source named; non-enumerable (OAuth-only) instances accept, matching launch policy |
| Instance added to `providers.toml` after daemon start | Picker may offer it (hub enumerates fresh); daemon rejects with the resolver's instance-listing error + a restart hint |
| History contains content the target builder cannot carry (document/audio) | Switch rejected, error names the kinds and the target; no state change |
| Switch to an instance with dead/missing credentials | Switch succeeds (resolution is credential-free); the next turn surfaces the provider auth error through the existing diagnostics path; user switches back. Documented, tested with a fake-instance unit test |
| History exceeds new model's window | Switch succeeds with warning line in the marker; next turn triggers existing compaction/oversize handling |
| Codex source | `Unavailable` (existing behavior, asserted by test) |
| Concurrent switches from two clients | Last-write-wins under the session lock; both clients converge via notifications |

## Test plan

**Unit (Go).**
- `server`: turn-active rejection; notification emission on both setters;
  snapshot carries model+effort after switch; `UpdateSessionInfo` synchrony.
- `agent`: `SetModel` error propagation (unknown instance, unknown enumerable
  model, unrepresentable history — each rejected, session state unchanged);
  post-switch delegate inheritance (spawn-after-switch uses new profile;
  explicit arg pins); delegate result echo; crash-restore: switch → meta
  flush → `RestoreSessionFromMetaWithConfig` → restored profile is the
  switched model (owned by plan Task 1); fallback re-validation on switch;
  knowledge-cutoff recompute; marker turn excluded from `expandHistory`.
- `llm/providers`: N4 provenance matrix — one test per destination tag per
  content kind (thinking same-model replays / cross-model drops with and
  without signatures; web_search same-tag replays / cross-tag drops;
  tool ids untouched); Responses fingerprint invalidation on model change.
- `cmd/serf-tui`: `/effort` registry entry, picker flow, notification apply.

**JSDOM (`cmd/serf-hub/jstest`).**
- Header chip opens picker; selection calls `thread/model/set`; chip updates
  on `thread/model/changed` without re-read; controls disable during a run;
  effort picker renders per-model levels.

**Scenario cards (`test/scenarios/`, deterministic-first).**
- `web-model-switch-mid-session.md`, `tui-model-switch.md`,
  `model-switch-resume.md` (switch → kill → resume on new model),
  `tui-effort-command.md`.

**Live ladder (SERF_E2E_LIVE=1, patterned on
`reasoning-effort-providers.md`).**
- One card walking anthropic → openai → kimi switches in a single session
  with a tool-using turn after each hop, asserting the turn runs on the
  switched model (response model stamp) and effort clamps to each ladder.

**Acceptance criteria.**
1. Two clients attached; switch from one; the other's chip updates without
   reload.
2. Reload after switch shows the new model (no stale hydration).
3. Switch → crash → resume runs on the switched model.
4. Mid-turn switch attempt is rejected and leaves no state change.
5. Transcript shows the switch marker after reload.
6. TUI `/effort` and web effort picker both show only the current model's
   levels, and both surfaces display the current model AND current effort
   for a cold-attached client (no prior notification).
7. Delegate spawned after a switch reports the new model in its result echo;
   one spawned before keeps the old.
8. Live ladder passes across three behavior tags.
9. Unknown-model and unrepresentable-history switch attempts return
   actionable errors and leave the session byte-identical.
10. After an anthropic→anthropic model switch, prior thinking blocks are
    absent from the next outgoing request (provenance rule observed on the
    wire), and the turn still succeeds.

## Implementation plan

See `docs/superpowers/plans/2026-07-12-model-switching.md` (task-by-task,
TDD, one Opus SDD worker session).
