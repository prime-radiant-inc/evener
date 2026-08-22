# Native Mobile Duplex Voice Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a focused full-screen iOS voice experience that recognizes speech, starts or steers Evener turns, speaks streamed assistant prose, survives audio/lifecycle failures, and supports measured barge-in.

**Architecture:** The versioned native plugin owns AVFoundation, Speech, permissions, audio routes, voice activity, synthesis, and ordered events. A pure TypeScript coordinator owns sentence chunking, AppWire turn semantics, de-duplication, and UI state; the voice screen consumes that coordinator and always leaves typed conversation available.

**Tech Stack:** Swift 6.3/XCTest, iOS 17+, `SFSpeechRecognizer`, `AVAudioEngine`, `AVSpeechSynthesizer`, `AVAudioSession`, Tauri 2 plugin channels, React/TypeScript/Vitest.

**Spec:** `docs/superpowers/specs/2026-08-21-native-mobile-app-design.md`

## Global Constraints

- Complete foundation and conversation plans first.
- Validate `en-US`; other available locales are best-effort and not acceptance targets.
- Voice is foreground-only. Typed session control remains usable after every voice/native failure.
- Partial recognition never mutates a session. Final text starts when idle and steers when active; structured ask mode disables voice submission.
- A complete assistant sentence queues within 250 ms and begins within 750 ms when synthesis is idle.
- Barge-in invokes synthesis stop within 250 ms of combined native voice activity plus a new nonempty recognition partial, clears queued speech, then waits for final recognition before steering.
- Speak only new assistant prose in the active voice session. Never speak reasoning, tools, notices, history, inactive sessions, or the same consumed range twice.
- Camera, microphone, speech, and local-network permissions are requested at first use and have nonempty final Info.plist strings.
- Physical audio claims require a real-device record; simulator tests cannot satisfy them.

## File Structure

- `mobile/tauri-plugin-evener-native/ios/Sources/` — permission, speech, synthesis, audio session, voice activity, QR, haptics, and plugin command implementations.
- `mobile/tauri-plugin-evener-native/ios/Tests/` — Swift protocol fakes and state-machine tests.
- `mobile/src/native/contract.ts` — voice commands/events added to bridge version 1.
- `mobile/src/voice/chunker.ts` — deterministic sentence and Markdown-to-speech logic.
- `mobile/src/voice/coordinator.ts` — turn, synthesis queue, de-duplication, and barge-in state machine.
- `mobile/src/state/voice.ts` — app-facing voice state.
- `mobile/src/screens/VoiceScreen.tsx` — focused voice UI.
- `mobile/src/components/voice/` — captions, level response, controls, status.
- `docs/verification/native-mobile-v1.md` and assets — commit-bound gate/device evidence.

---

### Task 1: Extend and Cross-Test the Native Voice Contract

**Files:**
- Modify: `mobile/src/native/contract.ts`, `contract.test.ts`
- Modify: plugin Rust contract and `contract-v1.json`
- Modify: Swift contract decoder/tests

**Interfaces:**
- Produces commands `voice.permissions`, `voice.start`, `voice.stop`, `voice.speak`, `voice.stopSpeaking`, `voice.setRate`; events `voice.level`, `voice.partial`, `voice.final`, `voice.speechStarted`, `voice.speechFinished`, `voice.bargeIn`, `voice.interrupted`, `voice.error`.

- [ ] **Step 1: Add failing shared-fixture tests**

Each event includes bridge version, voice session ID, monotonic sequence, and monotonic timestamp. Recognition events include locale and text; errors contain only opaque ID/kind/message. Unknown version/discriminator must fail closed.

- [ ] **Step 2: Verify failures in all languages**

```bash
npm --prefix mobile test -- --run src/native/contract.test.ts
cargo test --manifest-path mobile/tauri-plugin-evener-native/Cargo.toml contract
xcodebuild test -scheme EvenerNativePlugin -destination 'platform=iOS Simulator,name=iPhone 16 Pro' -only-testing:EvenerNativePluginTests/ContractTests
```

Expected: FAIL on missing voice variants.

- [ ] **Step 3: Implement the schema in TypeScript, Rust, and Swift**

Sequence numbers are strictly increasing per voice session. Rust/TypeScript reject a late session ID before it reaches UI state.

- [ ] **Step 4: Run contract gates**

Expected: all three suites pass with the same fixture.

- [ ] **Step 5: Commit**

```bash
git add mobile/src/native/contract.ts mobile/src/native/contract.test.ts mobile/tauri-plugin-evener-native
git commit -m "feat(mobile): define duplex voice bridge"
```

---

### Task 2: Build Deterministic iOS Audio and Recognition State

**Files:**
- Create: Swift `VoiceSession.swift`, `SpeechRecognizing.swift`, `AudioSessionControlling.swift`, `VoiceActivityDetecting.swift`
- Test: `VoiceSessionTests.swift`, fakes

**Interfaces:**
- Produces: `VoiceSession.start(locale:)`, `stop()`, ordered callback stream; injected recognizer/audio/VAD protocols.

- [ ] **Step 1: Write failing permission/start tests**

Cover not-determined→authorized, denied, restricted, unavailable locale, audio-session failure, duplicate start, stop idempotence, and no event after stopped session.

- [ ] **Step 2: Write failing recognition tests**

Cover partials, final, monotonic sequence, input levels, recognizer restart after a final, interruption, route loss, background stop, and cleanup of taps/tasks/session category.

- [ ] **Step 3: Verify failure**

Run focused XCTest; expect missing types.

- [ ] **Step 4: Implement audio capture and recognition**

Configure `.playAndRecord`, `.voiceChat`, `.defaultToSpeaker`, `.allowBluetooth`, and supported Bluetooth A2DP option. Install one input tap; enable available voice processing; request `en-US`; never store audio or transcript to disk.

- [ ] **Step 5: Implement deterministic voice activity**

VAD reports activity from input levels with injected clock and threshold state; production threshold calibrates from a short ambient window. Tests drive explicit samples and time without sleeps.

- [ ] **Step 6: Run XCTest and leak cleanup assertions**

Expected: PASS and no live audio task/tap after every terminal path.

- [ ] **Step 7: Commit**

```bash
git add mobile/tauri-plugin-evener-native/ios/Sources mobile/tauri-plugin-evener-native/ios/Tests
git commit -m "feat(mobile): add native speech recognition"
```

---

### Task 3: Build Synthesis Queue and Native Barge-In

**Files:**
- Create: Swift `SpeechSynthesizing.swift`, `SynthesisQueue.swift`, `BargeInController.swift`
- Test: corresponding XCTest files/fakes

**Interfaces:**
- Produces: enqueue chunk with stable ID; stop/clear; speech started/finished events; barge-in event after VAD+partial.

- [ ] **Step 1: Write failing synthesis tests**

Assert FIFO chunks, rate application, stable IDs, start/finish ordering, stop clears queue, duplicate ID ignored, interruption clears, and late delegate callbacks ignored.

- [ ] **Step 2: Write failing barge-in timing tests**

With an injected monotonic clock, assert VAD alone does nothing, partial alone does nothing, combined post-synthesis signals stop once within 250 ms, clear before barge event, and pre-synthesis partial cannot trigger.

- [ ] **Step 3: Verify failure**

Run focused XCTest; expect missing types.

- [ ] **Step 4: Implement AVSpeechSynthesizer adapter and controller**

Never put raw spoken text into diagnostic events. Barge event may include current recognized partial because it is required UI state, but diagnostics omit it.

- [ ] **Step 5: Run XCTest**

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add mobile/tauri-plugin-evener-native/ios/Sources mobile/tauri-plugin-evener-native/ios/Tests
git commit -m "feat(mobile): add synthesis and barge-in"
```

---

### Task 4: Build the Pure Voice Coordinator

**Files:**
- Create: `mobile/src/voice/chunker.ts`, `chunker.test.ts`, `coordinator.ts`, `coordinator.test.ts`
- Create: `mobile/src/state/voice.ts`, test
- Modify: conversation service selectors

**Interfaces:**
- Produces: `VoiceCoordinator.start(ref)`, `end()`, `consumeConversation(delta)`, `handleNative(event)`; state selectors for captions/status/latest assistant/level.

- [ ] **Step 1: Write failing chunker tests**

Cover punctuation, abbreviations, code fences, Markdown links, lists, incomplete sentence hold, 180-character fallback boundary, whitespace, Unicode, and no raw tool/reasoning content. Assert deterministic chunk IDs by session/turn/item/range.

- [ ] **Step 2: Write failing coordinator tests**

Cover idle final→send, active final→steer, partial→no mutation, ask active→voice disabled, consumed range de-duplication, reconnect replay, session switch, only active assistant prose, 250 ms queue deadline, 750 ms idle-start report, native error fallback, and end cleanup.

- [ ] **Step 3: Write failing barge-in tests**

Assert native barge event stops UI speaking state, retains partial voice draft, waits for final, then steers once; stale session/sequence events do nothing.

- [ ] **Step 4: Verify failures**

Run: `npm --prefix mobile test -- --run src/voice src/state/voice.test.ts`

Expected: FAIL.

- [ ] **Step 5: Implement chunker/coordinator/store**

Use injected clock, native bridge, and conversation service. The coordinator stores consumed offsets only for the active process/voice session and clears on end.

- [ ] **Step 6: Run tests**

Expected: PASS without wall-clock sleeps.

- [ ] **Step 7: Commit**

```bash
git add mobile/src/voice mobile/src/state/voice.ts mobile/src/state/voice.test.ts mobile/src/services/conversation.ts
git commit -m "feat(mobile): coordinate streamed voice turns"
```

---

### Task 5: Build the Full-Screen Voice Experience

**Files:**
- Create: `mobile/src/screens/VoiceScreen.tsx`, test, CSS
- Create: `mobile/src/components/voice/VoiceLevel.tsx`, `VoiceCaptions.tsx`, `VoiceControls.tsx`, tests/CSS
- Modify: composer microphone action and navigation
- Add fixture data

**Interfaces:**
- Produces: voice screen with listening/speaking/interrupted/error states, level response, live captions, latest assistant sentence, end, and keyboard fallback.

- [ ] **Step 1: Write failing UI tests**

Assert permission rationale, connection title/status, all voice states, captions as text, latest assistant sentence, 44-pixel End/Keyboard controls, error system-settings action, ask-mode disabled reason, haptic calls, and focus return to composer.

- [ ] **Step 2: Write failing accessibility/motion tests**

Assert status live-region throttling, waveform is decorative, no transcript flood announcement, reduced-motion disables waveform/springs, Dynamic Type scales without clipping, and keyboard fallback ends native voice before navigation.

- [ ] **Step 3: Verify failure**

Run: `npm --prefix mobile test -- --run src/screens/VoiceScreen.test.tsx src/components/voice`

Expected: FAIL.

- [ ] **Step 4: Implement mobile-only voice UI**

Use semantic tokens and CSS transforms driven by bounded level state. Do not copy web composer/transcript surfaces. Keep compact session title and connection mark visible.

- [ ] **Step 5: Add fixture routes and geometry cases**

Fixture listening, speaking, barge-in, permission denied, disconnected, default/XXL/AXXXL type, reduced motion, both themes, portrait/landscape, and keyboard fallback.

- [ ] **Step 6: Run frontend gates**

```bash
npx --yes @biomejs/biome@2.5.5 check --write mobile/src
npm --prefix mobile run check
npm --prefix mobile test
npm --prefix mobile run boundary
npm --prefix mobile run build
npm --prefix mobile run test:geometry
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add mobile/src/screens/VoiceScreen.tsx mobile/src/screens/VoiceScreen.test.tsx mobile/src/screens/VoiceScreen.module.css mobile/src/components/voice mobile/src/components/composer mobile/src/navigation mobile/src/dev
git commit -m "feat(mobile): add focused duplex voice UI"
```

---

### Task 6: Verify iOS Configuration, Simulator, and Physical Device

**Files:**
- Modify generated iOS configuration as needed
- Create: `docs/verification/native-mobile-v1.md`
- Create redacted assets only under `docs/verification/native-mobile-v1-assets/`

**Interfaces:**
- Produces: auditable commit-bound verification record.

- [ ] **Step 1: Run final Info.plist test**

Assert iOS 17.0 and all four nonempty usage strings in the built app.

- [ ] **Step 2: Run Swift/Rust/frontend suites**

```bash
xcodebuild test -scheme EvenerNativePlugin -destination 'platform=iOS Simulator,name=iPhone 16 Pro'
cargo fmt --manifest-path mobile/src-tauri/Cargo.toml --check
cargo clippy --manifest-path mobile/src-tauri/Cargo.toml --all-targets -- -D warnings
cargo test --manifest-path mobile/src-tauri/Cargo.toml
npm --prefix mobile run check
npm --prefix mobile test
npm --prefix mobile run boundary
npm --prefix mobile run build
npm --prefix mobile run test:geometry
```

Expected: all exit zero.

- [ ] **Step 3: Build and smoke the simulator app**

```bash
cd mobile
npx tauri ios build --debug --target aarch64-sim
```

Install with `xcrun simctl`, exercise pairing error, fixtures, conversation, activity, and voice permission/error UI. Record simulator/runtime and screenshot paths.

- [ ] **Step 4: Run physical-device checklist when a device/signing identity is available**

On one iOS 17+ iPhone with `en-US`, record timestamps for sentence queue/start and VAD+partial/stop; require ≤750 ms and ≤250 ms respectively. Verify QR, local network, microphone/speech, captions, no duplicate chunks, built-in speaker, available wired/Bluetooth routes, call/route interruption, background/resume, keyboard fallback, rotation, Dynamic Type, and safe areas. Mark unavailable optional hardware unavailable, never passed.

- [ ] **Step 5: Run repository gates**

```bash
make lint
make vet
make test
make test-web-browser
git diff --check
```

Expected: every executed gate exits zero. Any unavailable physical-device step remains explicitly incomplete.

- [ ] **Step 6: Write the verification record**

Record verified commit, date, tool versions, commands/statuses, simulator, physical device/OS, checklist outcomes, and redacted asset links. Include no credentials, transcript text, filenames, or speech text.

- [ ] **Step 7: Commit evidence**

```bash
git add docs/verification/native-mobile-v1.md docs/verification/native-mobile-v1-assets
git commit -m "test(mobile): record native v1 verification"
```
