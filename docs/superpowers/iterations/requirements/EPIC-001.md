# EPIC-001 — Manager binding and durable voice intake

**Summary:** Manager binding and durable voice intake
**Stories:** STORY-0001, STORY-0002, STORY-0003, STORY-0004, STORY-0005
**Primary sources:** `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md`
**Status:** 0/5 done

## STORY-0001

**Epic:** EPIC-001 — Manager binding and durable voice intake
**Title:** Choose one durable manager for voice and text

**As a** Jesse
**I want** to bind voice to one persistent chief-of-staff manager
**So that** I can inspect work, start it, correct myself and hear results without keeping a screen open

**Acceptance criteria:**
- AC-1: Select or create a manager explicitly, with one chosen manager per armed phone. Manager mode is immutable at launch: do not toggle an ordinary session into a manager or turn independently top-level managed workers into owned delegates. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0001` · scenario:`SCENARIO-0002`
- AC-2: The authenticated voice binding contains device identity, selected session reference, live instance identity and an armed-listening epoch. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0001` · scenario:`SCENARIO-0002`
- AC-3: Only one active phone owns speech output for the session in v1. A second Arm identifies the existing owner and offers explicit takeover; takeover fences old audio without cancelling manager work. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0001` · scenario:`SCENARIO-0002`
- AC-4: Evener retains reasoning, tools, permissions, work history and recovery. Voice and ordinary text use the same manager session; its bound daemon and existing authority determine which sessions it can inspect or control, and speech grants no additional authority. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0001` · scenario:`SCENARIO-0002`
- AC-5: Speak concise paraphrases of verified, committed user-facing Evener outputs with consumed-input provenance. Committed Evener text remains canonical; a prompt to a generative speech model is not an exact-reading guarantee. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0001` · scenario:`SCENARIO-0002`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:5-8`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:17-20`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:34-46`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:84-88`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:162-166`

**Status:** pending

## STORY-0002

**Epic:** EPIC-001 — Manager binding and durable voice intake
**Title:** Relay voice through an authenticated replaceable Realtime adapter

**As a** a voice integration maintainer
**I want** a narrow authenticated voice frontend rather than a second agent
**So that** provider speech can change without moving Evener's authority or durable session

**Acceptance criteria:**
- AC-1: The first transport is native WebSocket to the configured hub and hub WebSocket to OpenAI Realtime. Keep provider credentials on the hub, off the phone, with a single tool-event owner and Swift ownership of the capture/playback graph used for local wake and barge-in. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0003` · scenario:`SCENARIO-0004`
- AC-2: The replaceable provider adapter interprets speech, presents verified Evener output and translates voice events; provider conversations are disposable. Expose one relay only, never filesystem, shell, delegate, approval or session-control tools; the adapter cannot manage sessions. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0003` · scenario:`SCENARIO-0004`
- AC-3: The hub authenticates the session binding, journals input admission, calls existing daemon mutation APIs and subscribes to committed output. Keep PCM on a dedicated voice stream; AppWire remains text/image/skill/command, and finalized interpreted text uses existing input mutations. Deliver generation-tagged output audio to native playback. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0003` · scenario:`SCENARIO-0004`
- AC-4: Use the configured hub's authenticated secure transport. Off-tailnet connections require TLS; tailnet deployments retain their established private boundary. Never send microphone audio over arbitrary public plaintext HTTP or WebSocket connections. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0003` · scenario:`SCENARIO-0004`
- AC-5: Voice is an explicitly advertised hub capability. With an older hub, text stays usable and voice is unavailable; do not try guessed RPC names. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0003` · scenario:`SCENARIO-0004`
- AC-6: Load the selected voice model independently of the manager's reasoning model. A model inventory listing is not proof that inference access works. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0003` · scenario:`SCENARIO-0004`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:5-8`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:26-30`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:65-72`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:85-88`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:97-130`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:168-171`

**Status:** pending

## STORY-0003

**Epic:** EPIC-001 — Manager binding and durable voice intake
**Title:** Bind foreground voice to the authenticated manager

**As a** user arming voice in the foreground
**I want** voice bound to the intended existing manager under authenticated device and session authority
**So that** the voice provider cannot redirect my conversation or create a replacement chief

**Acceptance criteria:**
- AC-1: The hub owns a binding containing the device, session, expected instance and listening epoch. Authenticated context supplies these values; provider arguments cannot choose or override them. VoiceBinding is a proposed domain record, not an assertion that these API fields already exist. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0017` · scenario:`SCENARIO-0022`
- AC-2: Arm binds an existing manager rather than creating another manager. The manager capability is an existing dependency, not new work in this story. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0017` · scenario:`SCENARIO-0022`
- AC-3: When no manager exists, foreground setup explicitly creates one and stores its returned identity before using that identity for voice. Lost creation responses and reconnect failures follow the separate binding-recovery contract, not automatic replacement creation. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0017` · scenario:`SCENARIO-0022`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:247-251`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:299-303`

**Status:** pending

## STORY-0004

**Epic:** EPIC-001 — Manager binding and durable voice intake
**Title:** Admit one faithful text intent per audio turn

**As a** user speaking to the bound manager
**I want** one faithful interpretation of each completed utterance
**So that** partial, duplicate or misassociated provider output cannot become an action

**Acceptance criteria:**
- AC-1: The native detector sends start, frames and end with a stable utterance sequence within the listening epoch. With application-owned endpointing, the hub persists the VoiceTurn ID and ordered input sequence before interpretation, commits the provider input and records the returned provider input item mapping. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0018` · scenario:`SCENARIO-0023`
- AC-2: Interpretation exposes only submit_utterance with the single argument text. It preserves the user's request, corrections, negations and references without resolving a target session or executing slash commands, and all speech from this phase is suppressed. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0018` · scenario:`SCENARIO-0023`
- AC-3: For Realtime, wait for response.done with completed status and exactly one function call named submit_utterance whose strict schema permits only the single text argument. Correlate the response through its metadata to the persisted VoiceTurn and committed input item. response.function_call_arguments.done alone is not admission: it also occurs for interrupted, incomplete or canceled responses. Ignore partial arguments and reject malformed, unknown, stale or additional calls; one closed audio turn admits at most one text intent. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0018` · scenario:`SCENARIO-0023`
- AC-4: If no valid call completes, ask the user to repeat rather than guessing an intent from partial text. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0018` · scenario:`SCENARIO-0023`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:247-252`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:258-271`

**Status:** pending

## STORY-0005

**Epic:** EPIC-001 — Manager binding and durable voice intake
**Title:** Persist voice admission through existing daemon custody

**As a** user submitting a spoken request
**I want** durable admission through the existing text-mutation contract
**So that** received means durable receipt and voice does not create duplicate submission paths

**Acceptance criteria:**
- AC-1: Before any daemon mutation attempt, atomically persist the VoiceIntent's immutable interpreted text, target, selected method and mutation identity, with receipt and custody state represented by the hub-owned intent record. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0019` · scenario:`SCENARIO-0020`
- AC-2: Select ordinary turn/start when idle and turn/steer when working, and persist the selected method with the payload. Existing admission, refusal and recovery paths own activity-state races; a later state observation does not authorize changing a retry's method. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0019` · scenario:`SCENARIO-0020`
- AC-3: Show received only after a durable receipt. Accepted or pending work remains represented until authoritative transcript or queue state reflects it. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0019` · scenario:`SCENARIO-0020`
- AC-4: The bridge is an AppWire client with a durable journal that can outlive the phone connection and reuses existing Go persistence and mutation primitives and daemon custody rules. It creates no second daemon queue, and the phone's JS outbox does not also submit the spoken utterance. · impact:`cross-surface` · seam:`integration` · scenario:`SCENARIO-0019` · scenario:`SCENARIO-0020`

**Sources:**
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:253-253`
- `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md:272-288`

**Status:** pending
