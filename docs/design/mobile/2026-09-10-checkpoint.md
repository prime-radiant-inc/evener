# Current iPhone checkpoint evidence

This page separates source qualification, the owned simulator journey, and delivery. The [status page](status.md) records the dated PR inventory; the [remaining-work plan](ios-v1-remaining.md) defines the acceptance work still open.

## Combined source qualification

Candidate: [`5137914ccb66b04aa1b980757888f63141cf2f27`](https://github.com/prime-radiant-inc/evener/commit/5137914ccb66b04aa1b980757888f63141cf2f27), published as `codex/mobile-checkpoint-qualified-513`.

`make merge-approval-gate` completed with exit code 0. It covered repository lint and generation checks, all backend modules, the web gate, 720 native tests, 686 shared-session tests, native TypeScript and the independently installed SDK qualification. The gate log SHA-256 is `810e44832ae1b53d7931f7189f65e6a1bd235b022c5221e376ed2fd8622defc1`.

This candidate includes the activity `77fcabca9`, native `016b118f7`, environment `2cc8d50b4` and remote timing `9362ae189` changes. It does not include the pending timing-compaction fix. A later public-session regression reproduces a live/cold timing position mismatch through the real `compact_context` tool. The initial owner-field correction does not close that finding; neither the existing gate nor this receipt qualifies a fix for it.

## Artifact and runtime identity

| Item | Verified identity |
| --- | --- |
| Simulator | iPhone 17 Pro, iOS 26.5 |
| Native app | Signed Release artifact from `0097e3841fe9296ebe32da4cb6780cd7d2a8923f`; native/protocol inputs match `016b118f78767e6b325bfe782c974135835242db` |
| Native executable SHA-256 | `3883015716cc2b24540aa96a984e42111fed51e9e67fb4dbf7567e8901eabc4f` |
| JavaScript bundle SHA-256 | `98478257829105c65d120a9c8925d699dc3e919c975d6c447e90b82c4fb4ffd2` |
| Installed backend | `0d75cf8b5fabb9992f2938caf42f2e8129d9c8d1` |
| Newer source candidate | `5137914cc` passed the combined gate but has not replaced the owned backend runtime |

The [signed-app install receipt](assets/2026-09-10-native-0097-install.json) identifies the native artifact, retention comparison and newer source gate. The backend's earlier [paired restart receipt](assets/2026-09-10-paired-restart-0d75.json) independently records its passing full gate, clean binary identities and cold-launch comparison. These are separate observations.

The fixture is isolated from production hubs and sessions. The backend restart retained its origin, credentials, state and scripted provider.

## Observed product behavior

The [earlier native control journey](assets/2026-09-10-paired-restart-d983.json) exercised send, queue, steering and Stop. The verified queue attempt used the full composer text, and both queued messages completed once. Steering retained its owner. Stop cancelled the provider request and left the turn interrupted without an answer. An earlier automation attempt submitted partially typed input; it was repeated with input verification before submission and is not presented as an app defect.

The [0d75 restart comparison](assets/2026-09-10-paired-restart-0d75.json) performed a real backend restart followed by an app cold launch. All 32 saved canonical items matched by key, turn identity/status, position, content and structured payload. The stopped turn remained interrupted. All seven draft tables and eleven reader entries outside the active fixture were unchanged; the exact unsent draft remained visible. The provider request count stayed at 41.

The subsequent signed `0097` app installation preserved those 32 canonical items, seven draft tables and eleven other reader positions, again with provider requests unchanged at 41 and the unsent draft visible. All twelve SQLite tables were compared before the installed app launched. This proves update retention for this simulator fixture, not physical-device qualification.

![Signed 0097 simulator app with the retained unsent draft](assets/2026-09-10-native-0097-installed.png)

The current fixture's reader position and navigation state may change during the journey. Cache tables are not user-draft preservation evidence. The earlier live-to-cold comparison covered 31 common persisted items because live startup diagnostics and the saved system-prompt item differ; saved-to-saved comparisons include that system prompt and cover 32 items. No receipt claims equality of transient wire IDs or proves the newly discovered compaction/timing case.

## Evidence that remains open

A fresh TestFlight archive, Apple processing, internal-group availability of the checkpoint, physical iPhone installation/update, real-network performance and the full supported workflow matrix remain separate acceptance gates. The TestFlight automation needs to land on main and run. Drew's external access needs the outstanding Apple beta setup and review.
