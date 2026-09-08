# AppWire protocol coverage inventory

This inventory is generated from the authoritative `appwire.Methods` and `appwire.Notifications` catalogs at HEAD `759e7b10f`, with the current packaged recipe set and credential/OAuth evidence from the same delivery line. The `turn/steer` and optional drain-input recipe mapping reflects the current queue recipe working tree and its deterministic contract; it has no live-hub or native acceptance claim here. It distinguishes cookbook presence, deterministic contract tests, real hub acceptance, and native qualification. A recipe or contract is not evidence of live-hub or native acceptance; “deterministic contract” below means the recipe logic contract, not server method acceptance. Dated acceptance links below are evidence from separate controlled owned fixtures, with their stated scope and limits. No operation was performed for this inventory.

## Counts and interpretation

The structured catalog contains **91 methods** and **36 notifications**. The cookbook inventory names **65 distinct methods** across **22 recipes** and explicitly observes three notification names in `coverage.mjs`; these are inventory counts, not outcome or branch coverage. “Actual acceptance” means the cited artifact reports a real owned hub or native run for that surface; it does not mean every branch or platform is qualified. `thread/turns/items/list` is cataloged but explicitly `unimplemented` and remains the one reserved unsupported entry.

## Methods

| Method | Scope | Cookbook recipe(s) | Evidence currently present | Unassessed or remaining qualification |
|---|---|---|---|---|
| `initialize` | `connection` | `oauth.mjs`, `credentials.mjs`, `commands.mjs`, `session-settings.mjs`, `preferences.mjs`, `session-lifecycle.mjs`, `streaming-rejoin.mjs`, `organization.mjs`, `plugins.mjs`, `plugin-management.mjs`, `instances.mjs`, `marketplaces.mjs`, `approvals.mjs`, `questions.mjs`, `goals.mjs`, `tasks.mjs`, `job-output.mjs`, `activity.mjs`, `queue.mjs`, `repository-trust.mjs`, `project-layer.mjs`, `inspect.mjs` | Live prerequisite in [SDK evidence](sdk-management-evidence.md#packaged-session-settings-and-command-catalog--7-september); no handshake-specific assertion | handshake failure/skew and native qualification not assessed here |
| `ping` | `connection` | — | — | live keepalive acceptance + native qualification not assessed here |
| `thread/list` | `both` | `inspect.mjs` | recipe | server/live acceptance + native qualification |
| `thread/read` | `both` | `session-settings.mjs`, `session-lifecycle.mjs`, `streaming-rejoin.mjs`, `approvals.mjs`, `questions.mjs`, `goals.mjs`, `queue.mjs` | [SDK management/question/reader/approval evidence](sdk-management-evidence.md#structured-question-recipe-and-real-completion), [reader evidence](reader-continuity-evidence.md#environment-identity-across-shutdown-7-september-2026), [approval evidence](approval-evidence.md#direct-v4-execution-through-native-and-packaged-sdk-decisions) | remaining failure/platform cases are documented in cited evidence |
| `thread/unsubscribe` | `both` | `session-lifecycle.mjs`, `streaming-rejoin.mjs` | recipe + deterministic logic contract `streaming-rejoin.contract.mjs` | server/live acceptance + native qualification |
| `thread/turns/list` | `both` | `streaming-rejoin.mjs`, `questions.mjs` | recipe + deterministic logic contract; question/reader workflows cite bounded reads | real paging/anchor restoration across all platforms not assessed here |
| `thread/turns/items/list` | `unimplemented` | — | Catalog only; server marks unimplemented | Reserved unsupported entry; no new functionality required for v1; classify expected rejection |
| `thread/start` | `hub` | `session-lifecycle.mjs` | Real native question session creation is cited in [question evidence](real-question-harness-evidence.md#direct-v4-questions-restart-and-keyboard-qualification) | standalone SDK creation not assessed here; Android is deferred beyond the iOS-only v1 scope |
| `thread/resume` | `hub` | — | — | server/live acceptance + native qualification |
| `thread/fork` | `hub` | — | — | server/live acceptance + native qualification |
| `thread/clear` | `both` | — | — | server/live acceptance + native qualification |
| `thread/model/set` | `both` | `session-settings.mjs` | Real packaged SDK setter acceptance in [SDK evidence](sdk-management-evidence.md#packaged-session-settings-and-command-catalog--7-september) | provider-specific and native qualification not assessed here |
| `evener/thread/name/set` | `both` | — | — | server/live acceptance + native qualification |
| `thread/reasoning-effort/set` | `both` | `session-settings.mjs` | Real packaged SDK setter acceptance in [SDK evidence](sdk-management-evidence.md#packaged-session-settings-and-command-catalog--7-september) | provider/native failure matrix not assessed here |
| `thread/vision-model/set` | `both` | `session-settings.mjs` | Real packaged SDK setter acceptance in [SDK evidence](sdk-management-evidence.md#packaged-session-settings-and-command-catalog--7-september) | provider/native failure matrix not assessed here |
| `thread/compact/start` | `both` | — | — | server/live acceptance + native qualification |
| `thread/shutdown` | `both` | — | Real SDK lifecycle shutdown/readback in [reader evidence](reader-continuity-evidence.md#environment-identity-across-shutdown-7-september-2026) | shutdown failure/reconnect and native qualification not assessed here |
| `turn/start` | `both` | `session-lifecycle.mjs`, `questions.mjs` | Real packaged SDK question submission in [question evidence](real-question-harness-evidence.md#direct-v4-questions-restart-and-keyboard-qualification) | non-question turn outcomes are not assessed here; Android is deferred beyond the iOS-only v1 scope |
| `turn/steer` | `both` | `queue.mjs` | deterministic logic contract `queue.contract.mjs` (including receipt correlation and optional drain composer input) | real hub/native acceptance not assessed here |
| `turn/interrupt` | `both` | `session-lifecycle.mjs` | recipe | server/live acceptance + native qualification |
| `turn/queue` | `both` | `queue.mjs` | recipe + deterministic logic contract `queue.contract.mjs` | server/live acceptance + native qualification |
| `turn/drainAsSteer` | `both` | `queue.mjs` | recipe + deterministic logic contract `queue.contract.mjs` | server/live acceptance + native qualification |
| `turn/promoteQueuedAsSteer` | `both` | `queue.mjs` | recipe + deterministic logic contract `queue.contract.mjs` | server/live acceptance + native qualification |
| `turn/cancelQueued` | `both` | `queue.mjs` | recipe + deterministic logic contract `queue.contract.mjs` | server/live acceptance + native qualification |
| `goal/set` | `both` | `goals.mjs` | recipe + deterministic logic contract `goals.contract.mjs` | server/live acceptance + native qualification |
| `evener/tasks/list` | `both` | `tasks.mjs` | recipe + deterministic logic contract `tasks.contract.mjs` | server/live acceptance + native qualification |
| `evener/jobs/list` | `both` | `activity.mjs` | recipe + deterministic logic contract `activity.contract.mjs` | server/live acceptance + native qualification |
| `evener/jobs/output` | `both` | `job-output.mjs` | recipe + deterministic logic contract `job-output.contract.mjs` | server/live acceptance + native qualification |
| `evener/thread/transcripts/list` | `hub` | — | — | server/live acceptance + native qualification |
| `evener/subagentPreview` | `hub` | — | — | server/live acceptance + native qualification |
| `evener/paths/complete` | `hub` | — | — | server/live acceptance + native qualification |
| `evener/dirs/create` | `hub` | — | — | server/live acceptance + native qualification |
| `evener/projects/recent` | `hub` | — | — | server/live acceptance + native qualification |
| `evener/path/validate` | `hub` | — | — | server/live acceptance + native qualification |
| `evener/git/head` | `hub` | — | — | server/live acceptance + native qualification |
| `evener/mobile/pairing` | `hub` | — | — | server/live acceptance + native qualification |
| `evener/navigation/read` | `hub` | `organization.mjs` | recipe + deterministic logic contract `organization.contract.mjs` | server/live acceptance + native qualification |
| `evener/favorite/set` | `hub` | — | — | server/live acceptance + native qualification |
| `evener/archive/set` | `hub` | — | — | server/live acceptance + native qualification |
| `evener/project/delete` | `hub` | — | — | server/live acceptance + native qualification |
| `evener/session/delete` | `hub` | — | — | server/live acceptance + native qualification |
| `evener/pin-section/rename` | `hub` | `organization.mjs` | recipe + deterministic logic contract `organization.contract.mjs` | server/live acceptance + native qualification |
| `evener/pin-section/delete` | `hub` | `organization.mjs` | recipe + deterministic logic contract `organization.contract.mjs` | server/live acceptance + native qualification |
| `evener/session-pin/assign` | `hub` | `organization.mjs` | recipe + deterministic logic contract `organization.contract.mjs` | server/live acceptance + native qualification |
| `evener/session-pin/unpin` | `hub` | `organization.mjs` | recipe + deterministic logic contract `organization.contract.mjs` | server/live acceptance + native qualification |
| `evener/search` | `hub` | — | — | server/live acceptance + native qualification |
| `evener/harnesses/list` | `hub` | — | — | server/live acceptance + native qualification |
| `evener/upgrade` | `hub` | — | — | server/live acceptance + native qualification |
| `evener/auth/status` | `hub` | `oauth.mjs`, `credentials.mjs` | `credentials.contract.mjs` + [owned-hub credential acceptance](sdk-management-evidence.md#packaged-stored-credentials--7-september) + [packaged device and browser OAuth evidence](sdk-management-evidence.md#packaged-device-and-browser-oauth--7-september) | real provider account and native qualification remain open |
| `evener/auth/test` | `hub` | — | — | server/live acceptance + native qualification |
| `evener/auth/login/start` | `hub` | `oauth.mjs` | `oauth.contract.mjs` + [packaged device and browser OAuth evidence](sdk-management-evidence.md#packaged-device-and-browser-oauth--7-september) | real provider account and native qualification remain open |
| `evener/auth/login/complete` | `hub` | `oauth.mjs` | `oauth.contract.mjs` + [packaged device and browser OAuth evidence](sdk-management-evidence.md#packaged-device-and-browser-oauth--7-september) | real provider account and native qualification remain open |
| `evener/auth/logout` | `hub` | `credentials.mjs` | `credentials.contract.mjs` + [owned-hub credential acceptance](sdk-management-evidence.md#packaged-stored-credentials--7-september) | OAuth/disconnect/provider/native outcomes remain open |
| `evener/auth/list` | `hub` | `credentials.mjs` | `credentials.contract.mjs` + [owned-hub credential acceptance](sdk-management-evidence.md#packaged-stored-credentials--7-september) | OAuth/disconnect/provider/native outcomes remain open |
| `evener/auth/apiKey/set` | `hub` | `credentials.mjs` | `credentials.contract.mjs` + [owned-hub credential acceptance](sdk-management-evidence.md#packaged-stored-credentials--7-september) | OAuth/disconnect/provider/native outcomes remain open |
| `evener/auth/apiKey/clear` | `hub` | `credentials.mjs` | `credentials.contract.mjs` + [owned-hub credential acceptance](sdk-management-evidence.md#packaged-stored-credentials--7-september) | OAuth/disconnect/provider/native outcomes remain open |
| `evener/auth/credentialJson/set` | `hub` | `credentials.mjs` | `credentials.contract.mjs` + [owned-hub credential acceptance](sdk-management-evidence.md#packaged-stored-credentials--7-september) | OAuth/disconnect/provider/native outcomes remain open |
| `evener/auth/device/start` | `hub` | `oauth.mjs` | `oauth.contract.mjs` + [packaged device and browser OAuth evidence](sdk-management-evidence.md#packaged-device-and-browser-oauth--7-september) | real provider account and native qualification remain open |
| `evener/auth/device/poll` | `hub` | `oauth.mjs` | `oauth.contract.mjs` + [packaged device and browser OAuth evidence](sdk-management-evidence.md#packaged-device-and-browser-oauth--7-september) | real provider account and native qualification remain open |
| `evener/launch/resolve` | `hub` | `repository-trust.mjs`, `project-layer.mjs`, `inspect.mjs` | recipe | server/live acceptance + native qualification |
| `evener/launch/schema` | `hub` | `project-layer.mjs`, `inspect.mjs` | recipe | server/live acceptance + native qualification |
| `evener/launch/getLayer` | `hub` | `project-layer.mjs` | recipe | server/live acceptance + native qualification |
| `evener/launch/setLayer` | `hub` | `project-layer.mjs` | recipe | server/live acceptance + native qualification |
| `evener/launch/trustRepo` | `hub` | `repository-trust.mjs` | recipe | server/live acceptance + native qualification |
| `model/list` | `both` | `session-lifecycle.mjs`, `inspect.mjs` | recipe | server/live acceptance + native qualification |
| `evener/instance/list` | `hub` | `instances.mjs` | Real installed SDK list/mutation workflow in [SDK evidence](sdk-management-evidence.md#executed-checks) | provider connectivity and native qualification remain open |
| `evener/instance/create` | `hub` | `instances.mjs` | Real installed SDK create/readback/cleanup in [SDK evidence](sdk-management-evidence.md#executed-checks) | provider connectivity and native qualification remain open |
| `evener/instance/edit` | `hub` | `instances.mjs` | Real installed SDK edit/readback/cleanup in [SDK evidence](sdk-management-evidence.md#executed-checks) | provider connectivity and native qualification remain open |
| `evener/instance/remove` | `hub` | `instances.mjs` | Real installed SDK remove/readback/cleanup in [SDK evidence](sdk-management-evidence.md#executed-checks) | provider connectivity and native qualification remain open |
| `evener/instance/setDefault` | `hub` | `instances.mjs` | Real installed SDK default mutation/readback/restore in [SDK evidence](sdk-management-evidence.md#executed-checks) | provider connectivity and native qualification remain open |
| `evener/plugin/checkNow` | `hub` | — | — | server/live acceptance + native qualification |
| `evener/plugin/preview` | `hub` | `plugins.mjs` | recipe | server/live acceptance + native qualification |
| `evener/marketplace/list` | `hub` | `marketplaces.mjs` | Real installed SDK marketplace list/restore in [SDK evidence](sdk-management-evidence.md#marketplace-and-sandbox-approval-recipes) | broader controlled-source/native cases remain open |
| `evener/marketplace/add` | `hub` | `marketplaces.mjs` | Real installed SDK marketplace add/remove acceptance in [SDK evidence](sdk-management-evidence.md#marketplace-and-sandbox-approval-recipes) | broader controlled-source/native cases remain open |
| `evener/marketplace/remove` | `hub` | `marketplaces.mjs` | Real installed SDK marketplace remove acceptance in [SDK evidence](sdk-management-evidence.md#marketplace-and-sandbox-approval-recipes) | broader controlled-source/native cases remain open |
| `evener/marketplace/refresh` | `hub` | `marketplaces.mjs` | Real installed SDK marketplace refresh acceptance in [SDK evidence](sdk-management-evidence.md#marketplace-and-sandbox-approval-recipes) | broader controlled-source/native cases remain open |
| `evener/marketplace/browse` | `hub` | `marketplaces.mjs` | Real installed SDK marketplace browse acceptance in [SDK evidence](sdk-management-evidence.md#marketplace-and-sandbox-approval-recipes) | broader controlled-source/native cases remain open |
| `evener/plugin/list` | `hub` | `plugin-management.mjs` | Real installed SDK plugin lifecycle acceptance in [SDK evidence](sdk-management-evidence.md#marketplace-and-sandbox-approval-recipes) | broader plugin/marketplace and native qualification remain scoped/open |
| `evener/plugin/install` | `hub` | `plugin-management.mjs` | Real installed SDK Git plugin install acceptance in [SDK evidence](sdk-management-evidence.md#marketplace-and-sandbox-approval-recipes) | broader controlled-source/native marketplace cases remain open |
| `evener/plugin/upgrade` | `hub` | `plugin-management.mjs` | Real installed SDK Git plugin upgrade acceptance in [SDK evidence](sdk-management-evidence.md#marketplace-and-sandbox-approval-recipes) | broader controlled-source/native marketplace cases remain open |
| `evener/plugin/remove` | `hub` | `plugin-management.mjs` | Real installed SDK plugin removal acceptance in [SDK evidence](sdk-management-evidence.md#marketplace-and-sandbox-approval-recipes) | broader controlled-source/native marketplace cases remain open |
| `evener/plugin/enable` | `hub` | `plugin-management.mjs` | Real installed SDK plugin enable acceptance in [SDK evidence](sdk-management-evidence.md#marketplace-and-sandbox-approval-recipes) | broader controlled-source/native marketplace cases remain open |
| `evener/plugin/disable` | `hub` | `plugin-management.mjs` | Real installed SDK plugin disable acceptance in [SDK evidence](sdk-management-evidence.md#marketplace-and-sandbox-approval-recipes) | broader controlled-source/native marketplace cases remain open |
| `evener/plugin/setAutoUpgrade` | `hub` | `plugin-management.mjs` | Real installed SDK auto-upgrade toggle acceptance in [SDK evidence](sdk-management-evidence.md#marketplace-and-sandbox-approval-recipes) | broader controlled-source/native marketplace cases remain open |
| `evener/command/list` | `hub` | `commands.mjs` | Real empty/one/empty catalog acceptance in [SDK evidence](sdk-management-evidence.md#packaged-session-settings-and-command-catalog--7-september); recipe + deterministic logic contract | broader controlled-source/native cases not assessed here |
| `evener/settings/overview` | `hub` | — | — | server/live acceptance + native qualification |
| `evener/settings/transcriptDisplay/get` | `hub` | `preferences.mjs` | recipe + deterministic logic contract `preferences.contract.mjs` | server/live acceptance + native qualification |
| `evener/settings/transcriptDisplay/patch` | `hub` | `preferences.mjs` | recipe + deterministic logic contract `preferences.contract.mjs` | server/live acceptance + native qualification |
| `evener/settings/keybindings/get` | `hub` | `preferences.mjs` | recipe + deterministic logic contract `preferences.contract.mjs` | server/live acceptance + native qualification |
| `evener/settings/keybindings/patch` | `hub` | `preferences.mjs` | recipe + deterministic logic contract `preferences.contract.mjs` | server/live acceptance + native qualification |
| `evener/sandbox/escalation/resolve` | `both` | `approvals.mjs` | Real packaged SDK and native Allow/Deny acceptance in [approval evidence](approval-evidence.md#direct-v4-execution-through-native-and-packaged-sdk-decisions) | concurrent/lost-ack, other tools, and broader controlled-source/native cases remain open; Android is deferred beyond the iOS-only v1 scope |

## Notifications

| Notification | Cookbook recipe(s) explicitly observing it | Evidence currently present | Unassessed or remaining qualification |
|---|---|---|---|
| `thread/started` | — | catalog only | live ordering/failure + native qualification |
| `thread/closed` | — | catalog only | live ordering/failure + native qualification |
| `thread/status/changed` | — | catalog only | live ordering/failure + native qualification |
| `thread/queueChanged` | — | catalog only | live ordering/failure + native qualification |
| `evener/thread/name/changed` | — | catalog only | live ordering/failure + native qualification |
| `thread/model/changed` | — | Observed by the seven-mutation settings driver in [SDK evidence](sdk-management-evidence.md#packaged-session-settings-and-command-catalog--7-september); recipe does not own subscriptions | broader notification ordering/failure and native qualification not assessed here |
| `thread/reasoning-effort/changed` | — | Observed by the seven-mutation settings driver in [SDK evidence](sdk-management-evidence.md#packaged-session-settings-and-command-catalog--7-september); recipe does not own subscriptions | broader notification ordering/failure and native qualification not assessed here |
| `thread/vision-model/changed` | — | Observed by the seven-mutation settings driver in [SDK evidence](sdk-management-evidence.md#packaged-session-settings-and-command-catalog--7-september); recipe does not own subscriptions | broader notification ordering/failure and native qualification not assessed here |
| `turn/started` | `session-lifecycle.mjs`, `streaming-rejoin.mjs` | recipe inventory + deterministic contract where listed | live ordering/failure + native qualification |
| `turn/completed` | `session-lifecycle.mjs`, `streaming-rejoin.mjs` | Real SDK lifecycle barriers in [reader evidence](reader-continuity-evidence.md#native-live-to-saved-continuity-7-september-2026) and [question evidence](real-question-harness-evidence.md#direct-v4-questions-restart-and-keyboard-qualification); recipe inventory + deterministic contract | broader ordering/failure remains open; Android is deferred beyond the iOS-only v1 scope |
| `item/started` | — | catalog only | live ordering/failure + native qualification |
| `item/completed` | — | catalog only | live ordering/failure + native qualification |
| `item/agentMessage/delta` | — | catalog only | live ordering/failure + native qualification |
| `item/agentMessage/reset` | — | catalog only | live ordering/failure + native qualification |
| `item/reasoning/summaryTextDelta` | — | catalog only | live ordering/failure + native qualification |
| `item/toolOutput/delta` | — | catalog only | live ordering/failure + native qualification |
| `warning` | — | catalog only | live ordering/failure + native qualification |
| `evener/thread/modelRetry` | — | catalog only | live ordering/failure + native qualification |
| `evener/steering/injected` | — | catalog only | live ordering/failure + native qualification |
| `evener/job/started` | — | catalog only | live ordering/failure + native qualification |
| `evener/job/finished` | — | catalog only | live ordering/failure + native qualification |
| `evener/delegate/updated` | — | catalog only | live ordering/failure + native qualification |
| `evener/jobs/treeUpdated` | — | catalog only | live ordering/failure + native qualification |
| `evener/auth/updated` | — | [independent observer received eleven credential updates](sdk-management-evidence.md#packaged-stored-credentials--7-september) | continuous recovery, failure and native qualification remain open |
| `evener/launch/updated` | `repository-trust.mjs`, `project-layer.mjs` | recipe inventory + deterministic contract where listed | live ordering/failure + native qualification |
| `evener/attention/changed` | — | catalog only | live ordering/failure + native qualification |
| `evener/navigation/invalidated` | — | catalog only | live ordering/failure + native qualification |
| `evener/marketplace/updated` | — | catalog only | live ordering/failure + native qualification |
| `evener/plugin/updated` | — | catalog only | live ordering/failure + native qualification |
| `evener/thread/resync` | — | catalog only | live ordering/failure + native qualification |
| `evener/task/updated` | — | catalog only | live ordering/failure + native qualification |
| `evener/goal/updated` | — | catalog only | live ordering/failure + native qualification |
| `evener/sandbox/escalation/requested` | — | catalog only | live ordering/failure + native qualification |
| `evener/sandbox/escalation/resolved` | — | catalog only | live ordering/failure + native qualification |
| `evener/settings/transcriptDisplay/changed` | — | catalog only | live ordering/failure + native qualification |
| `evener/settings/keybindings/changed` | — | catalog only | live ordering/failure + native qualification |

## Lifecycle and failure qualification gaps

- Lifecycle evidence covers selected owned SDK/native creation, read, shutdown, and question flows. It does not establish every reconnect, subscription restoration, process-death, concurrent-writer, stale-cursor, or cross-platform outcome.
- Failure evidence is strongest for bounded contract cases, uncertain mutation/readback handling, malformed replies, and the cited approval/question workflows. It does not establish complete transport failure, provider failure, credential, or native release behavior for every method.
- For every row without a dated acceptance link, the missing-qualification text means “not assessed by this inventory,” not that the server operation is absent.

## Known qualification gaps

- The package has typed request/result/notification declarations and strict handshake decoding, but method payloads/results are not generally runtime-decoded; see `cmd/evener-hub/frontend/src/protocol/client.ts` and its README.
- `thread/turns/items/list` is generated but `ScopeUnimplemented` in `appwire/protocol.go`; it remains unsupported.
- The cookbook does not prove complete streaming, notification, failure, native-release, credential, or continuous reconnect coverage; `README.md:95-106` and `README.md:456-460` state these limits.
- Environment identity now has deterministic projector, server differential, session tracker, and the completed iPhone live-to-saved journey in [reader evidence](reader-continuity-evidence.md#native-live-to-saved-continuity-7-september-2026). Broader native/platform cases remain open.

## Reproduction boundary

Recheck parity with a temporary Go program importing `primeradiant.com/evener/appwire`; compare the 91 method and 36 notification names with this table. The source catalog remains authoritative.
