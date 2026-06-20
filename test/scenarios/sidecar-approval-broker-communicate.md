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

## Steps

1. Spawn a parent session via `/api/spawn` with `working_dir=$tmpdir`.
2. Use this prompt:

   > Run the approval broker sidecar scenario.
   > 1. Start an observer delegate with `max_wait_ms` 120000 and this
   >    exact task: "You are APPROVAL_BROKER. First turn communicate
   >    exactly APPROVAL_READY. Later, for a Watch frame whose
   >    event.message contains APPROVAL_REQUEST, extract delivery_id and
   >    delegate_send to caller exactly APPROVAL_PACKET
   >    delivery=<delivery_id> decision=approve target=staging-deploy
   >    expires=2026-06-20T23:59:00Z. Then communicate exactly
   >    APPROVAL_RECORDED delivery=<delivery_id>. For nonmatching
   >    frames, return bare APPROVAL_IGNORED and use no tools."
   > 2. After the observer returns APPROVAL_READY, create a `job_watch`
   >    with target `caller`, events ["communicate"], and send it to the
   >    observer with message "Approval broker check.". Capture the
   >    watch_id.
   > 3. Communicate exactly APPROVAL_REQUEST action=deploy
   >    target=staging-deploy risk=low.
   > 4. Wait for the observer packet if needed, clear the watch, then
   >    communicate exactly SCENARIO_DONE approval-broker.

## Expected

- The parent waits for `APPROVAL_READY` before creating the watch.
- The watch condition is `events: [communicate]`, not
  `events: [assistant.message]`.
- The observer sends `APPROVAL_PACKET` to the caller and then records
  `APPROVAL_RECORDED` with the same delivery id.
- The watch is cleared and has no dropped deliveries or self-loop
  verdict.

## Doctor audit

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count delegate_send
```

## Sharp edges

- Kimi sometimes chooses `assistant.message` for caller-message
  scenarios. That is a fluency failure here because it wakes the
  observer on the parent's own tool-call turns.

