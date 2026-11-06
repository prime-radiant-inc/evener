# EPIC-002 — Native audio and listening

**Summary:** Native audio and listening
**Stories:** STORY-0006, STORY-0007, STORY-0008, STORY-0009, STORY-0010, STORY-0011, STORY-0012
**Primary sources:** `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md`
**Status:** 0/7 done

## STORY-0006

**Epic:** EPIC-002 — Native audio and listening
**Title:** Run an app-lifetime native foreground audio service

**As a** an iPhone user
**I want** native capture and playback that do not belong to a screen
**So that** voice activation and interruption share a working audio graph

**Acceptance criteria:**
- AC-1: Add an app-lifetime Swift owner for capture, AEC, routes, local activation, playout and the voice socket, independent of screens and React Native AppState. Deliberately implement the approved microphone/background product contract, including the formerly disabled microphone configuration and old permission-absence assertion, rather than silently changing that contract. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0005`
- AC-2: Configure AVAudioSession as playAndRecord/voiceChat and enable AVAudioEngine voice processing while the engine is stopped, before final graph setup. Capture and provider PCM playback share one engine and its echo reference; voiceChat mode alone is not evidence that AEC is enabled. · impact:`local` · seam:`integration` · scenario:`SCENARIO-0005`
- AC-3: Use actual route formats, convert to the adapter format and track output samples in the provider audio timeline, with the played watermark owned by native playback. The exercised Realtime format is mono PCM16 at 24 kHz, not an assumption about every hardware route. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0005`
- AC-4: Audio callbacks perform neither network I/O nor inference. Feed bounded buffers to local wake/VAD workers and use a real speech detector, not an RMS threshold labelled as VAD. · impact:`local` · seam:`integration` · scenario:`SCENARIO-0005`
- AC-5: Keep capture alive while provider audio is playing, using the shared capture/playback echo reference for native AEC and local speech detection. · impact:`journey` · seam:`e2e` · scenario:`JOURNEY-0002` · scenario:`JOURNEY-0008`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:89-90`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:108-110`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:113-124`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:134-144`

**Status:** pending

## STORY-0007

**Epic:** EPIC-002 — Native audio and listening
**Title:** Arm local listening and activate with Talk

**As a** an iPhone user
**I want** an explicit private listening period with local wake or Talk activation
**So that** background room audio is not sent to a speech provider

**Acceptance criteria:**
- AC-1: Start Disarmed. Only explicit foreground Arm with successful microphone permission and voice grant enters ArmedQuiet. Load the narrow voice grant into native memory at Arm without weakening the general hub credential's existing unlocked SecureStore accessibility. · impact:`cross-surface` · seam:`app-level` · scenario:`SCENARIO-0006`
- AC-2: ArmedQuiet processes microphone audio locally, retaining a rolling two-second PCM buffer in RAM and sending no microphone frames to a cloud provider. An authenticated manager-update control subscription may remain open. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0006`
- AC-3: Talk moves ArmedQuiet to Conversing, plays a short local cue and opens the provider session. · impact:`journey` · seam:`e2e` · scenario:`JOURNEY-0003` · scenario:`JOURNEY-0004` · scenario:`JOURNEY-0008`
- AC-4: A Talk press uploads audio beginning at the press, never the earlier rolling-buffer segment. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0006`
- AC-5: Anyone nearby can speak during the armed period. Wake detection is activation, not authentication or speaker recognition. · impact:`journey` · seam:`e2e` · scenario:`JOURNEY-0003` · scenario:`JOURNEY-0004` · scenario:`JOURNEY-0008`
- AC-6: User Disarm from every armed state, including Recovering, enters Disarmed and stops microphone capture and speech, including proactive speech. · impact:`journey` · seam:`e2e` · scenario:`JOURNEY-0003` · scenario:`JOURNEY-0004` · scenario:`JOURNEY-0008`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:31-35`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:90-90`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:178-209`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:217-221`

**Status:** pending

## STORY-0008

**Epic:** EPIC-002 — Native audio and listening
**Title:** Activate local wake without losing command onset

**As a** an iPhone user
**I want** an explicit private listening period with local wake or Talk activation
**So that** background room audio is not sent to a speech provider

**Acceptance criteria:**
- AC-1: Local wake or Talk moves ArmedQuiet to Conversing, plays a short local cue and opens the provider session while preserving speech immediately following the wake phrase. · impact:`journey` · seam:`e2e` · scenario:`JOURNEY-0003` · scenario:`JOURNEY-0004`
- AC-2: Upload from the detected wake start, never from an earlier rolling-buffer segment; Talk begins at the press. If wake timing is uncertain, use the conservative detected start and qualify command-onset loss rather than uploading arbitrary pre-wake audio. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0006`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:31-35`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:90-90`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:178-209`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:217-221`

**Status:** pending

## STORY-0009

**Epic:** EPIC-002 — Native audio and listening
**Title:** Continue conversations with one timing policy

**As a** an iPhone user
**I want** natural follow-ups and bounded microphone activity
**So that** I do not repeat the wake phrase for each turn or keep cloud input open indefinitely

**Acceptance criteria:**
- AC-1: Keep the initial listening and conversation timing defaults in one policy configuration, not scattered client timers; these are testable starting values, not measured optima. · impact:`local` · seam:`unit` · scenario:`SCENARIO-0007` · scenario:`SCENARIO-0008`
- AC-2: In Conversing, 600 ms of trailing nonspeech ends a turn and the maximum utterance is 30 seconds. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0007` · scenario:`SCENARIO-0008`
- AC-3: Follow-ups do not require another wake word. The follow-up window lasts 12 seconds after output ends; expiry after Speaking or SpeakingUpdate returns to ArmedQuiet. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0007` · scenario:`SCENARIO-0008`
- AC-4: Close cloud microphone input after 30 seconds without speech. Conversing returns to ArmedQuiet on idle or explicit end conversation. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0007` · scenario:`SCENARIO-0008`
- AC-5: A long backend wait may close the provider connection while the manager keeps working; a fresh result reopens output without replacing the persistent manager session. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0007` · scenario:`SCENARIO-0008`
- AC-6: Conversing enters Speaking for a fresh eligible committed reply, rather than treating an uncommitted provider utterance as the manager's answer. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0007` · scenario:`SCENARIO-0008`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:5-8`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:88-88`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:175-189`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:210-216`

**Status:** pending

## STORY-0010

**Epic:** EPIC-002 — Native audio and listening
**Title:** Stop speech locally while preserving manager work

**As a** an iPhone user
**I want** barge-in and Stop speaking to silence the phone without accidentally stopping work
**So that** I can correct the conversation separately from cancelling an Evener task

**Acceptance criteria:**
- AC-1: In Speaking and SpeakingUpdate, continue AEC and local VAD. Near-end speech immediately stops playback locally, enters Conversing and opens the follow-up window without depending on a provider round trip. · impact:`journey` · seam:`e2e` · scenario:`JOURNEY-0005`
- AC-2: Acoustic barge-in and Stop speaking stop speech, not manager work. Local silence does not imply that any Evener cancellation occurred. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0009`
- AC-3: Manager work stops only after a separate explicit instruction reaches Evener and its outcome is known. Preserve turn/interrupt's semantics of cancelling the current turn on arrival and optionally holding queued input; never use it as the acoustic stop mechanism. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0009`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:5-8`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:34-35`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:87-87`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:144-144`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:185-186`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:215-216`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:227-229`

**Status:** pending

## STORY-0011

**Epic:** EPIC-002 — Native audio and listening
**Title:** Expose native voice controls and truthful state

**As a** an iPhone user
**I want** accessible controls and an accurate account of listening and delivery
**So that** I know which manager and route are active and what was actually received and answered

**Acceptance criteria:**
- AC-1: Expose Arm/Disarm, Talk and Stop speaking together with the bound chief-of-staff session and current audio route. · impact:`local` · seam:`app-level` · scenario:`SCENARIO-0011`
- AC-2: Show the interpreted request, delivery state and canonical committed reply alongside the voice interaction. · impact:`cross-surface` · seam:`app-level` · scenario:`SCENARIO-0011`
- AC-3: Distinguish local-only microphone processing from cloud audio transmission. If capture cannot be restored, show listening paused, not a green microphone derived only from desired state. · impact:`cross-surface` · seam:`app-level` · scenario:`SCENARIO-0011`
- AC-4: Use existing native components and accessibility conventions. The source sketch describes behavior and does not approve new widget primitives or require its sample wording and layout. · impact:`none` · seam:`app-level`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:42-46`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:149-151`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:227-243`

**Status:** pending

## STORY-0012

**Epic:** EPIC-002 — Native audio and listening
**Title:** Recover native listening across lock and audio changes

**As a** an armed iPhone user
**I want** honest locked listening and safe recovery from audio interruptions
**So that** voice resumes only when real capture and a safe output route are available

**Acceptance criteria:**
- AC-1: An explicitly foreground-armed native listening lifetime can continue while the iPhone is locked, using active audio background execution, not silent playback, VoIP workarounds or a background URLSession treated as a persistent realtime WebSocket. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0012` · scenario:`SCENARIO-0013`
- AC-2: Persistence means durable Evener identity plus the user-armed audio lifetime. Force-quit, reboot, process eviction or permission revocation can end listening; process exit or revocation disarms it, and Reopen plus Arm restores the lifetime. Do not claim system-wide hotword access, background relaunch or indefinite listening from a running timer. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0012` · scenario:`SCENARIO-0013`
- AC-3: On headset-to-speaker change, stop output before it leaks onto the new route, rebuild the graph and resume listening when the route is healthy. Ask before resuming speech on the newly public route. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0012` · scenario:`SCENARIO-0013`
- AC-4: Route failures and iOS interruptions enter Recovering as appropriate. Stop capture/output as required by iOS, retry healthy portions and automatically re-arm only if the OS permits and the user has not Disarmed; return to ArmedQuiet when capture and authorization recover. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0012` · scenario:`SCENARIO-0013`
- AC-5: After media-services reset, rebuild the engine and fence old-engine callbacks. If actual capture cannot recover, remain truthfully listening paused rather than indicating a healthy microphone from desired state alone. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0012` · scenario:`SCENARIO-0013`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:17-20`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:32-33`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:56-59`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:144-158`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:190-199`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:222-225`

**Status:** pending
