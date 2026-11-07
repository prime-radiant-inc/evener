# EPIC-005 — Qualification and distribution

**Summary:** Qualification and distribution
**Stories:** STORY-0028, STORY-0029, STORY-0030, STORY-0031, STORY-0032, STORY-0033, STORY-0034, STORY-0035, STORY-0036, STORY-0037, STORY-0038, STORY-0039
**Primary sources:** `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md`
**Status:** 0/12 done

## STORY-0028

**Epic:** EPIC-005 — Qualification and distribution
**Title:** Qualify the supported iPhone v1 without expanding scope

**As a** a voice release maintainer
**I want** qualification of the selected native voice path and explicit limits on v1
**So that** protocol experiments and future options are not presented as a finished voice product

**Acceptance criteria:**
- AC-1: Keep locked-iPhone listening, physical echo cancellation and the combined voice-to-manager journey as qualification gates. The Realtime spikes prove protocol boundaries only; compiling the native graph source for iOS/Simulator and checking a disarmed simulator stale-callback fence do not prove graph operation, microphone behavior, AEC, locked lifetime or acoustic stop. · impact:`journey` · seam:`e2e` · scenario:`JOURNEY-0007`
- AC-2: The shipping default is gpt-realtime-2.1 through the same adapter; gpt-realtime-2.1-mini remains the cheaper comparison model, and both run the same live quality checks. The full model's API listing and the comparative protocol history are not speech-quality or inference-access evidence. · impact:`journey` · seam:`e2e` · scenario:`JOURNEY-0007`
- AC-3: Select and qualify the shipping wake/VAD package rather than treating the first desktop all-miss wake experiment as a selection. Hey Evener is only a proposed phrase, not a qualified model or required spelling; uncertain detected wake timing requires command-onset-loss qualification without uploading arbitrary pre-wake audio. · impact:`journey` · seam:`e2e` · scenario:`JOURNEY-0007`
- AC-4: Keep v1 iOS-focused. Do not add Android, browser background/locked operation, simultaneous microphones, speaker recognition, custom user-trained wake words, offline autonomous manager work, voice-only permission escalation, arbitrary spoken slash-command execution or a speech-provider agent brain. · impact:`none` · seam:`integration`
- AC-5: Keep GPT-Live as a later adapter experiment and streaming ASR to Evener to speech as a control baseline/future adapter, not a second v1 implementation. A later foreground browser adapter preserves the bridge/session contract. Direct WebRTC is a later option requiring separate media, cancellation and background qualification; do not infer its behavior from WebSocket evidence. · impact:`none` · seam:`integration`
- AC-6: Leave evergreen product guides unchanged at design extraction time; update them when the specified behavior is implemented and verified. · impact:`none` · seam:`unit`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:10-13`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:19-31`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:48-54`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:65-78`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:94-95`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:122-124`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:137-143`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:155-158`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:168-171`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:205-209`

**Status:** pending

## STORY-0029

**Epic:** EPIC-005 — Qualification and distribution
**Title:** Qualify local wake and quiet-state privacy

**As a** iPhone voice user
**I want** physical evidence for wake accuracy and quiet-state privacy
**So that** an armed phone responds reliably without exporting ambient speech

**Acceptance criteria:**
- AC-1: Choose a viable wake model only after it passes the same qualification gates and separate review of runtime license and model/training-data rights. The wake engine remains unresolved, Hey Evener remains blocked by all-miss evidence, and Porcupine or a trained phrase model are candidates, not untested substitutions; human audio, model fit and operating point remain unseparated evidence gaps. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0044`
- AC-2: Using human recordings and a physical phone, run at least 60 intended wake-plus-immediate-command trials across quiet, noise, distance and representative routes; require at least 95% detection and no lost command onset, and score near-misses separately. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0044`
- AC-3: Require zero activations in at least 30 hours of representative negative audio, plus 100 own-output trials including the wake phrase. Report corpus and conditions; zero in 30 hours supports a one-sided 95% Poisson upper bound near 0.10/hour, not a universal rate. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0044`
- AC-4: Capture actual network/provider ingress during Arm, silence, ordinary speech and pre-wake buffer roll. Require zero microphone frames uploaded until activation and no upload of the buffered pre-wake prefix. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0044`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:469-472`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:486-490`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:495-496`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:506-506`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:540-542`

**Status:** pending

## STORY-0030

**Epic:** EPIC-005 — Qualification and distribution
**Title:** Qualify locked iPhone audio per supported route

**As a** iPhone voice user
**I want** qualified locked listening, interruption and device recovery
**So that** the phone tells the truth about where voice works

**Acceptance criteria:**
- AC-1: Start with a small opt-in debug surface in the existing app, working local wake/VAD and real locked-device runs. Do not ship the standalone research probe over the installed app; resolve the permission-test conflict against the approved new product contract. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0045` · scenario:`SCENARIO-0046`
- AC-2: Qualify the iPhone built-in microphone/speaker route first. Keep other routes unavailable until qualified; enable a headset only after its own AEC, barge-in and recovery checks. Unsupported routes explain why and return to the last qualified route with user consent, without inferring all Bluetooth profiles from speakerphone evidence. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0045` · scenario:`SCENARIO-0046`
- AC-3: On a physical phone, Arm in the foreground, lock with no playback for 5 and 30 minutes, then wake, converse and interrupt while still locked. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0045` · scenario:`SCENARIO-0046`
- AC-4: Complete an unplugged eight-hour locked soak without a debugger, recording capture gaps, battery and thermal behavior. Require no unexplained capture loss and incremental battery use at most 2 percentage points/hour over a matched disarmed run. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0045` · scenario:`SCENARIO-0046`
- AC-5: Run at least 30 physical double-talk trials per supported route. Require p95 near-end speech onset to audible stop at most 200 ms, no stale tail and a live microphone; report provider acknowledgement time separately. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0045` · scenario:`SCENARIO-0046`
- AC-6: Physical AEC, wake accuracy, locked lifetime, acoustic interruption and battery remain NOT PROVEN by mocks, simulated playback, native compilation or disarmed simulator checks. Retain missing audio initialization and pending Mac Xcode package setup as evidence gaps; use Jesse's reachable phone or a TestFlight install for physical evidence. · impact:`none` · seam:`e2e`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:486-490`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:497-498`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:507-513`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:517-520`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:543-546`

**Status:** pending

## STORY-0031

**Epic:** EPIC-005 — Qualification and distribution
**Title:** Qualify physical device recovery and locked renewal

**As a** iPhone voice user
**I want** qualified locked listening, interruption and device recovery
**So that** the phone tells the truth about where voice works

**Acceptance criteria:**
- AC-1: Physically test interruption, media reset, Bluetooth route change, Wi-Fi/cellular change and locked reconnect/renewal. Listening recovers automatically where allowed, work survives, speaker leakage is prevented, and genuinely paused states are exposed otherwise. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0045` · scenario:`SCENARIO-0046`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:486-490`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:497-498`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:507-513`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:517-520`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:543-546`

**Status:** pending

## STORY-0032

**Epic:** EPIC-005 — Qualification and distribution
**Title:** Prove the first voice-to-chief journey

**As a** voice delivery maintainer
**I want** a real foreground voice journey backed by honest test boundaries
**So that** separate component successes are not mistaken for working voice

**Acceptance criteria:**
- AC-1: Default tests are deterministic with a scripted LLM/provider boundary and real Evener components below it. Live voice/model behavior tests require explicit opt-in in addition to credentials. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0047` · scenario:`SCENARIO-0048`
- AC-2: Prove one foreground voice turn through native transport, the production relay, real bridge and existing manager tools to a committed reply and playback, with consumed-input provenance for corrections. Use real model calls to establish voice semantics. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0048`
- AC-3: Treat the design as approved but not implemented and all qualification gates as targets, not current passes. The production relay/native transport journey remains NOT PROVEN by combining read-only fixture calls and separate manager tests. A failed gate yields evidence and a narrowed experiment, never a weaker assertion; builder-selected layout, wire names, SQLite details, detector implementation and initial prompts remain within these contracts. · impact:`none` · seam:`e2e`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:486-494`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:521-523`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:533-536`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:547-549`

**Status:** pending

## STORY-0033

**Epic:** EPIC-005 — Qualification and distribution
**Title:** Prove the same chief across conversations and disconnect

**As a** voice delivery maintainer
**I want** a real foreground voice journey backed by honest test boundaries
**So that** separate component successes are not mistaken for working voice

**Acceptance criteria:**
- AC-1: With a real hub/daemon manager, complete two voice conversations and a disconnect while retaining the same chief session identity and earlier committed context. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0048`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:486-494`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:521-523`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:533-536`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:547-549`

**Status:** pending

## STORY-0034

**Epic:** EPIC-005 — Qualification and distribution
**Title:** Qualify meaning and spoken fidelity with live models

**As a** voice user
**I want** independently scored evidence for spoken meaning and action outcomes
**So that** a plausible paraphrase cannot conceal a changed instruction or result

**Acceptance criteria:**
- AC-1: Run at least 30 scripted human turns covering names, amounts, negations, corrections and cancellation. Score canonical text and generated speech against independent fixtures; allow no unintended mutation or material change to an action outcome. · impact:`journey` · seam:`e2e` · scenario:`JOURNEY-0020`
- AC-2: Full and Mini run the same corpus before a quality winner is claimed. Preserve the gaps that Mini has only protocol evidence, Full is unmeasured and both tested speech products changed supplied wording; existing evidence does not support exact reading or live quality qualification. · impact:`journey` · seam:`e2e` · scenario:`JOURNEY-0020`
- AC-3: Recheck current published model IDs and prices at build time rather than treating spike-era values as current configuration or cost evidence. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0049`
- AC-4: Qualify the production detector, application-owned client commit and exact production relay with live meaning-preservation scenarios. The spike's server-VAD exercise and read-status probe do not establish this path's fidelity. · impact:`journey` · seam:`e2e` · scenario:`JOURNEY-0020`
- AC-5: Live speech quality checks cover numbers, names, negations, option labels and outcome claims. If the speaking model fails this gate, use the chained adapter with a qualified exact-reading engine rather than relaxing the gate. · impact:`journey` · seam:`e2e` · scenario:`JOURNEY-0020`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:488-490`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:499-499`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:523-523`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:550-552`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:247-252`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:258-271`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:247-254`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:307-315`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:328-348`

**Status:** pending

## STORY-0035

**Epic:** EPIC-005 — Qualification and distribution
**Title:** Qualify useful-answer latency

**As a** voice user
**I want** a measured time to a useful spoken result
**So that** a fast acknowledgement does not hide a slow answer

**Acceptance criteria:**
- AC-1: Run at least 30 warm-session read-only turns with a bounded backend workload. Target p95 final voiced input to first useful audible result at most 3 seconds; an audible cue is not an answer. · impact:`journey` · seam:`e2e` · scenario:`JOURNEY-0021`
- AC-2: Report wake/cold-connect timing and manager execution separately from the warm useful-answer result; the target remains unproven until measured. · impact:`journey` · seam:`e2e` · scenario:`JOURNEY-0021`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:486-490`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:500-500`

**Status:** pending

## STORY-0036

**Epic:** EPIC-005 — Qualification and distribution
**Title:** Qualify durable voice intent through faults

**As a** voice user
**I want** fault-tested preservation of accepted intent and known outcomes
**So that** reconnects and provider replacement do not duplicate or retarget work

**Acceptance criteria:**
- AC-1: With the real bridge, daemon and durable storage, test duplicate provider calls, hub crashes before and after submit, lost receipts, phone reconnect and changed instance. Require one transcript admission per intent, no retargeted retry, and retained uncertainty and recovery. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0050` · scenario:`SCENARIO-0051` · scenario:`SCENARIO-0052`
- AC-2: Qualify the persistence/interruption delivery slice with local silence, duplicate/crash/instance fences, correction races, background transport and locked renewal, preserving accepted manager work and reconciling committed state before fresh audio admission. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0050` · scenario:`SCENARIO-0051` · scenario:`SCENARIO-0052`
- AC-3: Run a timed provider replacement test for Realtime's documented 60-minute limit even though normal idle policy closes sessions sooner. Verify the same manager, bounded committed context and known action outcomes without unplayed-speech claims, replayed function calls or old spoken backlog; uncertain playback remains interrupted and repeat is user-requested. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0050` · scenario:`SCENARIO-0051` · scenario:`SCENARIO-0052`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:444-451`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:501-503`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:524-525`

**Status:** pending

## STORY-0037

**Epic:** EPIC-005 — Qualification and distribution
**Title:** Qualify correction and ordinary question semantics

**As a** chief-of-staff user
**I want** qualification of corrections, references and distinct stop actions
**So that** spoken coordination respects the same durable conversation as text

**Acceptance criteria:**
- AC-1: Test a slow Alpha task, admitted Beta correction and Alpha completion before steering consumption. Alpha remains in the transcript but is not spoken as Beta's answer; only a fresh output whose consumed frontier includes Beta can speak for it. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0053` · scenario:`SCENARIO-0054`
- AC-2: On a locked phone, speak a durable ask_user batch and answer by voice while retaining question identity and option order and never re-speaking resolved questions. After a spoken job list is reordered and interrupted, cancel the second one only when uniquely grounded, otherwise ask a named clarification. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0053` · scenario:`SCENARIO-0054`
- AC-3: Qualify that speech onset and Disarm never issue session Stop, explicit work cancellation reports the actual result and never describes irreversible work as undone, and existing Stop still holds queued messages as specified. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0053` · scenario:`SCENARIO-0054`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:502-505`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:524-525`

**Status:** pending

## STORY-0038

**Epic:** EPIC-005 — Qualification and distribution
**Title:** Qualify mixed text and voice after control loss

**As a** chief-of-staff user
**I want** qualification of corrections, references and distinct stop actions
**So that** spoken coordination respects the same durable conversation as text

**Acceptance criteria:**
- AC-1: Repeat the correction race with a typed Beta steer during voice output, then disconnect the control stream. Freshness reconciliation suppresses the old reply without losing either input. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0053` · scenario:`SCENARIO-0054`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:502-505`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:524-525`

**Status:** pending

## STORY-0039

**Epic:** EPIC-005 — Qualification and distribution
**Title:** Distribute opt-in voice without replacing the existing app

**As a** existing iPhone app user
**I want** a qualified voice update that preserves my installed app
**So that** testing voice does not sacrifice app identity, data or platform compliance

**Acceptance criteria:**
- AC-1: Produce a signed Release build preserving the existing bundle identity, App Group, share extension, signing and app data. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0055`
- AC-2: Qualify TestFlight processing and install/update on Jesse's phone. Upload is permitted but has not been done; neither build/launch nor upload alone proves locked operation or a shipped voice feature. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0055`
- AC-3: Keep voice opt-in until physical gates pass. Active recording behavior and clear microphone disclosure must comply with current Apple rules; background-audio capability is not evidence of App Review acceptance. · impact:`journey` · seam:`e2e` · scenario:`SCENARIO-0055`
- AC-4: Defer the optional browser foreground adapter. If pursued later, require a secure-context microphone and shared bridge, mutation and provenance contracts; tab suspension and locked iPhone Safari are not substitutes for native v1 qualification. · impact:`none` · seam:`integration`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:508-508`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:526-531`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:553-555`

**Status:** pending
