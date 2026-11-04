# Roadmap

This is an implementation plan for the approved
[voice design](../specs/2026-11-04-voice-chief-of-staff-design.md), not evidence of
implemented behavior. All iterations are pending. The manager already exists at
`f5e5516c19`; no manager merge, architecture reapproval, browser implementation or
broad compatibility project is a prerequisite. Status lives here, not in a second
progress ledger. Source coverage and review evidence live in [evidence.md](evidence.md).

## Walking skeleton (ITER-0000)

**Intent:** Talk to the existing manager from a foreground iPhone and hear its committed answer without creating a second agent.
**Design rationale:** Talk avoids making an unqualified wake phrase a prerequisite for the first useful complete journey. Durable intake, scoped authority and originating-model-request provenance are foundations, not later retrofit tasks. Native wake/locked qualification begins in parallel and is the next critical-path closure, not a substitute desktop demo.
**Journey scenario:** JOURNEY-0008 — First foreground conversation reaches the existing manager and real playback.
**Stories committed:**
- STORY-0001 (EPIC-001) — Choose one durable manager for voice and text
- STORY-0002 (EPIC-001) — Relay voice through an authenticated replaceable Realtime adapter
- STORY-0003 (EPIC-001) — Bind foreground voice to the authenticated manager
- STORY-0004 (EPIC-001) — Admit one faithful text intent per audio turn
- STORY-0005 (EPIC-001) — Persist voice admission through existing daemon custody
- STORY-0006 (EPIC-002) — Run an app-lifetime native foreground audio service
- STORY-0007 (EPIC-002) — Arm local listening and activate with Talk
- STORY-0009 (EPIC-002) — Continue conversations with one timing policy
- STORY-0010 (EPIC-002) — Stop speech locally while preserving manager work
- STORY-0011 (EPIC-002) — Expose native voice controls and truthful state
- STORY-0014 (EPIC-003) — Present authoritative output with a powerless speech model
- STORY-0016 (EPIC-003) — Fence speech with every channel's consumed inputs
- STORY-0018 (EPIC-003) — Reconcile barge-in using rendered playback
- STORY-0019 (EPIC-003) — Keep speech controls separate from session Stop
- STORY-0024 (EPIC-004) — Authorize armed voice with revocable native grants
- STORY-0025 (EPIC-004) — Minimize retained audio and disclose provider privacy
- STORY-0027 (EPIC-004) — Replace voice runtimes without replacing the chief
- STORY-0032 (EPIC-005) — Prove the first voice-to-chief journey
**Status:** pending

```mermaid
flowchart LR
    A[Foreground Arm and Talk on iPhone] --> B[Scoped hub voice socket]
    B --> C[Persist VoiceTurn, Realtime interpretation]
    C --> D[Persist immutable VoiceIntent]
    D --> E[Existing manager start or steer]
    E --> F[Committed reply, originating request frontier]
    F --> G[Tool-free presentation, generation-tagged audio]
    G --> H[Native playback, actual played acknowledgement]
```

The initial observable request is a read-only manager inspection of an existing
session. The manager must invoke its existing tool, commit the result, and have
that result presented through the real production voice path. It is not a direct
provider status fixture and not a spoken speculative tool result. The same manager
remains available through typed chat. A deterministic provider can drive and prove
Evener plumbing, but cannot prove human meaning, speech fidelity, acoustics or
locked lifetime.

### Ordered implementation work

1. **Harness first.** Extend the real isolated hub/daemon fixture before feature
   code. Add a voice journey test and an opt-in real-provider/native procedure,
   initially failing because the voice capability/path is absent. Establish
   source/intent/output observations and the exact teardown. Require the named
   test to be listed and the triggering path to execute; an empty test selector,
   skipped live check or pre-rendered reply is not a pass.
2. **Intake and provenance contract.** Reuse mutation/persistence primitives.
   Persist binding, turn and immutable intent with stable method/payload/mutation
   ID/expected instance before dispatch. Keep voice recovery ownership distinct
   from the JS outbox. Add all-channel input identities and exact model-request
   consumed-frontier metadata to committed output; do not derive consumption from
   acceptance, transcript order, a greatest sequence, or current mutation state.
3. **Real hub bridge and one Realtime adapter.** Advertise capability only for
   supporting hub/daemon paths. Issue a narrow revocable in-memory native grant
   through ordinary foreground authentication. The voice socket validates device,
   epoch, session and instance without broadening the general hub credential.
   Exclude it from raw HTTP request recording. Keep PCM off AppWire input. Use
   app-owned turn boundaries, mono PCM16 24 kHz conversion, no interpretation audio,
   one strict `submit_utterance(text)` call, and one tool-free presentation phase.
4. **Native owner and opt-in controls.** Put capture/playout/voice socket in an
   app-lifetime Swift service, not the existing AppState-owned JS connection.
   Foreground Arm loads the narrow grant; Talk opens cloud input. Use a real local
   speech detector for endpointing and local playback stop, one voice-processed
   engine, bounded buffers, actual route formats and played-sample accounting.
   Expose Arm/Disarm, Talk, Stop speaking, bound session, route, interpreted text,
   delivery, canonical reply, and local/cloud mic state using existing components.
   Preserve general credential storage and installed app identity/data.
5. **Close the first assembled path.** Demonstrate one read-only manager request,
   receipt, canonical committed result and native playback on the same instance.
   Exercise text fallback and old-hub/old-daemon capability absence. Run the
   deterministic sentinel every iteration. Run the exact live relay/presentation
   and physical procedure separately; record any unavailable resource as an open
   residual rather than synthesizing a pass.

### File-level integration seams

Existing anchors below are not a claim that the voice implementation exists.
New filenames are proposed implementation targets, not a plugin framework.

| Responsibility | Existing seam | Proposed first-slice files/checks |
|---|---|---|
| Real integration harness | `cmd/evener-hub/e2e_turn_control_test.go`, `startHubStackWithOptions` | `cmd/evener-hub/e2e_voice_test.go`, isolated real hub/daemon journey and crash-ready fixture controls |
| HTTP lifetime and auth | `cmd/evener-hub/web.go`, `WebServer`, `newWebServer`, `Handler`; `cmd/evener-hub/http_recorder.go` | `cmd/evener-hub/voice_http.go`, scoped grant/socket handler excluded from header/body recording |
| Capability and manager routing | `cmd/evener-hub/app_rpc.go`, `newHubAppServerWithNavigationAndTrace`, `registerThreadHandlers`, session operations | Existing capability types plus new `voice_bridge.go`; preserve existing start/steer admission, no acoustic interrupt RPC |
| Voice records and adapter | `agent/session_client_mutation.go`, `agent/session_client_mutation_persist.go`, `agent/session_client_mutation_queue.go`; existing hub SQLite convention `cmd/evener-hub/internal/hubcore/sqlite_dsn.go` | `cmd/evener-hub/voice_journal.go`, `voice_realtime.go`, focused tests for persisted-before-send and completed-response correlation |
| Consumed-input provenance | `agent/session_model_call.go` final `prepareModelRequestWithError` history after skill rebuilds, `stagePresentedAttentionCoverage`; `agent/session_lifecycle.go`, `agent/session_execution.go`, `agent/session_tools.go`; full restore in `agent/session_init.go` | Carry originating request frontier through model-attempt metadata into assistant/tool-output persistence and history projections. Preserve exact input identities through drain/promote/fold and restore, including daemon-minted identities for direct inputs. Choose a bounded nonquadratic encoding without freezing the scout's proposed type names. |
| Native service and UI | `mobile-native/App.tsx`, `mobile-native/app.config.js`, `mobile-native/plugins/with-ios-preconditions.js`, `mobile-native/plugins/with-shared-sources.js`; leave `mobile-native/src/ConnectionProvider.tsx` and `hubConnection.ts` foreground JS lifetime intact | New tracked `mobile-native/modules/evener-voice/` Swift Expo local module, `mobile-native/src/voice/` controller/controls, voice microphone/background configuration and lifecycle tests. Follow Expo SDK 57 docs. Replace the approved iOS no-microphone contract and `mobile-native/src/cameraPermission.test.ts` assertion with generated voice permission: both existing plugins' `microphonePermission:false` remove the iOS usage description. Preserve camera behavior and Android `recordAudioAndroid:false` / blocked `RECORD_AUDIO`, not iOS microphone-disabled behavior. |
| Product references | `docs/product/subsystems.md`, `docs/product/message-recovery.md`, native build/distribution guide | Update evergreen ownership and recovery contracts with implemented behavior, not before it exists |

The proposed native module uses `expo-module.config.json` with iOS-only
`EvenerVoiceModule` and `EvenerVoiceAppDelegateSubscriber` registration and a local
pod depending on `ExpoModulesCore`. Its tracked `ios/` sources are
`EvenerVoiceModule.swift`, `EvenerVoiceAppDelegateSubscriber.swift`,
`VoiceService.swift`, `AudioDevice.swift`, `VoiceSocket.swift`,
`PlaybackTimeline.swift`, and `WakeAndSpeechDetector.swift`. The subscriber retains
the service independently of React screens. Add `src/voice/VoiceProvider.tsx` and
`VoiceControls.tsx`, wire them through `App.tsx`, `src/screens.tsx` and
`src/SessionMenu.tsx`, include new TypeScript in `package.json` checks, and regenerate
`Podfile.lock` under the existing controlled process. Never edit generated iOS
AppDelegate/SceneDelegate files. Keep the narrow grant only in native memory;
removing its bound hub explicitly revokes it, while screen unmount does not.

Typed-correction admission must retire active speech in the first slice, not wait
for full recovery. Use a revisioned control notification at the existing admission
refresh seam in `server/appwire_turn_handlers.go`; sampling a later transcript or
waiting for incorporated status is too late. The same path supports ITER-0003
reconciliation without moving first-slice correction safety to that iteration.

### No hidden prerequisites

- The existing real-hub manager fixture passed in the parent's baseline run; it
  proves manager plumbing, not voice. The first slice must add and execute its own
  journey through production voice code.
- The working baseline has no voice capability, scoped grant, bridge journal,
  production relay, native audio owner or consumed-input frontier. Those are
  first-slice work, never assumed prerequisites supplied by a spike.
- `response.function_call_arguments.done` can describe interrupted/incomplete or
  cancelled output. Admit only a completed `response.done` containing exactly one
  valid relay call, correlated to the persisted turn and committed input item.
  Audio/transcript completion is not playback completion.
- A steer marked incorporated can already be appended while an older model
  request is executing. Output must retain that older request's frontier even if
  newer input was appended before tool completion. The daemon's mutation-state
  authority marker, not the hub's broad transcript-read flag, governs reconciliation.
- An unavailable provider, missing grant, uncertain receipt or missing provenance
  never licenses guessed text, duplicate retries, guessed RPCs or stale speech.
  Retain durable uncertainty and usable text; full automatic recovery follows.
- Local native dependencies, CocoaPods/Xcode, signing, provider inference access,
  and a reachable physical iPhone are explicit execution prerequisites. Planning
  installs none. The current Mac probe reported Xcode first-launch exit 69 / authorization required;
  no physical phone was available. These block their checks, not Go implementation.

### Harness design and first-journey evidence

**Launch/teardown:** Reuse the isolated `startHubStackWithOptions` fixture with
private HOME/state/configuration, ephemeral ports, real daemon/session storage,
real manager tools and cleanup that waits for process exit. Never touch the live
hub or installed phone data as a disposable fixture. Each run owns its records.

**Inputs/observations:** Feed deterministic PCM/provider events only at the
external voice-provider/device boundary. Observe real journal rows, mutation
receipts, transcript admissions, manager tool effects, input-consumption IDs,
committed output provenance, presentation generations and playback acknowledgements.
Native unit clocks may prove mapping/fencing; actual audible stop remains physical.
Use structured state/action assertions, not copied human-readable UI phrases.

**Live/hardware mode:** A separate explicit voice opt-in plus credentials selects
real provider calls and independently authored human utterance fixtures. Record
canonical text, generated wording, action outcomes and actual heard/rendered output
for scored qualification, never in default diagnostics. Physical runs record build,
device/OS, route, lock state, trial counts and acquisition method. Credentials,
provider headers and raw ambient audio are not routine evidence artifacts.

**Fixtures/sentinel:** Version consented intentional utterance fixtures and
independent expected intent/outcome observations with the tests. Do not generate
expected outcomes from adapter prompts or implementation code. Keep one foreground
read-only journey and one old-output/correction fence case as the small deterministic
sentinel; add later recovery cases to the same harness. Physical and live portions
stay separately named and unpassed until run.

**Closure:** Passing deterministic plumbing allows continued development; it does
not close the full real-audio journey or qualify v1. A foreground native/live run
closes only that journey. Wake, lock, acoustic, recovery and release gates below
remain mandatory and cannot be inferred by adding separate spike results together.

## Iteration list

### ITER-0001 — Native wake and locked audio qualification

**Stories:** STORY-0008, STORY-0012, STORY-0029, STORY-0030
**Rationale:** Implement local wake activation and qualify built-in mic/speaker first. Begin candidate/license and native lock work alongside the skeleton rather than letting hosted plumbing hide the highest risk.
**Status:** pending
**Impacted scenarios:** JOURNEY-0003, JOURNEY-0004, JOURNEY-0006, JOURNEY-0015, JOURNEY-0016, JOURNEY-0017, JOURNEY-0018, SCENARIO-0006, SCENARIO-0012, SCENARIO-0013, SCENARIO-0044, SCENARIO-0045, SCENARIO-0046
**Look-ahead check:** The native graph/grant/Talk path comes from ITER-0000. Full journal and transport fault recovery remains ITER-0003; neither missing hardware nor an all-miss phrase licenses a simulated pass.

### ITER-0002 — Clarification, references and proactive work conversation

**Stories:** STORY-0013, STORY-0015, STORY-0017, STORY-0020, STORY-0037
**Rationale:** Add durable pending-question batches and platform-sourced lower-trust presentation context, then proactive updates and real scoped correction/cancellation. Reuse the first-slice admission/provenance and playback contracts.
**Status:** pending
**Impacted scenarios:** JOURNEY-0011, JOURNEY-0012, JOURNEY-0013, JOURNEY-0014, JOURNEY-0022, JOURNEY-0023, SCENARIO-0010, SCENARIO-0026, SCENARIO-0030, SCENARIO-0033, SCENARIO-0053, SCENARIO-0054, SCENARIO-0058
**Look-ahead check:** Pending question events today precede durable tool output and live helpers do not restore the full batch. Add durable identity/public projection before speech, not a second questionnaire. The complete 13-interaction correction chain is JOURNEY-0013.

### ITER-0003 — Full recovery and persistent conversations

**Stories:** STORY-0021, STORY-0022, STORY-0023, STORY-0026, STORY-0031, STORY-0033, STORY-0036, STORY-0038
**Rationale:** Complete journal reconciliation, distinct recovery projection, original-instance fences, voice/socket/provider replacement and physically qualified locked renewal. Preserve earlier work through two conversations and disconnect.
**Status:** pending
**Impacted scenarios:** JOURNEY-0013, JOURNEY-0017, JOURNEY-0018, JOURNEY-0019, JOURNEY-0022, JOURNEY-0023, SCENARIO-0014, SCENARIO-0015, SCENARIO-0019, SCENARIO-0020, SCENARIO-0021, SCENARIO-0022, SCENARIO-0023, SCENARIO-0039, SCENARIO-0040, SCENARIO-0041, SCENARIO-0045, SCENARIO-0046, SCENARIO-0047, SCENARIO-0048, SCENARIO-0050, SCENARIO-0051, SCENARIO-0052, SCENARIO-0053, SCENARIO-0054
**Look-ahead check:** Use authoritative daemon mutation state and a revisioned control notification for typed input retirement; saved transcript flags alone are insufficient. Refresh before fresh admission. Never blindly replay creation or retarget unresolved work.

### ITER-0004 — Live quality and signed TestFlight qualification

**Stories:** STORY-0028, STORY-0034, STORY-0035, STORY-0039
**Rationale:** Run the full meaning/latency and remaining release corpus, recheck model access/prices and Apple policy, then signed Release and TestFlight update while preserving the installed app.
**Status:** pending
**Impacted scenarios:** JOURNEY-0007, JOURNEY-0020, JOURNEY-0021, JOURNEY-0024, SCENARIO-0016, SCENARIO-0049, SCENARIO-0055, SCENARIO-0056
**Look-ahead check:** Built-in route first. No other route or Full model becomes qualified by association. If generative speech fidelity fails, use a separately qualified exact-reading chained adapter rather than weakening the gate. Browser is optional deferred.

## Physical and live residuals — mandatory, all unpassed

- Native wake: at least 60 intended human/physical trials, ≥95% detection and no lost command onset; score near misses separately. At least 30 negative-audio hours with zero activations and 100 self-output trials, including the phrase. The old Hey Evener all-miss model is not qualified. Review runtime license and model/training-data rights separately.
- Locked built-in route: foreground Arm then 5 and 30 minutes locked with no playback, wake/converse/interrupt while locked; unplugged eight-hour no-debugger soak, no unexplained capture loss, battery and thermal records, incremental battery ≤2 percentage points/hour against matched disarmed use.
- Acoustic stop: at least 30 double-talk trials per enabled route, p95 onset-to-audible-stop ≤200 ms, no stale tail, mic live; provider acknowledgement measured separately. Unsupported routes need explanation and consent before returning to a qualified route; no untested Bluetooth promise.
- Meaning and latency: at least 30 human turns covering names/amounts/negations/corrections/cancellation with independent canonical/speech scoring, no unintended mutation or material outcome change. Full and Mini use the same corpus before a quality claim. At least 30 warm read-only turns, p95 final voiced input to first useful audible result ≤3 seconds; report cold/wake/backend separately.
- Recovery and distribution: real crashes/lost receipts/changed instances, timed 60-minute provider replacement, physical interruptions/media reset/Bluetooth/network changes and locked reconnect/renewal. Signed Release plus TestFlight processing and install/update on Jesse's phone; build, launch and upload alone prove none of the acoustic/locked promises.

No resource absence is a waiver. Keep voice opt-in until the physical gates pass.
Jesse's reachable phone or an authorized TestFlight install is needed for physical
evidence. The builder can choose the wake/VAD package and prompts within the
approved contract. Escalate only a real consequential change, such as incompatible
licensing/payment obligations, a requested policy/authority change, or failure
requiring a different shipping promise. Do not ask again to approve microphone
permission, ambient armed authority, proactive locked speech or paraphrasing.

## Scope boundaries and checks

Foreground browser, Android, system-wide hotword/background relaunch, multi-mic,
speaker identity, arbitrary spoken commands and voice-only approval escalation are
not committed work. Capability absence disables only voice. The runtime/provider
interfaces are four replaceable responsibilities, not a framework.

All stories have a single delivery assignment. Dependency splits were made before
first numeric IDs, avoiding the workflow validator's unsupported letter suffixes.
Pending-question, voice recovery projection, wake activation, persistent journey
and physical recovery obligations remain separate from the foreground foundation.
The first slice may be implemented in parallel at the harness, runtime provenance,
hub adapter/journal and native-module seams once their minimal shared contracts
are agreed; no speculative universal protocol or full adapter matrix is required.

Planning validators and their exact observed outputs are in [evidence.md](evidence.md).
Behavior commands remain TBD until implementation supplies real executables. Before
claiming a targeted check, list the test selector, inspect the runner, run it to
exit zero, and confirm the required scenario actually executed. Hardware/live
checks must name their unmet residual when unavailable.
