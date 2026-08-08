# Provider-Failure Feedback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement spec `docs/superpowers/specs/2026-08-07-provider-failure-feedback-design.md` (v7): early-stop for failing retry groups, salvage of partial output as persisted history, model-visible failure steering, and honest retry liveness.

**Architecture:** A phase/stats-aware attempt contract in `llm.RetryStream` powers two early-stop rules that raise `llm.ProviderUnhealthyError`. An agent-side round recorder retains per-attempt stats and best partials across the round's retry groups; settlement persists the best partial as a normal assistant turn plus a steering turn composed from templates. Events keep live clients consistent; the retry chip gains honest denominators and elapsed time.

**Tech Stack:** Go (daemon: `llm/`, `agent/`), TypeScript (web: `cmd/serf-hub/frontend/`), Go TUI (`cmd/serf-tui/`).

## Global Constraints

- Read the spec first; it is normative. Every wording rule in it (steering templates, exclusions) is copied here, but on conflict the spec wins.
- `FailFastAfter = 4` (streak), cap detection = 2 consecutive substantial attempts, substantial = content-event window ≥ 60s. **No byte thresholds anywhere** — no salvage floor (any nonzero salvage persists; wording scales), no cap byte floor.
- Content events for phase classification = text deltas + tool-arg deltas + reasoning deltas. Salvage = text + tool-arg extraction only; reasoning is NEVER salvaged.
- Both early-stop rules disabled when `FailFastAfter == 0`; non-agent `RetryStream` users must see zero behavior change.
- Existing kata r128 (retry-after-declined fallback) tests must stay green. `TurnFailure` markers stay model-invisible.
- TDD every task: failing test → red → implement → green → commit. Test output pristine. Never `git add -A`.
- Branch: `wip/provider-failure-feedback`. Commit style: see `git log --oneline -15`.

---

### Task 1: Attempt phase/stats contract + `ProviderUnhealthyError` + early-stop rules in RetryStream

**Files:**
- Modify: `llm/stream_retry.go`
- Create: `llm/provider_unhealthy.go`
- Modify: `llm/stream_generate.go` (adapt closure to new signature)
- Test: `llm/stream_retry_test.go` (extend), `llm/provider_unhealthy_test.go`

**Interfaces:**
- Produces:
  ```go
  type AttemptPhase int
  const (
      PhaseOpen        AttemptPhase = iota // rejected at/before stream open (429/5xx/auth)
      PhaseConsume                         // stream opened, content events flowed, then died
      PhaseSilentStall                     // stream opened, ZERO content events, ended in stall timeout
      PhaseFastReject                      // stream opened, ZERO content events, ended fast (decoded in-band rejection)
  )
  type AttemptReport struct {
      PartialOutput bool          // was partial output delivered to the caller (existing semantics)
      Phase         AttemptPhase
      ContentWindow time.Duration // first content event → last content event
      SalvagedBytes int           // text+tool-arg bytes accumulated (0 for reasoning-only)
  }
  type StreamAttempt func(ctx context.Context) (AttemptReport, error)
  // RetryStreamOptions gains: FailFastAfter int  (0 = both rules disabled)
  type ProviderUnhealthyError struct { // in provider_unhealthy.go
      Shape    string        // "stall" | "cap"
      Attempts int
      Elapsed  time.Duration
      LastErr  error
  }
  func (e *ProviderUnhealthyError) Error() string // "provider unhealthy after N stream failures (Xs): <last>"
  func (e *ProviderUnhealthyError) Unwrap() error // LastErr
  ```
- Rules (implement exactly): streak = `FailFastAfter` consecutive attempts with Phase ∈ {PhaseConsume, PhaseSilentStall} → stop, Shape "stall" (or "cap" if the last attempts were cap-shaped). Cap = 2 consecutive PhaseConsume attempts each with `ContentWindow >= 60*time.Second` → stop immediately, Shape "cap". PhaseOpen and PhaseFastReject are TRANSPARENT: neither count nor reset either rule. Success ends the group (unchanged).

- [ ] **Step 1: Write failing tests** in `llm/stream_retry_test.go`:

```go
func attemptScript(t *testing.T, reports []llm.AttemptReport, errs []error) (llm.StreamAttempt, *int) {
    calls := 0
    return func(ctx context.Context) (llm.AttemptReport, error) {
        i := calls; calls++
        if i >= len(reports) { t.Fatalf("unexpected attempt %d", i) }
        return reports[i], errs[i]
    }, &calls
}

func TestRetryStream_StreakStopsAtFailFastAfter(t *testing.T) {
    // 4 consume-phase failures -> ProviderUnhealthyError, exactly 4 attempts.
    rep := llm.AttemptReport{Phase: llm.PhaseConsume}
    e := llm.NewStreamError("p", "cut", nil)
    attempt, calls := attemptScript(t,
        []llm.AttemptReport{rep, rep, rep, rep}, []error{e, e, e, e})
    err := llm.RetryStream(context.Background(), llm.RetryStreamOptions{
        Policy: llm.RetryPolicy{MaxRetries: 10, BaseDelay: time.Nanosecond, BackoffMultiplier: 1},
        FailFastAfter: 4,
    }, attempt)
    var pu *llm.ProviderUnhealthyError
    if !errors.As(err, &pu) { t.Fatalf("want ProviderUnhealthyError, got %v", err) }
    if *calls != 4 { t.Fatalf("attempts = %d, want 4", *calls) }
    if pu.Attempts != 4 || pu.Shape != "stall" { t.Fatalf("bad stats: %+v", pu) }
}

func TestRetryStream_OpenPhaseTransparent(t *testing.T) {
    // stall,429,stall,429,stall,stall -> trips at the 4th stall (6 attempts total).
    stall := llm.AttemptReport{Phase: llm.PhaseSilentStall}
    open := llm.AttemptReport{Phase: llm.PhaseOpen}
    e := llm.NewStreamError("p", "cut", nil)
    attempt, calls := attemptScript(t,
        []llm.AttemptReport{stall, open, stall, open, stall, stall},
        []error{e, e, e, e, e, e})
    err := llm.RetryStream(context.Background(), llm.RetryStreamOptions{
        Policy: llm.RetryPolicy{MaxRetries: 10, BaseDelay: time.Nanosecond, BackoffMultiplier: 1},
        FailFastAfter: 4,
    }, attempt)
    var pu *llm.ProviderUnhealthyError
    if !errors.As(err, &pu) || *calls != 6 { t.Fatalf("calls=%d err=%v", *calls, err) }
}

func TestRetryStream_CapDetectionStopsAtTwo(t *testing.T) {
    long := llm.AttemptReport{Phase: llm.PhaseConsume, ContentWindow: 70 * time.Second, SalvagedBytes: 100}
    e := llm.NewStreamError("p", "cut", nil)
    attempt, calls := attemptScript(t, []llm.AttemptReport{long, long}, []error{e, e})
    err := llm.RetryStream(context.Background(), llm.RetryStreamOptions{
        Policy: llm.RetryPolicy{MaxRetries: 10, BaseDelay: time.Nanosecond, BackoffMultiplier: 1},
        FailFastAfter: 4,
    }, attempt)
    var pu *llm.ProviderUnhealthyError
    if !errors.As(err, &pu) { t.Fatalf("want ProviderUnhealthyError, got %v", err) }
    if *calls != 2 || pu.Shape != "cap" { t.Fatalf("calls=%d shape=%q", *calls, pu.Shape) }
}

func TestRetryStream_FastRejectTransparent_AndDisabledWhenZero(t *testing.T) {
    // FailFastAfter=0: 4 consume failures ride the policy budget (MaxRetries=3 -> 4 attempts, last err returned raw).
    rep := llm.AttemptReport{Phase: llm.PhaseConsume}
    e := llm.NewStreamError("p", "cut", nil)
    attempt, calls := attemptScript(t, []llm.AttemptReport{rep, rep, rep, rep}, []error{e, e, e, e})
    err := llm.RetryStream(context.Background(), llm.RetryStreamOptions{
        Policy: llm.RetryPolicy{MaxRetries: 3, BaseDelay: time.Nanosecond, BackoffMultiplier: 1},
    }, attempt)
    var pu *llm.ProviderUnhealthyError
    if errors.As(err, &pu) { t.Fatal("FailFastAfter=0 must not early-stop") }
    if *calls != 4 { t.Fatalf("calls=%d", *calls) }
}
```

- [ ] **Step 2: Run — expect compile failure** (`AttemptReport` undefined): `go test ./llm/ -run TestRetryStream -count=1`
- [ ] **Step 3: Implement.** Change `StreamAttempt` to the new signature; keep `partial, err := attempt(ctx)` call sites reading `rep.PartialOutput`. Track `consumeStreak` and `capStreak` ints in the loop: increment per rules after each failed attempt; reset both to 0 only on… nothing (success returns; open/fast-reject leave them untouched). When `opts.FailFastAfter > 0` and either rule trips, return `&ProviderUnhealthyError{...}` (Elapsed = time since first attempt — capture `start := time.Now()` at loop entry; inject a clock only if the package already does — it does not, wall clock is fine for stats). Update `stream_generate.go`'s closure to `return llm.AttemptReport{PartialOutput: partial}, err` (phase left zero-valued `PhaseOpen` — StreamGenerate passes no FailFastAfter, rules disabled).
- [ ] **Step 4: Green:** `go test ./llm/ -count=1`
- [ ] **Step 5: Commit** `feat(llm): attempt phase contract + ProviderUnhealthyError early stops in RetryStream`

### Task 2: Phase + stats observation in the agent's attempt closure

**Files:**
- Modify: `agent/session_stream.go` (`callModel`, `consumeModelStream`)
- Test: `agent/session_stream_attempt_test.go` (create)

**Interfaces:**
- Produces:
  ```go
  // consumeModelStream now ALSO returns its observation on every path:
  type attemptObservation struct {
      Partial       *llm.Response  // accumulator snapshot; nil only if nothing accumulated
      Phase         llm.AttemptPhase
      ContentWindow time.Duration
      SalvagedBytes int            // text + tool-arg bytes (not reasoning)
  }
  func (s *Session) consumeModelStream(ctx, req, st) (sessionModelResponse, attemptObservation, error)
  ```
- Classification inside `consumeModelStream`: record `firstContent`/`lastContent` timestamps on EVERY delta event type including `StreamEventReasoningDelta`; count `SalvagedBytes` only for text + tool-arg bytes. On error/no-finish: `Phase = PhaseConsume` if any content event was seen; else `PhaseSilentStall` when `errors.Is(err, llm.ErrSSEReadTimeout)` or elapsed ≥ 30s; else `PhaseFastReject`. Open-phase (Stream() returned error before any events) is classified in `callModel`, not here.
- `callModel`'s attempt closure returns `llm.AttemptReport{PartialOutput: partial, Phase: obs.Phase, ContentWindow: obs.ContentWindow, SalvagedBytes: obs.SalvagedBytes}` and passes `FailFastAfter: 4` in `RetryStreamOptions`. `s.client.Stream` error → `AttemptReport{Phase: llm.PhaseOpen}`.

- [ ] **Step 1: Failing test** — drive `consumeModelStream` with a scripted `llm.Stream` (use the existing `newSessionStreamAccumulator` seam and a `llm.NewChanStream`-fed fake): reasoning deltas then error → `PhaseConsume`, `SalvagedBytes == 0`, `Partial` non-nil with empty text; text deltas then error → `SalvagedBytes == len(text)`; no events then error wrapping `llm.ErrSSEReadTimeout` → `PhaseSilentStall`.
- [ ] **Step 2: Red.** `go test ./agent/ -run TestConsumeModelStream_Observation -count=1`
- [ ] **Step 3: Implement** (accumulate observation alongside existing switch; `acc.Response()` is returned in the observation even on the error path).
- [ ] **Step 4: Green** + full `go test ./agent/ -count=1`.
- [ ] **Step 5: Commit** `feat(agent): attempt observations from consumeModelStream`

### Task 3: Round salvage recorder + mid-chain abort + fallback non-eligibility

**Files:**
- Create: `agent/round_recorder.go`
- Modify: `agent/session_stream.go` (`callModel` records), `agent/session_model_call.go` (`callModelWithFallback` aggregates + aborts), `agent/session_init.go` (`modelFallbackEligible` arm)
- Test: `agent/round_recorder_test.go`, extend `agent/session_model_call` fallback tests

**Interfaces:**
- Produces:
  ```go
  type attemptRecord struct {
      Phase         llm.AttemptPhase
      Err           error
      Duration      time.Duration
      ContentWindow time.Duration
      SalvagedBytes int
  }
  type groupRecord struct {
      Model, Provider string
      Attempts        []attemptRecord
      BestPartial     *llm.Response // largest SalvagedBytes snapshot, captured before each retry
      BestBytes       int
  }
  type roundRecorder struct{ Groups []groupRecord }
  func (r *roundRecorder) BestSalvage() (partial *llm.Response, from *groupRecord) // across ALL groups
  func (r *roundRecorder) SteeringGroup() *groupRecord // salvage-producing group, else last consume-phase group, else nil
  func (r *roundRecorder) HasConsumePhaseFailure() bool
  ```
- `callModel` gains a `*groupRecord` parameter it fills (one per invocation). `callModelWithFallback` owns a `roundRecorder` per round, passes a fresh group to each `callModel`, and: after ANY group returns, `errors.As(err, &pu)` → abort the chain immediately and return the unhealthy error as terminal (preserved past last-error-wins). `modelFallbackEligible` gains, before all other checks: `var pu *llm.ProviderUnhealthyError; if errors.As(err, &pu) { return false }`.
- The recorder rides on the round: store it on `Session` as `s.currentRoundRecorder` set at round start in the round loop and read by settlement (Task 6). Single-goroutine per session round loop — no locking beyond existing `s.mu` conventions (check how neighboring per-round state is stored and match it).

- [ ] **Step 1: Failing tests:** recorder selection (fallback trickle never shadows primary partial; `SteeringGroup` prefers the salvage-producing group); `modelFallbackEligible(ProviderUnhealthyError{...})` == false; chain-walk abort: a scripted `callModel` (use the existing test seams in session tests for model calls) where the primary returns permanent error → chain walks → first fallback group returns ProviderUnhealthyError → second fallback NEVER runs and the terminal error is the unhealthy one.
- [ ] **Step 2: Red.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Green** incl. existing r128 tests: `go test ./agent/ -run 'Fallback|r128|Retry' -count=1` then full package.
- [ ] **Step 5: Commit** `feat(agent): round salvage recorder, mid-chain unhealthy abort, non-eligible fallback arm`

### Task 4: Salvage text extraction

**Files:**
- Create: `agent/salvage.go`, `agent/salvage_test.go`

**Interfaces:**
- Produces:
  ```go
  // salvageText renders a partial response's recoverable output:
  // text parts verbatim, in order; then for each incomplete tool call,
  // a marker block:
  //   [incomplete tool call: <name> — this call never executed]
  //   <field>: <extracted string value>
  // Returns "" when nothing recoverable. Reasoning parts are ignored.
  func salvageText(partial *llm.Response) string
  // partialJSONStringFields extracts ALL top-level string-valued fields
  // from possibly-truncated JSON object text, in encounter order.
  func partialJSONStringFields(raw string) []struct{ Key, Value string }
  ```
- Build `partialJSONStringFields` by generalizing the existing `partialJSONStringField` scanner in `agent/session_stream.go:273` (do not duplicate its escape handling — refactor the shared inner loop into a helper both call).

- [ ] **Step 1: Failing tests:** truncated `write_file` args `{"path":"a.md","content":"# Plan\nlots of tex` → both fields extracted, content unterminated-string tail preserved; text parts + tool marker ordering; reasoning-only partial → `""`.
- [ ] **Step 2: Red.** **Step 3: Implement.** **Step 4: Green (`go test ./agent/ -run TestSalvage -count=1` + package).**
- [ ] **Step 5: Commit** `feat(agent): salvage text extraction from partial responses`

### Task 5: Failure steering composer

**Files:**
- Create: `agent/failure_steering.go`, `agent/failure_steering_test.go`

**Interfaces:**
- Produces:
  ```go
  type settlementKind int
  const (
      settleNone settlementKind = iota // excluded class: no turns persisted
      settleSteeringOnly
      settleSalvageAndSteering
  )
  // classifySettlement applies the spec's gating: consume-phase rounds
  // settle; cancellations/context-length/open-phase/content-filter do not
  // (content-filter → settleSteeringOnly with filter wording ONLY when a
  // consume-phase failure exists; otherwise settleNone).
  func classifySettlement(rec *roundRecorder, terminalErr error) settlementKind
  // composeFailureSteering renders the steering text from the spec's
  // templates: shape from the SteeringGroup's attempts (stall / silent
  // stall / cap / decoded in-band), count-aware singular/plural, the
  // fallback-also-failed clause when the terminal group differs from the
  // steering group, draft wording when salvagedBytes is substantial and
  // fragment wording ("a small fragment (N bytes) was produced and not
  // delivered") otherwise, and the cap-shape "keep each response well
  // under that size" advice only for cap shape.
  func composeFailureSteering(rec *roundRecorder, terminalErr error, salvagedBytes int) string
  ```
- Copy the exact template sentences from spec component 3 — do not paraphrase.

- [ ] **Step 1: Failing table-driven tests:** every terminal class maps to exactly one template; 1-attempt permanent mid-stream failure never contains "repeatedly"; mixed round (primary stalls + fallback 401 terminal) → wording describes the stall group + "the configured fallback model also failed (authentication error)"; content-filter → filter wording, no draft reference; interrupt wording is NOT this composer's job (Task 7).
- [ ] **Step 2: Red. Step 3: Implement. Step 4: Green. Step 5: Commit** `feat(agent): failure steering composer`

### Task 6: Settlement persistence + live events

**Files:**
- Modify: `agent/session_model_call.go` (`handleModelError` terminal branch), `agent/session_events.go`
- Test: `agent/settlement_salvage_test.go` (create)

**Interfaces:**
- Consumes: `s.currentRoundRecorder`, `salvageText`, `classifySettlement`, `composeFailureSteering`, existing `appendSteeringTurn` (`agent/session_lifecycle.go:1414`) and assistant-turn persistence helpers (`appendAssistantTurn`, `agent/session.go:1398`).
- Behavior: in the terminal branch of `handleModelError`, before `emitTurnFailure`: run `classifySettlement`. For `settleSalvageAndSteering`: persist (1) a `TurnAssistant` whose content is one text part = `salvageText(best)`, stamped with model/provider provenance fields ONLY — grep `schema/turn.go:157-169` and set NONE of the Responses-continuation metadata fields; (2) a steering turn via `appendSteeringTurn`. Then emit the live sequence: `EventAssistantTextReset`, `EventAssistantTextStart`+`Delta`(salvaged text)+ item completion events matching what the projector needs (mirror the event sequence a normal assistant text turn produces — copy from the existing emit path in `consumeModelStream`), then the steering's own event, then `emitTurnFailure` (unchanged).
- Salvaged turn is persisted for ANY nonzero salvage. `TurnFailure` stays presentational.

- [ ] **Step 1: Failing tests:** end-to-end session test with a scripted failing model (existing fake-provider seams in `agent` tests — grep `LLMSleep`/scripted client usage): stall-streak round → steering-only turns persisted, transcript order `[STEERING][TURN_FAILURE]`; cap round with 10KB partial → `[ASSISTANT(salvaged)][STEERING][TURN_FAILURE]`, salvaged turn model-visible in `buildHistory` output and carrying no continuation metadata; cancellation → neither; context-length terminal with consume-phase primary salvage → salvage still persists (mixed-round precedence).
- [ ] **Step 2: Red. Step 3: Implement. Step 4: Green (`go test ./agent/ -count=1`). Step 5: Commit** `feat(agent): partial-preserving settlement with live-event consistency`

### Task 7: Interrupt salvage

**Files:**
- Modify: `agent/session_lifecycle.go` (interrupt marker path, ~line 673), `agent/session_model_call.go` (cancel branch)
- Test: extend `agent/settlement_salvage_test.go`

**Interfaces:**
- Behavior: when a turn cancellation/interrupt lands and `s.currentRoundRecorder` holds a nonzero `BestSalvage`, persist the salvaged assistant turn plus a one-line steering: `"This response was interrupted; the content above was produced before the interruption and was not delivered."` No provider-failure claim, no "continue" push, no failure steering composer involvement.

- [ ] **Step 1: Failing test:** interrupt mid-retry-group with a recorded 5KB partial → salvaged turn + interrupt steering persisted; interrupt with zero salvage → today's behavior exactly.
- [ ] **Step 2-4: Red / implement / green. Step 5: Commit** `feat(agent): salvage survives user interrupts`

### Task 8: Compaction atomicity of the salvage pair

**Files:**
- Modify: `agent/internal/contextmgr/` (only if the test fails — `safeCutoff` reportedly already walks back over `TurnSteering`)
- Test: `agent/internal/contextmgr/salvage_pair_test.go` (create)

- [ ] **Step 1: Write the test:** a history whose compaction cutoff would land between salvaged `TurnAssistant` and its steering turn → assert the cutoff walks back so the pair stays together; pair within `PreserveRecentTurns` tail → untouched.
- [ ] **Step 2: Run.** If green already (existing `safeCutoff` behavior), keep the test as a pin and note it in the commit. If red, extend `safeCutoff` minimally.
- [ ] **Step 3: Commit** `test(contextmgr): pin salvage-pair compaction atomicity`

### Task 9: Group-transition assistant-text reset

**Files:**
- Modify: `agent/session_model_call.go` (`callModelWithFallback` chain loop)
- Test: extend fallback tests

- [ ] **Step 1: Failing test:** primary group delivers partial output (PartialOutput=true recorded) then fails permanent → before the fallback group's `callModel` runs, `EventAssistantTextReset` is emitted exactly once (assert on the session event bus recorder used by existing event tests).
- [ ] **Step 2-4: Red / implement (emit reset in the chain loop before invoking the next group when any prior group had `PartialOutput`) / green. Step 5: Commit** `fix(agent): reset partial output between fallback groups`

### Task 10: Retry liveness — events + projector

**Files:**
- Modify: `agent/events/payloads.go` (`ModelRetryData` += `GroupElapsedMS int64`, `AttemptCap int`), `agent/session_stream.go` (`emitModelRetry` fills both; cap = policy budget until a consume-phase failure is recorded in the current group, then `FailFastAfter`), `internal/appprojector/appwire_projection.go` (forward both), `appwire/types.go` (`ThreadModelRetryParams` += `GroupElapsedMS`, `AttemptCap`)
- Test: extend `agent/events` payload tests + projector tests

- [ ] **Step 1: Failing tests:** `emitModelRetry` after 2 open-phase failures → `AttemptCap == policy.MaxRetries+1`; after 1 consume-phase failure → `AttemptCap == 4`; `GroupElapsedMS` monotonic. Projector forwards both fields on `serf/thread/modelRetry`.
- [ ] **Step 2-4: Red / implement / green (regenerate any TS types the repo generates — grep `types.gen.ts` build step). Step 5: Commit** `feat(events): honest retry chip data — GroupElapsedMS + AttemptCap`

### Task 11: TUI + web chip rendering and clearing rules

**Files:**
- Modify: `cmd/serf-tui/composer_render.go` (`composerRetryChip`: denominator = `AttemptCap`; append ` · <model>` when `retry.Model` differs from the session's primary model; append ` · <Nm> on this call` from `GroupElapsedMS`), `cmd/serf-tui/hub_notifications.go` (`clearModelRetryOnProgress`: REMOVE `NotifyAgentMessageDelta`, `NotifyReasoningSummaryDelta`, `NotifyToolOutputDelta` from the clear set; keep `NotifyTurnCompleted`, `NotifyTurnStarted`; keep `NotifyItemCompleted` ONLY for model-output items — the notification carries the item kind; systemMessage/user items must not clear), web `cmd/serf-hub/frontend/src/protocol/reducer.ts` + `LivenessLine.tsx` (same rules; render "in progress" while deltas flow instead of clearing)
- Test: TUI notification tests, web reducer tests

- [ ] **Step 1: Failing tests:** chip survives an assistant delta and a systemMessage item completion; clears on turn completion; renders `attempt 3/4`, model tag on fallback group, elapsed.
- [ ] **Step 2-4: Red / implement / green (`go test ./cmd/serf-tui/... -count=1`; `npm test` per frontend README). Step 5: Commit** `feat(ui): sticky honest retry chip in TUI and web`

### Task 12: Delegate surfacing

**Files:**
- Modify: `agent/session_lifecycle.go` (`noteParentJobActivity` call sites — add a retry/unhealthy job phase), `agent/subagents.go`/`agent/job_delegate.go` (failed delegate result gains the salvaged-draft note)
- Test: extend delegate job tests

- [ ] **Step 1: Failing tests:** child in a retry group surfaces a `jobPhaseModelRetrying` (new phase constant, rendered wherever job phases render — grep `jobPhaseModelStreaming` for the enumeration) in parent job activity; a failed delegate whose child transcript persisted a salvaged turn returns a result containing `"partial draft salvaged in the child transcript — resume it with delegate_send rather than re-dispatching"`.
- [ ] **Step 2-4: Red / implement / green. Step 5: Commit** `feat(agent): delegate visibility for retry grinds and salvaged drafts`

### Task 13: End-to-end integration scenarios + doc sync

**Files:**
- Create: `agent/provider_failure_e2e_test.go`
- Modify: `docs/agentic-testing.md` (append the new forensics surfaces: typed in-band errors in api.jsonl, salvaged turns in transcripts) — keep to the file's existing voice
- Test: the new file

- [ ] **Step 1: Write both incident-shape scenarios against a scripted fake provider:** (a) stall streak → settle ~4 attempts → steering persisted → resumed turn's `buildHistory` carries it; (b) cap shape (two ≥60s-window attempts with big partials) → 2-attempt stop → salvage + steering persisted → resume → history contains draft; assert the transcript outline (`serf-doctor transcript` rendering path — use the doctor package's renderer directly) shows the salvaged ASSISTANT turn and TURN_FAILURE.
- [ ] **Step 2: Green everything:** `go test ./... -count=1` (respect the repo's flake/timeout policy in `docs/` if a suite is known-slow).
- [ ] **Step 3: Commit** `test(agent): provider-failure end-to-end scenarios + forensics doc sync`

---

## Self-Review Notes

- Spec coverage: component 1 → Tasks 10–11; component 2 → Tasks 1–3, 9; component 3 → Tasks 4–8, 12; adapter fix → already landed (c3b80eb60) + Opus fleet agent (separate); testing section bullets map 1:1 onto task tests; requirements doc (cross-provider) → deliberately not planned (own spec later); the per-entry-filter extension point exists implicitly as the `errors.As` arm placement in Task 3 — no extra abstraction until the cross-provider spec needs it (YAGNI).
- Types cross-checked: `AttemptReport`/`AttemptPhase` (T1) consumed by T2 closure; `attemptObservation` (T2) feeds `attemptRecord` (T3); `roundRecorder` (T3) consumed by T5/T6/T7; `salvageText` (T4) consumed by T6/T7; `ModelRetryData` fields (T10) consumed by T11.
- Known sequencing: Task 1 changes `StreamAttempt`'s signature — `stream_generate.go` and `session_stream.go` must compile in the same commit (T1 updates both call sites mechanically; T2 then adds real classification).
