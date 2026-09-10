# iPhone project status

Status snapshot: 10 September 2026, 16:38 UTC. This is a checkpoint toward iPhone v1. It is not a claim that all supported workflows or a fresh TestFlight release are qualified.

## Where the project stands

The landing batch has **22 merged PRs and six open PRs**. Main is `2664cc881`. The independently installed TypeScript SDK is on main. The native application remains in #1096 until its current review finishes.

The complete combined candidate `5137914ccb66b04aa1b980757888f63141cf2f27` passes `make merge-approval-gate`: backend and web modules, lint, generated-output freshness, secret scan, native tests, shared tests, native TypeScript, and the independently installed SDK qualification. This gate does not include the pending #1100 timing fix. See the [evidence map](2026-09-10-checkpoint.md).

The signed Release simulator app is the artifact built from `0097e3841fe9296ebe32da4cb6780cd7d2a8923f`. Its native and protocol inputs match native candidate `016b118f7` (full source identity retained in the local receipt); the backend used for the simulator journey remains `0d75cf8b5fabb9992f2938caf42f2e8129d9c8d1`. The TestFlight workflow will build its own signed device artifact from main after the remaining PRs land.

## Current review queue

| PR | Current head | Verified checks and remaining step |
| --- | --- | --- |
| [#1091: portable activity](https://github.com/prime-radiant-inc/evener/pull/1091) | `77fcabca9` | CI green; current RoboRev review pending. Continuation settlement and structured failure rendering corrections are included. |
| [#1096: native checkpoint](https://github.com/prime-radiant-inc/evener/pull/1096) | `016b118f7` | CI green; current RoboRev review pending. Includes final activity fixes and current main. |
| [#1098: environment recovery](https://github.com/prime-radiant-inc/evener/pull/1098) | `2cc8d50b4` | CI green; current RoboRev review pending. Restore publication, reserved identity, hook durability, rollback errors and diagnostic classification are covered. |
| [#1100: durable round timings](https://github.com/prime-radiant-inc/evener/pull/1100) | `9362ae189` | CI green; current RoboRev review has an unresolved compaction/timing identity finding. A public session regression reproduces the live/cold position mismatch. The initial owner-field fix is insufficient and remains local; this PR is not ready to merge. |
| [#1039: TestFlight automation](https://github.com/prime-radiant-inc/evener/pull/1039) | `7fe472fcf` | Clean RoboRev review at this head. Stacked on the native branch; no CI run at this head. Retarget to main, refresh, and obtain current CI/review before merge. |
| [#1102: checkpoint documentation](https://github.com/prime-radiant-inc/evener/pull/1102) | `dd6d1d085` | Clean RoboRev review at this head. Stacked on the native branch; no CI run at this head. Retarget or merge its prerequisites, then obtain current CI/review before merge. |

The merge rule is current-head CI **and an actual clean current-head RoboRev verdict**. A successful review status alone does not establish a clean verdict. Jesse authorized merging without another human approval when both conditions hold.

## What is implemented, and what is qualified

The native candidate contains saved hub connections, project-organized sessions, automatic list paging, conversation reading and live updates, durable drafts, send/queue/steer/stop, questions and approvals, session management, and provider/plugin/settings screens. Source presence and deterministic tests establish implementation coverage; they do not establish that every workflow has passed on a physical iPhone.

The simulator evidence covers the main conversation controls, question/approval slices from earlier journeys, reconnect, draft retention, and the final durable restart/cold-launch comparison. The latest comparison preserves 32 saved canonical items, all seven draft tables, eleven reader positions outside the current fixture, the interrupted turn, and the visible unsent draft. The provider request count remains 41.

Jesse's reported quality problems remain the product acceptance priorities: sessions must be easy to find by project, scrolling must continue loading, and sessions must open promptly. Project grouping and automatic loading are implemented; representative performance and full device acceptance remain open.

## Distribution

The last authenticated Apple query at 15:02 UTC found valid internally available builds 1, 2 and 3. They are historical artifacts; no build 4 was present at that observation. Requery before selecting the next build number. The next delivery step is a fresh main-based archive, upload, processing check and verified internal-group availability, followed by physical installation/update and smoke.

Drew's external tester record exists, but the last Apple query reported `NOT_INVITED`. External access is not complete. Beta review contact and demo information remain unfilled; the browser session requires fresh sign-in. Internal delivery can proceed with the existing API credentials independently of that external-review follow-up.

## Merged checkpoint work

| PR | Landed change |
| --- | --- |
| [#1066](https://github.com/prime-radiant-inc/evener/pull/1066) | fix(hub): bind device authorization polls to their provider |
| [#1067](https://github.com/prime-radiant-inc/evener/pull/1067) | fix(hub): report actual stored credential removal on logout |
| [#1068](https://github.com/prime-radiant-inc/evener/pull/1068) | fix(hub): return an empty array when no sessions are listed |
| [#1069](https://github.com/prime-radiant-inc/evener/pull/1069) | fix(hub): preserve restart entrypoints across directory aliases |
| [#1071](https://github.com/prime-radiant-inc/evener/pull/1071) | fix(server): include build version in AppWire initialization |
| [#1072](https://github.com/prime-radiant-inc/evener/pull/1072) | fix(hub): return an empty array for subagent previews |
| [#1073](https://github.com/prime-radiant-inc/evener/pull/1073) | fix(appwire): complete reasoning items at turn boundaries |
| [#1074](https://github.com/prime-radiant-inc/evener/pull/1074) | fix(appwire): omit absent images from steering notifications |
| [#1075](https://github.com/prime-radiant-inc/evener/pull/1075) | fix(transcript): preserve goal continuation identity on reload |
| [#1076](https://github.com/prime-radiant-inc/evener/pull/1076) | fix(client): validate and publish successful AppWire handshakes |
| [#1077](https://github.com/prime-radiant-inc/evener/pull/1077) | refactor(client): share question answer formatting with native consumers |
| [#1078](https://github.com/prime-radiant-inc/evener/pull/1078) | perf(hub): skip persisted task and delegate detail in session lists |
| [#1079](https://github.com/prime-radiant-inc/evener/pull/1079) | test(web): await provider dialog import before asserting its state |
| [#1081](https://github.com/prime-radiant-inc/evener/pull/1081) | fix(daemon): retry interrupted Linux process-exit probes |
| [#1082](https://github.com/prime-radiant-inc/evener/pull/1082) | test(appwire): prevent full-queue observation from blocking admission |
| [#1083](https://github.com/prime-radiant-inc/evener/pull/1083) | fix(web): let Vite own browser guard port allocation |
| [#1092](https://github.com/prime-radiant-inc/evener/pull/1092) | feat(client): package the standalone TypeScript AppWire SDK |
| [#1094](https://github.com/prime-radiant-inc/evener/pull/1094) | refactor(web): share pure composer and catalog helpers |
| [#1095](https://github.com/prime-radiant-inc/evener/pull/1095) | fix(keybindings): reject shortcuts with no key |
| [#1097](https://github.com/prime-radiant-inc/evener/pull/1097) | fix(transcript): preserve interrupted turns after reload |
| [#1099](https://github.com/prime-radiant-inc/evener/pull/1099) | fix(appwire): preserve steering ownership through transcript replay |
| [#1101](https://github.com/prime-radiant-inc/evener/pull/1101) | test(web): retain Vite startup diagnostics in concurrent guard failures |

## Remaining scope and known limits

Follow the [iPhone v1 remaining-work plan](ios-v1-remaining.md): deliver the current checkpoint, establish the daily loop, qualify the implemented functionality, then improve measured performance and interaction quality.

The initial #1101 CI run failed a stable-active-turn assertion once. More than 200 focused runs, the complete local root race gate, and the single CI rerun passed; the failure did not reproduce. #1101 itself adds browser-startup diagnostics and does not claim to fix that identity failure. Preserve the failed-run evidence if it recurs.

iPad and dedicated accessibility work are paused. Android and voice are deferred. Higher-level web state extraction into the SDK follows this checkpoint as a separate architectural task.
