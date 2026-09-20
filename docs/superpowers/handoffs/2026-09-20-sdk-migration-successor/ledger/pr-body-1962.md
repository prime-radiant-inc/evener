## Result

When a human-note carrier fails or yields while questions remain unanswered, the live session now returns to awaiting input, matching restoration from the durable transcript. This covers provider and generic failures, exhausted no-tool and tool-round budgets, queued-notification yields, and observer handoffs. Successful and cancellation boundaries retain their existing behavior.

The current B slice also fixes the transcript-poison refusal path so it uses the pending-aware failure boundary. The two missing TRIPWIRE timeout comments that caused the earlier check failure are fixed in test support; the split-lock race report was refuted by the serialized turn loop and is not carried as a production issue.

## Scope and stack

Stacked on #1958, this is the second preservation slice replacing part of #1906. The current implementation is 104 production lines plus focused regression coverage and retains the committed live-boundary regressions covered by the slice. #1907 remains the separate carrier-claim successor, and the original branch is preserved.

The A/B ask stack remains held pending the separate durable-admission C work now being implemented. The text-evidence Low remains separately tracked in #1946; it is not folded into this PR.

## Validation and status

Focused normal, race, and tagged agent tests; normal/tagged/Windows vet; golangci-lint; toolchain formatting; and diff checks passed for the current implementation. Local RoboRev2595 passed after the poison-path fix. The current head is `794679226ac1a202bce59b66cca25a31b5da8890` against target `main` `6cf3f0263887914e87dbc70d558979ecea143abb`. Current CI and the raw review panel are still pending, and the A/B hold remains active; Fresh exact-head CI and complete raw-panel qualification are required before landing.
