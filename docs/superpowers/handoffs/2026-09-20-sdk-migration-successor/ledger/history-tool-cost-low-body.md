Low test-coverage follow-up from PR #1982, exact head e6d550e472a0ed09d8290665a8682bbb97336ca2, remote DeepSeek review 22130.

The existing no-tool and one-tool identity-read guards catch the original quadratic collector. An independent immutable-helper reproduction measured old 9747f949 as 72→272 reads (bound 232) and 460→1720 (bound 1420), both failing; the current implementation performs zero counted ordinary-item ID reads and passes. The claim that these tests are vacuous for every implementation is therefore refuted. Keep these regressions.

The narrower remaining gap is that the fixtures do not grow and count active tool-call/result candidates. A regression confined to work after the callId guard could evade the ordinary-item counters. A probe with 20→40 genuine active calls measured 60→120 reads on current code, versus 480→1760 on the old collector.

Add a separate deterministic N/2N active-call/result guard through the real mergeOlderItemPage path, assert a nonzero measured count, preserve meaningful output/field-precedence assertions, and verify a representative quadratic mutation makes it fail. Avoid elapsed-time thresholds. This is test coverage only; no production correctness defect was found. Keep it separate from the otherwise qualified #1982 patch under the Lows-only follow-up rule.
