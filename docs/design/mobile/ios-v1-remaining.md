# iPhone v1 remaining work

This checklist follows the approved iOS-only scope. Jesse paused iPad implementation/qualification and dedicated accessibility work on 8 September. Android and voice remain deferred. They are not v1 blockers.

## 1. Usable: checkpoint and daily loop

Complete the current integration checkpoint before expanding scope. Resolve the remaining review and CI state for #1091, #1096, #1098, #1100, and #1039 using current heads. For #1091, the Medium generation-race and Low failure-glyph findings need a current-head resolution. For #1098, finish the running broad tests and lint after the public rollback regressions. Read #1039’s required current CI result even though its RoboRev result is clean.

Produce a fresh current-source internal TestFlight build before the feature-acceptance pass. Build 4 must have a source identity, archive and upload evidence, Apple processing and internal availability, physical installation/update, and an actual smoke result. Existing builds 1–3 cannot substitute for this current candidate. Drew remains unavailable externally while Apple reports `NOT_INVITED`; missing beta contact, demo, and notes details remain delivery follow-up.

Use the [paired restart receipt](assets/2026-09-10-paired-restart-d983.json) as the simulator baseline. Re-run the durable backend restart and app cold launch on the final candidate and preserve transcript, queue, environment, draft, and reader results. The receipt currently proves 31 canonical conversation items, seven draft tables, and 11 reader-preservation entries; the owned reader changes, so do not convert that evidence into an all-tables-unchanged claim.

The daily iPhone loop then needs observed interruption recovery around the implemented conversation lifecycle: reconnect, stopped-turn recovery, queue and steering state, durable environment context, and transcript restoration. Record actual device behavior and daemon readback for each exercised path.

## 2. Useful: implemented functionality that still needs current-artifact qualification

The source audit and existing implementation cover hub identity and lifecycle, hub switching, hub-scoped drafts, reconnect, profile removal, conversation paging and fragments, stable transcript identity, live merge, rich Markdown and images, saved reader position/offset and Latest behavior, question and creation sheets, approvals, task/activity/plugin and marketplace surfaces, provider/profile management, browser and project navigation, and SDK notification/mutation helpers. A source route or focused unit test does not close a workflow.

Qualify the current iPhone artifact against representative data and interruption conditions:

- hub credential rotation, uncertain writes and process death;
- conversation paging, streaming/image combinations, authenticated image recovery and long-transcript loading;
- nested delegate/output paging and native output-line selection;
- question and creation draft isolation, concurrent approval batches, stale rejected submissions and reconnect;
- task/activity, plugin/marketplace lifecycle, hub settings conflicts and two-hub identity;
- provider sign-in, endpoint clearing, browser/device recovery and credential failure;
- session organization, automatic loading and opening speed, which Jesse identified as immediate product priorities.

The receipt and current native tests provide useful slices of this work. They do not replace a physical-device pass or representative performance measurements.

## 3. Good: interaction and delivery quality

After the current build and Useful workflow evidence are available, measure loading and opening speed with representative session history, verify keyboard/composer and error recovery behavior, and inspect the visual hierarchy of the active iPhone flows. Keep performance and physical-device qualification as explicit evidence rows.

Accessibility and iPad qualification remain paused by Jesse and should not be assigned as v1 completion work. Android and voice remain outside the current delivery scope.

Close the iPhone v1 checkpoint only when each supported iPhone workflow has a current build identity, relevant deterministic checks, observed device behavior, and authoritative daemon results, including ordinary interruption recovery. Do not call the entire mobile project or release ready from the combined gate alone.
