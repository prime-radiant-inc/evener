# AppWire SDK method outcome index — in progress

Catalog: all 91 method names in [appwire.Methods](../../../appwire/protocol.go), source `5307e8515`; 90 supported and one reserved. **This is a partially reviewed evidence index, not a completed qualification matrix.** Earlier draft counts inferred execution from examples and inferred capability exemptions from catalog shapes; those claims are excluded.

`RECORDED` means the linked receipt records the specific executed scenario stated in the cell. It does not cover every valid input, every backend/version, current native UI, or the other outcome columns. Each receipt retains its own package and backend identity. `U` means **unreviewed in this index**: evidence may already exist elsewhere, and applicability still needs source review. U is not a failed test or a claim that evidence does not exist. A declared unsupported contract does not establish an executed rejection.

Failure and lifecycle columns require method-specific review of actual handlers and SDK behavior. Do not mark them inapplicable merely because request types lack a capability field. Session capabilities, target identity, permission, reconnect and mutation uncertainty can affect the route.

## Outcome cells

| Method | Scope | Recorded success/effect/readback | Invalid or unsupported input | Capability or permission | Conflict or stale binding | Disconnect or uncertainty | Lifecycle |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `initialize` | connection | RECORDED — [2026-09-08-sdk-plugin-marketplace-producers](assets/2026-09-08-sdk-plugin-marketplace-producers.json): two installed clients connected before recorded requests; malformed handshake outcomes remain unreviewed. | U | U | U | U | U |
| `ping` | connection | U | U | U | U | U | U |
| `thread/list` | both | U | U | U | U | U | U |
| `thread/read` | both | RECORDED — [2026-09-08-iphone-execution](assets/2026-09-08-iphone-execution.json): realController.assertions; independent vision readback and second-client propagation, on the host. | U | U | U | U | U |
| `thread/unsubscribe` | both | U | U | U | U | U | U |
| `thread/turns/list` | both | U | U | U | U | U | U |
| `thread/turns/items/list` | unimplemented | Declared reserved/unimplemented in [catalog](../../../appwire/protocol.go); no successful operation. Executed rejection remains U. | U | U | U | U | U |
| `thread/start` | hub | U | U | U | U | U | U |
| `thread/resume` | hub | RECORDED — [sdk-lineage-receipt](assets/sdk-lineage-receipt.json): endedParentResumed and outputs resume.json / independent-parent.json. | U | U | U | U | U |
| `thread/fork` | hub | RECORDED — [sdk-lineage-receipt](assets/sdk-lineage-receipt.json): regularForkAcknowledged, asideForkAcknowledged, originalInputRetained, independentChildParentLinks. | U | U | U | U | U |
| `thread/clear` | both | U | U | U | U | U | U |
| `thread/model/set` | both | RECORDED — [sdk-settings-receipt](assets/sdk-settings-receipt.json): mutations[5:7], notifications and settingsRestored; configuration state only, no model execution. | U | U | U | U | U |
| `evener/thread/name/set` | both | U | U | U | U | U | U |
| `thread/reasoning-effort/set` | both | RECORDED — [sdk-settings-receipt](assets/sdk-settings-receipt.json): mutations[0:3], notifications and settingsRestored; configuration state only, no model execution. | U | U | U | U | U |
| `thread/vision-model/set` | both | RECORDED — [2026-09-08-iphone-execution](assets/2026-09-08-iphone-execution.json): realController.assertions; off, catalog model, second-client change and restoration. No native interaction. | U | U | U | U | U |
| `thread/compact/start` | both | U | U | U | U | U | U |
| `thread/shutdown` | both | U | U | U | U | U | U |
| `turn/start` | both | U | U | U | U | U | U |
| `turn/steer` | both | RECORDED — [sdk-steer-receipt](assets/sdk-steer-receipt.json): receipts.steer, exactMarkerCounts, turnCompletedReadback. | U | U | U | U | U |
| `turn/interrupt` | both | U | U | U | U | U | U |
| `turn/queue` | both | RECORDED — [sdk-steer-receipt](assets/sdk-steer-receipt.json): receipts.queued and preDrainIds; consumed by drain in this scenario. | U | U | U | U | U |
| `turn/drainAsSteer` | both | RECORDED — [sdk-steer-receipt](assets/sdk-steer-receipt.json): receipts.drain, postDrainDepth=0, exact combined transcript marker. | U | U | U | U | U |
| `turn/promoteQueuedAsSteer` | both | U | U | U | U | U | U |
| `turn/cancelQueued` | both | U | U | U | U | U | U |
| `goal/set` | both | U | U | U | U | U | U |
| `evener/tasks/list` | both | U | U | U | U | U | U |
| `evener/jobs/list` | both | U | U | U | U | U | U |
| `evener/jobs/output` | both | U | U | U | U | U | U |
| `evener/thread/transcripts/list` | hub | RECORDED — [sdk-lineage-receipt](assets/sdk-lineage-receipt.json): transcriptTargets and outputs transcripts.json. | U | U | U | U | U |
| `evener/subagentPreview` | hub | RECORDED — [sdk-lineage-receipt](assets/sdk-lineage-receipt.json): previewItems=5 and previewTruncated=true. | U | U | U | U | U |
| `evener/paths/complete` | hub | U | U | U | U | U | U |
| `evener/dirs/create` | hub | U | U | U | U | U | U |
| `evener/projects/recent` | hub | U | U | U | U | U | U |
| `evener/path/validate` | hub | U | U | U | U | U | U |
| `evener/git/head` | hub | U | U | U | U | U | U |
| `evener/mobile/pairing` | hub | U | U | U | U | U | U |
| `evener/navigation/read` | hub | U | U | U | U | U | U |
| `evener/favorite/set` | hub | U | U | U | U | U | U |
| `evener/archive/set` | hub | U | U | U | U | U | U |
| `evener/project/delete` | hub | U | U | U | U | U | U |
| `evener/session/delete` | hub | U | U | U | U | U | U |
| `evener/pin-section/rename` | hub | U | U | U | U | U | U |
| `evener/pin-section/delete` | hub | U | U | U | U | U | U |
| `evener/session-pin/assign` | hub | U | U | U | U | U | U |
| `evener/session-pin/unpin` | hub | U | U | U | U | U | U |
| `evener/search` | hub | U | U | U | U | U | U |
| `evener/harnesses/list` | hub | U | U | U | U | U | U |
| `evener/upgrade` | hub | U | U | U | U | U | U |
| `evener/auth/status` | hub | U | U | U | U | U | U |
| `evener/auth/test` | hub | U | U | U | U | U | U |
| `evener/auth/login/start` | hub | U | U | U | U | U | U |
| `evener/auth/login/complete` | hub | U | U | U | U | U | U |
| `evener/auth/logout` | hub | U | U | U | U | U | U |
| `evener/auth/list` | hub | U | U | U | U | U | U |
| `evener/auth/apiKey/set` | hub | U | U | U | U | U | U |
| `evener/auth/apiKey/clear` | hub | U | U | U | U | U | U |
| `evener/auth/credentialJson/set` | hub | U | U | U | U | U | U |
| `evener/auth/device/start` | hub | U | U | U | U | U | U |
| `evener/auth/device/poll` | hub | U | U | U | U | U | U |
| `evener/launch/resolve` | hub | U | U | U | U | U | U |
| `evener/launch/schema` | hub | U | U | U | U | U | U |
| `evener/launch/getLayer` | hub | RECORDED — [2026-09-08-sdk-launch-producer](assets/2026-09-08-sdk-launch-producer.json): observation.readback maxRounds=7, two project-layer updates and original layer restored. | U | U | U | U | U |
| `evener/launch/setLayer` | hub | RECORDED — [2026-09-08-sdk-launch-producer](assets/2026-09-08-sdk-launch-producer.json): observation.readback maxRounds=7, two project-layer updates and original layer restored. | U | U | U | U | U |
| `evener/launch/trustRepo` | hub | U | U | U | U | U | U |
| `model/list` | both | U | U | U | U | U | U |
| `evener/instance/list` | hub | U | U | U | U | U | U |
| `evener/instance/create` | hub | U | U | U | U | U | U |
| `evener/instance/edit` | hub | U | U | U | U | U | U |
| `evener/instance/remove` | hub | U | U | U | U | U | U |
| `evener/instance/setDefault` | hub | U | U | U | U | U | U |
| `evener/plugin/checkNow` | hub | U | U | U | U | U | U |
| `evener/plugin/preview` | hub | U | U | U | U | U | U |
| `evener/marketplace/list` | hub | RECORDED — [2026-09-08-sdk-plugin-marketplace-producers](assets/2026-09-08-sdk-plugin-marketplace-producers.json): marketplace-add/remove readbacks; original names restored. No executable plugin effect. | U | U | U | U | U |
| `evener/marketplace/add` | hub | RECORDED — [2026-09-08-sdk-plugin-marketplace-producers](assets/2026-09-08-sdk-plugin-marketplace-producers.json): records[name=marketplace-add]; owned directory source/readback. No executable plugin effect. | U | U | U | U | U |
| `evener/marketplace/remove` | hub | RECORDED — [2026-09-08-sdk-plugin-marketplace-producers](assets/2026-09-08-sdk-plugin-marketplace-producers.json): records[name=marketplace-remove]; original marketplace names restored. No executable plugin effect. | U | U | U | U | U |
| `evener/marketplace/refresh` | hub | U | U | U | U | U | U |
| `evener/marketplace/browse` | hub | RECORDED — [2026-09-08-sdk-plugin-marketplace-producers](assets/2026-09-08-sdk-plugin-marketplace-producers.json): retained sdk-producer.mjs asserts owned marketplace and native-tools before later recorded mutations. No executable plugin effect. | U | U | U | U | U |
| `evener/plugin/list` | hub | RECORDED — [2026-09-08-sdk-plugin-marketplace-producers](assets/2026-09-08-sdk-plugin-marketplace-producers.json): plugin readbacks track version, enabled and autoUpgrade, then empty list. No executable plugin effect. | U | U | U | U | U |
| `evener/plugin/install` | hub | RECORDED — [2026-09-08-sdk-plugin-marketplace-producers](assets/2026-09-08-sdk-plugin-marketplace-producers.json): records[name=plugin-install]; native-tools@1.0.0 present/enabled. No executable plugin effect. | U | U | U | U | U |
| `evener/plugin/upgrade` | hub | U | U | U | U | U | U |
| `evener/plugin/remove` | hub | RECORDED — [2026-09-08-sdk-plugin-marketplace-producers](assets/2026-09-08-sdk-plugin-marketplace-producers.json): records[name=plugin-remove]; authoritative plugins list empty. No executable plugin effect. | U | U | U | U | U |
| `evener/plugin/enable` | hub | RECORDED — [2026-09-08-sdk-plugin-marketplace-producers](assets/2026-09-08-sdk-plugin-marketplace-producers.json): records[name=plugin-enable]; enabled=true. No executable plugin effect. | U | U | U | U | U |
| `evener/plugin/disable` | hub | RECORDED — [2026-09-08-sdk-plugin-marketplace-producers](assets/2026-09-08-sdk-plugin-marketplace-producers.json): records[name=plugin-disable]; enabled=false. No executable plugin effect. | U | U | U | U | U |
| `evener/plugin/setAutoUpgrade` | hub | RECORDED — [2026-09-08-sdk-plugin-marketplace-producers](assets/2026-09-08-sdk-plugin-marketplace-producers.json): records[name=plugin-auto-upgrade]; autoUpgrade=true; scheduled execution untested. No executable plugin effect. | U | U | U | U | U |
| `evener/command/list` | hub | RECORDED — [sdk-settings-receipt](assets/sdk-settings-receipt.json): commands.empty/populated/restored; one owned user command discovered, not executed. | U | U | U | U | U |
| `evener/settings/overview` | hub | U | U | U | U | U | U |
| `evener/settings/transcriptDisplay/get` | hub | U | U | U | U | U | U |
| `evener/settings/transcriptDisplay/patch` | hub | U | U | U | U | U | U |
| `evener/settings/keybindings/get` | hub | U | U | U | U | U | U |
| `evener/settings/keybindings/patch` | hub | U | U | U | U | U | U |
| `evener/sandbox/escalation/resolve` | both | RECORDED — [sandbox receipt](assets/2026-09-08-sdk-sandbox-producers.json): exact Allow/Deny escalation identities, successful/denied persisted tool results and resolved events. Allow target preexistence remains unknown. | U | U | U | U | U |

## Review progress and next order

This index has 26 narrowly recorded success/effect cells. The other 64 supported methods and all failure/lifecycle cells still need indexing or qualification. Existing broader evidence remains in [protocol coverage](protocol-coverage.md), [management evidence](sdk-management-evidence.md) and the dated receipts; it has not been invalidated by an unreviewed cell here.

1. Index existing receipts and raw assertions before creating duplicate fixtures. Verify actual installed SDK execution, exact target/operation and authoritative effect.
2. Review mutation identity, stale binding and lost-reply outcomes for send, queue, decisions and lifecycle operations needed by the active iPhone journey.
3. Finish actual nested-work, sandbox and plugin execution outcomes alongside their notification producers.
4. Review the remaining catalog routes and connection lifecycle with method-specific applicability. Keep native end-to-end gates separate.
