# Summary Failure Diagnostics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make failed summaries diagnosable from safe warning fields and the existing private daemon log, without changing compaction or provider routing behavior.

**Architecture:** Preserve terminal and previous route failures inside the cheap-model caller, then convert them into a bounded summary-specific diagnostic at the context-manager boundary. Publish that diagnostic with the existing warning and log it once at the session publication boundary. Existing clients keep rendering message/title/hint; recorded producer output proves the whole path.

**Tech Stack:** Go, existing `llm` typed errors and `log/slog`, AppWire, TypeScript, Vitest, existing browser/native warning renderers.

**Spec:** `docs/superpowers/specs/2026-10-15-summary-failure-diagnostics-design.md`, approved by Jesse on 2026-10-15. Both independent design reviewers reported no significant findings against SHA256 `49b6cfab24f312ae666336c32fbbb43d2b4151c97aa26be70a622cb54290b5bf`.

## Global Constraints

- Preserve existing model selection, refusal learning, retry counts, fallback eligibility, cancellation, warning visibility, and history/checkpoint retention.
- A successful fallback still completes normally without a summary-failure warning.
- Do not enable full API logging, call live providers in tests, restart a session, deploy a binary, add a new log store, or redesign warning widgets.
- Omit arbitrary provider error codes, raw `err.Error()` output, raw finish reasons, URLs, headers, bodies, prompts, summary text and credentials.
- Assertions use typed fields, request counts, selected routes, persisted data and state transitions, not wording.
- Local summary validation must not alter API-attempt settlement: `SettleResult` continues receiving only `apiErr`.
- Read `docs/developing-evener/testing.md` before tests and the design-system guide before touching frontend tests. Do not add dependencies or compatibility layers. Never run `npm ci` through a symlink.
- Record progress in this plan's checkboxes; the session task list tracks phase status only. Full repository gates belong to CI; run heavy local gates sequentially.

## Review Focus

1. A shared refusal probe or a refusal latch changes which route runs. Attribute the selected route honestly and preserve the existing call count; cover in Task 1.
2. An earlier cheap refusal outranks a later server failure in aggregate `llm.Kind`. Report the terminal server failure and retain the cheap refusal separately; cover in Tasks 1 and 2.
3. Identifiers contain control characters, invalid UTF-8, or excessive length. Warning and log serialization remain bounded and cannot inject log lines; cover in Task 2.
4. A delegate shares its root daemon's stderr, or the logger fails. Attribute the delegate correctly, emit once, and preserve useful session work; cover in Task 3.
5. A failed summary is followed by successful fallback or later compaction. Install a real summary, retain human messages and transcript content, and produce no failure record for success; cover in Tasks 2–4.

## Proposed interfaces and file responsibilities

These are implementation decisions, not changes to the approved behavior.

- `agent/internal/cheapmodel/resolution_error.go`: new internal-routing evidence. `AttemptFailure` holds `Provider string`, `Model string`, and `Err error`. `ResolutionError` holds `Terminal AttemptFailure`, `Previous []AttemptFailure`, and its original aggregate cause. Its `Error() string` and `Unwrap() error` preserve the existing cause. This raw-error type must never be serialized into a warning or log.
- `agent/diagnostic/summary.go`: safe types and formatting. `SummaryAttempt` has `Provider`, `Model`, `Route`, `Kind`, `Stage`, `Reason` string fields, `Status int`, and `Retryable *bool`. `SummaryFailure` has `Terminal SummaryAttempt` and `Previous []SummaryAttempt`. String categories are controlled values, never provider text. Route is `cheap`, `session` or `none`; kind uses known `llm.Kind` names plus `cancelled`, `configuration`, `local_budget` and `rejected_output`; stage is `provider`, `pre_dispatch`, `validation` or `unknown`. Use `unknown` where evidence does not establish a category or stage. Constructors are `SummaryAttemptFromError(provider, model, route string, err error) SummaryAttempt` and `SummaryFailureInfo(failure SummaryFailure) (message string, info Info)`. Limit identifiers to 256 UTF-8 bytes, replace controls/invalid UTF-8, accept only HTTP statuses 100–599, and retain at most two previous route failures. Unknown retryability is nil, distinct from false.
- `agent/events/payloads.go`: add `WarningCodeSummaryFailure = "summary_failure"` and `WarningData.SummaryFailure *diagnostic.SummaryFailure` with JSON name `summary_failure,omitempty`. Do not add fields to AppWire's strict `raw.warning` object.
- `agent/internal/contextmgr/summary_failure.go`: typed validation reasons and the error wrapper carrying safe `diagnostic.SummaryFailure` alongside its unwrap cause. Own `summarizerFailureWarning(err error) events.WarningData` here instead of enlarging `context_manager.go`.
- `agent/summary_diagnostics.go`: `func (s *Session) logSummaryFailure(data events.EventData)` writes only code, actual session identity and the safe diagnostic through the existing process logger. No new writer, file, config option or persistence owner.
- Cross-client fixtures under `agent/testdata/summaryfailurewire`, with a shared test loader at `appwire-client/typescript/testing/summaryFailureWireFixtures.ts`, are recorded from real Go producers and projection, never handwritten warning JSON.

## Task 1: Preserve route-specific failure evidence

**Files:** create `agent/internal/cheapmodel/resolution_error.go` and `resolution_error_test.go`; modify `agent/internal/cheapmodel/caller.go` and its existing tests where needed.

**Consumes:** existing `Caller.CompleteConfigured` and `CompleteRouted`, both retaining their current signatures.

**Produces:** `*cheapmodel.ResolutionError` discoverable with `errors.As`, containing the actual terminal route/cause and earlier route causes. Existing `errors.Is`, `llm.Kind`, `llm.Classify`, refusal learning and attempt settlement retain their results.

- [ ] Write `TestResolutionErrorPreservesTerminalRoute` using scripted adapters. Cases: direct cheap 429; no configured cheap model; unservable cheap route; refusal-latched cheap route; cheap refusal then active 503; both routes refused; cancellation while waiting for a probe. Assert selected provider/model, terminal kind/status, earlier cause, exact call order, and unchanged `ranSessionModel`. Do not describe a skipped route as attempted.

  For the cheap-refusal/active-503 case, using independently chosen model IDs `cheap-fixture` and `active-fixture`:
  ```go
  var failure *cheapmodel.ResolutionError
  if !errors.As(err, &failure) { t.Fatal("missing route evidence") }
  if failure.Terminal.Model != "active-fixture" || llm.Kind(failure.Terminal.Err) != llm.KindServer { t.Fatal("wrong terminal failure") }
  if len(failure.Previous) != 1 || failure.Previous[0].Model != "cheap-fixture" { t.Fatal("missing prior failure") }
  if !errors.Is(err, activeErr) || !errors.Is(err, cheapErr) { t.Fatal("lost underlying cause") }
  ```

- [ ] Run `go test ./agent/internal/cheapmodel -run '^TestResolutionError' -count=1`; observe the missing metadata failure. If types must first be declared for compilation, rerun and observe a behavioral assertion fail before implementing capture.
- [ ] Attach route evidence where `caller.go` knows which route it selected and which error came from that route. Preserve the original aggregate error as the unwrap cause. Leave probe synchronization, fallback decisions, API scopes and public signatures unchanged.
- [ ] Run `go test ./agent/internal/cheapmodel -count=1` and `go test -race ./agent/internal/cheapmodel -count=1`. Verify both successful fallback and error classification regression cases pass.
- [ ] Format only touched Go files, inspect the staged diff and commit this tested unit.

## Task 2: Publish safe, actionable summary warnings

**Files:** create `agent/diagnostic/summary.go`, `summary_test.go`, `agent/internal/contextmgr/summary_failure.go`, and `summary_failure_test.go`; modify `agent/events/payloads.go`, `agent/internal/contextmgr/context_manager.go`, `summarizer_warning_canary_test.go`, and relevant summary tests. Modify strategy files only if required to preserve their current warning and recovery contracts.

**Consumes:** Task 1's `ResolutionError`; existing `llm.Error`, `llm.Kind`, `ConfigurationError`, `ContextBudgetError` and cancellation identities.

**Produces:** a bounded `WarningData.SummaryFailure`, stable code `summary_failure`, and safe route/category/status in the warning message. Title and hint come from summary-specific controlled classifications; generic `diagnostic.FromError` stays unchanged.

- [ ] Add `TestSummaryFailureDiagnostics` and `TestSummaryFailureWarningsNeverExposeProviderContent`. Table cases cover every `llm.ErrorKind`, bare/wrapped deadline and cancellation, missing credentials/sign-in, local budget, unknown errors, invalid HTTP status, unknown versus false retryability, and hostile identifier encoding. Put distinct opaque canaries in raw error message, error code, response body, prompt and summary text. Independently scan every serialized warning field for their absence.

  The scripted HTTP-429 case must establish these facts before checking the privacy canaries:
  ```go
  if warning.Code != "summary_failure" || warning.SummaryFailure == nil { t.Fatal("missing diagnostic") }
  got := warning.SummaryFailure.Terminal
  if got.Model != "cheap-fixture" || got.Kind != "rate_limit" || got.Status != 429 { t.Fatal("wrong failure details") }
  if got.Retryable == nil || !*got.Retryable { t.Fatal("lost retry disposition") }
  encoded, err := json.Marshal(warning)
  if err != nil { t.Fatal(err) }
  for _, canary := range canaries { if bytes.Contains(encoded, []byte(canary)) { t.Fatal("private content escaped") } }
  ```

- [ ] Run `go test ./agent/diagnostic ./agent/internal/contextmgr -run 'TestSummaryFailure' -count=1` and observe behavioral failures before implementing the diagnostic conversion.
- [ ] Implement the safe types/formatting and context-manager wrapper. Known provider kinds use the existing LLM taxonomy; cancellation, configuration, local budget and local validation get their own controlled categories/stages. Unknown errors remain unknown. Formatting never receives raw provider text.
- [ ] Replace the local validation errors with typed reasons at their sources: `empty_reply`, `missing_summary_section`, `output_limit`, `empty_batch`, `uncondensable_partner_messages`, and `missing_model`. Preserve validation rules. Errors raised before a route selection have no invented model. Keep `SummaryAttemptFromError` focused on typed error classification; local validation supplies its controlled reason directly.
- [ ] Add `TestSummaryFailureRoutingAndRecovery`: cheap validation rejection then active failure, cheap rejection then active success, internal cheap refusal fallback, missing cheap route, and later successful compaction. Assert terminal/prior attribution, provider call order, actual installed summary/history and unchanged API-attempt settlement. Cover automatic, explicit, checkpoint-pred and session-log warning paths plus partner condensation; retain predictive checkpoint's existing informational recovery.
- [ ] Run `go test ./agent/internal/contextmgr ./agent/internal/cheapmodel ./agent/diagnostic ./agent/events -count=1`. Run the relevant new tests under `-race`. Review assertions for copy oracles and implicit provider calls, format and commit.

## Task 3: Retain safe evidence and prove session recovery

**Files:** create `agent/summary_diagnostics.go`, `agent/summary_failure_journey_test.go`, and `cmd/evener-hub/summary_failure_daemonlog_test.go`; modify `agent/session_events.go` and extend existing hook tests only where needed.

**Consumes:** Task 2's warning code and safe typed payload.

**Produces:** one structured process-log record per published failure, with the actual session identity. The hub-owned inherited stderr log persists that record with API body logging disabled. No runtime deployment changes.

- [ ] Write `TestSummaryFailureJourney` around a real `Session`, scripted LLM boundary, actual compaction, event consumer, Notification hook input and temporary transcript. Fail a summary, then return a valid summary and continue a model turn. Assert structured warning details, retained human content/transcript, installed summary, and no failure record on the successful operation.
- [ ] Write `TestSummaryFailureDaemonEvidence` with the existing hub `openDaemonLog` owner and production slog sink. Reopen its file after a real session compaction failure; assert owner-only permissions and parse session/code/route/kind/status/retryability. Body logging stays off. Include a delegate identity and an erroring sink case; neither logging failure nor a full non-authoritative event buffer may change compaction's outcome. Capture and validate all expected error output.

  Independently parse retained log records and count the summary-failure code before and after recovery; do not count every stderr line:
  ```go
  if failureCount != 1 || recoveryFailureCount != 0 { t.Fatal("wrong diagnostic lifecycle") }
  if loggedSessionID != sess.ID() { t.Fatal("wrong session attribution") }
  if fileInfo.Mode().Perm() & 0o077 != 0 { t.Fatal("diagnostic log is not private") }
  if !summaryInstalled || !humanMessagesPreserved || !nextTurnCompleted { t.Fatal("session did not recover") }
  ```

- [ ] Run `go test ./agent ./cmd/evener-hub -run '^TestSummaryFailure' -count=1`; observe absent log metadata before implementation.
- [ ] Add the narrow log helper at the open-session publication boundary in `sendEventContext`, outside `eventsMu`. Do not tie it only to channel delivery: delegates may publish through their synchronous callback. Emit safe fields with slog structured attributes or a JSON-encoded safe payload; do not call `Error()` or serialize the raw wrapper. Do not emit a second record through daemon/CLI event consumers.
- [ ] Repeat the focused command, then race-check the new agent and hub tests. Tests replacing the global logger must not use `t.Parallel` and must restore it. Commit the tested unit.

## Task 4: Verify existing clients and document ownership

**Files:** create `agent/summary_failure_wire_test.go`, `agent/testdata/summaryfailurewire/README.md`, its generated JSON fixture, `appwire-client/typescript/testing/summaryFailureWireFixtures.ts`, and `appwire-client/typescript/summaryFailureWire.test.ts`; extend `internal/appoverlay/notices_test.go`, `cmd/evener-hub/frontend/src/panes/session/transcript/messages/SystemNoticeItem.wire.test.tsx`, `mobile-native/src/projectedRows.test.ts`, and CLI/TUI warning tests. Update `docs/product/history-content.md` and `docs/product/subsystems.md` S09/S21, linking the private daemon evidence location and unchanged recovery behavior.

**Consumes:** real failure/recovery events from Task 3, existing appoverlay projection, shared reducer and warning adapters.

**Produces:** checked-in producer evidence exercised through the strict shared parser, existing browser/native renderers and CLI/TUI adapters. No production client renderer/schema changes are planned.

- [ ] Add a deterministic Go producer recording test following `agent/testdata/systemeventwire` conventions. Its default mode verifies the checked-in fixture, not silently rewrites it. Record only safe warning/projection data and an observable successful recovery, never private prompt/error canaries. Document the explicit fixture regeneration command in its README.
- [ ] Add `summaryFailureWire.test.ts` and renderer cases. Assert model/provider sentinel identities, numeric HTTP status, stable warning code and preservation of producer fields; do not match authored warning prose or obtain expected categories from the implementation being tested. Confirm strict `raw.warning` parsing and existing visibility policy. CLI tests must inspect the message it actually prints, not just title/hint.
- [ ] Run `go test ./agent ./internal/appoverlay ./cmd/evener ./cmd/evener-tui -run 'SummaryFailure|Warning' -count=1`. Before Node checks, inspect the existing preflight scripts and dependency directories, and install/reuse dependencies safely without deleting a shared install.
- [ ] From `cmd/evener-hub/frontend`, run `npm test -- src/panes/session/transcript/messages/SystemNoticeItem.wire.test.tsx ../../../appwire-client/typescript/summaryFailureWire.test.ts` and `npm run typecheck`. From `mobile-native`, run `npm test -- src/projectedRows.test.ts` and `npm run check`. Both wrappers forward filters to Vitest; verify their output names the intended test files and reports no failures.
- [ ] Run touched-file Biome from its owning tree, changed-package Go lint, and `git diff --check`. Update the owning product guide and subsystem rows with verified behavior. Commit the complete user journey.

## Final verification and landing

- [ ] Compare the whole diff and evidence against every spec requirement. Run two independent competing implementation reviews against immutable base/head, including new fixtures and the approved spec. Resolve legitimate findings red-first.
- [ ] Use `simplify-code:simplify-code`, rerun affected checks, then `shepherd-pr:shepherd-pr`. One complete edit round, one push, one matching review/CI wait. Inspect matching review bodies and own per-commit findings.
- [ ] Once repository merge policy is satisfied, admin squash merge with exact head matching, verify GitHub's merged SHA/content, then clean up only the owned worktree/branch/scratch after proving the squash contains them.
- [ ] Report behavior and tests, PR/merge SHA, cleanup and any limits. Do not deploy or restart live sessions.
