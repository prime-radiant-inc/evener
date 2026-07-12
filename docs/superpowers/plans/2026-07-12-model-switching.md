# Mid-Session Model Switching — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Finish serf's mid-session model switching per the spec at
`docs/superpowers/specs/2026-07-12-model-switching-design.md` (PRI-2574):
validated + acknowledged switches, change notifications, snapshot freshness,
turn-boundary gating, replay provenance rules, transcript markers, web/TUI
surfaces, effort parity, delegate echo, and full test coverage.

**Architecture:** The engine setter (`agent/session.go:651 SetModel`), wire
method (`thread/model/set`), hub relay, TUI `/model`, and web palette action
already exist. Every task below hardens or completes an existing seam; none
invents a new subsystem. The spec's Design decisions and N1–N7 sections are
normative — when this plan and the spec disagree, the spec wins.

**Tech stack:** Go (agent, server, appwire, llm, cmd/serf-tui), vanilla JS +
JSDOM tests (cmd/serf-hub/assets, jstest), Go `httptest`, scenario cards in
`test/scenarios/`.

## Global constraints

- Read `docs/testing.md` before writing tests. Default tests must be
  deterministic: no live network, credentials, or sleeps as correctness
  proof. Live-gated work is confined to Task 12.
- AppWire changes are additive only. After touching `appwire/protocol.go` or
  wire types, run `make generate` and commit the regenerated
  `docs/appwire-protocol.md`; the catalog cross-check test must pass.
- Read each seam before editing it; the file:line anchors below were verified
  on main @ f4ec5267 and may drift. Never invent field or function names —
  mirror the neighboring code's style.
- TDD every task: failing test → minimal implementation → green → commit with
  the given message. Never skip or disable a pre-commit hook.
- Do not stage or modify the pre-existing untracked files under
  `docs/superpowers/plans/` in the main checkout; work only in this worktree.
- `agent/session.go` and `server/appwire_runtime.go` are shared seams across
  Tasks 1–5: complete tasks in order, one at a time.

## File structure

| Area | Files |
|---|---|
| Engine setter + validation | `agent/session.go`, `agent/session_model_call.go`, `agent/session_prompts.go`, new `agent/session_set_model_test.go` |
| Events + marker persistence | `agent/events/` (new event kinds/payloads), `agent/schema/turn.go` (marker turn kind), `internal/apptranscript/apptranscript.go` |
| Daemon RPC + snapshot | `server/appwire_runtime.go`, `server/server_handlers.go`, `server/server.go`, `server/bridge.go` |
| Protocol | `appwire/types.go`, `appwire/protocol.go`, `internal/appprojector/appwire_projection.go`, `docs/appwire-protocol.md` (generated) |
| Replay provenance | `agent/session_model_call.go` (`expandHistory` signature change), `llm/providers/{anthropic,openai,openaicompat,google}/…` tests |
| Web UI | `cmd/serf-hub/assets/{appwire.js,search.js,model-display.js,settings-pickers.js,sidebar.js}`, new `assets/model-switch.js` (if a new file reads cleaner), `templates/partials/workspace.html`, `cmd/serf-hub/jstest/` |
| TUI | `cmd/serf-tui/hub_command_registry.go`, `hub_commands.go`, `hub_session_keys.go`, `hub_session_view.go`, `hub_types.go` (snapshot field mapping), `hub_dashboard_view.go`, notification apply path |
| Delegates | `agent/job_delegate.go`, `agent/subagents.go`, `agent/internal/jobstore/record.go` |
| Scenarios/docs | `test/scenarios/*.md`, `test/scenarios/INDEX.md`, `docs/llm-providers.md` |

---

### Task 1: `SetModel` returns an error; callers propagate

**Files:** `agent/session.go:651-691`, `cmd/serf/serve.go:406`,
`server/server.go:497` (hook type), `agent/session_set_model_test.go` (new)

- [ ] **Step 1: Failing tests.** In the new test file: (a) `SetModel` with an
  unknown instance ref returns a non-nil error whose text lists configured
  instances, and `currentProfile()` is unchanged afterward; (b) a valid
  same-provider switch returns nil and the profile's `Model()` changes;
  (c) a valid cross-provider switch via an injected resolver returns nil and
  swaps `profile.ID()`; (d) crash-restore: switch, then restore via
  `RestoreSessionFromMetaWithConfig` from the flushed meta — the restored
  session's profile is the switched model. Mirror the resolver-injection
  pattern from `agent/session_resolve_profile_test.go`.
- [ ] **Step 2: Verify failure** (compile error on signature or silent-nil).
- [ ] **Step 3: Implement.** Change `func (s *Session) SetModel(model string)`
  to return `error`. Replace the swallow at `session.go:659-662` with an
  error return (no state change). The sole production caller is the daemon
  hook closure (`cmd/serf/serve.go:406`) — update it to return the error and
  adjust `SetModelFunc`'s signature (`server/server.go:497`) to
  `func(string) error`; run `grep -rn "\.SetModel(" --include="*.go"` to
  catch tests. (Fallbacks and delegate spawn do NOT call `SetModel` — they
  use `resolveProfileForRef` directly; expect no other production hits.) Do
  not change `SetReasoningEffort`'s signature.
- [ ] **Step 4: Green.** `go test ./agent/... ./server/... ./cmd/serf/...`
- [ ] **Step 5: Commit** — `feat(agent): SetModel reports resolution errors`

### Task 2: Model validation + unrepresentable-history preflight

**Files:** `agent/session.go` (SetModel body), helper in `agent/` near the
setter, tests in `agent/session_set_model_test.go`

- [ ] **Step 1: Failing tests.** (a) Switching to a model absent from an
  enumerable instance's model set is rejected (error names the instance);
  (b) a non-enumerable instance accepts an unlisted model, and **an
  enumeration failure of any error class fails open (accepts)** — this keeps
  the dead-credentials failure-mode row true. Read the launch policy first
  and reuse it: `cmd/serf/internal/launchcheck/launchcheck.go:217`
  (`validateLaunchCheckModel` — note it fails open only for an error-message
  allowlist; the switch path fails open unconditionally, per spec) and
  `cmd/serf-hub/app_models.go:143` (`launchProviderAllowsUnreportedModels`,
  behavior-tag keyed); (c) history containing a `document` part → switch to
  anthropic-family, google, and openai-compat targets each rejected with the
  kind named (spec's explicit per-tag policy table — compat *silently drops*
  documents/audio today, which counts as unrepresentable by policy);
  (d) same history → openai Responses target accepted (it carries
  documents); (e) audio in history → rejected for anthropic-family, google, openai-compat,
  AND openai Responses targets (`responses.go:913-938`); (f) every rejection
  leaves profile, meta.json, and history byte-identical; (g) after a successful cross-tag switch,
  `cfg.ModelFallbacks` entries that no longer validate against the new
  profile are dropped (re-run of `validateModelFallbacks`,
  `agent/session_init.go:775-805`) and the dropped names are surfaced for
  the Task 5 marker's warning line; a same-tag switch keeps valid entries.
- [ ] **Step 2: Verify failure.**
- [ ] **Step 3: Implement.** Add a preflight in `SetModel` before the profile
  swap: scan canonical history content kinds (walk `s.history` turns'
  `ContentPart` kinds — see `llm/types.go:40-49`) against the spec's
  explicit per-tag table (anthropic-family: document+audio,
  `anthropic/request.go:512-513`; google: document+audio,
  `google/request.go:280-281,328-329`; openai-compat: document+audio by
  policy — silent drop today; openai Responses: audio only,
  `responses.go:913-938`). Model membership: launchcheck's live-enumeration
  + catalog source of truth with the unconditional fail-open amendment.
  Fallback re-validation runs after the swap commits, before the setter
  returns.
- [ ] **Step 4: Green**, including the Task 1 tests.
- [ ] **Step 5: Commit** — `feat(agent): validate model switches up front`

### Task 3: Turn-active gate + RPC error propagation

**Files:** `server/appwire_runtime.go:382-398`,
`server/server_handlers.go:192-217`, `server/reasoning_effort_test.go`
(pattern source), new/extended `server` tests

- [ ] **Step 1: Failing tests.** (a) `thread/model/set` while the session is
  processing **or holds a reserved turn id** (`processing ||
  appReservedTurnID != ""` — the compound `appCapabilities` uses at
  `appwire_runtime.go:629`; note `thread/clear`'s gate at `:365-371` checks
  only `processing`, so don't copy it verbatim) returns a structured AppWire
  error naming the active turn id when one exists, and the model hook is
  never invoked (fake hook records calls); (b) a hook error surfaces as an
  AppWire error with the hook's message; (c) success returns
  `EmptyResponse`; (d) `POST /model` (`server_handlers.go:192`) mirrors all
  three; (e) slashed-ref grammar: params `{modelProvider: "openrouter",
  model: "anthropic/claude-x"}` reach the hook as
  `"openrouter/anthropic/claude-x"` — today the join is skipped when the
  model contains a slash (`appwire_runtime.go:387-388`), silently rerouting
  to the anthropic instance; the join becomes unconditional when
  `modelProvider` is non-empty. Follow `server/reasoning_effort_test.go`
  structure.
- [ ] **Step 2: Verify failure.**
- [ ] **Step 3: Implement.** Gate + error propagation in
  `handleAppThreadModelSet` and the HTTP handler; unconditional provider
  join per the spec's ref-grammar decision.
- [ ] **Step 4: Green.** `go test ./server/...`
- [ ] **Step 5: Commit** — `feat(server): gate model switches on turn state`

### Task 4: Change events, notifications, and snapshot freshness

**Files:** `agent/session.go` (both setters emit events; find the events
package via `EventSessionNameChanged`'s definition),
`internal/appprojector/appwire_projection.go:652` (template case),
`appwire/types.go`, `appwire/protocol.go`, `server/appwire_runtime.go:545-565`
(appThread), `server/server.go:278 UpdateSessionInfo`, `cmd/serf/serve.go:406`

- [ ] **Step 1: Failing tests.** (a) `SetModel` success emits a model-changed
  session event carrying old + new `provider/model`, the new profile's
  `ReasoningEffortLevels()`, and `SupportsReasoning()`; (b)
  `SetReasoningEffort` emits its event; (c) projector maps them to
  `thread/model/changed` / `thread/reasoning-effort/changed` notifications
  with the payload fields from the spec's Protocol changes section;
  (d) after a switch, `thread/read` hydration reports the new model
  **without** any intervening turn (this pins the `UpdateSessionInfo`
  synchrony fix — today it only fires on `EventSessionStart`,
  `server/bridge.go:28-30`); (e) the thread snapshot carries
  `reasoningEffort`, `reasoningEffortLevels`, and `supportsReasoning` — a
  cold-attached client can render both settings and populate pickers with
  no prior notification; (f) the appwire catalog cross-check test passes
  with the two new notification rows.
- [ ] **Step 2: Verify failure.**
- [ ] **Step 3: Implement.** New event kinds beside `EventSessionNameChanged`
  (they live in `agent/events/` — events, payloads, eventdata); emit from
  both setters after the state change commits; projector cases; catalog
  rows + params types; `appThread()` populates the three snapshot fields
  from live session state and the daemon refreshes its cached session info
  on the model hook path. Run `make generate` for the protocol doc.
- [ ] **Step 4: Green.** `go test ./agent/... ./server/... ./appwire/...
  ./internal/appprojector/...`
- [ ] **Step 5: Commit** — `feat(appwire): broadcast model and effort changes`

### Task 5: Persisted switch marker + knowledge-cutoff refresh

**Files:** `agent/schema/turn.go` (new marker turn kind beside
`TurnCheckpoint`/`TurnSummary`), `agent/session.go` (SetModel appends it),
`internal/apptranscript/apptranscript.go:216-230` (replay `systemMessage`
case), `internal/appprojector/appwire_projection.go` (live case — read how
`systemAnnouncementWithRaw` at `:852` shapes the item, but note it alone is
live-only: the notification ring is bounded in-memory,
`internal/appserver/notifier.go:15-44`, and `thread/read` prefers transcript
turns, `server/appwire_turns.go:13-24` — hence the persisted turn),
`agent/session_model_call.go:1009` (`expandHistory` exclusion),
`agent/session_prompts.go:73`, `agent/session_init.go:652`

- [ ] **Step 1: Failing tests.** (a) A successful switch appends the marker
  turn to the persisted transcript; `apptranscript.ProjectTurn` renders it
  as a `systemMessage` item reading `Switched model: <old> → <new>` (extend
  the existing `TurnCheckpoint` projection tests); (b) the live projector
  emits the equivalent `systemMessage` item when the switch event fires;
  (c) `expandHistory` excludes the marker turn from model requests;
  (d) when estimated context usage exceeds the new profile's window
  threshold, the marker carries the warning line; dropped fallback entries
  (Task 2g) are named in a warning line too; (e) after a switch the cached
  system prompt contains the new model's knowledge cutoff (extend the
  prompt-cache test pattern near `agent/session_prompts.go` tests).
- [ ] **Step 2: Verify failure.**
- [ ] **Step 3: Implement.** New `schema.Turn` kind; append from the SetModel
  success path; both projection cases; expansion exclusion; recompute
  `envInfo.KnowledgeCutoff` before `refreshSystemPromptCache`.
- [ ] **Step 4: Green.** `go test ./agent/... ./internal/apptranscript/...
  ./internal/appprojector/...`
- [ ] **Step 5: Commit** — `feat(agent): persist model-switch markers`

### Task 6: Replay provenance enforcement (spec N4)

**Files:** `agent/session_model_call.go:1009-1049` — change `expandHistory`'s
signature to accept the target `(provider, model)` and filter
**pre-flattening**, where per-turn provenance (`schema.Turn`) is still in
hand (do NOT try `buildModelRequest`: it receives already-flattened
`[]llm.Message` with provenance gone, and `expandHistory` today has no
profile parameter — the profile is known at its call site in
`prepareModelRequest`). Keep adapters' existing guards as backstops. Tests
beside the builders: `llm/providers/{anthropic,openai,openaicompat,google}/`,
plus `agent/` expansion tests.

- [ ] **Step 1: Failing tests (the N4 matrix).** For each destination tag
  (anthropic-family, google, openai Responses, openai-compat): (a) thinking
  produced by the same (instance id, requested model) replays; (b) for
  anthropic-family targets, thinking from a different model of the same
  provider is absent from the outgoing request (the G12 case); for google
  targets, same-provider thinking replays regardless of model (thought
  signatures are mandatory for prior tool calls); (c) thinking from a
  different provider is absent everywhere; (d) `web_search` raw blocks
  replay same-tag, drop cross-tag; (e) tool_call/tool_result ids replay
  verbatim in all cases; (f) the comparison semantics per spec N4: match on
  the producing turn's `(ResponseProvider, ResponseRequestModel)`
  (`agent/schema/turn.go:44-46`), falling back to catalog-canonicalized
  `ResponseModel` when empty; **turns with empty provenance replay** (legacy
  transcripts unchanged); (g) exemptions: rounds of the in-flight turn are
  never filtered, and a fallback-built request retains the primary model's
  thinking (pin `callModelWithFallback`'s post-build model swap at
  `session_model_call.go:797-812` outside the rule); (h) the stored
  transcript is untouched by projection (switch back → full replay
  returns). Also (i): a fingerprint test pinning that an OpenAI Responses
  continuation anchor is not reused across a model change
  (`llm/providers/openai/responses_continuation_fingerprint.go:32-42`) — it
  may already exist; if so, reference it in the task commit message instead
  of duplicating.
- [ ] **Step 2: Verify failure** (at minimum the G12 case and cross-tag
  web_search must fail today).
- [ ] **Step 3: Implement** the strip/keep rules in the re-signed
  `expandHistory`, threading the target from `prepareModelRequest`. Leave
  the existing per-builder guards in place.
- [ ] **Step 4: Green.** `go test ./agent/... ./llm/...`
- [ ] **Step 5: Commit** — `feat(llm): enforce replay provenance on switch`

### Task 7: Web — header chip picker, live updates, run-state disable

**Files:** `cmd/serf-hub/templates/partials/workspace.html:71-78`,
`cmd/serf-hub/assets/appwire.js` (notification map + `setModel`),
`assets/search.js:337` (palette source stays), a picker module (reuse the
pattern of `assets/settings-pickers.js:250`), `assets/model-display.js`,
`cmd/serf-hub/jstest/` (new `test-model-switch.js`)

- [ ] **Step 1: Failing JSDOM tests.** (a) Clicking `[data-model-trigger]`
  opens a picker populated from `model/list` grouped by provider, current
  model marked; (b) choosing an entry calls `thread/model/set` with the
  first-slash split (provider = instance; model keeps any remaining slashes
  — openrouter ids); (c) a `thread/model/changed` notification updates
  `[data-model-display]` without a thread re-read and re-keys the cached
  effort levels; (d) while `Status.Type == "active"` / `ActiveTurnID` is set
  (the signal `workspace.html:84-87` already uses — NOT `activeFlags`,
  which serf daemons never populate) the trigger is disabled and the
  palette action refuses with a notice; (e) an AppWire error from the set
  call renders the server message as a notice and leaves the chip
  unchanged; (f) a failed `model/list` fetch renders an error state in the
  picker, not a silent empty list (`fetchModels` currently swallows,
  `search.js:245-253`); (g) `thread/model/changed` is added to the
  sidebar's resync whitelist (`sidebar.js:914 QUALIFYING`) so the session
  list re-syncs after a switch.
- [ ] **Step 2: Verify failure.**
- [ ] **Step 3: Implement.** Map the two new notifications in
  `eventsFromNotification`; wire the trigger; respect
  `ThreadCapabilities.ChangeModel`; extend QUALIFYING.
- [ ] **Step 4: Green.** `NODE_PATH=… node cmd/serf-hub/jstest/test-model-switch.js`
  plus `sh cmd/serf-hub/jstest/run-all.sh`.
- [ ] **Step 5: Commit** — `feat(webui): model switching from the header chip`

### Task 8: Web — per-model effort levels for the live session

**Files:** `cmd/serf-hub/assets/search.js:356-370`, effort control module,
`cmd/serf-hub/jstest/`

- [ ] **Step 1: Failing JSDOM tests.** (a) The live effort picker lists
  exactly the current model's levels, sourced **snapshot-first** (the Task 4
  `reasoningEffortLevels`/`supportsReasoning` thread fields — NOT the
  appwire `model/list`, which carries provider+model only, nor REST
  `/api/models`, which the live surface shouldn't need), updated by
  notifications; (b) spawn-pattern semantics (`spawn.js:1608-1633`):
  `supportsReasoning === false` → known-empty (no options); absent levels
  on an unknown model → full-vocabulary fallback; (c) after a
  `thread/model/changed` notification the options re-key; (d) a current
  effort chip renders beside the header model chip from the snapshot and
  updates on `thread/reasoning-effort/changed` (a cold-attached client
  shows the right value with no prior notification).
- [ ] **Step 2: Verify failure** (today's list is hardcoded at
  `search.js:361-369` and no live effort display exists).
- [ ] **Step 3: Implement** by porting the spawn-form option logic and
  adding the chip.
- [ ] **Step 4: Green** + `run-all.sh`.
- [ ] **Step 5: Commit** — `feat(webui): per-model effort levels live`

### Task 9: TUI — `/effort` command, live refresh, notices

**Files:** `cmd/serf-tui/hub_command_registry.go:303-329` (`/model` as the
template), `hub_commands.go:552-566`, `hub_session_keys.go:90-108`,
`hub_session_view.go:53`, the notification-apply path
(`applyHubNotification`), `cmd/serf-tui` tests

- [ ] **Step 1: Failing tests.** (a) Registry contains `/effort` gated on
  `c.ChangeModel` (documented reuse — there is NO effort capability on the
  wire, `cmd/serf-hub/app_rpc.go:550-552`; do not invent one); (b) bare
  `/effort` opens a picker of the current model's levels, snapshot-first
  (Task 4 fields mapped through `hub_types.go` `hubDetailFromThread`
  `:205-236`), notification-on-update; `supportsReasoning === false` means
  known-empty; (c) `/effort high` sends `thread/reasoning-effort/set`;
  (d) `thread/model/changed` updates `m.detail.Model` (header re-renders
  via `AbbreviateModel`), the cached levels, and the dashboard row's Model
  column (`hub_dashboard_view.go:348`); (e) `/model`'s picker renders the
  `(active)` tag — `activeModel` is ALREADY passed (`hub_update.go:451`);
  the real bug is normalization: item ids are `provider/model`
  (`hub_commands.go:415`) compared exactly against bare `detail.Model`
  (`model_picker.go:180,190`) — fix the comparison, not the plumbing;
  (f) the session header renders an `effort` part beside `model`
  (`hub_session_view.go:53`); (g) a rejected switch (turn active /
  validation error) renders the server's message as a notice.
- [ ] **Step 2: Verify failure.**
- [ ] **Step 3: Implement**, reusing `tuipick` primitives; no new UI
  paradigms.
- [ ] **Step 4: Green.** `go test ./cmd/serf-tui/...`
- [ ] **Step 5: Commit** — `feat(tui): /effort command and live model chip`

### Task 10: Delegate result model echo

**Files:** `agent/job_delegate.go:111-149` (sandbox echo as template, echo
set at `:281,:322,:333`), `agent/subagents.go:361-383`,
`agent/internal/tool/definitions.go:128` (description), delegate tests

- [ ] **Step 1: Failing tests.** (a) `delegateResult` carries the resolved
  `provider/model` the child ran; (b) a delegate spawned after a parent
  switch echoes the new model; one spawned before keeps its captured model;
  (c) an explicit `model` arg echoes the pinned value; (d) restore echoes
  the descriptor's persisted model (`jobstore/record.go:76-78`).
- [ ] **Step 2: Verify failure.**
- [ ] **Step 3: Implement** the echo mirroring the sandbox report shape;
  update the tool param description to state the inheritance rule.
- [ ] **Step 4: Green.** `go test ./agent/...`
- [ ] **Step 5: Commit** — `feat(agent): delegates echo their resolved model`

### Task 11: Scenario cards + docs

**Files:** `test/scenarios/` (new: `web-model-switch-mid-session.md`,
`tui-model-switch.md`, `model-switch-resume.md`, `tui-effort-command.md`),
`test/scenarios/INDEX.md`, `docs/llm-providers.md` (the "Setting it" runtime
paragraph, `:663-666`)

- [ ] **Step 1:** Author the four cards from `test/scenarios/_template.md`,
  asserting the spec's acceptance criteria 1–6 (convergence without reload,
  fresh hydration, resume-on-switched-model, mid-turn rejection, marker on
  reload, per-model effort levels + cold-attach display).
  `model-switch-resume.md` must kill the daemon between switch and resume.
  The mid-turn card also asserts the queued-input case: with messages
  queued, the switch stays rejected until the queue drains (spec failure
  modes).
- [ ] **Step 2:** `go test ./test/scenarios/...` (scenario_docs_test) green;
  run the deterministic cards per the e2e-scenario-testing harness.
- [ ] **Step 3:** Update `docs/llm-providers.md` runtime-switch paragraph to
  mention validation, the notifications, and the marker; confirm the
  regenerated `docs/appwire-protocol.md` from Task 4 is committed.
- [ ] **Step 4: Commit** — `test(scenarios): mid-session model switch cards`

### Task 12: Live cross-provider ladder + full verification

**Files:** `test/scenarios/model-switch-providers-live.md` (new, patterned on
`test/scenarios/reasoning-effort-providers.md`)

- [ ] **Step 1:** Author the live card following
  `test/scenarios/reasoning-effort-providers.md`'s conventions exactly: an
  **isolated `SERF_PROVIDERS_CONFIG`** declaring the instances the ladder
  needs (instance NAMES are deployment-local config, not repo facts — the
  live `~/.serf/providers.toml` on this machine has no `anthropic` instance;
  discover/declare names in the card, refs are `instanceName/model`). Ladder:
  one session, tool-using turn on the anthropic instance, switch → openai
  instance → turn → switch → the kimi coding instance → turn, plus one
  anthropic→anthropic model hop (fable-5 → sonnet-5). After each hop assert
  the persisted turn's `response_model` matches the switched model and the
  effort clamps to that model's ladder; assert the persisted markers.
  Thinking-absence assertion: run with `SERF_LOG_RAW_HTTP=1` and inspect
  `sessions/<id>.api-raw.jsonl` request bodies (`llm/apilog.go:176-197` —
  the default api.jsonl records metadata only, NO bodies); the
  deterministic backstop for the same rule is Task 6's unit matrix.
- [ ] **Step 2:** Run it with `SERF_E2E_LIVE=1`. Record results in the card.
- [ ] **Step 3: Full sweep.** `go test ./... -count=1`, `cd
  cmd/serf-hub/jstest && sh run-all.sh`, `make lint`, `git diff --check`,
  `git status --short` (only files named by tasks — as amended during
  implementation — changed).
- [ ] **Step 4: Commit** — `test(e2e): live model-switch ladder` (plus any
  verified fix as its own focused commit).

## Plan self-review

- Spec coverage: deltas 1→Tasks 1-3, 2-3→Task 4, 4→Task 3, 5→Task 6,
  6→Task 5, 7→Task 7, 8→Tasks 8-9, 9→Tasks 5+10, 10→Tasks 11-12.
- Every task names exact files, failing-first tests, and commit scope; no
  TBDs. Line anchors are advisory (verified @ f4ec5267); workers re-read
  before editing.
