The `tests` job for #1916 at `8d7732de2baa376378c94d627dbd8a5b895044da` failed `TestDaemonRetirementProcessAdmittedWorkResetsInterval` in `cmd/evener-hub` after 90.37 seconds. This is a different test and module from the agent retirement-eligibility failures tracked in #1879; its root cause has not yet been established.

Evidence: https://github.com/prime-radiant-inc/evener/actions/runs/35401357514/job/105781738298

Failure: `daemon_retirement_e2e_test.go:1395: the reset interval never expired: retirement beat claim_consumed: watchdog deadline`.

The emitted event trace shows:
- The fake clock starts at `00:33:20Z` and arms a one-hour interval.
- An advance to `01:03:20Z` leaves 30 minutes; admitted work disarms it.
- After provider release and session settlement, the interval arms for one hour at `01:03:20Z`.
- Another `armed` event appears at `01:33:20Z` with a full one-hour duration.
- The final advance to `02:03:20Z` still reports 30 minutes remaining, and `claim_consumed` never arrives before the watchdog.

Investigate ordering between admitted-work settlement, the timer rearm, and the test's fake-clock advances. Do not widen the watchdog or classify this as #1879 without mechanism evidence. Add deterministic coverage for the actual ordering defect, if confirmed.

Discovered while reconciling the #1934 handoff. The memo had not read this job and tentatively attributed it to #1879. No local reproduction or fix has been attempted yet. Searched existing issues for the full test name and “reset interval”; no match found.
