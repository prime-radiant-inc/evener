# Mobile checkpoint status

**Updated 10 September 2026.** The current integration line includes merged reasoning lifecycle #1073 and steering identity #1099. The full combined canonical gate passed at `3dcde7559ff21ce273ba94d57d12e9528f8b9dac`; its evidence is `/tmp/evener-combined-3dcde-merge-gate.log`. That gate includes 720 native tests, 686 shared tests, and the installed API package check. The older backend-only gate at `f8f0d6b29` is superseded.

## Review and integration

| Area | Current state | Qualification needed |
| --- | --- | --- |
| Activity (#1091) | Current head `279f32e7a` | New CI and review remain pending; the Medium generation-race and Low failure-glyph findings are still open on this head. |
| Native checkpoint (#1096) | Head `5da0ef15f`; CI green | Current review remains pending. |
| Environment (#1098) | Root finished the public rollback regressions | Broad tests and lint are running; final integration review remains open. |
| Steering (#1099) | Merged | Included in the combined gate and post-merge review. |
| Round timing (#1100) | Head `789c932be`; CI green | Current review remains pending. |
| TestFlight (#1039) | Head `244b980af`; stacked on native | RoboRev is clean; current CI is still required. |
| Diagnostic follow-up (#1101) | Merged at `2664cc881`; current CI and RoboRev are green | One initial CI identity-test failure remains unreproduced. It is neither fixed by that rerun nor a basis for a general gate claim. |

The current review inventory is a release checkpoint, not a release declaration. A current-head review and its relevant CI evidence must be read before merging each remaining item.

## Durable simulator evidence

The [paired restart receipt](assets/2026-09-10-paired-restart-d983.json) records a real backend restart followed by an app cold launch. It preserves 31 canonical persisted conversation items, seven draft tables, and 11 reader-preservation entries. The owned reader changed during the journey, so the receipt does not claim that every table remained unchanged. It is evidence for the recorded restart path and does not qualify a physical iPhone, performance, or TestFlight archive.

## Apple delivery state

Apple has internal builds 1, 2, and 3; there is no fresh build 4. Build 3 is valid and internally available. Drew is registered in the external group, but Apple reports `NOT_INVITED`, so external access is unavailable. The beta review contact, demo, and notes fields are not filled. A fresh current-source archive, upload, processing receipt, internal availability, physical installation/update, and smoke result remain pending.

Jesse paused iPad implementation/qualification and dedicated accessibility work. Android and voice remain deferred. Those areas are preserved for later work and do not block this iPhone-only v1 checkpoint.
