# Remove `on_idle` from `delegate_send`

## Goal

Make `delegate_send` reliable and simple: a valid message to an idle delegate always wakes or resumes that delegate. Callers should not need to know whether the target is currently running before sending a message.

## Behavior

- Remove the `on_idle` argument from the `delegate_send` tool schema and documentation.
- A message to a running delegate is steered into its active turn, as today.
- A message to an idle, retained/resumable delegate starts its next job and delivers the message.
- A message to an idle delegate whose runtime must be restored uses the existing restore path.
- Invalid, unknown, non-messageable, disposed, busy/disposal-gated, and non-resumable targets retain their existing errors.
- `max_wait_ms` behavior is unchanged: omitted or zero returns without waiting; a positive value waits for a newly started job, while live steering returns on delivery.
- `delegate_send` continues to accept only child `delegate_id` targets; caller callbacks and watch delivery routes are unchanged.

This is an intentional breaking API change. Callers that send `on_idle="fail"` must remove that argument. The prior `target_idle` failure is no longer a normal result for an otherwise resumable delegate.

## Implementation boundaries

1. Update argument decoding and target classification so no idle policy is parsed, validated, or passed through the send path.
2. Remove the idle-failure branch and let the existing resume/start path handle every idle resumable delegate.
3. Remove `on_idle` from the tool definition and revise its description to state the always-wake behavior.
4. Update `docs/job-control.md` and any related generated/help text to describe the new contract and remove examples that pass `on_idle`.
5. Update deterministic unit, contract, seed, and fuzz fixtures that encode the old option or `target_idle` default behavior.

No changes are planned to delegate ownership, restoration validation, worktree disposal, watch routing, or wait semantics.

## Testing

Use deterministic Go tests with the existing test harness:

- Assert an omitted-argument `delegate_send` wakes an idle delegate and delivers the message.
- Assert running delegates still receive a live steer.
- Assert the schema does not expose `on_idle` and rejects obsolete arguments according to the repository's normal argument handling.
- Exercise invalid and non-resumable target errors to ensure removing the policy does not broaden resumability.
- Run the focused agent package tests and the repository's applicable deterministic gate.

## Compatibility and migration

This change removes the fail-fast mode rather than silently changing its name or retaining a hidden compatibility alias. Existing callers should send the message without `on_idle`; they should handle the existing target/resumability errors when a delegate genuinely cannot be resumed.
