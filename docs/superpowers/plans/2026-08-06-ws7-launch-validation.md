# WS7: Launch Validation and Quota Classification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Kill the "config-death" failure class the 2026-08-05 session study found:
sessions that died on their first LLM call because an invalid model or
reasoning-effort value was baked into config with zero pre-flight validation,
delegates dispatched onto unsupported models/quota-exhausted providers and
burned a whole session finding out, and a billing-limit 403 was reported to
orchestrators as generic "access denied" instead of quota exhaustion — so they
kept re-dispatching delegate waves into it.

**Architecture:** Reuse, don't re-derive. `agent/session_set_model.go` already
has a correct, tested model-membership check
(`validateModelSwitchMembership` + `resolveModelSwitchTarget`) that
`SetModel` uses; the only work for the model seams is wiring the two
*unwired* call sites (`NewSession`, delegate dispatch) through the same
functions, and enriching the shared error with live-list alternatives so all
three seams get that improvement for free. Same principle for the Codex
model-compatibility table: it plugs into the existing membership check via a
new optional adapter interface (mirroring the existing `ModelLister` /
`ToolChoiceSupporter` optional-interface pattern in `llm/client.go`), so
agent-side code stays provider-agnostic. Reasoning-effort validation is a new
small pure function (`llm.ValidateReasoningEffort`) wired at three call sites
that currently skip it. Quota-403 classification is a self-contained change
to `llm/errors.go`'s status-code switch plus one addition to
`llm/usagelimit.go`'s body parser, grounded in the actual captured Kimi
billing-cycle 403 body (see Task 1).

**Tech stack:** Go. Modules: `llm` (errors/types/provider adapter),
`agent` (session init, delegate dispatch, plugin agent config), root
(no cmd/serf changes needed — `launchcheck.go` is a separate, already-correct
copy of this policy at the CLI-preflight layer and is out of scope here).
Test conventions per `docs/testing.md`.

**Context (verified 2026-08-06, file:line on branch `ws7-launch-validation`
at 4f5ae4a75):**

- `NewSession` (`agent/session_init.go:115`) calls
  `resolveLiveModelProfileWithTimeout(client, profile)` at line 128, which
  (`agent/live_model_metadata.go:14-33`) fetches the live model list with a
  fail-open 2s timeout, fills live metadata when found, and does **no
  membership check** — an absent model silently proceeds.
- `SetModel` (`agent/session.go:868-899`) already validates correctly:
  `resolveProfileForRef` → `unrepresentableHistoryKinds` preflight →
  `resolveModelSwitchTarget` (`agent/session_set_model.go:83-104`, 8s
  timeout, fail-open on enumeration failure only) →
  `validateModelSwitchMembership` (`session_set_model.go:117-134`), which
  scans the live list for `m.ID == profile.Model()` +
  `modelSwitchVisible` (family/tool-support filtering), with an
  `openrouter-anthropic` unreported-models carve-out.
- Delegate dispatch: `s.selectSubagentModel` (`agent/subagent_model_selection.go:27-119`)
  has two call sites (lines ~67, ~106) that call `s.resolveProfileForRef(base,
  explicitModel)` for an explicit `delegate` model override and use the
  result **unvalidated**. (The plugin-agent-preferred-model path,
  `resolvePluginAgentModel` ~line 121, already does its own live-list check
  with a graceful warn-and-fallback — out of scope, do not touch.)
- `effortRank` (`llm/types.go:628-635`) is the vocabulary
  (`minimal|low|medium|high|xhigh|max`); `ClampReasoningEffort`
  (`llm/types.go:670-704`) passes unknown values through unchanged by
  design (provider decides) — validation is a separate, new concern, not a
  change to clamp semantics.
- Three seams accept `reasoning_effort` with no vocabulary check:
  1. `SessionConfig.ReasoningEffort` at `NewSession` entry
     (`agent/session_init.go:115`).
  2. Delegate call args: `agent/job_delegate.go:328` calls
     `s.selectSubagentModel`, and `args.ReasoningEffort` flows unchecked into
     `prepareSubagentRunWithModelSelection` (`agent/subagents.go:510-512`),
     which sets `subCfg.ReasoningEffort` directly.
  3. Plugin-agent task-template config load: `agent/plugin/agents.go:161-163`
     parses a task's `reasoning_effort:` YAML/frontmatter field with zero
     validation into `task.TaskTemplate.ReasoningEffort`; at session init
     (`agent/session_init.go:280-283`) the current in-progress task's
     `ReasoningEffort` is copied straight into `s.cfg.ReasoningEffort`. This
     is the literal "reasoning_effort baked into session config" the study
     cites (`docs/research/2026-08-05-agentic-ux-session-study.md:199`) —
     rejecting it at parse time (`ParseAgent`,
     `agent/plugin/agents.go:44`) fails the plugin load loudly instead of
     the session dying on its first turn.
  (The CLI `--reasoning-effort` flag already validates via
  `cmdutil.ResolveReasoningEffort`, `cmdutil/cmdutil.go:237-261` — a
  hardcoded switch statement, not touched here; out of scope.)
- `errorFromHTTPStatus` (`llm/errors.go:273-324`) calls `parseUsageLimit` only
  in the `case 429`. `case 403` (`llm/errors.go:284-296`) never inspects the
  body: every 403 becomes `accessDeniedError`, retryable only for
  `cyber_policy_violation`.
- **Real captured body** (session `0341i3MDP7PKYfPqGhqPWO`, provider instance
  `kimi-anthropic-api`, attempt at 2026-08-05T05:48:28Z, status 403):
  ```json
  {"error":{"type":"permission_error","message":"You've reached your usage limit for this billing cycle. Your quota will be refreshed in the next cycle. To continue now, purchase extra usage or upgrade your plan: https://www.kimi.com/code/#pricing"},"type":"error"}
  ```
  Critically, `error.type` is `"permission_error"` — **not** one of
  `usageLimitCodes` (`usage_limit_reached`, `insufficient_quota`,
  `llm/usagelimit.go:25-28`) — and there is no `resets_at`/`resets_in_seconds`.
  Only the message text names it. `parseUsageLimit`
  (`llm/usagelimit.go:47-80`) must gain a message-substring fallback (`"usage
  limit"`) for when no structured code is present — see Task 1.
- Codex-backend model compatibility: `wireModel`
  (`llm/providers/openai/responses.go:910-915`) hardcodes
  `gpt-5.6 → gpt-5.6-sol`. The model catalog
  (`llm/data/litellm_model_catalog.json`, confirmed by grep) lists exactly
  four `gpt-5.6*` entries: `gpt-5.6`, `gpt-5.6-sol`, `gpt-5.6-terra`,
  `gpt-5.6-luna`. `gpt-5.6-mini` (the study's failing case) is **not a real
  model** in the catalog at all — it 400s because it was never a valid slug,
  not because of a live-availability gap. `llm/client.go` already has a
  precedent for optional per-adapter capabilities checked via type-assertion
  (`ModelLister` at `:330-337`, dispatch at `:391-410`; `ToolChoiceSupporter`
  at `:326-329`, dispatch at `:372-384`) — Task 4 adds one more in the same
  shape.

## Global Constraints

- Smallest reasonable change; no drive-by refactors (e.g. do not touch
  `cmdutil.ResolveReasoningEffort` or `cmd/serf/internal/launchcheck` even
  though they duplicate small pieces of this policy — they are already
  correct and out of the spec's anchors).
- TDD: failing test before implementation, per behavior.
- Every error message introduced or touched must name the invalid value AND
  the valid alternatives (vocabulary list, live-list alternatives, or
  supported-slug list as appropriate).
- Validation must fail open on enumeration/network failure — never block
  offline/test paths. The membership check rejects only a *definitive*
  "model absent from a successfully-fetched list"; it must never block on a
  timeout, dead credentials, or an adapter that can't list models.
- Multi-module gates per `docs/conventions/go-workspace.md`: `go build ./...
  && go test ./...` (exit codes only, never read `$?` after a pipe) in
  `llm`, `agent`, and the root module.
- Match surrounding style exactly (error wrapping idioms, comment density,
  doc-comment conventions already visible in each touched file).

---

### Task 1: Quota-403 classification

**Files:**
- Modify: `llm/usagelimit.go` (`parseUsageLimit` message-fallback)
- Modify: `llm/errors.go` (`case 403:` in `errorFromHTTPStatus`, ~:284-296)
- Test: `llm/usagelimit_test.go`, `llm/errors_test.go`

**Interfaces:**
- Consumes: existing `parseUsageLimit(raw any, now time.Time) (usageLimit,
  bool)`, `usageLimitMessage(limit usageLimit, now time.Time) string`,
  `quotaExceededError`.
- Produces: no signature changes. `parseUsageLimit` additionally reports a
  match when `error.message` contains the phrase `"usage limit"` even absent
  a recognized `error.code`/`error.type` (the real Kimi body's shape —
  `error.type` there is `"permission_error"`, a generic Anthropic-API error
  type, not a usage-limit code). `errorFromHTTPStatus`'s `case 403:` runs the
  same `parseUsageLimit` + `quotaExceededError` construction the `case 429:`
  branch already does, after the existing `cyber_policy_violation` check
  (which still returns early — a cyber-policy 403 is not a usage-limit 403).

- [ ] **Step 1: Write the failing tests.**
  - In `llm/usagelimit_test.go`: add a `const kimiBillingCycle403Body` holding
    the exact real body quoted above (captured from session
    `0341i3MDP7PKYfPqGhqPWO`, provider `kimi-anthropic-api`, 2026-08-05 —
    note the provenance in a doc comment same as `chatGPTUsageLimitBody`'s).
    Add `TestParseUsageLimit_MessageOnlyFallback` asserting
    `parseUsageLimit(rawBody(t, kimiBillingCycle403Body), now)` returns
    `ok=true` with `resetsAt` zero (the body has no reset fields) and
    `message` containing "usage limit for this billing cycle". Add a negative
    case: a 403 body with an unrelated `permission_error` message (e.g. "you
    do not have access to this resource") must NOT match (`ok=false`) —
    proves the substring match doesn't over-fire on the same `error.type`.
  - In `llm/errors_test.go`: add `TestUsageLimit403IsQuotaExceededAndNotRetryable`
    mirroring `TestUsageLimit429IsQuotaExceededAndNotRetryable`
    (`llm/usagelimit_test.go:28`): `ErrorFromHTTPStatus("kimi-anthropic", 403,
    msg, rawBody(t, kimiBillingCycle403Body), nil)`, assert
    `errors.As(err, &quotaExceededError{})`, `Kind(err) == KindQuotaExceeded`,
    `!Retryable(err)` (or the package's existing retryability accessor —
    match `TestUsageLimit429...`'s exact assertions), and
    `strings.Contains(err.Error(), "usage limit for this billing cycle")`.
    Add/confirm a regression that a **plain** 403 (no usage-limit body, e.g.
    `{"error":{"type":"permission_error","message":"insufficient
    permissions"}}`) still yields `accessDeniedError`/`KindAccessDenied` —
    this is probably `TestRegular403_StillNonRetryable`
    (`llm/errors_test.go:284`); confirm it still passes, extend if it
    doesn't already cover a body-bearing case.
- [ ] **Step 2: Run `go test ./llm/...`, confirm the new tests fail** (403
  still classifies as access-denied; the message-only fallback doesn't
  exist).
- [ ] **Step 3: Implement.**
  - `llm/usagelimit.go`: add `usageLimitMessagePattern(message string) bool`
    (lowercased `strings.Contains(..., "usage limit")`) with a doc comment
    explaining why ("Kimi's coding-plan Anthropic-compatible API returns
    `error.type=\"permission_error\"` on its billing-cycle limit, with no
    dedicated code — only message text names it; a precise phrase match
    avoids the broader false-positive surface of `classifyByMessage`'s bare
    `\"quota\"`/`\"billing\"` substrings"). In `parseUsageLimit`, after the
    existing `code` loop, if `code == ""` fall back to
    `usageLimitMessagePattern(message)`; if that's false too, return
    `(usageLimit{}, false)` unchanged. Keep `resets_at`/`resets_in_seconds`
    extraction unchanged (a message-fallback match still fills reset time
    when the body happens to carry one).
  - `llm/errors.go`: in `case 403:`, after the `cyber_policy_violation`
    branch's early `return`, add the same `now := time.Now(); if limit, ok :=
    parseUsageLimit(base.rawResponse, now); ok { ... return
    &quotaExceededError{...} }` shape the `case 429:` branch uses, then fall
    through to the existing `return &accessDeniedError{base}`.
- [ ] **Step 4: Run `go test ./llm/...`; all green.**
- [ ] **Step 5: Commit** (`fix(llm): classify a billing-cycle 403 as quota-exceeded, not access-denied`).

### Task 2: reasoning_effort vocabulary validation

**Files:**
- Modify: `llm/types.go` (new `ValidateReasoningEffort`, `ReasoningEffortVocabulary`)
- Modify: `agent/session_init.go` (`NewSession` entry)
- Modify: `agent/job_delegate.go` (`createDelegate`, ~:328)
- Modify: `agent/plugin/agents.go` (`ParseAgent` task parsing, ~:161-163)
- Test: `llm/types_test.go`, `agent/session_config_test.go` (or a new
  `agent/session_reasoning_effort_validation_test.go` if that file is
  already large — match existing file-naming convention), `agent/job_delegate_test.go`
  (or the existing delegate-args validation test file — grep for
  `TestCreateDelegate` / `delegation_allowance must be less` for the
  sibling pattern), `agent/plugin/agents_test.go`

**Interfaces:**
- Produces: `llm.ReasoningEffortVocabulary() []string` (the six levels in
  rank order, derived from/kept in sync with `effortRank`) and
  `llm.ValidateReasoningEffort(effort string) error` — nil for `""` (unset)
  and any known level (case-insensitive, trimmed, mirroring
  `NormalizeReasoningEffort`'s normalization but *not* treating
  none/off/0/etc as valid — those are the CLI's disable-alias sugar, already
  normalized to `""` before reaching this function at every call site below);
  a `%q (expected one of: ...)`-shaped error naming the vocabulary otherwise.
- Consumes at each seam:
  - `NewSession`: `cfg.ReasoningEffort` as passed by the caller (before
    `cfg.applyDefaults()`, which does not touch this field).
  - `createDelegate`: `args.ReasoningEffort` (the `delegate` tool's
    `reasoning_effort` arg), validated **before**
    `prepareSubagentRunWithModelSelection` is called, so a bad value fails
    the delegate tool call immediately (matching Task 3's "fail the call,
    don't burn a session" principle) instead of dying on the child's first
    turn.
  - `ParseAgent`: each task template's `reasoning_effort:` field, validated
    at parse time so a broken plugin/agent config fails to load with a
    clear error instead of silently baking a bad value into every session
    that uses it.

- [ ] **Step 1: Write the failing tests.**
  - `llm/types_test.go`: table-driven test over
    `ValidateReasoningEffort`: `""` → nil; each of the six known levels
    (any case/whitespace) → nil; `"ultra"` (the exact historical bad value)
    → error containing `"ultra"` and all six vocabulary levels.
  - `agent`: a `NewSession` test constructing `SessionConfig{ReasoningEffort:
    "ultra"}` and asserting `NewSession` returns a non-nil error naming
    `"ultra"` and the vocabulary, with **no session created** (mirror the
    existing nil-client/nil-profile early-return tests' assertion shape in
    `agent/session_init_test.go` or wherever `NewSession`'s validation tests
    already live — grep `func TestNewSession` for the convention).
  - `agent`: a delegate-dispatch test (scripted provider, no live model
    list needed) calling `createDelegate` / the `delegate` tool with
    `reasoning_effort: "ultra"` and asserting `delegateResult` carries a
    failure naming `"ultra"` and the vocabulary, with no child session
    spawned.
  - `agent/plugin/agents_test.go`: extend/add a test parsing an agent
    frontmatter block whose task has `reasoning_effort: ultra`, asserting
    `ParseAgent` returns an error naming `"ultra"` and the vocabulary
    (follow the existing `reasoning_effort: high` fixture at
    `agents_test.go:24` and `loader_program_fuzz_test.go:505` for the
    frontmatter shape).
- [ ] **Step 2: Run the `llm` and `agent` module tests, confirm all four new
  tests fail** (no validation exists yet; "ultra" is accepted everywhere).
- [ ] **Step 3: Implement `llm.ValidateReasoningEffort` +
  `ReasoningEffortVocabulary`** in `llm/types.go`, next to `effortRank`.
- [ ] **Step 4: Wire `NewSession`.** In `agent/session_init.go`, right after
  the existing nil-arg checks (before `resolveLiveModelProfileWithTimeout`
  or after — either is fine since they're independent; pick whichever reads
  more naturally next to the other early-return validations), add:
  ```go
  if err := llm.ValidateReasoningEffort(cfg.ReasoningEffort); err != nil {
      return nil, err
  }
  ```
- [ ] **Step 5: Wire delegate dispatch.** In `agent/job_delegate.go`'s
  `createDelegate`, after the `selectSubagentModel` error check (~:330) and
  before `prepareSubagentRunWithModelSelection` is reached, add:
  ```go
  if err := llm.ValidateReasoningEffort(args.ReasoningEffort); err != nil {
      return delegateStartFailed(err)
  }
  ```
- [ ] **Step 6: Wire plugin/agent config load.** In `agent/plugin/agents.go`'s
  task-parsing loop (~:161-163), after `tt.ReasoningEffort = v` is set (title
  already parsed by this point — use it in the error for context), validate:
  ```go
  if err := llm.ValidateReasoningEffort(tt.ReasoningEffort); err != nil {
      return Agent{}, fmt.Errorf("agent task %q: %w", tt.Title, err)
  }
  ```
  (Add the `primeradiant.com/serf/llm` import; `agent/plugin` is inside the
  `agent` Go module, which already depends on `llm` — same import path
  `agent/session_init.go` uses.)
- [ ] **Step 7: Run `go build ./... && go test ./...`` in `llm` and `agent`;
  all green.** Pay attention to any existing test that constructs a
  `SessionConfig`/delegate call/plugin fixture with a nonstandard
  `reasoning_effort` value (e.g. `"none"`, an already-normalized empty
  string, or a level with mixed case/whitespace) — those must keep passing
  unchanged; only genuinely unknown vocabulary should start failing.
- [ ] **Step 8: Commit** (`feat(agent,llm): validate reasoning_effort vocabulary at NewSession, delegate dispatch, and plugin-agent config load`).

### Task 3: Model-membership validation at NewSession and delegate dispatch

**Depends on:** none (independent of Tasks 1–2); Task 4 depends on this task.

**Files:**
- Modify: `agent/live_model_metadata.go` (extract shared fetch-and-fill core;
  add `resolveLiveModelProfileValidated`)
- Modify: `agent/session_set_model.go` (`validateModelSwitchMembership`
  gains live-list alternatives in its error; `resolveModelSwitchTarget`
  reuses the extracted core)
- Modify: `agent/session_init.go` (`NewSession` call site, ~:128)
- Modify: `agent/subagent_model_selection.go` (both explicit-model call
  sites, ~:67, ~:106)
- Test: `agent/session_init_test.go` (or wherever `NewSession`/live-model
  tests already live — grep `resolveLiveModelProfile`/`TestNewSession`),
  `agent/subagent_model_selection_test.go` (already has scripted-provider
  coverage for `selectSubagentModel` — extend it), `agent/session_set_model_test.go`

**Interfaces:**
- Produces:
  - `fillLiveModelMetadata(ctx, client, profile) (*provider.Profile,
    []llm.ModelInfo, bool)` in `agent/live_model_metadata.go`: the shared
    fetch-once core (fail-open on any `ListModels` error → `ok=false`,
    profile unchanged); `resolveLiveModelProfile` (existing, used by
    Restore and tests — signature **unchanged**) becomes a thin wrapper
    discarding `models`/`ok`.
  - `resolveLiveModelProfileValidated(client, profile) (*provider.Profile,
    error)` (new): 2s timeout (`liveModelMetadataTimeout`, unchanged
    constant), fills metadata via `fillLiveModelMetadata`, and — only when
    enumeration succeeded — runs `validateModelSwitchMembership`. Returns
    `(profile, nil)` unchanged on any enumeration failure (fail-open,
    identical to today's `NewSession` behavior in that case).
  - `resolveModelSwitchTarget` (existing, `session_set_model.go:83-104`):
    reimplemented on top of `fillLiveModelMetadata` (8s timeout,
    unchanged), same external behavior, no duplicated fetch-and-fill logic.
  - `validateModelSwitchMembership` (existing): unchanged signature; its
    not-a-member error gains a live-list alternatives clause (new
    `formatModelAlternatives(models []llm.ModelInfo, tag string, cat
    *llm.ModelCatalog) string` helper — visible model IDs, sorted, capped
    at 20 with a "+N more" suffix). This is the ONE place all three
    consumers (`SetModel`, `NewSession`, delegate dispatch) share, so all
    three get alternatives for free.
- Consumes: Task 4 will add a `client.ValidateModelCompatibility(provider,
  model string) error` call inside `validateModelSwitchMembership` — not
  part of this task; leave a `// TODO(WS7 Task 4)`-free gap, Task 4 inserts
  it directly (no seam needed now, it's a straight-line addition to the
  same function).

- [ ] **Step 1: Write the failing tests.**
  - `agent/session_set_model_test.go`: extend whatever test currently
    exercises `validateModelSwitchMembership`'s not-a-member error (or add
    one) to assert the error also lists at least one live alternative model
    ID from the scripted list.
  - A `NewSession` test: scripted client whose `ListModels` returns a fixed
    list NOT containing the requested profile's model; assert `NewSession`
    returns a non-nil error naming the requested model and an alternative
    from the list, and (critically) a **second** test with a client whose
    `ListModels` returns an error (or no client/nil profile) asserting
    `NewSession` still succeeds — the fail-open path must be unaffected.
  - `agent/subagent_model_selection_test.go`: extend with a scripted client
    whose live list doesn't contain an explicitly-requested delegate model;
    call `selectSubagentModel(ctx, "requested-model", "")` and assert the
    returned error names the model and an alternative. Add a companion test
    where `ListModels` fails/is absent, asserting selection still succeeds
    unvalidated (fail-open).
- [ ] **Step 2: Run `go test ./agent/...`, confirm the new/extended tests
  fail** (no alternatives in the message; `NewSession`/delegate dispatch
  don't validate at all yet).
- [ ] **Step 3: Implement the shared core + alternatives helper.**
  Extract `fillLiveModelMetadata` in `live_model_metadata.go`; rewrite
  `resolveLiveModelProfile` as a thin wrapper (no behavior change — confirm
  existing direct callers in `agent_misc_program_fuzz_test.go`,
  `cov_s5_helpers_test.go`, `session_misc_fuzz_test.go` still compile
  unchanged, since the signature isn't touched). Add
  `resolveLiveModelProfileValidated`. Rewrite `resolveModelSwitchTarget` in
  `session_set_model.go` on top of the same core. Add
  `formatModelAlternatives` and call it from
  `validateModelSwitchMembership`'s not-a-member `return
  fmt.Errorf(...)`.
- [ ] **Step 4: Wire `NewSession`.** Replace
  `profile = resolveLiveModelProfileWithTimeout(client, profile)`
  (`session_init.go:128`) with:
  ```go
  resolvedProfile, err := resolveLiveModelProfileValidated(client, profile)
  if err != nil {
      return nil, err
  }
  profile = resolvedProfile
  ```
  (Do **not** touch the `RestoreSession...` call site at `session_init.go:513`
  — out of scope per the spec's anchors; leave it on the unvalidated
  `resolveLiveModelProfileWithTimeout` so a resumed session with a
  since-retired model can still be inspected/read rather than failing
  closed on resume.)
- [ ] **Step 5: Wire delegate dispatch.** In both explicit-model branches of
  `selectSubagentModel` (`agent/subagent_model_selection.go`, ~:67 and
  ~:106), after `resolved, crossProvider, err :=
  s.resolveProfileForRef(base, explicitModel)` succeeds, add:
  ```go
  resolved, err = resolveModelSwitchTarget(s.client, resolved)
  if err != nil {
      return subagentModelSelection{}, fmt.Errorf("model override %q: %w", explicitModel, err)
  }
  ```
  (`s.client` — confirm the field name via `Session.Client()`'s
  implementation, `agent/session.go` — use whatever the struct field
  actually is, likely `s.client`.) Do **not** touch
  `resolvePluginAgentModel`'s own independent live-list check.
- [ ] **Step 6: Run `go build ./... && go test ./...` in `agent`; all
  green.** Also re-run `llm` (no changes there, just confirm no cross-module
  breakage) and root.
- [ ] **Step 7: Commit** (`feat(agent): validate model membership at NewSession and delegate dispatch, name live-list alternatives everywhere`).

### Task 4: Codex-backend model compatibility table

**Depends on:** Task 3 (extends `validateModelSwitchMembership`).

**Files:**
- Modify: `llm/client.go` (new optional `ModelCompatibilityValidator`
  interface + `Client.ValidateModelCompatibility`, mirroring the existing
  `ModelLister`/`ToolChoiceSupporter` pattern)
- Modify: `llm/providers/openai/responses.go` (`wireModel` table-driven;
  new `codexModelVariants` map)
- Modify: `llm/providers/openai/adapter.go` (`Adapter.ValidateModel`
  implementing the new interface)
- Modify: `agent/session_set_model.go` (`validateModelSwitchMembership`
  calls `client.ValidateModelCompatibility`)
- Test: `llm/client_test.go` (or wherever `ModelLister` dispatch is tested),
  `llm/providers/openai/gpt56_codex_test.go` (already the home of the
  `wireModel` tests), `agent/session_set_model_test.go`

**Interfaces:**
- Produces:
  ```go
  // ModelCompatibilityValidator is implemented by adapters that enforce a
  // static model-support map independent of live enumeration — e.g. the
  // OpenAI Codex backend, whose ChatGPT-account model set is narrower than
  // the platform API and isn't reliably distinguished by a live models list.
  type ModelCompatibilityValidator interface {
      ValidateModel(model string) error
  }

  // ValidateModelCompatibility runs an adapter's static compatibility check
  // for model, when the adapter implements ModelCompatibilityValidator; nil
  // (no opinion) otherwise, including for unknown providers.
  func (c *Client) ValidateModelCompatibility(provider, model string) error
  ```
  in `llm/client.go`, dispatch shaped exactly like `SupportsToolChoice`
  (`llm/client.go:372-384`: normalize provider name, look up the adapter,
  type-assert, no-op if absent).
  - `codexModelVariants map[string]string` in
    `llm/providers/openai/responses.go`: `{"gpt-5.6": "gpt-5.6-sol",
    "gpt-5.6-sol": "gpt-5.6-sol", "gpt-5.6-terra": "gpt-5.6-terra",
    "gpt-5.6-luna": "gpt-5.6-luna"}` — grounded in
    `llm/data/litellm_model_catalog.json`'s exhaustive `gpt-5.6*` entries
    (confirmed by grep during planning: exactly these four slugs exist;
    `gpt-5.6-mini` is not a cataloged model). Doc-comment this provenance so
    a future added variant updates both the catalog data and this table
    together.
  - `wireModel` becomes: `if a.usesCodexBackend() { if wire, ok :=
    codexModelVariants[model]; ok { return wire } }; return model` — same
    external behavior as today for every currently-tested case (bare
    `gpt-5.6`→`gpt-5.6-sol`; `gpt-5.6-terra` passes through unchanged;
    anything not in the table, including `gpt-5.6-mini`, passes through
    unchanged — rejection is `ValidateModel`'s job, not `wireModel`'s).
  - `Adapter.ValidateModel(model string) error`: no-op (`nil`) unless
    `a.usesCodexBackend()`; then `nil` if `model` is a `codexModelVariants`
    key, else an error naming `model` and the sorted distinct table values
    (`gpt-5.6-luna, gpt-5.6-sol, gpt-5.6-terra`).
- Consumes: Task 3's `validateModelSwitchMembership`
  (`agent/session_set_model.go`) — insert
  `if err := client.ValidateModelCompatibility(profile.ID(),
  profile.Model()); err != nil { return err }` as the **first** check in
  the function (before the live-list scan), because it's a static fact
  independent of network/enumeration state. This makes `SetModel`,
  `NewSession` (via `resolveLiveModelProfileValidated`), and delegate
  dispatch (via `resolveModelSwitchTarget`) all reject an unsupported
  Codex slug uniformly, for free.

- [ ] **Step 1: Write the failing tests.**
  - `llm/providers/openai/gpt56_codex_test.go`: add
    `TestValidateModel_CodexBackend` table-driven over
    `{"gpt-5.6", nil}, {"gpt-5.6-sol", nil}, {"gpt-5.6-terra", nil},
    {"gpt-5.6-luna", nil}, {"gpt-5.6-mini", error naming "gpt-5.6-mini" and
    "gpt-5.6-sol"/"terra"/"luna"}` against a codex-backend adapter, and a
    platform-API adapter (non-codex) for which `gpt-5.6-mini` is **not**
    rejected (ValidateModel is a no-op off the Codex backend — the platform
    API's own live model list is the authority there).
  - `llm/client_test.go` (or nearest fitting file): a test that
    `Client.ValidateModelCompatibility` is a no-op (`nil`) for an adapter
    that doesn't implement the interface, and for an unknown provider name.
  - `agent/session_set_model_test.go`: a `SetModel`/membership test with a
    scripted codex-backend-flavored client (or a fake adapter implementing
    `ModelCompatibilityValidator` directly, if wiring a real codex-flagged
    scripted adapter is awkward — match whatever fake-adapter convention
    the file already uses) asserting `gpt-5.6-mini` is rejected with the
    supported-slug list, even when `ListModels` would otherwise
    (incorrectly, for test purposes) include it — proving the static check
    runs first and independently of live enumeration.
- [ ] **Step 2: Run `go test ./llm/... ./agent/...`, confirm the new tests
  fail.**
- [ ] **Step 3: Implement.** `llm/client.go` interface + dispatch method;
  `llm/providers/openai/responses.go` table + `wireModel` rewrite;
  `llm/providers/openai/adapter.go` `ValidateModel` method; one-line
  addition to `agent/session_set_model.go`'s
  `validateModelSwitchMembership`.
- [ ] **Step 4: Run `go build ./... && go test ./...` in `llm` and `agent`;
  all green.** Specifically re-run the existing `gpt56_codex_test.go` suite
  in full to confirm `wireModel`'s table-driven rewrite is byte-identical
  in behavior for every pre-existing case.
- [ ] **Step 5: Commit** (`feat(llm,agent): table-drive Codex-backend model support and reject unsupported slugs at validation time`).

## Acceptance (whole workstream)

- A `NewSession` call with a live-enumerable, absent model fails closed,
  naming the model and live alternatives; the same call with dead/no
  credentials still succeeds (fail-open unchanged).
- A `delegate` call with an explicit unavailable model fails the delegate
  call (not the session), naming alternatives.
- A `NewSession`/delegate/plugin-agent-config `reasoning_effort: "ultra"`
  is rejected before any LLM call, naming the six-level vocabulary.
- The real captured Kimi billing-cycle 403 body classifies as
  `KindQuotaExceeded`, not `KindAccessDenied`, surfacing the provider's own
  "usage limit for this billing cycle" message.
- `gpt-5.6-mini` on a Codex-backend instance is rejected at validation time
  naming `gpt-5.6-luna, gpt-5.6-sol, gpt-5.6-terra`; the platform API and
  every non-`gpt-5.6` model are unaffected.
