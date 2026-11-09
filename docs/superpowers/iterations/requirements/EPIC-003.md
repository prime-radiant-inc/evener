# EPIC-003 — Authoritative presentation and conversation

**Summary:** Authoritative presentation and conversation
**Stories:** STORY-0013, STORY-0014, STORY-0015, STORY-0016, STORY-0017, STORY-0018, STORY-0019, STORY-0020
**Primary sources:** `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md`
**Status:** 0/8 done

## STORY-0013

**Epic:** EPIC-003 — Authoritative presentation and conversation
**Title:** Speak only current proactive manager updates

**As a** an armed iPhone user
**I want** brief fresh chief-of-staff updates without opening the cloud microphone
**So that** I can hear relevant progress without historical replay or unintended microphone streaming

**Acceptance criteria:**
- AC-1: While armed, a new committed chief-of-staff update may move ArmedQuiet to SpeakingUpdate and play on the selected route after a cue. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0010`
- AC-2: Do not replay historical backlog at Arm. Coalesce pending notifications through the manager into one short current update. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0010`
- AC-3: Proactive output alone never opens cloud microphone input; the first local speech onset does. Disarmed phones do not continue proactive speech. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0010`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:42-46`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:183-188`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:217-221`

**Status:** pending

## STORY-0014

**Epic:** EPIC-003 — Authoritative presentation and conversation
**Title:** Present authoritative output with a powerless speech model

**As a** user hearing the chief's response
**I want** speech grounded only in eligible durable public output
**So that** a voice provider cannot invent action authority, receipts or claims about what I heard

**Acceptance criteria:**
- AC-1: For the foreground foundation, present only eligible committed communicate messages. Reasoning, arbitrary tool arguments and speculative streaming text never become speech. The separate pending-question story adds the only other allowed source, a durable still-pending canonical ask_user batch. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0024` · scenario:`SCENARIO-0025`
- AC-2: The speaking phase has no tools and receives only the eligible committed message plus a bounded presentation instruction. The later pending-question path uses this same powerless presentation boundary. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0024` · scenario:`SCENARIO-0025`
- AC-3: Speech may paraphrase the source, but its generated transcript is recorded separately from canonical text. Speech-model wording cannot record an action outcome, answer an approval or assert that Jesse heard something. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0024` · scenario:`SCENARIO-0025`
- AC-4: The hub and native acknowledgements maintain SpeechDelivery's durable presentation source, consumed-input frontier, presentation generation, provider response and item identities, and played-sample watermark. Generated, queued and played are distinct states rather than interchangeable delivery claims; provider audio-done or transcript-done events do not establish playback. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0024` · scenario:`SCENARIO-0025`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:247-254`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:307-315`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:328-348`

**Status:** pending

## STORY-0015

**Epic:** EPIC-003 — Authoritative presentation and conversation
**Title:** Answer the existing pending question batch by voice

**As a** user answering a clarification, including while the phone is locked
**I want** the chief's existing pending question and free-text answer semantics
**So that** voice neither changes the questionnaire nor resurrects an answered question

**Acceptance criteria:**
- AC-1: Use ParseAskUserCallArguments and the existing pending-question owner to obtain validated canonical public question data. Preserve the batch's question order, option labels and pending identity, and keep arbitrary tool arguments private. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0026` · scenario:`SCENARIO-0058`
- AC-2: Ordinary clarification works while locked. A voice reply follows the existing free-text answer semantics for that question batch, with no separate voice questionnaire and no silent option selection. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0026` · scenario:`SCENARIO-0058`
- AC-3: Re-read authoritative pending identity immediately before speaking a question and before attaching an answer. A question resolved in the meantime, including by a concurrent typed reply, cannot be resurrected or receive a stale voice answer. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0026` · scenario:`SCENARIO-0058`
- AC-4: Persist the full validated pending batch, durable pending identity, session instance and run generation with the exact originating model-request consumed frontier. Restore preserves that identity and frontier; a live-only pending helper or current mutation state is not authoritative provenance. · impact:`cross-surface` · seam:`process-level` · scenario:`SCENARIO-0058`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:307-315`

**Status:** pending

## STORY-0016

**Epic:** EPIC-003 — Authoritative presentation and conversation
**Title:** Fence speech with every channel's consumed inputs

**As a** user correcting or extending the conversation by any input channel
**I want** speech tied to the inputs the model actually consumed
**So that** an old result cannot sound like an answer to my newer request

**Acceptance criteria:**
- AC-1: Committed output carries session instance, durable source item identity, run generation and the immutable consumed-input frontier of the exact originating model request. Freeze it with that request history and carry it through tool/output persistence, never sample current mutation or consumption state at publication. An agent assertion is insufficient. The pending-question story applies the identical provenance contract to that second allowed source. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0027` · scenario:`SCENARIO-0028` · scenario:`SCENARIO-0029` · scenario:`SCENARIO-0057`
- AC-2: The frontier covers accepted voice, typed and steering inputs from every channel and retains the identities relevant to outstanding presentation fences. It correctly represents queued and steered inputs consumed out of admission order rather than relying only on a greatest sequence number, including through compaction and restore. · impact:`cross-surface` · seam:`process-level` · scenario:`SCENARIO-0027` · scenario:`SCENARIO-0057`
- AC-3: After a new conversational input is admitted, old results remain in the canonical transcript but their speech is suppressed until an eligible output includes that input's consumed frontier. Typed corrections receive the same protection as spoken corrections. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0027` · scenario:`SCENARIO-0028` · scenario:`SCENARIO-0029` · scenario:`SCENARIO-0057`
- AC-4: Admission of a new input retires an already-playing older presentation. Typed-input retirement reaches the phone through the control stream; local speech onset retains its faster on-device stop path. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0027` · scenario:`SCENARIO-0028` · scenario:`SCENARIO-0029` · scenario:`SCENARIO-0057`
- AC-5: Ordinary progress with no newer input fence can speak in committed order. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0027` · scenario:`SCENARIO-0028` · scenario:`SCENARIO-0029` · scenario:`SCENARIO-0057`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:317-340`

**Status:** pending

## STORY-0017

**Epic:** EPIC-003 — Authoritative presentation and conversation
**Title:** Ground references in durable lower-trust spoken context

**As a** user referring to something the chief just presented
**I want** my words interpreted against the presentation I was offered
**So that** a reordered list or partial playback cannot silently select the wrong job

**Acceptance criteria:**
- AC-1: Attach current SpeechDelivery context to the next input as platform-sourced metadata separate from the user's words: canonical source identity, generated wording and order, and completed, interrupted or uncertain playback status. Persist this association with the intent so recovery cannot attach a different presentation to the same words. · impact:`cross-surface` · seam:`process-level` · scenario:`SCENARIO-0030`
- AC-2: Presentation context supplies conversational context only, never new action authority or a replacement action receipt. Generated wording remains quoted lower-trust data even though its source association is platform-owned, and cannot add instructions or override the user's request. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0030`
- AC-3: The chief grounds the second one in the presentation offered to the user rather than silently applying the order of a different canonical list. The frontend never selects the job ID. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0030`
- AC-4: Completed playback establishes rendering, not human attention. Without word-aligned evidence, an interrupted millisecond watermark does not establish which words were heard. An ambiguously resolvable reference requires a short named clarification from the chief before acting. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0030`
- AC-5: Qualify spoken-reference behavior with real model tests covering reordered lists, partial playback and a concurrent typed reply; platform metadata and model behavior must both preserve the association and authority boundaries. · impact:`journey` · seam:`e2e` · scenario:`JOURNEY-0012`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:352-368`

**Status:** pending

## STORY-0018

**Epic:** EPIC-003 — Authoritative presentation and conversation
**Title:** Reconcile barge-in using rendered playback

**As a** user speaking over a reply
**I want** immediate local silence and truthful provider playback history
**So that** late audio cannot resume an obsolete reply or make an unheard tail conversational fact

**Acceptance criteria:**
- AC-1: Local speech onset stops and flushes the player, discards its queue and advances presentation generation before waiting for the hub. It wins over every later packet, including old-generation audio already buffered by transport. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0028` · scenario:`SCENARIO-0031` · scenario:`SCENARIO-0032`
- AC-2: Report actually rendered audio using provider item identities and the single playback FIFO. Do not substitute bytes received, scheduled duration or generated transcript length; when resampling, map the physical player's sample clock into the provider item timeline. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0028` · scenario:`SCENARIO-0031` · scenario:`SCENARIO-0032`
- AC-3: For Realtime WebSockets, send response.cancel for the active response and conversation.item.truncate for the played item with its played audio_end_ms. Generated but never-played items are truncated or removed as the provider supports. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0028` · scenario:`SCENARIO-0031` · scenario:`SCENARIO-0032`
- AC-4: If the provider cannot reconcile unheard output, replace that voice conversation and seed the replacement from durable text. Do not restore the unheard tail as conversational fact. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0028` · scenario:`SCENARIO-0031` · scenario:`SCENARIO-0032`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:372-392`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:394-405`

**Status:** pending

## STORY-0019

**Epic:** EPIC-003 — Authoritative presentation and conversation
**Title:** Keep speech controls separate from session Stop

**As a** user stopping speech, correcting a request or canceling a named job
**I want** each control to retain its actual scope and authority
**So that** silencing the phone never falsely promises that work was canceled or rolled back

**Acceptance criteria:**
- AC-1: Starting to speak stops speech immediately while Evener work continues until a new intent arrives. Stop speaking is already satisfied locally and does not automatically issue an interrupt RPC. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0033`
- AC-2: The existing session Stop control stops speech while preserving existing session-scoped Stop semantics and held queues. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0033`
- AC-3: Disarm or microphone loss stops capture and speech but retains accepted work and its receipts. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0033`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:407-419`

**Status:** pending

## STORY-0020

**Epic:** EPIC-003 — Authoritative presentation and conversation
**Title:** Correct work and cancel only the manager-resolved job

**As a** user stopping speech, correcting a request or canceling a named job
**I want** each control to retain its actual scope and authority
**So that** silencing the phone never falsely promises that work was canceled or rolled back

**Acceptance criteria:**
- AC-1: Wait, I meant Beta drops obsolete speech and delivers the correction through durable steering. A correction is not rollback: effects already taken remain reported, and the manager says when something already happened or cannot be canceled. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0033`
- AC-2: Cancel the build job stops current speech, then the manager resolves the named job to its authoritative identity, invokes its real scoped cancellation tool and reports the result. The voice layer cannot promise that a delayed session interrupt will stop a particular earlier job. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0033`
- AC-3: The foreground iPhone voice path supports asking what is happening, starting work through existing manager tools, correcting the interaction mid-sentence and hearing the result without watching the screen. Continuing this interaction while locked is covered separately by the native-lifetime story. · impact:`journey` · seam:`e2e` · scenario:`JOURNEY-0013` · scenario:`JOURNEY-0014`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:407-419`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:5-8`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:17-20`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:34-46`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:84-88`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:162-166`

**Status:** pending
