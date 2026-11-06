# Behavior Scenarios

All cards are required future behavior, not passing tests. `pending` means no executable has been supplied. `manual-residual` names live or physical evidence still required. Execution commands stay TBD until implementation provides and runs them.

## Journey Scenarios

## JOURNEY-0001 — Ask for work and correct the answer through foreground voice

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0001

**Preconditions:**
- A physical iPhone remains foreground-armed and is bound to a manager with permitted work available; locked lifetime is a separate scenario.
- Committed user-facing outputs and their consumed-input provenance can be inspected afterward.

**Steps:**
1. Without watching the foreground app, activate voice, ask what is happening and ask the manager to start permitted work.
   → AC-4: Existing manager tools perform work within the same session's authority.
   → AC-6: The user can ask status and start work through foreground voice without visual attention.
2. While hearing the response, speak a mid-sentence correction and hear the resulting reply.
   → AC-6: The foreground phone supports the correction and returns an audible result without requiring visual attention.
   → AC-5: Spoken content is a concise paraphrase of eligible committed user-facing Evener output with consumed-input provenance, not an independent provider answer.
3. Open ordinary text chat and compare the available canonical reply with the spoken paraphrase and session history.
   → AC-4: Text exposes the same manager session and work history.
   → AC-5: The committed Evener text remains canonical; no exact-reading claim is inferred from a generative-speech prompt.

**Final observables:**
- The assembled foreground voice-to-manager path completes status, work, correction and reply in one durable session without visual attention; locked operation is checked separately.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:17-20`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:34-46`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:88-88`

## JOURNEY-0002 — Keep physical capture active during provider playback

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0006

**Preconditions:**
- A physical iPhone uses the shipping native graph and an active provider playback stream.
- Actual capture activity, shared echo reference and local detector input can be observed.

**Steps:**
1. Play provider PCM through the configured route while observing the audio engine and capture samples.
   → AC-2: Capture and provider playback use the same voice-processing engine and echo reference; setting voiceChat alone is not counted as AEC proof.
   → AC-5: Actual capture continues throughout playback and feeds native AEC and local speech detection.
2. Introduce near-end speech during playback and observe that capture and detection remain available.
   → AC-5: The physical audio path remains able to capture local speech while the phone is speaking.

**Final observables:**
- Capture continuity and shared-reference operation are observed on the physical assembled audio path, not inferred from compilation.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:134-144`

## JOURNEY-0003 — Activate from wake or Talk without sending earlier room audio

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0007, STORY-0008

**Preconditions:**
- A physical iPhone is ArmedQuiet with observable local-buffer boundaries and outgoing provider frames.
- The shipping wake detector can provide both confident and uncertain detected-start cases.

**Steps:**
1. Speak marked room audio while remaining ArmedQuiet, then inspect local buffering and provider traffic.
   → AC-2: The phone retains a rolling two-second PCM buffer in RAM but sends no microphone frames to the provider; an authenticated manager-update control stream may remain subscribed.
2. Have a nearby speaker say the wake phrase followed immediately by a command; repeat with another nearby speaker.
   → AC-3: Each local activation cues, opens the provider session, enters Conversing and preserves speech immediately after the wake phrase.
   → AC-4: Uploaded audio begins at the detected wake start and excludes earlier marked room audio.
   → AC-5: Activation is not speaker authentication; nearby people can speak during the user's armed period.
3. Repeat with uncertain detected wake timing and separately press Talk after marked pre-press room audio.
   → AC-4: Uncertain wake uses the conservative detected start and never arbitrary pre-wake audio; command-onset loss is observed for qualification. Talk begins at the press and excludes pre-press audio.
   → AC-3: Talk also cues, opens the provider session and enters Conversing.

**Final observables:**
- Local-only listening becomes cloud conversation only at activation; the audio boundary does not leak the earlier rolling prefix.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:31-33`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:180-182`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:202-209`

## JOURNEY-0004 — Disarm every armed state on the physical phone

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0007, STORY-0008

**Preconditions:**
- A physical iPhone can be placed in ArmedQuiet, Conversing, Speaking, SpeakingUpdate and Recovering.
- Capture, audible output, outgoing microphone frames and proactive-output requests can be observed.

**Steps:**
1. For each armed state, press Disarm and then allow any pending recovery callback or committed proactive update to arrive.
   → AC-6: Every case enters Disarmed and stops capture and speech, including proactive output; recovery or pending updates do not undo the user's Disarm.

**Final observables:**
- The physical microphone and speech lifetime end on Disarm from every armed state.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:195-199`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:217-221`

## JOURNEY-0005 — Silence physical playback without waiting for the provider

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0010

**Preconditions:**
- A physical iPhone can play both a reply and a proactive update while manager work is running.
- Provider round-trip completion can be delayed without disabling native audio observation.

**Steps:**
1. During Speaking and separately SpeakingUpdate, delay provider responses and speak near the phone.
   → AC-1: Native near-end detection immediately stops actual playback, keeps AEC/local VAD active, enters Conversing and opens the follow-up window before a provider round trip is required.
   → AC-2: The acoustic stop does not cancel running manager work.
2. Resume output and press Stop speaking without sending a work-stop instruction.
   → AC-2: Speech stops while the manager work continues; silence is not reported as a known Evener cancellation.

**Final observables:**
- Actual acoustic silence is independent of provider response and distinct from manager work cancellation.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:34-35`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:87-87`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:185-186`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:215-216`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:227-229`

## JOURNEY-0006 — Continue an armed lifetime while locked and restore it after termination

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0012

**Preconditions:**
- A physical iPhone has the shipping native service and a durable manager identity.
- Force-quit, reboot, process eviction and permission revocation cases can be exercised or induced on the physical device.

**Steps:**
1. Arm in the foreground, lock the iPhone and perform a voice interaction while observing real capture and socket behavior.
   → AC-1: The armed audio lifetime can continue while locked using active audio background execution, without silent playback, VoIP workarounds or a background URLSession substituted for realtime WebSocket ownership.
2. Separately terminate the lifetime by force-quit, reboot, process eviction and permission revocation.
   → AC-2: Listening can end and process exit or revocation disarms it; durable manager identity is distinct from the terminated audio lifetime, with no system-wide hotword or background-relaunch promise.
3. Reopen and Arm again with permission and grant available.
   → AC-2: User action restores the native listening lifetime for the durable manager; an unattended running timer is not treated as indefinite-listening evidence.

**Final observables:**
- Locked listening and user-restored listening are demonstrated as bounded native lifetimes, not automatic relaunch or always-on system hotword behavior.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:17-20`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:32-33`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:56-59`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:152-153`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:194-195`

## JOURNEY-0007 — Qualify the selected physical path rather than promoting spike evidence

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0028

**Preconditions:**
- A candidate shipping iPhone wake/VAD package and native service are assembled with the Realtime adapter and manager.
- Historical protocol, all-miss desktop wake and disarmed simulator evidence are distinguished from new qualification results.

**Steps:**
1. Exercise locked-iPhone listening, physical echo cancellation, actual acoustic interruption and the combined voice-to-manager journey on the assembled candidate.
   → AC-1: Qualification depends on the physical behaviors, not the protocol spike, compilation, voiceChat selection or disarmed stale-callback check alone.
2. Qualify the selected wake/VAD package, including immediate post-wake command onset and uncertain detected-start cases.
   → AC-3: The package is selected using qualification rather than the prior all-miss result; Hey Evener is not imposed as a required spelling or assumed qualified model, and onset loss is checked without pre-wake upload.
3. Run voice quality checks with gpt-realtime-2.1-mini first, then apply the same checks to gpt-realtime-2.1 before making the full model available.
   → AC-2: The full model is offered through the same adapter only after the same checks; inventory visibility and comparative protocol history are not substituted for inference or speech-quality evidence.

**Final observables:**
- The future qualification record distinguishes measured native/model behavior from limited historical evidence; this extraction claims no gate has passed.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:10-13`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:28-31`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:74-78`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:137-143`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:155-158`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:168-171`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:205-209`

## JOURNEY-0008 — First foreground conversation reaches the existing manager and real playback

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0001, STORY-0002, STORY-0006, STORY-0007, STORY-0003, STORY-0004, STORY-0005, STORY-0014, STORY-0016, STORY-0011, STORY-0024, STORY-0032

**Preconditions:**
- An existing manager is available through authenticated hub and daemon connections; manager creation itself is already landed at the supplied HEAD f5e5516c19.
- The phone is foregrounded with a valid listening grant; the production detector, application-owned commit, exact interpretation relay, admission journal, output-provenance path and real native playback are the assembled dependencies under test.
- The manager can provide a meaningful committed response to a spoken request, such as reporting the state of its actual work; generated audio alone will not count as playback evidence.
- Explicit live-test opt-in and inference credentials are required for live semantics; actual native playback is required for physical closure. Scripted provider/device inputs prove plumbing only.

**Steps:**
1. Arm voice against the existing manager.
   → The authenticated binding records device, manager session, expected instance and listening epoch.
   → No replacement manager is created and no provider-selected target is accepted.
2. Press Talk, speak a read-only request about an existing managed session, and end the utterance through the production detector.
   → Native start, frames and end carry a stable epoch-local utterance sequence.
   → The hub persists VoiceTurn, commits input and records the provider input item before interpretation.
3. Run the narrow interpretation phase for this input and response.
   → For Realtime, response.done has completed status and exactly one strict submit_utterance(text) call, correlated by response metadata to the persisted turn and committed input item.
   → The interpretation preserves the request, emits no speech and admits at most one text intent; arguments.done alone does not admit it.
4. Admit the intent to the bound manager.
   → The hub atomically journals immutable text, target, method and mutation identity before sending ordinary turn/start or turn/steer as appropriate.
   → The phone's JS outbox does not also submit the utterance.
5. Let the existing manager invoke its real session-inspection tool before committing the answer.
   → The inspected session and tool result are observable in real hub/daemon history; a direct provider status fixture or fabricated speech answer cannot satisfy the journey.
6. Observe the manager's durable receipt and input consumption.
   → Received appears only after the durable receipt, with pending representation retained until authoritative queue or transcript reflection.
   → The manager consumes the accepted input at the authoritative consumption boundary.
7. Let the manager commit its meaningful response.
   → The canonical communicate source carries session instance, source identity, run generation and a persisted frontier including the request.
   → Reasoning and speculative streamed text have not been spoken.
8. Present the eligible response through the tool-free speaking phase.
   → Only the committed response and bounded presentation instruction are provided.
   → Generated wording is retained separately from canonical text and is not an action receipt.
9. Render the response on the physical native player and observe acknowledgements.
   → SpeechDelivery distinguishes generated, queued and actually played audio for the current presentation generation and provider item.
   → The rendered-sample watermark supplies real playback evidence, without asserting that the user paid attention.

**Final observables:**
- One meaningful foreground spoken request has one durable admission in the existing manager, one eligible canonical response and actual rendered playback.
- The binding, VoiceTurn, VoiceIntent, source frontier and SpeechDelivery form an inspectable chain; this scenario is required evidence, not a claim that physical or model qualification has already passed.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:251-288`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:299-348`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:394-398`

## JOURNEY-0009 — Live production interpretation preserves corrections, negations and references

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0004

**Preconditions:**
- Use the real production detector, application-owned client commit and exact production relay, not the spike's server-VAD or read-status-probe tool.
- Use a bound existing manager and known canonical and spoken context for reference-bearing requests.
- The source utterances are independently specified so comparison does not use the relay's own wording as the expected answer.

**Steps:**
1. Speak independent requests containing a correction such as Beta, not Alpha, a negated instruction, a reference to prior presentation and slash-command wording.
   → Application-owned endpointing yields the intended complete audio turns.
   → Each completed submit_utterance preserves the request, corrections, negations and reference wording rather than resolving a target or executing a slash command.
2. Observe each interpretation and resulting manager intake.
   → No interpretation-phase speech is rendered.
   → The exact production relay supplies at most one eligible text intent per turn, and manager intake preserves its meaning.

**Final observables:**
- Live evidence identifies the actual detector, client-commit path and relay tested.
- Server-VAD or read-status-probe success is not substituted for these results; live/model qualification remains unproven until this scenario runs.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires explicit live opt-in plus provider inference access and independent human/meaning fixtures. Scripted provider responses cannot satisfy model behavior.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:258-271`

## JOURNEY-0010 — Live speech quality preserves critical wording or uses the exact-reading fallback

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0014

**Preconditions:**
- Use the actual speaking adapter, native playback and independently specified eligible committed text or pending questions.
- The source set contains numbers, names, negations, option labels and action outcome claims.
- A chained adapter with a qualified exact-reading engine is the required alternative when the primary speaking path fails, not assumed qualified by this extraction.

**Steps:**
1. Present the source set through the live speaking path and render the returned audio.
   → Compare audible output and separately recorded generated transcript with canonical sources.
   → Numbers, names, negations, option labels and outcome claims retain their meaning.
   → Canonical text, action receipts and approval state are not rewritten from speech wording.
2. Evaluate a failing speaking-path result against the same quality gate.
   → The failure is not accepted by relaxing fidelity requirements.
   → Use the chained adapter with a qualified exact-reading engine instead.
3. Run the same critical-content checks through that qualified fallback.
   → The fallback satisfies the unchanged quality gate with actual rendered output.

**Final observables:**
- Quality evidence covers every specified critical-content class, and failure selects the required fallback rather than weakening the gate.
- No physical or live-model result is claimed until the scenario is executed.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires explicit live opt-in plus provider inference access and independent human/meaning fixtures. Scripted provider responses cannot satisfy model behavior.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:342-348`

## JOURNEY-0011 — A locked-phone clarification preserves the existing question batch and free-text answer

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0015, STORY-0014, STORY-0004

**Preconditions:**
- The existing manager has a durable, still-pending ask_user batch with ordered questions and labeled options.
- The voice conversation is already armed and the phone is locked; locked operation is supplied by the separate native work, not redefined here.
- Pending-question ownership, voice intake and consumed-frontier eligibility are available dependencies.

**Steps:**
1. Read the pending batch through ParseAskUserCallArguments and its pending-question owner, then re-read pending identity before presenting it.
   → Only canonical validated public question and option data is presented.
   → Question order, option labels and pending identity are preserved; arbitrary tool arguments remain private.
2. Speak the eligible question batch while the phone remains locked.
   → The ordinary clarification is audible without inventing a voice-only questionnaire or silently choosing an option.
3. Give a spoken free-text answer and admit its completed interpretation.
   → Voice intake preserves the answer and follows the existing batch free-text semantics.
   → The current pending identity is re-read before attaching the answer.
4. Let the existing pending-question owner process that answer.
   → The answer is attached to the still-pending batch through the ordinary question path, not an invented option-selection path.
   → Any resulting speech remains subject to canonical source and consumed-frontier eligibility.

**Final observables:**
- An ordinary clarification and free-text reply complete while locked using the same batch identity, order and option labels as the existing question system.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:263-280`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:307-326`

## JOURNEY-0012 — Real model resolves offered order and clarifies ambiguous partial references

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0017, STORY-0015, STORY-0016

**Preconditions:**
- Use real interpretation and manager model behavior with canonical lists whose presentation order can differ.
- Use actual playback evidence for completed, interrupted and uncertain presentation cases; interrupted cases have no word-aligned evidence.
- The durable lower-trust presentation association and ordinary pending-question path are assembled dependencies.

**Steps:**
1. Present a reordered list and then say the second one.
   → The chief grounds the reference in the generated wording and order offered to the user, not silently in the canonical list's different order.
   → The frontend forwards the reference and its presentation metadata rather than choosing a job ID.
2. Repeat with interrupted or uncertain playback that leaves the reference nonunique.
   → A millisecond watermark without word alignment is not used to prove which words were heard.
   → The chief asks a short named clarification before acting.
3. Repeat with completed playback and then with instruction-like generated wording conflicting with the user's request.
   → Completed playback establishes rendering only, not attention.
   → Quoted generated wording cannot add instructions, action authority or a replacement receipt.
4. Exercise a concurrent typed reply while a spoken reference or its clarification is in flight.
   → The voice intent keeps the presentation association captured with its own words.
   → Typed input receives the same freshness protections; any question resolved by the typed reply is not resurrected by a stale voice answer.
   → Where the reference is still not unique, the chief clarifies rather than letting the frontend select the target.

**Final observables:**
- Real model behavior demonstrates offered-order grounding, partial-playback uncertainty, named clarification and concurrent typed-reply handling.
- These are required model and playback observations, not qualification already established by metadata-only tests.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:312-315`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:317-340`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:352-368`

## JOURNEY-0013 — Complete correction sequence from local barge-in to fresh committed playback

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0018, STORY-0004, STORY-0005, STORY-0022, STORY-0014, STORY-0016, STORY-0019, STORY-0020

**Preconditions:**
- The user is hearing an older reply from the bound existing manager; its work may still be running and may already have effects.
- The native audio owner, hub bridge, voice provider and manager are assembled with durable admission, consumed-input provenance and real playback.
- Every sequence-diagram interaction from source lines 379-391 is exercised in order; physical and model behavior must be observed, not inferred from mocked packets.

**Steps:**
1. User -> Native audio owner: speak over the reply, for example Wait, I meant Beta.
   → The phone detects local speech onset; existing work has not been automatically canceled.
2. Native audio owner -> itself: stop player, discard queue and advance speech generation.
   → Local speech wins before a hub round trip and old-generation late or buffered packets cannot restart the reply.
3. Native audio owner -> Hub bridge: report interruption and actual played watermark.
   → The report reflects rendered audio and its provider item timeline, including any resampling mapping.
4. Hub bridge -> Voice provider: cancel the response and truncate the played item.
   → The active response is canceled and the item is truncated at played audio_end_ms; never-played output is reconciled rather than treated as heard.
5. Native audio owner -> Hub bridge: send the new utterance frames and end.
   → The new utterance is associated with its stable listening-epoch sequence and persisted turn.
6. Hub bridge -> Voice provider: commit and interpret the new input.
   → The returned input item is mapped to the turn; interpretation exposes only submit_utterance(text) and speaks nothing.
7. Voice provider -> Hub bridge: return completed submit_utterance.
   → For Realtime, wait for response.done with completed status and exactly one strict submit_utterance(text) call, with response metadata correlated to the persisted turn and committed input item.
   → The call preserves the correction and admits at most one text intent; arguments.done alone is insufficient.
8. Hub bridge -> itself: persist text and stable mutation identity.
   → Immutable text, target, selected method and mutation identity are durably recorded before mutation.
9. Hub bridge -> Evener manager: start or steer under the existing mutation contract.
   → A working manager receives durable steering; an idle manager uses ordinary start according to persisted method selection.
   → No frontend-selected job identity or rollback promise is introduced.
10. Evener manager -> Hub bridge: return the durable receipt.
   → Only now can received be shown, with pending representation retained until authoritative reflection.
11. Evener manager -> Hub bridge: commit a reply with the consumed-input frontier.
   → The frontier demonstrates actual consumption of the correction.
   → The manager reports effects that already happened or cannot be canceled rather than implying correction rolled them back.
   → Old results remain in the transcript but are not spoken as the answer to the correction.
12. Hub bridge -> Voice provider: speak the fresh committed reply.
   → The tool-free speaking phase receives only eligible canonical content and bounded presentation instruction.
13. Voice provider -> Native audio owner: deliver audio for the current presentation generation.
   → The current-generation reply is actually rendered and its played watermark is acknowledged.
   → Obsolete audio does not resume and generated wording does not replace the canonical action outcome.

**Final observables:**
- All thirteen interactions in the complete correction sequence remain ordered and observable.
- Local interruption, provider history repair, faithful correction admission, durable manager receipt, consumed-input eligibility and fresh real playback form one journey.
- Already-taken effects remain honestly reported; interruption of speech is not rollback or scoped job cancellation.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:258-280`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:317-348`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:372-419`

## JOURNEY-0014 — The manager cancels the named job and reports actual irreversibility

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0019, STORY-0020

**Preconditions:**
- The existing manager owns authoritative job identities and exposes the real scoped cancellation tool.
- Run separate real-model cases with a cancelable build job, an already-completed job and work that cannot be canceled.
- Voice intake, committed outcome presentation and actual playback are available; frontend interpretation has no job-selection authority.

**Steps:**
1. Say Cancel the build job while a reply is playing.
   → Current speech stops locally and the request is durably delivered.
   → The manager, not the frontend or voice provider, resolves the named job to its authoritative identity.
2. Let the manager act on the resolved request.
   → The manager invokes its real scoped cancellation tool against that job.
   → Its committed reply reports the actual result, including already-happened or cannot-cancel outcomes in the corresponding cases.
3. Exercise an older job alongside a delayed session interrupt and a correction after effects have occurred.
   → The voice layer makes no promise that the delayed session interrupt stops that particular earlier job.
   → The correction is not represented as rollback and effects already taken remain reported.
4. Present the eligible committed result and render it.
   → The spoken result is grounded in the manager's actual outcome, not an invented speech-model success claim.

**Final observables:**
- Scoped cancellation is evidenced by the real manager tool call and authoritative job result.
- Completed or irreversible effects are stated plainly rather than erased by a correction or a speech stop.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:411-419`

## JOURNEY-0015 — Qualify a licensed wake candidate with immediate human commands

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0029

**Preconditions:**
- A physical phone, human recordings and candidate local wake/VAD are available.
- No candidate is considered qualified from reference-phrase detection, the all-miss Hey Evener probe or a mocked microphone.

**Steps:**
1. Review runtime licensing separately from model/training-data rights and record unresolved human-audio, model-fit and operating-point evidence.
   → A candidate is not chosen without appropriate rights and the same qualification gate.
   → Hey Evener remains blocked and Porcupine or a trained model remains a candidate until qualification succeeds.
2. Run at least 60 intended wake-plus-immediate-command trials across quiet, noise, distance and representative routes.
   → Detection is at least 95% with no lost command onset.
   → Near-misses are scored separately.

**Final observables:**
- Physical trial evidence and rights review, not historical spike success, support any wake-engine choice.
- Absent passing evidence, wake accuracy remains NOT PROVEN.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:469-472`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:489-490`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:495-495`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:540-542`

## JOURNEY-0016 — Measure negative-audio and self-output wake resistance

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0029

**Preconditions:**
- Physical wake detection is exercised with a documented representative corpus and conditions.

**Steps:**
1. Run at least 30 hours of representative negative audio and 100 own-output trials including the wake phrase.
   → The negative-audio gate has zero activations.
   → Own-output trials are included in false-activation/self-output qualification, not omitted as ordinary positive wake trials.
   → Corpus, conditions and observed activation results are reported.
2. Interpret a result of zero activations in 30 hours.
   → Report the one-sided 95% Poisson upper bound near 0.10/hour.
   → Do not claim a universal false-activation rate.

**Final observables:**
- The gate retains its 30-hour, zero-activation and 100-own-output requirements and bounded statistical claim.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:496-496`

## JOURNEY-0017 — Measure locked listening lifetime and incremental battery use

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0030, STORY-0031

**Preconditions:**
- Jesse's reachable physical phone or a TestFlight install is available.
- Missing audio initialization and pending Mac Xcode package setup are tracked as unresolved evidence gaps until addressed; compilation and a disarmed simulator do not qualify audio.

**Steps:**
1. Arm in the foreground, lock without playback for 5 minutes and separately for 30 minutes, then wake, converse and interrupt without unlocking.
   → Wake, conversation and interruption work while still locked after both no-playback intervals.
2. Run an unplugged eight-hour locked soak without a debugger and compare it with a matched disarmed run.
   → Capture-gap, battery and thermal records are retained as qualification evidence.
   → There is no unexplained capture loss.
   → Incremental battery use is at most 2 percentage points/hour.

**Final observables:**
- Only physical records can establish locked lifetime and battery qualification; those gates remain NOT PROVEN without them.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:486-490`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:497-497`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:543-546`

## JOURNEY-0018 — Measure physical acoustic interruption on each supported route

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0030, STORY-0031

**Preconditions:**
- A physical phone and each route proposed for support are available; mocked capture and simulated playback cannot qualify this gate.

**Steps:**
1. Run at least 30 double-talk trials per supported route and measure near-end speech onset to audible stop.
   → Per-route p95 is at most 200 ms.
   → No stale output tail remains and the microphone stays live.
2. Record provider acknowledgement timing alongside acoustic timing.
   → Acknowledgement is reported separately and is not substituted for audible-stop latency.

**Final observables:**
- Per-route physical AEC/barge-in evidence supports only the tested routes; acoustic interruption remains NOT PROVEN without it.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:489-490`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:498-498`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:510-513`

## JOURNEY-0019 — Complete a real foreground turn and retain the chief across conversations

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0032, STORY-0033

**Preconditions:**
- Real native transport, production relay, bridge, hub/daemon manager and existing manager tools are assembled.
- Live voice semantics are explicitly opted in with credentials; the manager is already implemented rather than a new prerequisite.

**Steps:**
1. Speak one foreground turn through the real model and manager path to a committed reply and playback.
   → The existing manager tools execute through the real bridge.
   → A committed reply reaches playback and consumed-input provenance needed by corrections is present.
2. Disconnect and complete a second voice conversation.
   → Both conversations use the same chief session identity.
   → Earlier committed context survives the disconnect.

**Final observables:**
- An assembled voice-to-manager journey, not separate fixture and manager successes, supports the result.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires explicit live opt-in plus provider inference access and independent human/meaning fixtures. Scripted provider responses cannot satisfy model behavior.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:494-494`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:521-523`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:547-549`

## JOURNEY-0020 — Score Full and Mini against the same independent human-turn fixtures

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0034

**Preconditions:**
- Live model tests have explicit opt-in and credentials.
- Independent fixtures cover at least 30 scripted human turns with names, amounts, negations, corrections and cancellation.

**Steps:**
1. Run Full and Mini over the same corpus and score both canonical text and generated speech.
   → There is no unintended mutation or material change to an action outcome.
   → Scores are against independent fixtures, not the generated answer's own claims.
2. Assess comparative quality and exact-reading claims.
   → No quality winner is claimed before both models run the same corpus.
   → Historical Mini protocol evidence, unmeasured Full quality and observed wording changes do not qualify live quality or exact reading.

**Final observables:**
- Model quality remains NOT PROVEN until the live corpus supplies the required evidence.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires explicit live opt-in plus provider inference access and independent human/meaning fixtures. Scripted provider responses cannot satisfy model behavior.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:488-490`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:499-499`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:550-552`

## JOURNEY-0021 — Measure warm useful-answer latency rather than acknowledgement speed

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0035

**Preconditions:**
- The assembled voice journey runs with explicit live-test opt-in and credentials.
- At least 30 warm-session read-only turns use a bounded backend workload.

**Steps:**
1. Measure each turn from final voiced input to its first useful audible result.
   → The p95 target is at most 3 seconds.
   → Cues and acknowledgements are not counted as useful answers.
2. Report wake/cold-connect and manager-execution timings.
   → These timings are reported separately from the warm useful-answer metric.
   → A target without measurements remains unproven.

**Final observables:**
- Trial count, bounded workload and independently distinguished timings support the latency result.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:486-490`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:500-500`

## JOURNEY-0022 — Keep slow Alpha out of the answer to spoken Beta

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0037, STORY-0038

**Preconditions:**
- The real manager and voice output path can delay Alpha execution and steering consumption.

**Steps:**
1. Start slow Alpha, admit a spoken Beta correction and complete Alpha before Beta steering is consumed.
   → Alpha remains in the transcript.
   → Alpha is not spoken as Beta's answer.
2. Commit a fresh output whose consumed-input frontier includes Beta.
   → That fresh output can speak for Beta.

**Final observables:**
- The race is actually exercised and transcript history remains distinct from current speakability.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:502-502`

## JOURNEY-0023 — Answer locked questions and ground interrupted spoken references

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0037, STORY-0038

**Preconditions:**
- A physical locked phone is connected to the real chief and durable ask_user questions and job lists exist.

**Steps:**
1. Speak a durable ask_user batch, answer by voice while locked, and revisit its resolved state.
   → Question identity and option order are retained.
   → Resolved questions are never re-spoken.
2. Reorder a spoken job list, interrupt it and ask to cancel the second one, exercising both uniquely grounded and ambiguous references.
   → Only a uniquely grounded target can be selected.
   → An ambiguous reference produces a named clarification instead of an invented target.

**Final observables:**
- Locked answers preserve durable question identity and spoken references do not authorize guesses.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:504-504`

## JOURNEY-0024 — Preserve the installed app through signed Release and TestFlight update

**Kind:** journey
**Proof seam:** e2e
**Owning stories:** STORY-0039

**Preconditions:**
- Jesse's phone and existing app identity/data are available for qualification.
- TestFlight upload is permitted but historical evidence contains no upload.

**Steps:**
1. Build and sign Release, process it through TestFlight, and exercise install/update on Jesse's phone.
   → Existing bundle identity, App Group, share extension, signing and app data are preserved.
   → TestFlight processing and device installation/update are evidenced, not inferred from compilation.
2. Assess locked voice readiness after build, launch or upload alone.
   → Locked operation is still NOT PROVEN without its physical gates.
   → The approved proposed implementation is not described as shipped merely because distribution is permitted.

**Final observables:**
- Distribution evidence covers the signed Release-to-phone path and preserves the existing app.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:508-508`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:526-528`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:553-555`

## Surface Scenarios

## SCENARIO-0001 — Bind an explicit manager without changing existing authority

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0001

**Preconditions:**
- The app is authenticated to a daemon with manager support already available.
- An ordinary session, a manager session and an independently top-level managed worker exist.

**Action:**
- Select the existing manager, then separately exercise explicit creation of a new manager for voice.
- Inspect the resulting authenticated binding and send voice-derived text and ordinary text to the chosen manager.
- Request inspection or control both within and outside the manager's existing bound-daemon authority.

**Expected observables:**
- AC-1: Each armed phone has one explicitly chosen manager; the ordinary session is not toggled into manager mode and the worker remains independent rather than becoming an owned delegate.
- AC-2: The binding includes authenticated device identity, selected session reference, live instance identity and listening epoch.
- AC-4: Both input surfaces reach the same persistent Evener session with its existing reasoning, tools, permissions, history and recovery.
- AC-4: Voice can use existing authority but grants none beyond it; the manager's launch mode and worker relationship stay unchanged.
- One selected durable manager and its existing authority serve both text and voice; the voice frontend does not create a second agent authority.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:5-8`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:42-46`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:84-84`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:162-166`

## SCENARIO-0002 — Offer explicit second-phone takeover without cancelling work

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0001

**Preconditions:**
- A first phone owns session speech output and manager work is running.
- A second authenticated phone can request Arm for the same session.

**Action:**
- Request Arm on the second phone without accepting takeover.
- Accept takeover and deliver audio and callbacks from the old binding.

**Expected observables:**
- AC-3: The response identifies the current owner and offers an explicit takeover rather than creating simultaneous output owners.
- AC-2: The active binding retains the required device, session, live-instance and listening-epoch identities.
- AC-3: Old audio is fenced, the second phone owns output, and the manager's existing work is not cancelled.
- Exactly one phone owns session speech output after explicit takeover; previously running manager work continues.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:162-166`

## SCENARIO-0003 — Keep provider events inside the narrow hub relay

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0002

**Preconditions:**
- A voice-capable configured hub, native service and replaceable Realtime adapter are connected.
- The manager and selected voice model can be configured independently.

**Action:**
- Establish the native-to-hub and hub-to-OpenAI connections and inspect credential placement and tool-event ownership without exposing secret values.
- Send active-conversation PCM and finalized interpreted text through the bridge, then publish a committed manager output and inspect the native audio delivery.
- Inspect the exposed provider tools, attempt direct manager-tool use, and replace a provider conversation while retaining the selected Evener session.
- Change the selected voice model without changing the manager model, then use a model that is listed but has no working inference access.

**Expected observables:**
- AC-1: Both legs use WebSockets, the provider credential stays on the hub, there is one tool-event owner, and Swift retains the wake/barge-in capture/playback graph.
- AC-3: PCM stays on the dedicated voice stream rather than AppWire audio input; the hub authenticates the binding, journals admission, uses existing daemon text mutations and receives committed output. Output audio delivered to native playback is generation-tagged.
- AC-2: The provider interprets speech and presents verified Evener output through its one relay, without directly managing sessions.
- AC-2: Filesystem, shell, delegate, approval and session-control tools are not exposed; the disposable provider conversation does not replace durable Evener identity or authority.
- AC-6: Voice-model selection is independent and the listing alone is not treated as successful inference access.
- A provider can be replaced without moving credentials to the phone, changing manager authority or introducing audio AppWire mutations.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:5-8`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:26-30`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:69-72`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:85-88`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:108-110`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:113-124`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:168-171`

## SCENARIO-0004 — Honor secure transport and advertised hub capability

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0002

**Preconditions:**
- Configured hub fixtures cover authenticated off-tailnet TLS, the established private tailnet boundary, arbitrary public plaintext and an older text-only hub.

**Action:**
- Attempt voice connection against each configured transport fixture and inspect whether microphone frames can leave the phone.
- Connect to the older hub without an advertised voice capability and send ordinary text.

**Expected observables:**
- AC-4: Off-tailnet voice requires TLS and authenticated configured-hub access; the established private tailnet boundary remains usable; arbitrary public plaintext carries no microphone audio.
- AC-5: Text remains usable, voice is unavailable and no guessed voice RPC is attempted.
- Voice is capability-negotiated over the permitted configured transport; unsupported hubs remain text-usable.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:126-130`

## SCENARIO-0005 — Construct the native owner and convert real route formats

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0006

**Preconditions:**
- Native audio, socket, callback and worker seams are observable.
- Route fixtures include formats that differ from the adapter's mono PCM16 24 kHz format.

**Action:**
- Create the service, set up its graph and change screen ownership and React Native AppState.
- Compare microphone configuration and permission-contract coverage with the approved native listening behavior.
- Capture and play known sample sequences using each actual route format and inspect adapter data, output accounting and the native played watermark.
- Exercise audio callbacks while network and wake/VAD workers are busy, then inspect buffer and detector wiring.

**Expected observables:**
- AC-1: An app-lifetime Swift owner retains capture, AEC, routing, activation, playout and socket ownership rather than inheriting the background-closing JS connection lifetime.
- AC-2: AVAudioSession uses playAndRecord/voiceChat; engine voice processing is enabled while stopped before final graph setup, and capture/playback share one engine and echo reference.
- AC-1: The previous disabled-microphone configuration and permission-absence assertion are deliberately replaced by the approved behavior, not silently bypassed.
- AC-3: Conversion uses actual route formats and emits the adapter format; output sample accounting stays on the provider audio timeline and native playback owns the played watermark, rather than assuming the hardware is mono PCM16 at 24 kHz.
- AC-4: Callbacks do no network I/O or inference; wake/VAD workers receive bounded buffers and use a speech detector rather than an RMS threshold presented as VAD.
- Graph construction and callback contracts are checked separately from physical acoustic qualification.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:89-90`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:108-110`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:113-124`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:134-143`

## SCENARIO-0006 — Require foreground Arm and preserve general credential storage

**Kind:** surface
**Proof seam:** app-level
**Owning stories:** STORY-0007, STORY-0008

**Preconditions:**
- The app starts Disarmed with the general hub credential under its existing unlocked SecureStore accessibility.
- Microphone permission and narrow voice-grant outcomes can independently succeed or fail.

**Action:**
- Attempt listening without a foreground Arm, then press Arm with permission denied and separately with the voice grant unavailable.
- Press Arm in the foreground with permission and grant available and inspect credential-access configuration without exposing credential values.

**Expected observables:**
- AC-1: No attempt enters ArmedQuiet without explicit foreground Arm and both successful permission and grant.
- AC-1: The app enters ArmedQuiet and loads only the narrow voice grant into native memory; the general hub credential's unlocked storage accessibility is not weakened.
- Listening has an explicit successful foreground consent/grant boundary, with general hub credential protection unchanged.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:32-33`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:90-90`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:180-182`

## SCENARIO-0007 — Apply centralized endpoint and follow-up defaults

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0009

**Preconditions:**
- The initial timing policy is loaded and a controlled clock drives the conversational state machine.
- Speech/nonspeech and completed Speaking or SpeakingUpdate output can be supplied at the native/bridge seam.

**Action:**
- Inspect where the default timing values are sourced and drive trailing nonspeech to just before and then through 600 ms.
- Supply continuous speech through the 30-second utterance boundary.
- Finish output, speak a follow-up before 12 seconds without a wake word, then repeat without follow-up through expiry for both Speaking and SpeakingUpdate.

**Expected observables:**
- AC-1: Clients use one policy configuration rather than scattered defaults; the values are not labelled measured optima.
- AC-2: The turn ends at the 600 ms trailing-nonspeech policy boundary, not before it.
- AC-2: The utterance does not exceed the 30-second maximum.
- AC-3: A follow-up is accepted without repeated wake; the window is anchored to output end and expiry returns either speaking state to ArmedQuiet after 12 seconds.
- The initial endpoint and follow-up behavior is driven by the single policy and its specified boundaries.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:175-189`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:210-214`

## SCENARIO-0008 — Close idle cloud input while a manager result remains pending

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0009

**Preconditions:**
- A conversational provider connection and durable manager session exist.
- A long-running manager request can outlive provider connection closure.

**Action:**
- Leave Conversing without speech for 30 seconds, then separately exercise an explicit end-conversation instruction.
- Let a long backend wait close the provider connection while observing the manager work, then deliver a fresh eligible committed result.
- While Conversing, first offer an uncommitted provider utterance as an answer, then deliver the fresh eligible committed manager reply.

**Expected observables:**
- AC-4: Cloud microphone input closes at the no-speech limit; idle and explicit end return Conversing to ArmedQuiet.
- AC-5: Manager work survives the disposable provider connection, and fresh output reopens without replacing the manager session.
- AC-6: The uncommitted utterance is not treated as the manager answer; the eligible committed reply moves Conversing to Speaking.
- Cloud input is bounded independently of manager work, and only eligible committed replies drive manager speech.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires explicit live opt-in plus provider inference access and independent human/meaning fixtures. Scripted provider responses cannot satisfy model behavior.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:5-8`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:88-88`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:184-189`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:210-214`

## SCENARIO-0009 — Keep explicit Evener interruption separate from acoustic stop

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0010

**Preconditions:**
- The manager has a current turn and queued input.
- Local speech-stop events and daemon turn/interrupt calls can be observed separately.

**Action:**
- Trigger barge-in and Stop speaking without submitting an explicit work-stop instruction.
- Submit a separate explicit stop-work instruction, delay its delivery, then let it reach Evener and obtain the outcome.
- Exercise turn/interrupt when it reaches the session, including its queued-input hold option.

**Expected observables:**
- AC-2: Neither operation alone cancels manager work.
- AC-3: Neither uses turn/interrupt as its acoustic stop mechanism.
- AC-3: Work is not represented as stopped merely because local audio is silent or an instruction is pending; any stop claim depends on the instruction reaching Evener and a known outcome.
- AC-3: The API retains current-turn-on-arrival cancellation and its ability to hold queued input, rather than acquiring acoustic timing semantics.
- Local silence, pending explicit stop and known Evener interruption remain distinct outcomes.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:34-35`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:87-87`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:215-216`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:227-229`

## SCENARIO-0010 — Coalesce fresh proactive updates without opening cloud input

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0013

**Preconditions:**
- A selected manager has historical messages and several pending notifications.
- The phone has a selected route, a controllable armed state and observable cue/output and cloud-input events.

**Action:**
- Arm the phone with the manager's historical messages present, then make pending notifications available for a current update.
- Commit a new chief-of-staff update while ArmedQuiet and observe route, cue, output and microphone events.
- Signal the first local speech onset during the update, then Disarm and make another committed update available.

**Expected observables:**
- AC-2: Arm does not replay the historical backlog; pending notifications are coalesced through the manager into a short current update.
- AC-1: The phone may enter SpeakingUpdate and output on the selected route after the cue.
- AC-3: Output by itself opens no cloud microphone input.
- AC-3: Local speech onset opens cloud input; after Disarm proactive speech does not continue.
- Current manager updates, not history, drive cued proactive output; microphone upload has its own local speech boundary.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:183-188`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:217-221`

## SCENARIO-0011 — Show accessible controls and actual listening and delivery state

**Kind:** surface
**Proof seam:** app-level
**Owning stories:** STORY-0011

**Preconditions:**
- The native app can render disarmed, local-only, cloud-sending and failed-capture states.
- Bound-session, route, interpreted-request, delivery and canonical-output updates can be supplied.

**Action:**
- Use the app's native accessibility conventions to locate and activate Arm/Disarm, Talk and Stop speaking, then change the bound session and route.
- Supply interpreted text, delivery-state changes and a committed canonical reply.
- Move between local-only processing and cloud transmission, then leave desired listening enabled while actual capture recovery fails.

**Expected observables:**
- AC-1: All named controls are exposed and the current bound chief-of-staff session and route are visible.
- AC-4: Controls use existing native components and accessibility conventions rather than new widget primitives required by the illustrative sketch.
- AC-2: The app displays the interpreted request, its delivery state and the canonical reply, not only transient speech.
- AC-3: The microphone display distinguishes local-only from cloud-sending audio and shows listening paused when capture fails rather than remaining green from desired state.
- Native controls and accessibility expose the actual session, route, request, delivery, reply and microphone state without prescribing the sketch's sample layout.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:149-151`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:227-243`

## SCENARIO-0012 — Prevent headset audio from leaking onto the public speaker route

**Kind:** failure-recovery
**Proof seam:** e2e
**Owning stories:** STORY-0012

**Preconditions:**
- A physical iPhone is speaking through a headset while armed.
- The speaker route and the native graph's recovery state can be observed.

**Action:**
- Change from headset to speaker during output and observe actual sound on the new route.
- Allow the route to become healthy without yet authorizing speech on it, then respond to the request to resume speech.

**Expected observables:**
- AC-3: Output stops before it can leak onto the newly public route; the native service rebuilds the graph.
- AC-3: Listening resumes when healthy, but the app asks before speech resumes on the newly public route.
- Physical output does not leak during the route change and public-route speech requires the specified user confirmation.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:144-148`

## SCENARIO-0013 — Recover audio interruptions and reset without stale-engine activity

**Kind:** failure-recovery
**Proof seam:** e2e
**Owning stories:** STORY-0012

**Preconditions:**
- A physical iPhone is armed in quiet, conversational or speaking states.
- iOS interruption, route failure and media-services reset can be induced, and old-engine callbacks can be delayed.

**Action:**
- Cause an iOS interruption or route failure, then restore capture and authorization with the OS permitting capture.
- Repeat while the OS forbids capture and separately after the user Disarms during recovery.
- Reset media services, rebuild the engine and release delayed old-engine callbacks.
- Make capture restoration fail after reset while desired listening remains enabled.

**Expected observables:**
- AC-4: The service enters Recovering as appropriate, stops capture/output as iOS requires, retries healthy portions and returns to ArmedQuiet only with recovered capture and authorization.
- AC-4: Automatic re-arm does not bypass the OS or reverse user Disarm.
- AC-5: The new engine is rebuilt and old callbacks are fenced rather than affecting current capture or playback.
- AC-5: The phone shows listening paused and does not show a healthy green microphone without real capture.
- Recovery is conditional on real native health, current authorization and continuing user intent; old engines cannot revive activity.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:144-153`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:190-199`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:222-225`

## SCENARIO-0014 — Replay uncertain admitted input without creating a second text submission

**Kind:** failure-recovery
**Proof seam:** integration
**Owning stories:** STORY-0021

**Preconditions:**
- The bridge exposes existing mutation custody fields and journal admission state.
- The manager can retain running work while transport is interrupted.

**Action:**
- Submit finalized interpreted text with clientMutationId and expectedInstanceId, admit it, then lose the acknowledgement and reconnect.
- Reuse the mutation identity with a conflicting payload and inspect the external-tool guarantee offered to the caller.
- Cause transport failure separately in Conversing, Speaking and SpeakingUpdate, then restore capture and authorization.
- Interrupt a request before journal admission and inspect spoken and displayed acceptance indications during recovery.

**Expected observables:**
- AC-1: Recovery preserves journaled input and valid manager work and retries healthy portions.
- AC-2: The uncertain replay retains both custody fields and the same payload without creating duplicate text submissions.
- AC-2: Conflicting reuse is rejected; text replay custody is not described as exactly-once external-tool execution.
- AC-1: Each appropriate failure enters Recovering and successful recovery returns to ArmedQuiet without cancelling valid work.
- AC-4: The unjournaled request is never announced as accepted.
- Uncertain delivery preserves replay identity and admitted work while unjournaled requests receive no false acceptance claim.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:42-46`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:86-86`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:191-195`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:222-225`

## SCENARIO-0015 — Keep text and existing work usable during cloud voice failure

**Kind:** failure-recovery
**Proof seam:** e2e
**Owning stories:** STORY-0021

**Preconditions:**
- A physical iPhone is armed and bound to a manager with running work.
- The hub and ordinary text path remain available while the provider voice path can fail.

**Action:**
- Cause a provider error, then say the local wake phrase while cloud voice remains unavailable.
- Send ordinary text to the same session, observe existing work and attempt a voice request that cannot be journaled.
- Restore cloud voice, capture and authorization while the phone remains armed.

**Expected observables:**
- AC-3: The phone produces a local unavailable cue; the error affects voice rather than the underlying session.
- AC-3: Same-session text and existing manager work continue.
- AC-4: The unjournaled voice request is not announced as accepted.
- AC-1: Healthy recovery retries automatically and returns to ArmedQuiet while preserving valid work and admitted input.
- Cloud voice failure is audible locally but does not take away durable manager work or text access.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:168-171`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:191-195`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:222-225`

## SCENARIO-0016 — Keep v1 exclusions and later options outside the shipping commitment

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0028

**Preconditions:**
- The proposed shipping surfaces, adapter choices, product claims and evergreen documentation changes are available for contract review.

**Action:**
- Compare the shipping scope and exposed capabilities with the explicit v1 exclusions.
- Inspect which adapters and transports are shipping versus future options and what evidence is used for any later transport.
- Review evergreen product guide changes before implementation and verification, then at the implementation handoff.

**Expected observables:**
- AC-4: V1 does not add Android, browser background/locked operation, simultaneous microphones, speaker recognition, custom user-trained wake words, offline autonomous manager work, voice-only permission escalation, arbitrary spoken slash-command execution or a provider-hosted agent brain.
- AC-5: Realtime is the first v1 adapter; GPT-Live stays a later experiment and ASR-to-Evener-to-speech stays a control/future adapter rather than a second v1 implementation. A later foreground browser preserves the bridge/session contract, and later WebRTC requires its own media, cancellation and background qualification rather than inherited WebSocket claims.
- AC-6: Design extraction alone does not change evergreen guides; guide updates accompany implemented and verified behavior.
- Exclusions remain constraints rather than extra implementation stories, and later options are not represented as existing v1 capability.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:48-54`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:65-72`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:94-95`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:122-124`

## SCENARIO-0017 — Authenticated binding rejects provider-selected identity and supports explicit setup

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0003

**Preconditions:**
- Exercise independent foreground setups with an existing manager and with no manager.
- Authenticated device, session, instance and listening-epoch values are known independently of provider output.

**Action:**
- Arm with an existing manager and supply provider arguments attempting to choose a different device, session, instance or epoch.
- In the no-manager setup, explicitly create the manager in the foreground and receive its identity.

**Expected observables:**
- The hub's authenticated binding remains authoritative for all four values.
- Arm reuses the existing manager; provider fields confer no routing authority.
- Setup stores the returned identity and voice binds to that identity.
- Creation is explicit, not a speculative reconnect replacement.
- Binding values come from authenticated context in both setup branches.
- Existing-manager reuse and explicit no-manager setup are distinct from the lost-response recovery path.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:251-251`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:299-303`

## SCENARIO-0018 — Only one completed call for the current audio input can be admitted

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0004

**Preconditions:**
- A valid authenticated binding and application-owned endpointing path exist.
- Provider input and response identities are observable, and independent turns exercise each invalid-call branch.

**Action:**
- Send native start, frames and end for an epoch-local utterance sequence.
- Inspect interpretation configuration and offer text containing a correction, negation, reference and slash-command wording.
- Supply partial arguments, malformed or extra arguments, an unknown tool, stale item or response metadata, and a completed response containing multiple function calls in separate branches.
- Emit response.function_call_arguments.done, then finish the associated response as interrupted, incomplete or canceled in separate branches.
- Complete response.done with completed status and exactly one valid submit_utterance for the current turn, committed item and response, then send additional calls or replay the response for the same VoiceTurn.
- Finish a separate interpretation attempt with no valid completed call.

**Expected observables:**
- VoiceTurn ID and input ordering are persisted before interpretation.
- Provider input is committed and its returned item ID is recorded.
- Only submit_utterance(text) is exposed, with no target-routing or execution function.
- The interpretation phase is silent and neither resolves the target nor executes the slash command.
- No branch admits these values as text intent.
- The response must contain exactly one function call named submit_utterance, with only its schema-valid text argument, correlated by response metadata to the persisted turn and committed input item.
- Partial text is not guessed into a request.
- The argument-done event alone admits no intent, even when its argument text looks complete.
- Without response.done having completed status, no intent or durable text receipt is produced and the user is asked to repeat.
- The valid completed response admits one intent.
- Additional calls or response replays cannot produce another admission for the closed audio turn.
- The user is asked to repeat and no partial text intent is admitted.
- Each closed turn has zero or one admitted intent, with valid current item and response association.
- Schema enforcement and isolation do not substitute for the separate live meaning-preservation test.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:252-252`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:258-271`

## SCENARIO-0019 — Journal admission, receipt projection and typed outbox retain one custody path

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0005, STORY-0022

**Preconditions:**
- A completed interpreted intent targets the authenticated manager.
- Run idle, working and activity-state-race cases against the existing daemon admission and custody paths.
- Observe bridge persistence, AppWire calls, phone outbox and recovery UI independently.

**Action:**
- Admit the intent while idle, then repeat in a working-manager fixture.
- Change manager activity between method selection and admission.
- Delay the durable receipt and then delay authoritative transcript or queue reflection after receipt.
- Disconnect the phone while the bridge owns the journal, then reconnect its recovery UI.
- Send an ordinary typed message alongside voice recovery projection.

**Expected observables:**
- Immutable text, target, selected method, payload and mutation identity are atomically persisted before any daemon mutation.
- The idle case selects turn/start and the working case selects turn/steer; receipt and custody state belong to the hub's intent record.
- The existing daemon admission, refusal and recovery behavior handles the race.
- The journal's method and payload do not mutate in response to the new activity observation.
- Received is absent before a durable receipt.
- After receipt, accepted or pending work remains represented until authoritative state reflects it.
- The AppWire bridge's durable journal continues to use existing Go persistence, mutation primitives and daemon custody rather than a second daemon queue.
- Voice recovery rows have a distinct source identity and are not copied into the phone send queue.
- The phone JS outbox never submits this spoken utterance.
- The typed message still follows its existing outbox path.
- Voice recovery display does not change typed-message ownership or create another voice sender.
- One durable voice mutation owns admission and recovery projection.
- UI receipt state follows durable custody evidence rather than transport success or mere text generation.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:253-253`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:272-288`

## SCENARIO-0020 — Lost acknowledgement and restart preserve the original mutation tuple

**Kind:** failure-recovery
**Proof seam:** process-level
**Owning stories:** STORY-0023, STORY-0005, STORY-0022

**Preconditions:**
- A voice intent has been persisted and attempted against a known original session and expected instance.
- Exercise both initially idle turn/start and initially working turn/steer selections.
- The daemon can retain authoritative receipt or admission state while the bridge process is restarted.

**Action:**
- Lose the acknowledgement after the mutation attempt, change manager activity, and restart the bridge.
- Recover the attempted intent.
- Attempt to reuse the same mutation identity with different text or another differing payload.

**Expected observables:**
- The persisted intent retains its original mutation ID, method, payload and expected instance.
- No new activity observation rewrites the selected method.
- The bridge reconciles with the original session and instance before any retry.
- Any required retry uses the same ID, method, payload and expected instance through the existing mutation contract.
- A persisted acceptance remains represented until authoritative queue or transcript reflection.
- The mismatch is an error, not an edit or replacement submission.
- The original mutation tuple is unchanged across loss, activity change and process restart.
- Recovery is reconciled with the original instance, with no second independently identified admission.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:272-280`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:290-294`

## SCENARIO-0021 — Crash recovery distinguishes never attempted, retired and accepted intents

**Kind:** failure-recovery
**Proof seam:** process-level
**Owning stories:** STORY-0023

**Preconditions:**
- Use separate persisted fixtures for a never-attempted intent and an accepted intent.
- The listening epoch, grant, binding and expected instance can be independently observed before and after bridge restart.

**Action:**
- Crash after intent persistence but before first attempt, then restart while the same current-epoch listening grant remains valid.
- Repeat the never-attempted crash with an invalid listening grant or a noncurrent epoch.
- Restart with a retired binding or a changed session instance, including a newly created chief being available.
- Persist acceptance, disarm, and restart.

**Expected observables:**
- The never-attempted intent may dispatch under its original valid binding.
- Recovery does not dispatch using the expired or noncurrent listening authority.
- Unresolved input is held for explicit recovery.
- It is never resent into the newly created chief or substituted instance.
- The acceptance stays accepted.
- Accepted work and its receipts remain represented rather than being revoked by disarm.
- Only the never-attempted current-epoch, valid-grant case permits dispatch without prior-attempt reconciliation.
- Retired or changed identity cannot redirect unresolved input, and disarm cannot undo persisted acceptance.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:290-294`

## SCENARIO-0022 — Lost manager creation response and failed reconnect do not create replacements

**Kind:** failure-recovery
**Proof seam:** process-level
**Owning stories:** STORY-0023, STORY-0003

**Preconditions:**
- Foreground setup explicitly creates a manager because none was available.
- Creation may have succeeded even though its response is lost; thread/start has not been granted the text-mutation replay contract.

**Action:**
- Lose the manager-creation response and restart setup or its bridge process.
- Fail reconnect to the retained manager identity.

**Expected observables:**
- Recovery reconciles the uncertain creation rather than blindly replaying thread/start.
- A recovered creation identity, when established by reconciliation, is the identity retained for binding.
- No replacement session is auto-created.
- Unresolved voice input is not moved to a newly created chief.
- Creation uncertainty and reconnect failure produce reconciliation or explicit unresolved recovery, not duplicate manager creation.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:290-303`

## SCENARIO-0023 — Interrupted audio without a completed intent asks for a repeat after recovery

**Kind:** failure-recovery
**Proof seam:** process-level
**Owning stories:** STORY-0023, STORY-0004

**Preconditions:**
- Audio has begun, but no completed text intent exists.
- Exercise interruption both during capture and during incomplete interpretation, with no raw microphone archive required.

**Action:**
- Interrupt the turn before a completed text intent and restart the bridge.
- Resume the user's conversation.

**Expected observables:**
- No durable text receipt is claimed for the interrupted turn.
- Partial audio is discarded instead of reconstructed into an admitted guess.
- Ask the user to repeat.
- Any subsequent valid utterance follows normal fresh turn admission rather than claiming delivery of the lost partial turn.
- Recovery makes only the durable-text claim supported by records: there was no completed intent and no text receipt.
- A raw audio archive is not a prerequisite for this repeat-based recovery contract.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:267-271`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:295-297`

## SCENARIO-0024 — Presentation source whitelist and speech wording confer no authority

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0014

**Preconditions:**
- Eligible committed communicate output and a durable pending question are available, with pending identity and consumed-frontier checks in place.
- Reasoning, arbitrary tool arguments and speculative streaming text are also observable upstream.

**Action:**
- Offer each public source and each private or speculative source to the presentation path.
- Inspect the speaking request and provide a paraphrased provider response.
- Return provider wording that claims an action outcome, an approval answer or that Jesse heard the message.

**Expected observables:**
- Only eligible committed communicate text or canonical public question and options may become speech.
- Reasoning, other tool arguments and speculative streams remain unspoken.
- The speaking phase has no tools and receives only its eligible source plus bounded presentation instruction.
- Generated wording is stored separately and does not overwrite canonical text.
- No such wording becomes a canonical action outcome, approval answer or human-attention assertion.
- The speaking provider gains no tool or action authority from its output.
- Canonical output and generated presentation remain separate.
- Speech eligibility, private-data exclusion and the provider's lack of authority are observable independently of wording fidelity.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:307-315`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:333-348`

## SCENARIO-0025 — SpeechDelivery distinguishes generated, queued and played output

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0014

**Preconditions:**
- An eligible durable source and its consumed-input frontier have a current presentation generation.
- Provider response and item identities and native rendered-sample acknowledgements can be observed independently.

**Action:**
- Generate audio and receive provider audio-done and transcript-done events without queueing it to the native player.
- Queue some audio but prevent rendering.
- Render a known portion and deliver the native acknowledgement.

**Expected observables:**
- SpeechDelivery retains source identity, frontier, generation and provider response and item identities.
- Generation and provider audio or transcript completion produce no played claim.
- Queued audio remains distinct from rendered audio.
- The played-sample watermark does not advance because bytes arrived or playback was scheduled.
- Only actual rendered samples advance the played watermark associated with the correct item and presentation.
- Generated, queued and played state remain separately inspectable.
- The durable presentation record and native acknowledgements support a played claim without collapsing generation, queueing and rendering.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:254-254`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:394-398`

## SCENARIO-0026 — Concurrent resolution cannot resurrect a spoken pending question

**Kind:** failure-recovery
**Proof seam:** integration
**Owning stories:** STORY-0015

**Preconditions:**
- A validated pending-question batch has a known identity and an eligible presentation candidate.
- Exercise separate races at speech selection and answer attachment, including resolution by a typed reply.

**Action:**
- Resolve the question after it is selected but before it would be spoken.
- In a new pending batch, capture a voice answer, then resolve the batch by a concurrent typed reply before voice answer attachment.

**Expected observables:**
- The pre-speech authoritative pending-identity re-read detects resolution.
- The resolved question is not resurrected as an active spoken question.
- The pre-attachment pending-identity re-read detects that the original question is no longer pending.
- The stale voice answer is not attached to the resolved batch or silently redirected to a different pending question.
- Both speech and answer attachment are fenced by the existing pending identity, with no stale-question resurrection.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:307-315`

## SCENARIO-0027 — Out-of-order every-channel consumption survives compaction and restore

**Kind:** contract
**Proof seam:** process-level
**Owning stories:** STORY-0016

**Preconditions:**
- Exercise committed communicate output. The later pending-question story repeats this same lineage contract for its second allowed source.
- Accepted inputs include voice, typed messages and steering from every supported channel, with queued and steered inputs consumed out of admission order.
- Observe the actual session input-consumption boundary independently of output assertions and notification order.

**Action:**
- Admit an earlier queued input and a later steering input, consume the later input first, and produce output before the earlier input enters model context.
- Offer an agent-supplied claim that the earlier input was consumed without consuming it at the boundary.
- Compact the conversation, restart and restore while presentation fences for these input identities remain outstanding.
- Consume the previously missing input at the authoritative boundary and produce committed public output again.

**Expected observables:**
- The persisted frontier includes the consumed later input but not the unconsumed earlier input.
- A greatest sequence number cannot falsely establish consumption of the missing identity.
- Output carries its session instance, durable source item identity and run generation.
- The claim cannot change authoritative frontier eligibility.
- The relevant consumed and still-outstanding identities survive compaction and restore.
- Committed output must not become eligible for the unconsumed input merely because another input had a greater sequence or newer run.
- The new persisted frontier reflects actual consumption and permits the corresponding fence to be satisfied.
- Repeat the ordering cases across voice, typed and all steering input channels.
- Committed outputs retain identity-based actual-consumption metadata across out-of-order intake, compaction and process restore.
- The metadata is newly supplied by the platform boundary, not assumed present in today's tool schemas.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:317-326`

## SCENARIO-0028 — New input suppresses stale speech without deleting history or ordinary progress

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0016, STORY-0018

**Preconditions:**
- An old operation can finish after a new correction is admitted.
- An older presentation is already playing; test spoken and typed corrections independently.
- Canonical output records and the phone control stream are observable.

**Action:**
- With no newer input fence, commit ordinary progress messages.
- Admit Beta, not Alpha as a typed correction while an Alpha presentation is playing.
- Repeat with a spoken correction.
- Produce an output whose authoritative frontier includes the new correction.

**Expected observables:**
- Progress may speak in committed order.
- The new accepted-input fence retires the older presentation through the phone control stream.
- A later-arriving Alpha result remains in canonical history but cannot masquerade in speech as the answer to Beta.
- Local speech onset takes the faster on-device stop path before admission or a hub round trip.
- After admission, the same consumed-frontier eligibility rule protects the spoken correction.
- An otherwise eligible fresh committed message or pending question may speak.
- Stale outputs were not deleted from the canonical transcript to achieve suppression.
- Newly admitted input fences both queued and already-playing stale presentation.
- Typed and spoken corrections receive equivalent freshness protection without blocking unfenced committed progress.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:328-340`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:394-398`

## SCENARIO-0029 — Reconnect and missed notifications refresh authoritative speech freshness

**Kind:** failure-recovery
**Proof seam:** integration
**Owning stories:** STORY-0016

**Preconditions:**
- Accepted inputs and consumed frontiers have advanced while the phone or bridge missed control events.
- Retained best-effort notifications can be delayed or reordered relative to authoritative state.

**Action:**
- Reconnect, or detect missed events, and deliver old and new notifications in misleading arrival order.
- Offer stale output alongside output eligible under refreshed frontier state.

**Expected observables:**
- Speech selection first refreshes authoritative accepted and consumed input state.
- Notification arrival order alone does not establish freshness.
- The stale output does not bypass an outstanding input fence.
- Speech selection uses the refreshed authoritative state and applicable pending-question identity rather than the last notification seen.
- Reconnect and event loss cannot restore obsolete speech by reordering notifications.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:336-340`

## SCENARIO-0030 — Presentation association survives recovery without promoting generated instructions

**Kind:** failure-recovery
**Proof seam:** process-level
**Owning stories:** STORY-0017

**Preconditions:**
- A SpeechDelivery has a canonical source, generated wording and order, and a completed, interrupted or uncertain playback status.
- The next utterance refers to that presentation; run each playback-status case independently.
- Generated wording includes a quoted instruction-like fragment to test its lower-trust status.

**Action:**
- Admit the next user input with the current SpeechDelivery association.
- Crash before delivery or acknowledgement, change the current presentation, then recover the intent.
- Pass the recovered input and its presentation metadata to the chief.

**Expected observables:**
- Platform-sourced metadata contains canonical source identity, generated wording and order, and playback status separately from the user's words.
- The association is persisted with the immutable intent.
- Recovery retains the original presentation association rather than attaching the new presentation to the same words.
- Generated wording is quoted lower-trust context despite platform ownership of the source association.
- Instruction-like wording cannot override the user's request, add action authority or replace an action receipt.
- A completed status is described as rendering evidence, not proof of human attention.
- The immutable intent retains exactly its original spoken context across recovery, with context and user authority kept separate.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:352-368`

## SCENARIO-0031 — Barge-in fences buffered packets and measures the physical player's clock

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0018

**Preconditions:**
- The native player has active audio, queued audio and old-generation packets buffered in transport.
- Use a single playback FIFO with known provider item boundaries and a resampled playback clock.
- The hub response can be delayed independently of local onset.

**Action:**
- Trigger local speech onset while delaying the hub.
- Release old-generation audio that was already buffered by transport and then deliver later old-generation packets.
- Observe the interruption report and compare its watermark with independently observed rendered samples.

**Expected observables:**
- The player stops and flushes, its queue is discarded and presentation generation advances without waiting for the hub.
- None resumes obsolete playback; local onset wins over every later packet.
- Provider item identities and the single FIFO determine the played item and watermark.
- The physical player's sample clock is mapped back into the provider timeline under resampling.
- Received bytes, scheduled duration and generated transcript length cannot stand in for rendered samples.
- The stop and generation fence work before a hub round trip, including transport-buffered late packets.
- The reported played watermark corresponds to actual rendering at the correct item timeline, not queued or generated output.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:379-382`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:394-402`

## SCENARIO-0032 — Realtime interruption truncates played history or replaces an unreconcilable conversation

**Kind:** failure-recovery
**Proof seam:** integration
**Owning stories:** STORY-0018

**Preconditions:**
- A Realtime WebSocket response has a partially played item and generated but never-played items.
- The bridge receives the actual rendered watermark with provider item identities and FIFO association.
- Exercise providers that support reconciliation and a provider path that cannot reconcile unheard output.

**Action:**
- Process the native interruption report for the active response.
- Exercise a case where unheard output cannot be reconciled.

**Expected observables:**
- Send response.cancel for the active response.
- Send conversation.item.truncate for the played item with its actual played audio_end_ms.
- Truncate or remove never-played items as the provider supports.
- Replace the voice conversation and seed the replacement from durable text.
- Do not restore the unheard tail as conversational fact.
- Provider history reflects the actual played boundary, or the unreconcilable provider conversation is replaced without an unheard-tail claim.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:400-405`

## SCENARIO-0033 — Speech stop, session Stop, disarm and microphone loss retain distinct work semantics

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0019, STORY-0020

**Preconditions:**
- The manager has accepted work, durable receipts and held queues where relevant, while audio capture and speech are active.
- Reset the same known work and queue state for each independent control case.

**Action:**
- Start speaking but do not yet complete a new intent.
- Say Stop speaking.
- Press the existing session Stop control.
- Disarm, then independently exercise microphone loss.

**Expected observables:**
- Speech stops immediately.
- Evener work continues until a new intent arrives.
- Speech was already stopped locally.
- The voice layer issues no automatic interrupt RPC.
- Speech stops.
- Existing session-scoped Stop behavior and held queues are preserved, not replaced by a voice-specific cancellation meaning.
- Both capture and speech stop in each case.
- Accepted work and its durable receipts remain intact.
- Each control has its specified speech effect without silently changing work, custody or held-queue semantics.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:407-414`

## SCENARIO-0034 — Load, renew and validate the native voice grant

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0024

**Preconditions:**
- Native authorization storage and the real voice-grant validation boundary are exercised with controlled credentials.
- The phone is unlocked and voice is not armed.

**Action:**
- Arm voice, renew while authorized, and inspect native memory and persisted credentials.
- Reconnect once with matching authorization and once for each mismatched device, epoch, session or scope.
- Disarm and inspect the native grant lifetime.

**Expected observables:**
- A revocable session-bound grant lives in native memory for the armed lifetime.
- Renewal uses the authenticated native connection.
- No broader hub credential is persisted with weaker Keychain accessibility to sustain voice.
- Matching authorization is validated.
- Each mismatched binding is rejected rather than treated as an authorized reconnect.
- The in-memory voice grant is not retained beyond the armed lifetime.
- Grant lifetime, renewal transport, four reconnect validation dimensions and credential persistence are inspectable.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:423-427`

## SCENARIO-0035 — Lose voice authorization without cancelling accepted work

**Kind:** failure-recovery
**Proof seam:** integration
**Owning stories:** STORY-0024

**Preconditions:**
- Voice is armed and manager work has been accepted.

**Action:**
- Exercise grant expiry and revocation separately.
- Lose and restart the native process.

**Expected observables:**
- Cloud access ends and an honest local cue indicates loss of access.
- Accepted manager work continues.
- Voice cannot resume merely from the lost in-memory grant.
- Foreground re-arm is required.
- Authorization loss changes voice access, not accepted manager-work lifetime.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:423-429`

## SCENARIO-0036 — Disclose ambient authority and preserve authenticated approvals

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0024

**Preconditions:**
- The Arm surface and existing sandbox, project and approval boundaries are available.

**Action:**
- Inspect Arm disclosure and activate an already authorized channel with a wake word.
- Request work crossing existing project, sandbox or approval boundaries and try to answer an approval by background speech.

**Expected observables:**
- Disclosure covers nearby people and recordings issuing requests and hearing replies, including while locked.
- Wake is not represented as speaker identification or informed consent.
- Existing boundaries still apply.
- Approval is handled by the existing authenticated UI, not answered for the user by background speech.
- Voice activation does not create additional authority.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:430-434`

## SCENARIO-0037 — Clear transient audio and retain only permitted records

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0025

**Preconditions:**
- The native audio ring, active transport buffers, journals and diagnostics are exercised together.

**Action:**
- Capture ambient audio and a finalized request, then independently Disarm, close a conversation and fail a partial turn.
- Inspect journal and diagnostic records after finalized and failed turns.

**Expected observables:**
- Audio is limited to the bounded RAM ring and active transport buffers.
- Each teardown clears both audio buffers.
- Journal retention contains finalized request text and receipts under existing session policy, not raw ambient audio.
- Diagnostics default to IDs, times, sizes, state and reason codes and contain no transcripts, PCM, credentials or provider headers.
- Buffer contents, persisted records and diagnostic fields demonstrate the distinct retention boundaries.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:435-439`

## SCENARIO-0038 — Show provider privacy without opening an armed-only cloud microphone

**Kind:** surface
**Proof seam:** integration
**Owning stories:** STORY-0025

**Preconditions:**
- Voice and manager models have separately identifiable provider configuration.

**Action:**
- Inspect provider disclosure, including an OpenAI Live or Realtime configuration using store:false.
- Arm without activating a conversation.

**Expected observables:**
- The chosen voice provider is named and is not conflated with a possibly different manager provider.
- No-API-training-by-default and the cited default 30-day abuse-monitoring retention are distinguished.
- store:false is not described as zero retention.
- Being armed alone does not keep the cloud microphone open.
- Provider privacy is stated separately from local retention and armed-state transport behavior.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:440-443`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:480-482`

## SCENARIO-0039 — Fence and reconcile a dropped voice socket before new admission

**Kind:** failure-recovery
**Proof seam:** integration
**Owning stories:** STORY-0026

**Preconditions:**
- A voice generation has journaled input, committed output, provider calls and playback with uncertain completion.

**Action:**
- Drop the voice socket during output and reconnect.
- Inspect uncertain playback, then explicitly request repeat.

**Expected observables:**
- Output stops, the old generation is fenced and reconnect uses bounded backoff.
- Journaled input and committed output reconcile before new audio admission.
- Provider function calls and old spoken backlog are not replayed.
- Uncertain playback is marked interrupted.
- Repeat is available on user request rather than automatic backlog replay.
- Old-generation activity cannot become a fresh request or unsolicited replay.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:444-447`

## SCENARIO-0040 — Replace an adapter connection using only committed context

**Kind:** failure-recovery
**Proof seam:** integration
**Owning stories:** STORY-0026

**Preconditions:**
- The manager has committed text, known action outcomes and speech that was generated but not played.

**Action:**
- Exercise provider replacement and expiry separately.

**Expected observables:**
- The manager session identity remains unchanged.
- Replacement context is bounded and comes from committed text and known action outcomes.
- Claims that unplayed speech was heard are excluded.
- The new provider conversation continues the same chief without inventing playback history.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:448-451`

## SCENARIO-0041 — Reject stale voice bindings without damaging text recovery

**Kind:** failure-recovery
**Proof seam:** integration
**Owning stories:** STORY-0026

**Preconditions:**
- Healthy text chat and queued work exist beside a voice binding.

**Action:**
- Reconnect with an unknown binding and with a stale instance in separate cases.

**Expected observables:**
- Only the invalid voice binding is rejected.
- Existing session recovery is offered.
- Text chat remains healthy and queued work is not deleted.
- The rejection is narrow and the existing recovery path remains usable.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:452-453`

## SCENARIO-0042 — Change session runtime while retaining chief identity

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0027

**Preconditions:**
- The initial implementation has one adapter and one device behind the four named responsibilities.
- A chief session is bound to an active voice generation.

**Action:**
- Inspect AudioDevice, WakeAndSpeechDetector, ConversationVoiceAdapter and ChiefSessionBridge responsibilities and session runtime configuration.
- Change session runtime configuration using the adapter boundary.

**Expected observables:**
- Responsibilities are replaceable without requiring a plugin framework or universal audio protocol.
- Initial delivery remains one adapter and one device.
- The previous voice generation retires.
- The chief-of-staff session remains unchanged.
- Later local ASR/TTS or GPT-Live implementations cannot redefine durable intent or manager authority.
- Runtime selection is session configuration rather than a new manager session.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:457-461`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:473-474`

## SCENARIO-0043 — Reject invented capabilities and separate voice usage

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0027

**Preconditions:**
- Adapter capability declarations, bridge semantics, local detection and usage records are available.

**Action:**
- Compare declarations to demonstrated input completion, tool-call completion, explicit cancel, playback truncation, exact reading, transcript availability, usage reporting and connection limits.
- Exercise an adapter with missing semantics, including a case without a tested bridge replacement.
- Run local quiet-state detection and a voice conversation that also invokes manager inference.

**Expected observables:**
- All eight dimensions describe actual semantics.
- A prompt requesting exact reading or another capability cannot alone set it true.
- Required missing semantics are supplied and tested by the bridge or the adapter stays experimental.
- Wake/VAD runs locally.
- Voice usage and manager inference are reported separately.
- Capability evidence and usage accounting expose rather than hide runtime differences.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:463-474`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:480-482`

## SCENARIO-0044 — Observe quiet-state privacy at actual network and provider ingress

**Kind:** contract
**Proof seam:** e2e
**Owning stories:** STORY-0029

**Preconditions:**
- The assembled native voice path exposes actual network/provider ingress for observation.

**Action:**
- Capture ingress while arming, remaining silent, speaking ordinary non-wake speech and rolling the pre-wake buffer.
- Activate the conversation after pre-wake buffer roll.

**Expected observables:**
- Zero microphone frames upload before activation.
- The buffered pre-wake prefix is never uploaded.
- Actual ingress evidence, not a mocked upload counter, establishes the quiet-state privacy gate.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:506-506`

## SCENARIO-0045 — Qualify built-in audio before enabling any headset route

**Kind:** surface
**Proof seam:** e2e
**Owning stories:** STORY-0030, STORY-0031

**Preconditions:**
- The existing app has a small opt-in native debug surface; the standalone research probe is not installed over it.
- Native permission tests are reconciled with the approved product contract, not silently weakened.
- The built-in microphone/speaker is the first candidate route and other routes lack qualification.

**Action:**
- Exercise local wake/VAD and physical locked-device qualification on the built-in route first.
- Select an unsupported headset or Bluetooth profile after a built-in route is qualified.
- Evaluate a headset candidate for enablement.

**Expected observables:**
- The native qualification surface belongs to the existing app.
- No other route is enabled merely because built-in speakerphone tests pass.
- The unsupported route stays unavailable and explains why.
- Return to the last qualified route requires user consent.
- Its own AEC, barge-in and recovery checks must pass first.
- Evidence is not generalized to every Bluetooth profile.
- Available routes correspond to route-specific qualification, beginning only with built-in audio.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:510-520`

## SCENARIO-0046 — Recover physical device and network changes honestly

**Kind:** failure-recovery
**Proof seam:** e2e
**Owning stories:** STORY-0030, STORY-0031

**Preconditions:**
- A physical phone has accepted manager work and an active voice configuration.

**Action:**
- Separately exercise physical interruption, media reset, Bluetooth route change, Wi-Fi/cellular change and locked reconnect/renewal.

**Expected observables:**
- Listening recovers automatically wherever permitted.
- Accepted work survives and output does not leak to the speaker.
- If listening cannot recover, the UI exposes a genuinely paused state.
- Unqualified routes do not become enabled as a recovery shortcut.
- Recovery records distinguish automatic listening from honest pause and remain route-specific.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:507-513`

## SCENARIO-0047 — Separate deterministic qualification from opt-in live tests

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0032, STORY-0033

**Preconditions:**
- The test runner can select scripted and live provider/model boundaries.

**Action:**
- Run the default tests, including a configuration where credentials happen to be present.
- Request live behavior tests with explicit opt-in, separately with and without credentials.

**Expected observables:**
- Tests remain deterministic with a scripted LLM/provider boundary and real Evener components below it.
- Credentials alone do not start live voice/model behavior tests.
- Live execution requires both explicit opt-in and credentials.
- Default deterministic evidence and explicitly requested live evidence remain distinct.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires explicit live opt-in plus provider inference access and independent human/meaning fixtures. Scripted provider responses cannot satisfy model behavior.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:486-490`

## SCENARIO-0048 — Keep unproven and failed gates distinct from passing evidence

**Kind:** contract
**Proof seam:** e2e
**Owning stories:** STORY-0032, STORY-0033

**Preconditions:**
- The approved design describes proposed implementation, not a shipped feature.
- Available historical evidence consists of separate fixture calls, manager tests and limited native/protocol checks.

**Action:**
- Assess journey and physical readiness using only those disconnected successes.
- Evaluate a candidate with a failed required gate.

**Expected observables:**
- Production relay/native transport end-to-end behavior and physical/live gates remain NOT PROVEN.
- Targets are not reported as measurements.
- Record the evidence and narrow the experiment rather than weaken the assertion.
- Implementation choices about layout, wire names, SQLite, speech detector and prompts remain allowed only within the contracts.
- Release readiness follows the gates rather than a sum of unrelated successes or historical design status.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:486-490`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:533-536`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:543-552`

## SCENARIO-0049 — Recheck published voice model identifiers and prices at build time

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0034

**Preconditions:**
- A build selects a hosted voice model and has spike-era identifiers or prices available.

**Action:**
- Compare build model IDs and pricing information to the provider's current published values.

**Expected observables:**
- Identifiers and prices are rechecked at build time.
- Historical probe spend and inspected rates are not treated as current billing, forecasts or model-quality evidence.
- Build-time provider selection is not silently based on stale spike values.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:476-480`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:550-552`

## SCENARIO-0050 — Fault the durable bridge across duplicate, crash and stale-instance cases

**Kind:** failure-recovery
**Proof seam:** e2e
**Owning stories:** STORY-0036

**Preconditions:**
- The real bridge, daemon and durable storage are assembled with observable transcript admissions.

**Action:**
- Exercise duplicate provider calls, hub crash before submit, hub crash after submit, a lost receipt, phone reconnect and changed instance as distinct cases.

**Expected observables:**
- Each intent has one transcript admission.
- Retries are not retargeted to a changed instance.
- Uncertainty is retained rather than fabricated into success or safely retryable failure.
- Existing recovery remains available.
- Durable admissions and recovery evidence cover every named fault, not only successful submission.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:501-501`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:524-525`

## SCENARIO-0051 — Exercise persistence across silence, background transport and locked renewal

**Kind:** failure-recovery
**Proof seam:** e2e
**Owning stories:** STORY-0036

**Preconditions:**
- The assembled native path has accepted manager work and can run background transport and renewal while physically locked.
- Duplicate/crash/instance and correction-race qualification cases are available in the same persistence slice.

**Action:**
- Enter local silence, background and lock the app, then renew or reconnect transport.
- Run duplicate/crash/instance fences and spoken/typed correction races as part of this delivery slice.

**Expected observables:**
- Accepted manager work survives.
- Journaled input and committed output reconcile before fresh audio admission.
- Those gates remain required alongside background transport and locked renewal, not deferred in exchange for a successful foreground turn.
- An old reply cannot be spoken as the answer to a newer correction.
- The persistence/interruption slice demonstrates its combined native and durable behavior.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:444-447`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:501-503`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:524-525`

## SCENARIO-0052 — Time Realtime replacement at its documented connection limit

**Kind:** failure-recovery
**Proof seam:** e2e
**Owning stories:** STORY-0036

**Preconditions:**
- Realtime is the adapter under qualification and live testing is explicitly opted in with credentials.
- The chief has committed text, known action outcomes and unplayed or uncertain speech.

**Action:**
- Run a timed replacement test covering Realtime's documented 60-minute limit rather than relying on the normally shorter idle policy.
- Inspect uncertain playback and request repeat explicitly.

**Expected observables:**
- The manager session identity remains the same.
- Bounded replacement context uses committed text and known action outcomes, not unplayed-speech claims.
- Provider function calls and old spoken backlog are not replayed.
- Playback uncertainty is marked interrupted.
- Repeat is user-requested rather than automatic backlog replay.
- Timed Realtime replacement is qualified separately from ordinary idle closure.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires explicit live opt-in plus provider inference access and independent human/meaning fixtures. Scripted provider responses cannot satisfy model behavior.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:444-451`

## SCENARIO-0053 — Reconcile typed Beta during voice output and control-stream loss

**Kind:** failure-recovery
**Proof seam:** e2e
**Owning stories:** STORY-0037, STORY-0038

**Preconditions:**
- Slow Alpha and the real text/voice manager path are active.

**Action:**
- Admit typed Beta during voice output, complete Alpha before steering consumption, then disconnect the control stream.

**Expected observables:**
- Freshness reconciliation suppresses Alpha's old reply.
- Neither Alpha nor typed Beta is lost.
- Text and voice share correction freshness even after the control stream disconnects.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:502-503`

## SCENARIO-0054 — Distinguish speech interruption, Disarm, cancellation and Stop

**Kind:** contract
**Proof seam:** e2e
**Owning stories:** STORY-0037, STORY-0038

**Preconditions:**
- The real chief has running work, an irreversible action outcome and queued messages.

**Action:**
- Trigger speech onset and, separately, Disarm.
- Explicitly cancel work, including work with irreversible effects.
- Use existing session Stop with queued messages present.

**Expected observables:**
- Neither action issues session Stop.
- The actual cancellation result is reported.
- Irreversible work is not described as undone.
- Queued messages are held as specified by existing Stop semantics.
- Distinct controls retain their distinct work and queue effects.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:505-505`

## SCENARIO-0055 — Keep voice opt-in and separate Apple policy from technical capability

**Kind:** contract
**Proof seam:** e2e
**Owning stories:** STORY-0039

**Preconditions:**
- A distribution candidate has technical background-audio capability but incomplete physical qualification.

**Action:**
- Inspect the candidate's voice enablement and microphone disclosure against current Apple rules for active recording.
- Review the candidate's readiness claims.

**Expected observables:**
- Voice remains opt-in until the physical gates pass.
- Recording behavior and clear microphone disclosure comply with current Apple rules.
- Technical background-audio capability is not represented as App Review acceptance.
- Absent physical and live evidence, those gates remain NOT PROVEN.
- Opt-in distribution, physical qualification and Apple-policy acceptance are not conflated.

**Automation status:** manual-residual
**Execution command:** TBD
**Manual residual:** NOT RUN. Requires the real iPhone/audio route and the stated measurements or signed install. No mocked microphone, simulated playback, compilation or simulator check can satisfy this card.

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:486-490`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:526-528`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:553-555`

## SCENARIO-0056 — Defer browser voice without substituting it for native qualification

**Kind:** contract
**Proof seam:** integration
**Owning stories:** STORY-0039

**Preconditions:**
- Native v1 delivery is being assessed; browser foreground voice is optional and deferred.

**Action:**
- Inspect v1 scope and the constraints on any later browser foreground adapter.

**Expected observables:**
- Browser implementation is not required for native v1.
- A later adapter requires a secure-context microphone and the shared bridge, mutation and provenance contracts.
- Tab suspension or locked iPhone Safari does not substitute for native v1 evidence.
- Optional browser work remains deferred without weakening native delivery gates.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:529-531`

## SCENARIO-0057 — Late output keeps its originating request frontier after newer input advances

**Kind:** contract
**Proof seam:** process-level
**Owning stories:** STORY-0016

**Preconditions:**
- A real session can delay committed-message publication from a model request while accepted/steering state advances.
- Capture request histories independently at the provider boundary, not from output provenance under test.

**Action:**
- Start request Alpha with no Beta input, then admit Beta and advance the relevant accepted/incorporated or later consumed state before delayed Alpha output is persisted.
- Publish Alpha, then publish output from a request whose captured model context actually contains Beta; repeat across compaction/restore.

**Expected observables:**
- Alpha output retains the exact immutable frontier captured for its originating request, even if newer state already includes Beta.
- Neither the current mutation state nor a greatest sequence can relabel Alpha as having consumed Beta.
- Alpha remains canonical but suppressed from speech; only the output originating from the Beta-containing request satisfies the Beta fence.
- Compaction and restore preserve the originating request lineage and cannot promote old output to a newer input fence.
- The test exercises the actual supported runtime ordering, including an old request whose tool output persists after newer steering was appended; it must not fabricate concurrent model calls if the runtime serializes them.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:317-340`

## SCENARIO-0058 — Restore a durable question batch with its exact originating frontier

**Kind:** failure-recovery
**Proof seam:** process-level
**Owning stories:** STORY-0015

**Preconditions:**
- A validated ask_user batch was durably committed with its full ordered public options, pending identity and originating model-request frontier.
- A later input can advance accepted/consumed state while an older pending batch remains in history.

**Action:**
- Restore the session from durable history and select the pending batch for presentation.
- Advance newer input state, then resolve the batch by another channel before speaking or attaching a voice answer.

**Expected observables:**
- The full batch and stable pending identity are reconstructed, not inferred from the live-only question helper or a suffix of history.
- The batch retains the exact originating request frontier, not current mutation state.
- The old batch cannot inherit the newer request frontier or become a fresh answer.
- Authoritative pending-identity rechecks suppress resolved questions and prevent stale answer attachment.
- Durable public question identity, option order and originating provenance survive restore without resurrecting a resolved batch.

**Automation status:** pending
**Execution command:** TBD

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:307-340`
