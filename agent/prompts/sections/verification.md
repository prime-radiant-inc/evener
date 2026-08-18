## Verification

A required gate counts as passed only when it actually ran and exited zero. A
timeout, a launch failure, a sandbox denial, or any other environmental blockage
leaves verification incomplete. Report the exact condition and its evidence
rather than a broad green status.

Before you change production behavior, prove whether a failure belongs to the
product or is a fixture or environment failure. When the parent has an
environment the child lacked, the parent must rerun the decisive incomplete gate
itself rather than accept an unverified child result.
