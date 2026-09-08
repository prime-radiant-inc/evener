# Native mobile acceptance ledger

This is the current, source-backed acceptance ledger for the native Evener
client. It is deliberately separate from historical implementation notes in
the coverage spec and backlog. A source or deterministic test proves that a
path exists; it does not prove that the complete native workflow is accepted.
Every row therefore records the independent real-daemon, iOS, Android, build,
and release evidence needed to close it.

## Active v1 platform scope

Jesse selected **iOS-only v1** during takeover continuation. All Android evidence
and remaining work below is preserved for later delivery and does not block v1.
Current acceptance requires iPhone/iPad, shared behavior and SDK qualification;
physical-device, VoiceOver, performance and signed install/update checks remain
required. Historical Android entries are not claims of current acceptance.

## Evidence rules

“Historical” means evidence recorded for an earlier source or installed
artifact. The AppWire v4 migrations are committed through `3356848d4`, followed
by protocol recovery at `9e6f232d7`. Both Release artifacts were rebuilt and
installed from `9e6f232d7`; the iOS and Android version-mismatch journeys passed
against an isolated old v3 hub. The [v4 integration record](v4-integration-evidence.md) now qualifies direct connection, roster/project navigation, native send/stop and cross-device publication on these artifacts; wider v4 acceptance remains open.
The full native suite reported 359 tests, and the coordinator separately ran
366 shared service/store tests plus 11 pretests. These checks are scoped
integration evidence, not final release acceptance. The gate scope and creation
lifetime regression are committed through `5c9124bca`.

Source anchors below identify the implementation contract to re-check when a
row is executed: server handlers and generated `MethodTypes` are authoritative
for available behavior; native screens/services/stores are the client path;
tests must use deterministic fakes at the provider boundary; daemon scenarios
must use an isolated current-v4 hub and scripted provider.

## Workflow ledger

| Requirement | Actual web/server source | Native source and status | Deterministic test | Real-daemon scenario | iOS evidence | Android evidence | Source/build identity | Remaining acceptance | Owner |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Hub profiles, secure credentials, switching, reconnect, and hub-scoped drafts | `mobile/src/state/connection.ts`; AppWire initialize/auth in `cmd/evener-hub/frontend/src/protocol/client.ts` | `mobile-native/src/connection.ts`, `ConnectionProvider.tsx`; implemented profile and foreground connection path | `hubSelection.test.ts` (18 deferred-storage cases), connection/removal/draft tests; real HTTP provider lifecycle contracts | Two isolated v4 hubs with identical session ref and instance ID; switch during a held model turn, preserve separate drafts, background/cold-launch B and restart A | [Current direct two-hub journey](multiple-hubs.md#two-direct-hubs-with-identical-session-identities--7-september-2026): distinct credentials, A/B transcript and draft isolation, late A completion, B cold launch, A process restart, scoped profile removal, independent SDK readback and cleanup; [deferred operations](multiple-hubs.md#delayed-saved-hub-operations--7-september-2026) retains deterministic timing evidence | Historical Android observations retained; release qualification deferred beyond iOS-only v1 | Rebuilt iPhone Release source `4bd05e280`; hub binaries `700873dc9`; bundle, binary, SDK and evidence hashes in linked receipt | Credential rotation/editor failure, actionable version mismatch, LAN/pairing, overlapping pending RPCs, lost replies/uncertain writes, complete iPad/accessibility and physical-device/signing/update qualification | Coordinator |
| Browse roster, search, current/recent/archived sessions, and project tiers | `cmd/evener-hub/web_api_tree.go`; `cmd/evener-hub/app_navigation.go`; generated navigation methods in `types.gen.ts` | `mobile-native/src/rosterSearch.ts`, `ProjectsScreen.tsx`, `navigationPages.ts`; v2 implementation reviewed | `mobile-native/src/rosterSearch.test.ts`, `navigationPages.test.ts`; navigation commit `4eafc70a8` focused 35/35 pass | Isolated hub search, paging, archived/current transitions, invalidation and sequence gap, then open a selected session | Historical project-browsing and roster-search evidence; first-pass v4 build not final reviewed source | Historical continuation/refresh evidence; first-pass v4 build not final reviewed source | Current source includes reviewed navigation commit; transcript integration reviewed at `3356848d4` | Production-scale latency, subagent lifecycle, final-head integrated build and daemon acceptance | Coordinator |
| Read a conversation with item paging, fragments, live merge, reconnect, and rich content | Server transcript handlers and generated `thread/read` types; `mobile/src/services/conversation.ts` | `mobile/src/services/conversation.ts`, `mobile/src/state/conversation.ts`, native timeline screens; v4 item cursor migration is implemented, with recovery review pending | `mobile/src/services/conversation.test.ts`; transcript round 3 reports 366 tests/check, with scoped review pending | Long transcript with overlapping fragments, stale cursor, reconnect during live item replacement, image replacement, and page/live deduplication | Historical Markdown/gallery/dark-mode evidence; first-pass v4 build not final reviewed source | Historical Markdown/table/large-text evidence; first-pass v4 build not final reviewed source | Reviewed commit `3356848d4`; root 366 service/store tests plus 11 separate pretests; Release rebuild at `9e6f232d7` passed on both platforms; v4 journeys pending | Complete native recovery acceptance; reader position, authenticated images, accessibility, large-content performance, final iOS build | Transcript worker + coordinator |
| Compose send, steer, queue, stop, and durable uncertain-delivery recovery | Server session/message/queue methods in generated `MethodTypes`; `mobile/src/services/conversation.ts` | `mobile/src/state/conversation.ts`, composer screens; implemented with no-blind-replay guards | Conversation store tests and queue tests; deterministic receipt/lost-reply cases | Scripted provider holds a turn; send/steer/queue/cancel/promote/drain/stop across reconnect and authoritative readback | Historical queue and composer settings evidence; pre-v4 build | Historical queue and composer settings evidence; pre-v4 build | Current dirty source; integrated 358 native tests predates transcript edits | Re-run current tests, final-head daemon delivery, conflict/reconnect cases, keyboard/accessibility and long-stream performance | Coordinator |
| Create a session with project, harness, model, reasoning, directory, plugins, images, trust, and failure recovery | `cmd/evener-hub/app_rpc.go` and generated creation/catalog methods | `mobile-native/src/newSession.ts`, creation screens, SQLite draft state; implemented in slices | Creation/store tests and shared AppWire fixture tests | Isolated disposable hub: valid/invalid path, large catalog, trust approval/rejection, image attachment, uncertain create, process restart, authoritative session check | Historical standalone creation evidence; first-pass v4 build not final reviewed source | Historical standalone creation evidence; first-pass v4 build not final reviewed source | Current source plus dirty migration; first-pass v4 Release builds installed but not final reviewed artifacts | Final-head both-platform creation, failure copy, large text/keyboard/a11y, storage failure and performance | Creation worker + coordinator |
| Manage a session: rename, clear/compaction, fork, remove, stop, and lifecycle transitions | `cmd/evener-hub/app_session_*.go`; generated session methods | `mobile-native/src/sessionControls.ts`, `mobile/src/state/conversation.ts`; rename/compaction/stop/clear slices exist, fork/remove gaps remain | `session-controls-evidence.md` tests and conversation state tests | Daemon rename, compaction, clear replacement, fork/remove, stop during reconnect, confirm authoritative state and no replay | Historical rename/compaction/stop/clear evidence; pre-v4 build | Historical rename/compaction/stop/clear evidence; pre-v4 build | Current source; prior 59-test/build evidence is historical | Implement/qualify fork/remove and disconnected/uncertain actions; final-head builds, a11y and physical devices | Coordinator |
| Model, reasoning, and launch-layer settings preserve server-derived capabilities | `cmd/evener-hub/app_models.go`, launch/config handlers, generated catalog/settings methods | `mobile-native/src/hubModels.ts`, composer controls, launch/settings screens; core selection exists | `composer-settings-evidence.md`, provider/launch controller tests | Change model/reasoning, reconnect, catalog scope change, invalid effort, launch-layer effective/inherited values, preserve unrelated fields | Historical model/reasoning/launch evidence; first-pass v4 build not final reviewed source | Historical model/reasoning/launch evidence; first-pass v4 build not final reviewed source | Current source; first-pass v4 Release builds installed, final integrated rebuild pending | Vision choices, schema/trust completeness, faults, accessibility, final-head validation | Creation/settings owners |
| Goals, tasks, activity, queue output, and delegate navigation remain readable and resumable | Generated goal/task/activity/queue methods; server projections in `cmd/evener-hub` | `mobile-native/src/goalCommand.ts`, `taskList.ts`, `activityList.ts`, task/activity screens; partial implementation | `docs/design/mobile/goal-controls.md`, `task-list.md`, `activity.md` test evidence | Real delegate/subagent creates work; page branches/output, background/reconnect, resolve from another device, stale actions | Historical goal/task/activity evidence; first-pass v4 build not final reviewed source | Historical goal/task/activity evidence; first-pass v4 build not final reviewed source | Current source; first-pass v4 Release builds installed, final integrated rebuild pending | Real daemon lifecycle rather than injected notifications, branch paging, faults, accessibility, scale | Workflow owner |
| Approvals pause and resume real execution with stale-request protection | Server approval handlers and sandbox execution in generated methods | Native approval sheet/controller; implemented with controlled-wire coverage | `approval-evidence.md` and approval state tests | Scripted harness pauses on approval; allow/deny resumes or rejects actual work; stale approval after hub/session switch | Historical controlled-wire and harness evidence; tested builds differ | Historical controlled-wire and harness evidence; tested builds differ | Current source; evidence is not same final v4 build | Multi-approval races, fault/reconnect, real sandbox execution, keyboard/a11y, final-head devices | Decisions owner |
| Questions present exact definitions, preserve answers, and resume work | Server question methods and question projection; current web parity audit | Native question sheet/controller; implemented partial | `question-evidence.md`, web parity tests | Harness asks multiple questions; answer/reconnect/switch hub; stale question and authoritative readback | Historical harness question evidence; build identity recorded in parity audit, pre-v4 | Historical harness question evidence; build identity recorded in parity audit, pre-v4 | Current source; final v4 builds absent | Multi-question/race/fault, a11y and other decision surfaces | Decisions owner |
| Organize projects/sessions: favorite, archive, remove, pin/unpin, pin-section create/rename/delete, and browse pins | `cmd/evener-hub/app_favorite.go`, `app_archive.go`, `app_pin_section.go`, `app_navigation.go` | `mobile-native/src/navigationActions.ts`, `ProjectsScreen.tsx`; favorite/archive and some pin row actions exist | `organization-evidence.md`, `navigationPages.test.ts`; server delete/pin tests under `cmd/evener-hub` | Mutate each operation on isolated hub, receive notification, refresh exact revision, test stale owner and deleted target | Historical archive/favorite/project pin checks; pre-v4 build | Historical archive/favorite/project pin checks; pre-v4 build | Current server v4 source; native final build absent | Session pin assign/unpin, section lifecycle, pinned browsing, removal and a11y | Navigation owner |
| Provider instances, API keys, credential JSON, endpoint reset, and sign-in/device flow | `cmd/evener-hub/app_credentials.go`, `app_auth.go`, provider handlers; generated `evener/auth/credentialJson/set` | `mobile-native/src/providerForm.ts`, `providerInstances.ts`, `ProvidersScreen.tsx`, `ProviderSignIn`; v4 migration implemented | `providerForm.test.ts`, `providerInstances.test.ts`, `hubOverview.test.ts`; 35 focused sign-in checks and 663 native tests/73 files plus TypeScript passed for the OAuth integration | Fresh real auth handlers/registry/storage with scripted external OAuth; hold exchange, close actual native connection by backgrounding, release, foreground and read credential status; cancel then switch | [Interrupted device/browser recovery](providers-evidence.md#ios-interrupted-oauth-recovery--7-september): same-process foreground, separate restart, retained device attempt, configured status read, cancel/switch and native credential cleanup | Historical Android provider evidence retained; release qualification deferred beyond iOS-only v1 | Rebuilt iPhone Release source `759e7b10f`; native bundle and fixture identities in the linked receipt | Current-artifact key/JSON/editor and endpoint-reset matrix, real account denial/revocation, bearer isolation, iPad/physical-device/a11y/signing qualification | Provider owner |
| Plugins and marketplaces support browse, install, upgrade, remove, enable/disable, and automatic upgrade | Server plugin/marketplace handlers and generated plugin methods | `mobile-native/src/marketplaces.ts`, `installedPlugins.ts`, plugin screens/controllers; implemented partial | `plugins-evidence.md` controller tests | Owned fixture marketplace lifecycle, Git failure, upgrade/remove and reconnect/readback | Historical owned-marketplace iOS evidence; pre-v4 build | Historical owned-marketplace Android evidence; pre-v4 build | Current source; no final v4 build | Every advertised operation against current contracts, failures, large catalogs, a11y and final-head run | Admin owner |
| Hub information, environment/fallback/MCP/path/launch settings preserve precedence and validate on hub | `cmd/evener-hub/app_rpc_settings_overview.go` and launch/config handlers; generated settings methods | `mobile-native/src/HubSettingsScreen.tsx`, editors and `hubOverview.ts`; implemented partial | `hubOverview.test.ts` and settings evidence; provider report covers omitted fields | Two hubs with inherited/effective/explicit values, invalid paths, stale open sheet, removal/readback, MCP command validation | Historical launch/environment/path/MCP evidence; iOS path gap noted; pre-v4 | Historical Android path/MCP evidence; pre-v4 | Current source; final v4 builds absent | iOS path, full schema, screen-reader/large-catalog and two-hub conflict coverage | Admin owner |
| Transcript preferences and keybindings expose current optional capabilities | Server keybinding/transcript handlers and generated capability/settings contracts | `mobile-native/src/NativePreferencesProvider.tsx`, `TranscriptPreferencesScreen.tsx`, `KeybindingPreferencesScreen.tsx`; screens, hub-scoped drafts, uncertainty journals and conflict review are implemented and wired through Hub Settings | [Keybinding evidence](keybinding-evidence.md) records 581 native tests/69 files plus TypeScript; [transcript evidence](transcript-preferences-evidence.md) records 436/54 plus TypeScript, including storage, conflict and recovery contracts; these are dated runs | Cited owned-v4 journeys checked revisioned readback, conflict/rebase, acknowledged cleanup and restoration; no real lost-reply injection is claimed | Dated keybinding pattern/unbind/restore/form journey on `d65d234f5`; transcript conflict/save/reader journey associated with `8bbff54c5`, not repeated on its final rebuild; details and limits in linked evidence | Historical Android work is retained; no Android acceptance claimed here, deferred beyond iOS-only v1 | Current screens exist; cited simulator artifacts and hashes remain dated feature evidence rather than a current release matrix | Current-artifact full journey, VoiceOver, hardware keyboard, iPad/landscape, largest text, reader combinations, overlapping hubs, lost-reply/process-death, physical device and signing/update qualification | Coordinator |
| Hub upgrade reports target, progress/result, disconnect recovery, and never replays mutation | Current `evener/upgrade` handler and real `selfupdate.Upgrade` | Native Hub update section and durable attempt/review controller implemented | 16 focused native tests; fixture archive/cancellation/repeated-hold race tests | Actual disposable-prefix download failure, canceled held download, deliberate retry, installation and installed-binary hub restart | [Current iPhone journey](hub-upgrade-evidence.md#actual-ios-installation-and-recovery--7-september-2026), cold-launch checkpoint and independent running-commit readback | Deferred beyond iOS-only v1 | Native `4f630af16`; backend `bb044658d-dirty`; hashes in receipt | Lost successful-install reply, overlapping hubs, public release service, iPad, physical-device/a11y and signed app update qualification | Coordinator |
| Reader position, navigation stack, drafts, and process lifecycle restore the right place | `mobile/src/state/navigation.ts`; conversation read/open contracts | `mobile-native/src/location.ts`, `nativeLocation.ts`, SQLite draft state; saved route/draft exists, reader offset remains open | Location restoration tests and conversation draft tests | Kill/background/restart during paging/streaming and hub switch; restore exact session, item identity/offset and newer draft | Historical cold-start/gesture evidence; first-pass v4 build not final reviewed source | Historical cold-start/back/font-recreation evidence; first-pass v4 build not final reviewed source | Current source; first-pass v4 Release builds installed but not final reviewed artifacts | Message-position restoration, streaming rejoin, form drafts, Android recreation cause and final-head builds | Coordinator |
| Physical accessibility, typography, gestures, light/dark, reduced motion, rotation, and performance meet release bar | Web UI source/style guide plus native platform contracts; server responsiveness measured in `docs/design/mobile/roster-performance.md` | Native UI is present; release qualification remains open (MOB-010/MOB-017) | Existing deterministic UI/controller tests; no complete physical/performance harness | Representative data streaming load, measured scroll/input latency, memory/leak checks, background/foreground and network recovery | Simulator observations only; no physical device, distribution signing, or final reviewed source build | Emulator observations only; ANR/font-scale root cause unresolved; no physical device/signing or final reviewed source build | First-pass v4 Release artifacts installed/launched but not final reviewed; source remains dirty | Physical iOS/Android, signing/distribution, large text/screen reader, reduced motion, dark/light, landscape, performance and final repository gates | Coordinator |
| Independently usable AppWire library and complete protocol recipes cover every supported method/notification | `cmd/evener-hub/frontend/src/protocol/types.gen.ts`, protocol README and server handlers | `cmd/evener-hub/frontend/src/protocol` package and docs; standalone package boundary exists | External tarball installation, ESM/CommonJS imports, declarations and packaged contract files passed; 13 queue contracts passed after RED/GREEN integration. [Protocol inventory](protocol-coverage.md) records 26 recipes, 83/91 methods and 3/36 notifications | [SDK management evidence](sdk-management-evidence.md) records the independent consumer and controlled owned-v4 workflows; the [protocol README](../../../cmd/evener-hub/frontend/src/protocol/README.md) is the runnable package guide | No native-specific evidence required; package acceptance is independent of native qualification | No native-specific evidence required; package acceptance is independent of native qualification | Lineage/maintenance recipes and empty-preview fix `ccdde59b9`; dated receipts identify independently tested tarballs and scope | Expand catalog-derived matrix, failures/disconnects, reserved rejection, and publish separately from local test; this inventory does not claim complete method/notification outcome coverage | Protocol owner |

## Required release record

Close a row only after recording the exact source commit (and dirty diff if
applicable), native artifact hashes/build configuration, deterministic command
and result, isolated hub/provider fixture identity without secrets, iOS
device/build observations, and the remaining acceptance decision. Android
qualification is deferred beyond the approved iOS-only v1 scope. The final
release record must include `make lint`, `make vet`, `make test`, the
frontend/browser gates, native tests/typecheck, the current-source iOS Release
build, and the physical-device/signing/performance evidence from the last
three rows. Historical screenshots and earlier green tests remain useful
context but cannot substitute for that record.

## Integration gates — 7 September 2026, b9e1b8926

Bot ran `make merge-approval-gate` to successful process exit at source
`b9e1b8926`: lint, build, full root/module/web tests, 639 native tests across
72 files, native TypeScript and outside-checkout package qualification all
passed. `make vet` also exited successfully. The five browser guards passed at
`e18c50c3e`; subsequent compiled changes were confined to native hub selection.
The two pre-existing Apple project/plist edits remained unchanged and unstaged.
The ledger clarification written during this run is documentation only.

The current iPhone Release bundle and actual save/connect/remove check are
recorded in [multiple-hub evidence](multiple-hubs.md#delayed-saved-hub-operations--7-september-2026).
The original owned hub, original conversation and seven retained drafts were
preserved. These gates and this scoped native journey do not close the remaining
distinct-hub fault/uncertain-operation, iPad, physical-device, accessibility,
performance, signing/distribution or full SDK workflow requirements.


## Credential integration gates — 7 September 2026, 700873dc9

`make merge-approval-gate` exited zero for the credential recipe integration:
lint, build, complete Go root/module tests, web, 639 native tests/72 files,
native TypeScript and external SDK package qualification passed. `make vet`
exited zero separately. The separately installed tarball passed 169 contracts
and the [owned-hub credential workflow](sdk-management-evidence.md#packaged-stored-credentials--7-september).
Only acceptance documentation changed while the gate ran; compiled source
remained at the stated commit. The unrelated Apple patch is unchanged.

The preference row now distinguishes implemented screens and dated observations
from missing current-artifact release evidence. Other historical rows still need
requirement-by-requirement refresh. Distinct-hub uncertain operations, OAuth and
remaining SDK workflows, full streaming/lifecycle, iPad, physical-device,
accessibility, performance and signing/distribution remain open for iOS v1.

## OAuth recovery and steering checkpoint — 7 September 2026

The [iOS OAuth recovery receipt](assets/ios-oauth-recovery-receipt.json)
qualifies interrupted device/browser completion, same-process foreground,
read-only credential status and cancel-then-switch against real auth handlers
with a scripted external OAuth service. Native Release source is
`759e7b10f`. The [SDK steering receipt](assets/sdk-steer-receipt.json)
records direct real-hub steering and drain composer input through a scripted
model boundary for SDK code integrated at `5429db421`.

The canonical `make merge-approval-gate` and separate `make vet` exited zero
over that integrated code: lint, build, Go root/modules, web, 663 native tests
across 73 files, strict native TypeScript and outside-checkout SDK package
qualification. The SDK code was committed during the run without changing its
bytes; subsequent edits are acceptance documentation. This is an exit-zero
canonical run, unlike the interrupted earlier OAuth gate.

The five real-browser guards in `make test-web-browser` and the final
`make secret-scan` also exited zero. Independent Luna evidence review found
no remaining scope, provenance or secret-exposure issue.

Both new runtime fixtures were shut down and their credential files removed.
The original hub/conversation, seven drafts and unrelated Apple changes were
preserved. The full iOS release matrix and complete method/notification recipe
coverage remain open; these scoped checks do not close the project.

## Upgrade and SDK coverage checkpoint — 7 September 2026

Native source `4f630af16` passed the actual disposable-hub upgrade journey,
including failure, durable recovery after cold launch, a deliberate later
attempt, installed-file verification and independent running identity after hub
replacement. The original hub/conversation, seven drafts and unrelated Apple
patch remain preserved. The canonical integration gate and separate vet run
exited zero with 671 native tests/73 files plus TypeScript and package checks.
See [upgrade evidence](hub-upgrade-evidence.md#actual-ios-installation-and-recovery--7-september-2026)
for artifact identities and limitations.

The new session-lineage and maintenance recipes bring the cookbook inventory to
83/91 cataloged methods across 26 recipes. Twenty-five focused contracts,
external installed-package qualification and the frontend gate passed. The
canonical integration gate and separate vet then exited zero at `ccdde59b9`,
including all Go modules, web, 671 native tests/73 files, native TypeScript and
the final package qualification. These recipe tests do not establish real
fork/resume/provider/plugin outcomes.
Seven supported methods still lack recipes, plus one reserved unsupported
method. Notification cookbook coverage remains 3/36.

Root's inspection of the compaction API log disproved a worker's attribution of
session-name generation to compaction summarization. The corrected
[SDK evidence](sdk-management-evidence.md#sdk-discovery-and-compaction-command-receipts--7-september)
records command acknowledgment and limited projection observations; completed
compaction remains unqualified. Current owned-hub discovery reads have separate
root-observed evidence in the upgrade receipt. The iOS release matrix and the
full SDK method/event acceptance goal remain open.
