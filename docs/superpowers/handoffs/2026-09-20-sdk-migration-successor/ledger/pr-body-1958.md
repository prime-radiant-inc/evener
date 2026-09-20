## Result

Restoration derives pending questions from the same steering and failure boundaries as the live session. Human-note rounds preserve unanswered questions, resolving user steering clears them, and forked history uses the correct session provenance instead of a child journal with reused mutation IDs.

## Scope and stack

This is the transcript/restore slice replacing part of #1906. Its live-boundary successor is #1962; carrier-defer work remains separate in #1907. The original implementation is preserved as `codex/ask-boundary-original-8c-backup`.

## Findings and dependencies

The raw panel's same-round reminder finding was refuted by the complete restore scan: after a non-resolving reminder, the outer scan still reaches the resolving user entry before any older ask round. The optional combined-fixture coverage remains #1946. The A/B ask stack is held pending the separate durable-admission C work now being implemented; this restore slice does not claim that successor's live-boundary fixes or qualification.

## Validation and status

Focused ask/oracle tests and race tests; normal/tagged/Windows vet; tagged compile gate; toolchain formatting and lint passed. Independent spec/quality/simplify review and local RoboRev2570 passed the original implementation. The current head is `ee578eab458abf9f0923752b5d04fcd6e4fd01da` against target `main` `6cf3f0263887914e87dbc70d558979ecea143abb`; the own scope remains 384 non-test lines. Current CI and the raw review panel are still pending, so this PR is not merge-ready. Fresh exact-head CI and complete raw-panel qualification are required before landing.
