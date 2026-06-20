# sidecar-approval-broker-communicate: approval broker reacts to a caller communicate frame

**What this covers**: the human approval broker use case from the
observer-sidecar research. The sidecar should stay idle until the main
agent emits an explicit approval request, then package the decision and
send it back through `delegate_send(to="caller")`.

## Pre-state

- Fresh `serf` and `serf-hub` from the branch under test.
- Hub running on `127.0.0.1:9180`; `TOKEN=$(cat ~/.serf/auth-token)`.
- `tmpdir=$(mktemp -d -t serf-e2e-approval-broker-XXXXX)`.
- When testing Kimi fluency, spawn with `model` set to
  `kimi/kimi-for-coding`.
- Repeat with `openai/gpt-5.4-mini` when that model is available. If an
  inherited `OPENAI_API_KEY` is exhausted but `serf openai status` shows
  OAuth is signed in, start the test hub with `OPENAI_API_KEY=` and keep
  the normal XDG state home so OAuth remains visible.

## Steps

1. Spawn a parent session via `/api/spawn` with `working_dir=$tmpdir`.
2. Use this prompt:

   > Run the approval broker sidecar scenario.
   > 1. Start an observer delegate with `agent_type` "subagent" and
   >    `max_wait_ms` 120000. Its task is: "You are APPROVAL_BROKER.
   >    First turn call the communicate tool with exactly
   >    APPROVAL_READY. Later, when a Watch frame arrives whose
   >    event.message contains APPROVAL_REQUEST, call
   >    delegate_send(to=\"caller\") with exactly APPROVAL_PACKET
   >    decision=approve target=staging-deploy, then call the
   >    communicate tool with exactly APPROVAL_RECORDED. For unrelated
   >    Watch frames, call the communicate tool with exactly
   >    APPROVAL_IGNORED."
   > 2. After the observer returns APPROVAL_READY, create a `job_watch`
   >    with target "caller", events ["communicate"], and send.to set to
   >    the observer delegate_id.
   > 3. In the response after the watch creation result, call the
   >    communicate tool with exactly APPROVAL_REQUEST action=deploy
   >    target=staging-deploy risk=low.
   > 4. When the observer callback arrives, call the communicate tool
   >    with exactly SCENARIO_DONE approval-broker.
   >
   > Use the delegate result, watch result, approval request result, and
   > observer callback as the happy-path signals. Diagnostic job or
   > transcript inspection is only for recovering from an actual error.

## Expected

- The parent waits for `APPROVAL_READY` before creating the watch.
- The watch condition is `events: [communicate]`. Attempts to use
  `events: [assistant.message]` should be rejected before a watch is
  installed.
- The observer sends `APPROVAL_PACKET` through
  `delegate_send(to="caller")` and then records `APPROVAL_RECORDED`
  with `communicate`.
- The parent does not use `job_list` or `job_read_output` as a waiting
  mechanism before the callback.
- The watch has no dropped deliveries or self-loop verdict.

## Doctor audit

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$SID" --count job_list
go run ./cmd/serf-doctor transcript "$SID" --count job_read_output
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count delegate_send
```

## Sharp edges

- Kimi previously chose `assistant.message` for caller-message
  scenarios. That is now an invalid event selection; a fluent run
  should recover by creating a `communicate` watch.
- Explicit `agent_type:"subagent"` must include `delegate_send`; without
  that tool, the observer can only finish with `communicate` and cannot
  wake the caller through the callback path.
- A terminal notification for the observer's watch-delivery job can
  arrive after `SCENARIO_DONE` as confirmation of `APPROVAL_RECORDED`.
  Treat a follow-up summary as noise, not as evidence of polling.
