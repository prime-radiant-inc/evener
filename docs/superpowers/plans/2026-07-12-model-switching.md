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
| Daemon RPC + snapshot | `server/appwire_runtime.go`, `server/server_handlers.go`, `server/server.go`, `server/bridge.go` |
| Protocol | `appwire/types.go`, `appwire/protocol.go`, `internal/appprojector/appwire_projection.go`, `docs/appwire-protocol.md` (generated) |
| Replay provenance | `agent/session_model_call.go` (expansion choke point), `llm/providers/{anthropic,openai,openaicompat}/…` tests |
| Web UI | `cmd/serf-hub/assets/{appwire.js,search.js,model-display.js,settings-pickers.js}`, new `assets/model-switch.js` (if a new file reads cleaner), `templates/partials/workspace.html`, `cmd/serf-hub/jstest/` |
| TUI | `cmd/serf-tui/hub_command_registry.go`, `hub_commands.go`, `hub_session_keys.go`, `hub_session_view.go`, notification apply path |
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
  swaps `profile.ID()`. Mirror the resolver-injection pattern from
  `agent/session_resolve_profile_test.go`.
- [ ] **Step 2: Verify failure** (compile error on signature or silent-nil).
- [ ] **Step 3: Implement.** Change `func (s *Session) SetModel(model string)`
  to return `error`. Replace the swallow at `session.go:659-662` with an
  error return (no state change). Keep the void-compatible behavior for every
  internal caller by updating call sites: the daemon hook closure
  (`cmd/serf/serve.go:406`) now returns the error (adjust `SetModelFunc`'s
  signature at `server/server.go:497` to `func(string) error`), and any other
  caller found by `grep -rn "\.SetModel(" --include="*.go"` handles or
  propagates. Do not change `SetReasoningEffort`'s signature.
- [ ] **Step 4: Green.** `go test ./agent/... ./server/... ./cmd/serf/...`
- [ ] **Step 5: Commit** — `feat(agent): SetModel reports resolution errors`

### Task 2: Model validation + unrepresentable-history preflight

**Files:** `agent/session.go` (SetModel body), helper in `agent/` near the
setter, tests in `agent/session_set_model_test.go`

- [ ] **Step 1: Failing tests.** (a) Switching to a model absent from an
  enumerable instance's model set is rejected (error names the instance);
  (b) a non-enumerable instance accepts an unlisted model (mirror the launch
  path's unreported-models allowance — read
  `cmd/serf-hub/app_rpc.go launchProviderAllowsUnreportedModels` and
  `cmd/serf/launch_check.go:198 validateLaunchCheckModel` first and reuse
  their policy, keyed on behavior tag); (c) history containing a `document`
  part → switch to an anthropic-family target rejected with the kind named;
  (d) same history → openai target accepted (Responses carries documents);
  (e) audio in history → rejected for anthropic and openai-compat targets;
  (f) every rejection leaves profile, meta.json, and history byte-identical.
- [ ] **Step 2: Verify failure.**
- [ ] **Step 3: Implement.** Add a preflight in `SetModel` before the profile
  swap: scan canonical history content kinds (walk `s.history` turns'
  `ContentPart` kinds — see `llm/types.go:40-49`) against a small
  target-tag capability table derived from the builders' hard errors
  (`llm/providers/anthropic/request.go:512-513` document+audio,
  `openai/responses.go:913-938` audio). Model-membership check: reuse the
  profile's model table / catalog / enumeration exactly as the launch policy
  does — do not invent a new source of truth.
- [ ] **Step 4: Green**, including the Task 1 tests.
- [ ] **Step 5: Commit** — `feat(agent): validate model switches up front`

### Task 3: Turn-active gate + RPC error propagation

**Files:** `server/appwire_runtime.go:382-398`,
`server/server_handlers.go:192-217`, `server/reasoning_effort_test.go`
(pattern source), new/extended `server` tests

- [ ] **Step 1: Failing tests.** (a) `thread/model/set` while the session
  reports an active turn returns a structured AppWire error and the model
  hook is never invoked (fake hook records calls); (b) a hook error surfaces
  as an AppWire error with the hook's message; (c) success returns
  `EmptyResponse`; (d) `POST /model` (`server_handlers.go:192`) mirrors all
  three. Follow `server/reasoning_effort_test.go` structure.
- [ ] **Step 2: Verify failure.**
- [ ] **Step 3: Implement.** In `handleAppThreadModelSet`, consult the same
  turn-activity state `thread/clear` uses for its "rejected while a turn is
  processing" behavior (find it by reading the clear handler in
  `server/appwire_runtime.go`) and reject before invoking the hook; then
  propagate the hook's error. Same for the HTTP handler.
- [ ] **Step 4: Green.** `go test ./server/...`
- [ ] **Step 5: Commit** — `feat(server): gate model switches on turn state`

### Task 4: Change events, notifications, and snapshot freshness

**Files:** `agent/session.go` (both setters emit events; find the events
package via `EventSessionNameChanged`'s definition),
`internal/appprojector/appwire_projection.go:652` (template case),
`appwire/types.go`, `appwire/protocol.go`, `server/appwire_runtime.go:545-565`
(appThread), `server/server.go:278 UpdateSessionInfo`, `cmd/serf/serve.go:406`

- [ ] **Step 1: Failing tests.** (a) `SetModel` success emits a model-changed
  session event carrying old + new `provider/model` and the new profile's
  `ReasoningEffortLevels()`; (b) `SetReasoningEffort` emits its event;
  (c) projector maps them to `thread/model/changed` /
  `thread/reasoning-effort/changed` notifications with the payload fields
  from the spec's Protocol changes section; (d) after a switch, `thread/read`
  hydration reports the new model **without** any intervening turn (this
  pins the `UpdateSessionInfo` synchrony fix — today it only fires on
  `EventSessionStart`, `server/bridge.go:28-30`); (e) the thread snapshot
  carries `reasoningEffort`; (f) the appwire catalog cross-check test passes
  with the two new notification rows.
- [ ] **Step 2: Verify failure.**
- [ ] **Step 3: Implement.** New event types beside `EventSessionNameChanged`;
  emit from both setters after the state change commits; projector cases;
  catalog rows + params types; `appThread()` populates `reasoningEffort`
  from live session state and the daemon refreshes its cached session info
  on the model hook path. Run `make generate` for the protocol doc.
- [ ] **Step 4: Green.** `go test ./agent/... ./server/... ./appwire/...
  ./internal/appprojector/...`
- [ ] **Step 5: Commit** — `feat(appwire): broadcast model and effort changes`

### Task 5: Transcript marker + knowledge-cutoff refresh

**Files:** `agent/session.go` (SetModel), the announcement hook
(`internal/appprojector/appwire_projection.go:852 systemAnnouncementWithRaw`
producer path — read how goal continuations emit it from
`agent/session_lifecycle.go:1163`), `agent/session_prompts.go:73`,
`agent/session_init.go:652`

- [ ] **Step 1: Failing tests.** (a) A successful switch appends a persisted
  `systemMessage` item reading `Switched model: <old> → <new>`; it survives
  transcript reload (assert via the replay projection used in
  `internal/apptranscript` tests); (b) when estimated context usage exceeds
  the new profile's window threshold, the marker carries the warning line;
  (c) after a switch the cached system prompt contains the new model's
  knowledge cutoff (extend the prompt-cache test pattern near
  `agent/session_prompts.go` tests).
- [ ] **Step 2: Verify failure.**
- [ ] **Step 3: Implement.** Emit the marker from the SetModel success path
  using the same mechanism as existing lifecycle announcements; recompute
  `envInfo.KnowledgeCutoff` before `refreshSystemPromptCache`.
- [ ] **Step 4: Green.**
- [ ] **Step 5: Commit** — `feat(agent): mark model switches in transcript`

### Task 6: Replay provenance enforcement (spec N4)

**Files:** `agent/session_model_call.go:1009-1049` (`expandHistory`) and/or
`buildModelRequest` (`:708-755`) — enforce at this choke point where the
target profile is known, keeping adapters' existing guards as backstops;
tests beside the builders: `llm/providers/anthropic/`, `llm/providers/openai/`,
`llm/providers/openaicompat/`, plus `agent/` expansion tests

- [ ] **Step 1: Failing tests (the N4 matrix).** For each destination tag
  (anthropic-family, openai Responses, openai-compat): (a) thinking produced
  by the same (provider, model) replays; (b) thinking from a different model
  of the same provider is absent from the outgoing request (this is the
  anthropic→anthropic G12 case); (c) thinking from a different provider is
  absent; (d) `web_search` raw blocks replay same-tag, drop cross-tag;
  (e) tool_call/tool_result ids replay verbatim in all cases; (f) the stored
  transcript is untouched by projection (switch back → full replay returns).
  Provenance comes from the producing turn's
  `ResponseProvider`/`ResponseModel` (`agent/schema/turn.go:42-50`).
  Also (g): a fingerprint test pinning that an OpenAI Responses continuation
  anchor is not reused across a model change
  (`llm/providers/openai/responses_continuation_fingerprint.go:32-42`) — it
  may already exist; if so, reference it in the task commit message instead
  of duplicating.
- [ ] **Step 2: Verify failure** (at minimum the G12 case and cross-tag
  web_search must fail today).
- [ ] **Step 3: Implement** the strip/keep rules at the expansion choke
  point, threading per-turn provenance to the filter. Leave the existing
  per-builder guards in place.
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
  split provider/model; (c) a `thread/model/changed` notification updates
  `[data-model-display]` without a thread re-read and re-keys the cached
  effort levels; (d) while thread status shows an active turn the trigger is
  disabled and the palette action refuses with a notice; (e) an AppWire
  error from the set call renders the server message as a notice and leaves
  the chip unchanged.
- [ ] **Step 2: Verify failure.**
- [ ] **Step 3: Implement.** Map the two new notifications in
  `eventsFromNotification`; wire the trigger; respect
  `ThreadCapabilities.ChangeModel`. Run-state comes from the status the
  renderer already tracks.
- [ ] **Step 4: Green.** `NODE_PATH=… node cmd/serf-hub/jstest/test-model-switch.js`
  plus `sh cmd/serf-hub/jstest/run-all.sh`.
- [ ] **Step 5: Commit** — `feat(webui): model switching from the header chip`

### Task 8: Web — per-model effort levels for the live session

**Files:** `cmd/serf-hub/assets/search.js:356-370`, effort control module,
`cmd/serf-hub/jstest/`

- [ ] **Step 1: Failing JSDOM tests.** (a) The live effort picker lists
  exactly the current model's `reasoning_effort_levels` (sourced from the
  snapshot/notification payload, not hardcoded); (b) a model without
  declared levels falls back to the full vocabulary; (c) after a
  `thread/model/changed` notification the options re-key.
- [ ] **Step 2: Verify failure** (today's list is hardcoded at
  `search.js:361-369`).
- [ ] **Step 3: Implement** by porting the spawn-form pattern
  (`assets/spawn.js:1609 effortLevelsForModel`).
- [ ] **Step 4: Green** + `run-all.sh`.
- [ ] **Step 5: Commit** — `feat(webui): per-model effort levels live`

### Task 9: TUI — `/effort` command, live refresh, notices

**Files:** `cmd/serf-tui/hub_command_registry.go:303-329` (`/model` as the
template), `hub_commands.go:552-566`, `hub_session_keys.go:90-108`,
`hub_session_view.go:53`, the notification-apply path
(`applyHubNotification`), `cmd/serf-tui` tests

- [ ] **Step 1: Failing tests.** (a) Registry contains `/effort` with the
  same capability gating shape as `/model`; (b) bare `/effort` opens a
  picker of the current model's levels (from the snapshot/notification
  payload); (c) `/effort high` sends `thread/reasoning-effort/set`;
  (d) `thread/model/changed` updates `m.detail.Model` (header re-renders via
  `AbbreviateModel`) and the cached levels; (e) `/model`'s picker marks the
  active model (pass `activeModel` into `NewModelPicker` — verify current
  call sites at `tuipick/model_picker.go:41`); (f) a rejected switch (turn
  active / validation error) renders the server's message as a notice.
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
  reload, per-model effort levels). `model-switch-resume.md` must kill the
  daemon between switch and resume.
- [ ] **Step 2:** `go test ./test/scenarios/...` (scenario_docs_test) green;
  run the deterministic cards per the e2e-scenario-testing harness.
- [ ] **Step 3:** Update `docs/llm-providers.md` runtime-switch paragraph to
  mention validation, the notifications, and the marker; confirm the
  regenerated `docs/appwire-protocol.md` from Task 4 is committed.
- [ ] **Step 4: Commit** — `test(scenarios): mid-session model switch cards`

### Task 12: Live cross-provider ladder + full verification

**Files:** `test/scenarios/model-switch-providers-live.md` (new, patterned on
`test/scenarios/reasoning-effort-providers.md`)

- [ ] **Step 1:** Author the live card: one session, tool-using turn on
  `anthropic/<current claude>`, switch → `openai/<current gpt>` → turn →
  switch → `kimi-anthropic/kimi-for-coding` → turn. After each hop assert
  the persisted turn's `response_model` matches the switched model and the
  effort clamps to that model's ladder; assert the transcript markers and
  that the anthropic→anthropic hop (add one: fable-5 → sonnet-5) sends no
  prior thinking blocks (api.jsonl inspection, `llm/apilog.go` records
  request bodies).
- [ ] **Step 2:** Run it with `SERF_E2E_LIVE=1` (credentials are configured
  on this machine). Record results in the card.
- [ ] **Step 3: Full sweep.** `go test ./... -count=1`, `cd
  cmd/serf-hub/jstest && sh run-all.sh`, `make lint`, `git diff --check`,
  `git status --short` (only plan-named files changed).
- [ ] **Step 4: Commit** — `test(e2e): live model-switch ladder` (plus any
  verified fix as its own focused commit).

## Plan self-review

- Spec coverage: deltas 1→Tasks 1-3, 2-3→Task 4, 4→Task 3, 5→Task 6,
  6→Task 5, 7→Task 7, 8→Tasks 8-9, 9→Tasks 5+10, 10→Tasks 11-12.
- Every task names exact files, failing-first tests, and commit scope; no
  TBDs. Line anchors are advisory (verified @ f4ec5267); workers re-read
  before editing.
