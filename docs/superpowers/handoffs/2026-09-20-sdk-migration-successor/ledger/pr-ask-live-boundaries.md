When a human-note carrier fails or yields while questions remain unanswered, the live session now returns to awaiting input, matching restoration from the durable transcript. This covers provider and generic failures, exhausted no-tool and tool-round budgets, queued-notification yields, and observer handoffs. Successful and cancellation boundaries retain their existing behavior.

Stacked on #1958. This is the second preservation slice replacing #1906; it changes 94 non-test lines. The complete stack retains every committed original regression, including five live-boundary tests carried in this slice, and adds two deterministic lifecycle tests for notification and observer yields. The original branch is preserved. #1907 remains the separate carrier-claim successor.

Validation: focused normal, race, and tagged agent tests; normal/tagged/Windows vet; golangci-lint; toolchain formatting; and diff checks passed. Independent spec/quality/simplify review found no must-fix issues, and local RoboRev branch review 2578 passed. Exact-head CI and remote raw panel qualification remain required before landing.

The existing Low text-evidence follow-up remains tracked in #1946. No global pending-ask fallback or asked-this-round counter behavior was added.
