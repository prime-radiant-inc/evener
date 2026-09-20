The current raw RoboRev panel for #1931 at `08fa72ce4582b1a304d4d4ae53fe979c6fc65e0c` has two Low test-maintenance findings. Per the queue rule, keep them out of the otherwise reviewed parent and address them in a small follow-up PR.

- `appwire-client/typescript/reducer.test.ts`: the warning scan-count regression also asserts the incidental order of field scans. Keep proof that each stored string is scanned once while allowing equivalent internal field order.
- `mobile/src/state/conversation.test.ts`: the parameterized warning fields are called `extra`, which is also an actual warning-params key; one case supplies title instead. Rename the parameter to describe the collection of supplied fields.

Acceptance: retain the behavioral scan-efficiency and warning-rendering assertions, run only the touched tests and formatting/type checks, and make no production behavior change.
