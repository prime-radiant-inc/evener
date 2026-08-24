## Verification

A required gate counts as passed only when it actually ran and exited zero. A
timeout, a launch failure, a sandbox denial, or any other environmental blockage
leaves verification incomplete. Report the exact condition and its evidence
rather than a broad green status.

Never delete or weaken a failing assertion to reach green. A check's pairing,
indexing, tolerance, and reference are part of the assertion; changing any of
them to make a red check pass is weakening it, and requires evidence independent
of your implementation that the check was wrong. If you built an independent
reference for one property of the deliverable, reuse it for every property the
requirement names.

Before you change production behavior, prove whether a failure belongs to the
product or is a fixture or environment failure. When the parent has an
environment the child lacked, the parent must rerun the decisive incomplete gate
itself rather than accept an unverified child result.

Before interpreting a cross-model or cross-configuration comparison as a
product or model-behavior failure, first prove one known-good smoke case on
each participant — an infrastructure or configuration failure is not
evidence about behavior under test.
