# AppWire item-only flag-day design

## Approval and scope

The user approved this design in chat with “do it” and requested parallel Luna implementers. This document records that approved boundary; it supersedes the migration/legacy-paging promises in the original atomic transcript-item paging design and plan. Baseline: `41790e3f43ddebae912a87cfe318398a631d2671`.

## Contract

AppWire becomes `evener-appwire-v4`. Normal Go and browser initialization rejects incompatible versions; there is no capability negotiation, downgrade, or retry against old paging. Preserve the separately scoped adapter-native initialization contract, not an old-daemon fallback.

Transcript `thread/read` and `thread/turns/list` have one paging unit: items. Remove `TranscriptPageUnit`, all request/response `PageUnit` fields, `ThreadReadParams.TurnLimit`, and `ThreadTurnsListParams.Limit`. Keep `ItemLimit`, normalized by the existing `NormalizeTranscriptItemLimit`: non-positive defaults to 40, positive values above 40 are invalid. List requests require a nonempty opaque item cursor; numeric legacy cursors are not accepted. Retired paging fields must not activate a compatibility path. Reject them at the existing request decoding boundary rather than silently interpreting an old request as an unbounded read.

Transcript reads with `IncludeTurns` return bounded item fragments. Metadata-only reads remain valid. Preserve existing method names and item response-validator function names to avoid unrelated API renaming. Item positions, transcript keys, fragment metadata, and response-byte limits remain mandatory. Logical turns remain containers; their existence does not imply a second paging mode. Empty successful responses remain valid under the single contract.

Keep grouped projection/index machinery, logical ordinals including empty groups, `NextEntry`, stable IDs, accounting, deletion fences, enrichment, and lifecycle behavior. Agent/doctor transcript semantics and the separate per-turn item API are outside this paging removal. Their similarly named limits are not obsolete transcript paging fields.
## Clients and documentation



## Acceptance

Use deterministic scripted transport/provider fixtures. First demonstrate failing regressions for item-only default behavior, old-version rejection, retired paging requests, and native bounded source operation. Preserve existing identity, cancellation, byte-budget, and ordinal assertions while migrating their input contract. Deleting tests of removed helpers is permitted only with retained coverage of still-required behavior; do not weaken a failing assertion to obtain green.

Run focused package tests/races, frontend gates, generation/format/lint checks, and independent review. Do not run full local `make test`, `make test-race`, `make merge-approval-gate`, or all-module equivalents; full tests/races run in CI after publication. Limit concurrent writers to three; use low-parallelism focused commands on this heavily loaded host. No push until reviewed and focused gates pass.
