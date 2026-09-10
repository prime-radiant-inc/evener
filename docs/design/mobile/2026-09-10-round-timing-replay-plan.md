# Durable round timing replay proposal

Status: implemented and published as [PR #1100](https://github.com/prime-radiant-inc/evener/pull/1100), stacked on steering ownership #1099. Real live/full/indexed comparisons now include all controlled-session items and assert timing presence in both inline and delayed paths. Failing filesystem and actual compaction-fold regressions pass. Review identified provider-context handling that has been corrected in repair, delegate snapshots, token/recent-history accounting and evaluation requests; agent continuation and elicitation follow-up is under final local verification. The [paired native restart](assets/2026-09-10-paired-restart-cf88.json) verifies persisted conversation continuity on its recorded artifacts while separately disclosing omitted startup diagnostics. Final current-head CI/RoboRev and merged-artifact qualification remain required.

## Observed defect

A real Session using the production lossless event consumer emits a round timing system item after each model round. The live item takes a transcript position, but the saved transcript contains no corresponding record. In a two-round turn, the final answer has live item ordinal 4 and cold ordinal 3. Stable steering ownership alone cannot repair that disagreement. Removing the timing item from a test oracle would hide the defect.

The reproducible full comparison is preserved privately with the owned simulator fixture as `round-timing-replay-regression.go`. The steering-owner PR tests its own persisted owner and steering carrier identity separately and does not claim complete multi-round replay parity.

## Proposed change

Add a dedicated presentational `TurnRoundTimings` record and schema-owned timing payload. Reuse the existing append transaction to persist the record before emitting `EventRoundTimings`. Store it in the session's semantic history for transcript/fold consistency, while excluding it from provider history through the existing presentational-record filter. Preserve every current round timing UI field, summary, and structured raw value.

Cold projection renders the saved record into the same system item that live projection already publishes. Logical grouping treats it as a continuation of the current turn. The derived index version changes so cached counts and boundaries are rebuilt using that rule. No historical timing data is invented for transcripts that lack it.

Keep this in its own PR after the current steering identity change. Do not migrate web request/state ownership or change transcript key formats as part of this repair.

## Implementation sequence

1. Add the persisted kind and payload without importing event types into schema. Share or narrowly convert the presentation fields so live and saved renderings agree.
2. Add a durability-first timing publisher at the three existing timing emission sites. On write failure, publish the normal warning and do not invent a durable timing item.
3. Exclude timing records from model inputs and exercise the existing fold/publication paths so diagnostics do not change provider continuation behavior.
4. Project the saved record and classify it as a logical continuation in full and indexed readers.
5. Restore the complete real-session live/cold comparison, including intermediate timings and later answers, then walk one-item saved pages and compare identities and positions.
6. Run focused writer-failure, model-history, projection, and append-index regressions; canonical CI and RoboRev must pass before merge.
7. Rebuild the owned backend and repeat the signed native app's multi-round and actual hub-restart journey. Record exact source and artifact hashes.

## Why the alternatives do not repair the defect

Attaching final timings to the prior tool-result entry requires rewriting an append-only record after the measured persistence step. Attaching them to the next answer changes order and loses timing-only final rounds. Filtering the UI or deduplicating by text conceals the identity mismatch and removes existing functionality. Reserving arbitrary numeric gaps changes the paging/key contract without supplying a durable timing record.

Other ephemeral diagnostics need an independent audit; this proposal is bounded to the demonstrated round timing defect.
