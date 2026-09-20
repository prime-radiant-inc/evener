Native outbox reads now normalize an absent optimistic display to `undefined` and omit the outbox-only `attempted` field from accepted optimistic records. Behavioral tests also cover nonempty attachments, composer text, submitting-client identity, attempted settlement, and restoration that leaves another target's records untouched.

This addresses the bounded decoding and native conformance portions of #1927 and #1929. Their shared test harness/helper work remains open; #1928 and the deferred contention, notes, and ID-hardening work are unchanged. The production change is 21 touched lines, including comment cleanup.

Validation: 29 targeted tests against real `node:sqlite`, native typecheck, package import lint, and diff checks passed. The two runtime-shape assertions failed before the decoder change. Independent spec/quality/simplify review found no must-fix issues, and local RoboRev branch review 2585 passed at `7e44afc038bfbdbf311b5b1521e42d9e3aa41146`.
