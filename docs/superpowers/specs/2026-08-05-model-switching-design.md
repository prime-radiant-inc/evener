# Recoverable Provider Failures Preserve Model Switching

Date: 2026-08-05
Status: Approved

## Summary

A provider can reject a turn for reasons the user can recover from by switching models: exhausted quota, invalid or expired credentials, retired model access, or provider policy. Serf currently turns a non-retryable `llm.Error` into a closed session. The web then disables its model control because the daemon advertises `ChangeModel=false` for closed sessions. This strands the user on the provider that failed.

Change the lifecycle rule: a terminal provider error fails the current turn and returns the session to idle. Provider retryability continues to control automatic retries, but no longer decides whether the conversation closes. The existing idle status and capability notification then restore model switching without a new protocol or UI feature.

## Problem

The observed sequence is:

1. A session runs on `kimi-anthropic/k3`.
2. Kimi rejects a turn with HTTP 403 because the billing-cycle quota is exhausted.
3. While the turn unwinds, a model-switch request correctly receives `session is processing`.
4. `agent/session_model_call.go:handleModelError` classifies the non-retryable `llm.Error` as terminal with `CloseSession=true`.
5. `handleModelError` calls `Session.Close()`.
6. `server/appwire_runtime.go:appCapabilities` advertises `ChangeModel=false` because the session is closed.
7. The web model control becomes disabled, so the user cannot switch to `openai/gpt-5.6-sol`.

The model-switch machinery itself already supports the requested cross-provider change. The bug is the provider-error lifecycle that runs before the switch.

## Goals

- Keep a session recoverable after a terminal provider response.
- Preserve the failed turn, provider error text, retry policy, and diagnostics.
- Restore idle capabilities after the failed turn, including `ChangeModel`.
- Let the existing web and TUI controls switch providers without reload or daemon restart.
- Keep active-turn switching forbidden until the failed turn reaches its boundary.

## Non-goals

- Do not retry quota, auth, or other non-retryable errors automatically.
- Do not add a quota-specific parser or Kimi-specific behavior.
- Do not add a closed-session model-switch escape hatch.
- Do not change cross-provider profile resolution, membership validation, model catalogs, or switch persistence.
- Do not add a recovery banner or change web copy.
- Do not keep a session open after explicit shutdown or genuine engine/session-integrity failures outside the provider-call error path.

## Design

### Error disposition

`classifyModelError` continues to choose among cancellation, one-time content-filter recovery, and terminal failure. Remove provider retryability as a source of session closure: a terminal `llm.Error`, whether retryable or non-retryable, ends the turn but does not close the session.

The `Retryable` property keeps its existing purpose. It determines whether the request retry policy may make another provider call. Exhausting retries or receiving a non-retryable response still produces one terminal failed turn with the original provider error.

Session closure remains owned by explicit lifecycle operations and failures that make continued session execution unsafe. No provider HTTP/API response alone proves that the transcript, tools, execution environment, or session state is unusable.

### Failure data flow

For a quota-like 403:

1. The adapter returns a non-retryable `llm.Error`.
2. Existing retry logic declines another automatic request.
3. `handleModelError` emits the existing turn-failure event and warnings.
4. It does not call `Session.Close()`.
5. `finishProcessingAtBoundary(..., SessionIdle)` settles the session.
6. The projector emits the failed turn and idle `thread/status/changed` notification.
7. Server capability stamping derives idle capabilities; `ChangeModel` is true because the model hook remains installed and the session is open.
8. The web reducer applies the capability set. `ModelSwitch` re-enables automatically.
9. Selecting `openai/gpt-5.6-sol` follows the existing `thread/model/set` path and applies on the next turn.

A switch attempted before step 5 still receives the current conflict response. This protects turn-boundary switching and requires no race-prone client exception.

### Components

#### Agent

Update the model-error decision in `agent/session_model_call.go` so `llmErrNonRetryable` does not set `CloseSession`. Remove the now-obsolete input or replace the close boolean with a lifecycle decision that cannot conflate request retryability with session viability. Keep the terminal failure, warning, and idle-boundary cleanup.

If a terminal provider error occurs while a session goal is active, terminate that goal through the existing error path before settling the session to idle. Otherwise an exhausted provider could immediately schedule another goal continuation and repeat the same doomed call. Goal termination and session closure are separate decisions: the goal stops, but the conversation remains available for a user-directed model switch.

Review comments and tests that currently state that every non-retryable `llm.Error` closes the session. Rewrite them to distinguish request-terminal, goal-terminal, and session-terminal outcomes.

#### Server and AppWire

No protocol change is required. The existing sequence already publishes a failed turn followed by idle status. `stampCapabilitiesOnStatusChange` attaches the capability set for the announced idle state, and `appCapabilities` advertises model switching for every open session.

The fix must verify this behavior end to end rather than add a second capability override.

#### Web and TUI

No production change should be necessary. Both surfaces gate model switching on the wire's `ChangeModel` capability. When the idle status frame carries the restored capability, the web button and TUI command become available through their existing state updates.

Add a web regression test to prove that a control disabled by an active status becomes enabled after a failed turn's idle status and can issue `thread/model/set`.

## Error handling

| Condition | Result |
|---|---|
| Switch while the provider turn is active | Existing conflict; no switch and no partial state change |
| Retryable provider error with retries remaining | Existing retry behavior |
| Retry policy exhausted | Failed turn; session returns idle |
| Non-retryable quota/billing response | Failed turn; session returns idle |
| Non-retryable auth/access/model response | Failed turn; session returns idle |
| Context-length failure | Existing warning plus failed turn; session returns idle |
| Explicit shutdown | Session closes |
| Engine/session integrity failure outside the provider-call path | Existing owning lifecycle may close the session |

The user sees the same provider error that failed the turn. Serf does not imply that switching will repair every error; it merely preserves the recovery action.

## Compatibility

- No AppWire methods, fields, codes, or notification shapes change.
- Successful model switching is unchanged.
- The active-turn conflict remains unchanged.
- Retry counts and provider-call volume remain unchanged.
- Failed-turn transcript and API-log evidence remain unchanged.
- Restored sessions and cold sessions keep their existing capability rules.
- Provider-specific behavior is avoided; all adapters receive the same lifecycle rule.

The intentional behavior change is that a non-retryable provider error no longer closes the conversation.

## Testing

Follow `docs/testing.md`; all default tests use scripted providers and require no credentials or network.

### Agent regression

Add or update a test in `agent/session_model_test.go` using a scripted adapter that returns a non-retryable quota-like `llm.Error`.

Assert:

- `ProcessInput` returns the provider error;
- the turn is recorded as failed;
- the provider call is not retried when marked non-retryable;
- `Session.State()` is `SessionIdle`, not `SessionClosed`;
- the session can accept a subsequent operation or turn.

Update the pure `classifyModelError` table so non-retryable provider errors have `CloseSession=false`. Retain coverage for cancellation, content-filter recovery, context warnings, and retry exhaustion.

### Daemon/AppWire regression

Exercise the real session loop and AppWire projection with a scripted provider failure. Assert notification ordering and state:

1. the failed turn completes as failed;
2. a following `thread/status/changed` announces idle;
3. that status carries `capabilities.changeModel=true`;
4. `thread/read` agrees;
5. a subsequent `thread/model/set` reaches the model hook.

This test must fail under the old `Session.Close()` behavior.

### Web regression

Render a session whose model control is disabled during an active turn. Apply the failed-turn completion and idle status/capability notification. Assert that the control becomes enabled, opens the picker, and selecting a model sends the qualified `thread/model/set` request.

The test uses the existing fake AppWire client and catalog fixtures; it makes no live provider call.

## Acceptance criteria

1. A scripted non-retryable quota error fails one turn without closing the session.
2. The first idle status after that failure advertises `ChangeModel=true`.
3. The web model control re-enables without reload.
4. Switching from the failed provider to another configured provider succeeds through the existing endpoint.
5. The next turn uses the new provider/model.
6. A switch submitted before the failed turn reaches idle still returns the existing conflict.
7. No default test depends on Kimi, OpenAI, credentials, quota, or network access.
