# 10 September iPhone checkpoint

This is the current evidence map for the iPhone candidate. The [status page](status.md) records moving review state; the [paired restart receipt](assets/2026-09-10-paired-restart-d983.json) records the durable simulator observation.

## Gate result

The full combined canonical gate passed at `3dcde7559ff21ce273ba94d57d12e9528f8b9dac`, with evidence in `/tmp/evener-combined-3dcde-merge-gate.log`. It includes 720 native tests, 686 shared tests, and the installed API package check. The earlier `f8f0d6b29` result was backend-only and is superseded.

#1073 reasoning lifecycle and #1099 steering identity are merged. #1101 is merged at `2664cc881` with green current CI and RoboRev. Its one initial CI identity-test failure remains unreproduced; the green rerun does not establish a root-cause fix or broad gate status.

The remaining review inventory is #1091 at `279f32e7a`, #1096 at `5da0ef15f`, #1098 with root-finished public rollback regressions, #1100 at `789c932be`, and #1039 at `244b980af`. #1091 has open Medium generation-race and Low failure-glyph findings. #1096 and #1100 have green CI but pending review. #1098 still has broad tests/lint running. #1039 has clean RoboRev but current CI pending.

## What the simulator receipt proves

The restart and cold launch restored 31 canonical persisted conversation items, seven draft tables, and 11 reader-preservation entries. The owned reader changed during the journey, so the receipt does not claim every table remained byte-identical. The receipt supports durable restart behavior for this observed path; it is not physical-device, performance, or TestFlight evidence.

## Delivery order

The checkpoint build and fresh internal TestFlight delivery come before the full feature-acceptance pass. Existing internal builds 1–3 are historical/currently available evidence, but there is no build 4 from the final candidate. The next delivery receipt must identify the current source, archive, upload, processing, internal availability, installation/update, and smoke result.

After that receipt exists, complete the remaining iPhone functionality and quality checks in the [usable/useful/good plan](ios-v1-remaining.md). iPad and dedicated accessibility work are paused; Android and voice are deferred.
