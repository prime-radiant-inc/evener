# EPIC-004 — Recovery, privacy and runtime boundaries

**Summary:** Recovery, privacy and runtime boundaries
**Stories:** STORY-0021, STORY-0022, STORY-0023, STORY-0024, STORY-0025, STORY-0026, STORY-0027
**Primary sources:** `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md`
**Status:** 0/7 done

## STORY-0021

**Epic:** EPIC-004 — Recovery, privacy and runtime boundaries
**Title:** Recover voice delivery without duplicate submissions

**As a** an iPhone user
**I want** voice failures to preserve admitted requests and existing manager work
**So that** reconnecting does not duplicate my text submission or disable ordinary chat

**Acceptance criteria:**
- AC-1: Transport failures in Conversing, Speaking and SpeakingUpdate enter Recovering. Preserve valid manager work and journaled input, retry healthy portions automatically and return to ArmedQuiet when capture and authorization recover. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0014` · scenario:`SCENARIO-0015`
- AC-2: Preserve clientMutationId and expectedInstanceId through the bridge, including uncertain delivery. Replay the same payload without duplicate text submission and reject conflicting reuse; this is not an exactly-once guarantee for external tools. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0014` · scenario:`SCENARIO-0015`
- AC-3: Provider errors affect voice only. When cloud voice is unavailable, local wake gives an unavailable cue while text chat to the same session and existing work continue. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0015`
- AC-4: Never announce an unjournaled request as accepted, including during recovery or cloud voice failure. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0014` · scenario:`SCENARIO-0015`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:42-46`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:86-86`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:115-117`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:168-171`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:191-195`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:222-225`

**Status:** pending

## STORY-0022

**Epic:** EPIC-004 — Recovery, privacy and runtime boundaries
**Title:** Project voice recovery beside the typed outbox

**As a** user submitting a spoken request
**I want** durable admission through the existing text-mutation contract
**So that** received means durable receipt and voice does not create duplicate submission paths

**Acceptance criteria:**
- AC-1: Typed messages retain their existing outbox path. Voice journal records project into the phone's recovery UI under a distinct source identity without being copied into its send queue. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0019` · scenario:`SCENARIO-0020`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:253-253`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:272-288`

**Status:** pending

## STORY-0023

**Epic:** EPIC-004 — Recovery, privacy and runtime boundaries
**Title:** Recover voice and manager identity without replay drift

**As a** user recovering from a lost acknowledgement, disconnect or crash
**I want** the original intent and binding reconciled without changed meaning or identity
**So that** recovery neither duplicates work nor sends unresolved input to a new chief

**Acceptance criteria:**
- AC-1: After a lost acknowledgement, retry with the same mutation ID, method, payload and expected instance. Reusing that identity with a differing payload is an error, not an edit, even if activity state has changed. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0020` · scenario:`SCENARIO-0021` · scenario:`SCENARIO-0022` · scenario:`SCENARIO-0023`
- AC-2: After a crash, a persisted, never-attempted intent may dispatch only while it belongs to the current listening epoch and its listening grant remains valid. Once attempted, reconcile with its original session and instance before retrying. · impact:`cross-surface` · seam:`process-level` · scenario:`SCENARIO-0020` · scenario:`SCENARIO-0021` · scenario:`SCENARIO-0022` · scenario:`SCENARIO-0023`
- AC-3: A retired binding or changed session instance holds unresolved input for explicit recovery and never resends it into a newly created chief. · impact:`cross-surface` · seam:`process-level` · scenario:`SCENARIO-0020` · scenario:`SCENARIO-0021` · scenario:`SCENARIO-0022` · scenario:`SCENARIO-0023`
- AC-4: A persisted acceptance remains accepted when the user disarms; recovery preserves accepted work and its receipts rather than treating disarm as revocation of admission. · impact:`cross-surface` · seam:`process-level` · scenario:`SCENARIO-0020` · scenario:`SCENARIO-0021` · scenario:`SCENARIO-0022` · scenario:`SCENARIO-0023`
- AC-5: An audio turn interrupted before a completed text intent has no durable text receipt. Discard partial audio and ask for a repeat; this recovery contract does not require a raw microphone archive to claim stronger delivery. · impact:`cross-surface` · seam:`process-level` · scenario:`SCENARIO-0020` · scenario:`SCENARIO-0021` · scenario:`SCENARIO-0022` · scenario:`SCENARIO-0023`
- AC-6: Lost manager-creation responses require reconciliation. Do not blindly retry thread/start as though it has text-mutation replay semantics, and do not auto-create a replacement session when reconnect fails. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0020` · scenario:`SCENARIO-0021` · scenario:`SCENARIO-0022` · scenario:`SCENARIO-0023`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:272-280`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:290-303`

**Status:** pending

## STORY-0024

**Epic:** EPIC-004 — Recovery, privacy and runtime boundaries
**Title:** Authorize armed voice with revocable native grants

**As a** iPhone user
**I want** an explicitly armed, revocable voice authorization
**So that** locked voice does not broaden my existing authority

**Acceptance criteria:**
- AC-1: Load a revocable, session-bound voice grant while unlocked, retain it only in native memory for the armed lifetime, and renew it through the authenticated native connection while authorized. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0034` · scenario:`SCENARIO-0035` · scenario:`SCENARIO-0036`
- AC-2: On reconnect, the server validates device, epoch, session and scope. Never persist a broader hub credential with weaker Keychain accessibility merely to keep voice alive. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0034` · scenario:`SCENARIO-0035` · scenario:`SCENARIO-0036`
- AC-3: Process loss requires foreground re-arm. Grant expiry or revocation ends cloud access and gives an honest local cue without killing accepted manager work. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0034` · scenario:`SCENARIO-0035` · scenario:`SCENARIO-0036`
- AC-4: Arm explains that nearby people or a recording can issue requests and hear replies, including while locked. A wake word activates an already authorized channel and proves neither speaker identity nor informed consent. · impact:`local` · seam:`integration` · scenario:`SCENARIO-0034` · scenario:`SCENARIO-0035` · scenario:`SCENARIO-0036`
- AC-5: Existing sandbox, tool approvals and project boundaries continue to apply. Approval requests use the existing authenticated UI and cannot be answered for the user by background speech. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0034` · scenario:`SCENARIO-0035` · scenario:`SCENARIO-0036`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:423-434`

**Status:** pending

## STORY-0025

**Epic:** EPIC-004 — Recovery, privacy and runtime boundaries
**Title:** Minimize retained audio and disclose provider privacy

**As a** voice user
**I want** bounded audio retention and truthful provider disclosure
**So that** arming does not silently retain or export ambient conversations

**Acceptance criteria:**
- AC-1: Retain audio only in the bounded RAM ring and active transport buffers; Disarm, conversation close and failed partial turns clear both. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0037` · scenario:`SCENARIO-0038`
- AC-2: Journals retain finalized request text and receipts under existing session retention, not raw ambient audio. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0037` · scenario:`SCENARIO-0038`
- AC-3: Diagnostics default to IDs, times, sizes, state and reason codes, with no transcripts, PCM, credentials or provider headers. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0037` · scenario:`SCENARIO-0038`
- AC-4: Disclose the chosen voice provider and distinguish it from a possibly different manager-model provider. Do not equate store:false with zero provider retention: the cited OpenAI policy has no API training by default and default 30-day abuse-monitoring retention for Live and Realtime. · impact:`local` · seam:`integration` · scenario:`SCENARIO-0037` · scenario:`SCENARIO-0038`
- AC-5: Never keep the cloud microphone open merely because the phone is armed. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0037` · scenario:`SCENARIO-0038`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:435-443`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:480-482`

**Status:** pending

## STORY-0026

**Epic:** EPIC-004 — Recovery, privacy and runtime boundaries
**Title:** Reconnect voice without replaying uncertain work

**As a** voice user
**I want** voice transport recovery without losing the chief or replaying old effects
**So that** a connection failure does not become a second request or misleading speech

**Acceptance criteria:**
- AC-1: A lost voice socket stops output, fences its generation and reconnects with bounded backoff. Reconcile journaled input and committed output before admitting new audio. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0039` · scenario:`SCENARIO-0040` · scenario:`SCENARIO-0041`
- AC-2: Reconnect replays neither provider function calls nor old spoken backlog. Mark uncertain playback interrupted and let the user request repeat. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0039` · scenario:`SCENARIO-0040` · scenario:`SCENARIO-0041`
- AC-3: Provider replacement or expiry preserves the manager session and seeds only bounded context from committed text and known action outcomes, excluding unplayed-speech claims. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0039` · scenario:`SCENARIO-0040` · scenario:`SCENARIO-0041`
- AC-4: Reject unknown bindings and stale instances narrowly, then offer existing session recovery without disabling healthy text chat or deleting queued work. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0039` · scenario:`SCENARIO-0040` · scenario:`SCENARIO-0041`
- AC-5: After reconnect or missed events, refresh authoritative accepted and consumed input state before choosing speech. Best-effort notification arrival order alone cannot establish freshness. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0039` · scenario:`SCENARIO-0040` · scenario:`SCENARIO-0041`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:444-453`

**Status:** pending

## STORY-0027

**Epic:** EPIC-004 — Recovery, privacy and runtime boundaries
**Title:** Replace voice runtimes without replacing the chief

**As a** voice runtime maintainer
**I want** small replaceable responsibilities with truthful capabilities and usage
**So that** runtime changes preserve durable intent and manager authority

**Acceptance criteria:**
- AC-1: Keep AudioDevice, WakeAndSpeechDetector, ConversationVoiceAdapter and ChiefSessionBridge as replaceable responsibilities, not a plugin framework or universal audio protocol; implement one adapter and one device first. · impact:`none` · seam:`integration`
- AC-2: Runtime selection is session configuration. Changing it retires the voice generation, never the chief-of-staff session. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0042` · scenario:`SCENARIO-0043`
- AC-3: Declare actual capabilities for input completion, tool-call completion, explicit cancel, playback truncation, exact-reading guarantee, transcript availability, usage reporting and connection limits; a prompt requesting a behavior cannot make its capability true. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0042` · scenario:`SCENARIO-0043`
- AC-4: Missing adapter semantics are supplied and tested by the bridge or the adapter remains experimental. Later local ASR/TTS or GPT-Live implementations of the voice-adapter boundary must not change durable intent or manager authority. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0042` · scenario:`SCENARIO-0043`
- AC-5: Local wake/VAD is mandatory for quiet-state privacy and cost. Report voice usage separately from manager inference. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0042` · scenario:`SCENARIO-0043`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:457-474`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:480-482`

**Status:** pending
