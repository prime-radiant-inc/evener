import XCTest
@testable import EvenerNativePlugin

// ---------------------------------------------------------------------------
// VoiceSessionTests
//
// Deterministic XCTest coverage driving VoiceSession through protocol fakes.
// No real audio, no real speech recognition, no sleeps. An injected monotonic
// clock advances explicitly; fakes record every call so cleanup and event
// ordering can be asserted precisely.
// ---------------------------------------------------------------------------

// MARK: - Fakes

private final class FakeAudioSession: AudioSessionControlling {
    enum Error: Swift.Error { case configureFailed, tapFailed }

    var configureError: Swift.Error?
    var tapError: Swift.Error?
    var deactivateError: Swift.Error?

    private(set) var configureCount = 0
    private(set) var deactivateCount = 0
    private(set) var installTapCount = 0
    private(set) var removeTapCount = 0
    private(set) var tapInstalled = false

    // Captured closure so tests can drive levels into the session.
    private(set) var levelsHandler: (([Float]) -> Void)?

    func configureForVoiceChat() throws {
        configureCount += 1
        if let configureError { throw configureError }
    }

    func deactivate() throws {
        deactivateCount += 1
        if let deactivateError { throw deactivateError }
    }

    func installTap(onLevels: @escaping ([Float]) -> Void) throws {
        installTapCount += 1
        if let tapError { throw tapError }
        tapInstalled = true
        levelsHandler = onLevels
    }

    func removeTap() {
        removeTapCount += 1
        tapInstalled = false
        levelsHandler = nil
    }

    /// Drive levels into the installed tap handler, as a real engine would.
    func deliverLevels(_ levels: [Float]) {
        levelsHandler?(levels)
    }
}

private final class FakeRecognizer: SpeechRecognizing {
    enum Error: Swift.Error { case startFailed }

    var authorizationState: SpeechAuthorizationState
    var authorizationAfterRequest: SpeechAuthorizationState
    var isAvailable: Bool
    var startError: Swift.Error?

    private(set) var requestAuthCount = 0
    private(set) var startCount = 0
    private(set) var stopCount = 0

    // Captured callbacks so tests can deliver partials/finals.
    private(set) var partialHandler: ((String) -> Void)?
    private(set) var finalHandler: ((String) -> Void)?
    private(set) var lastLocale: String?

    init(
        authorizationState: SpeechAuthorizationState = .notDetermined,
        authorizationAfterRequest: SpeechAuthorizationState = .authorized,
        isAvailable: Bool = true
    ) {
        self.authorizationState = authorizationState
        self.authorizationAfterRequest = authorizationAfterRequest
        self.isAvailable = isAvailable
    }

    func requestAuthorization() async -> SpeechAuthorizationState {
        requestAuthCount += 1
        authorizationState = authorizationAfterRequest
        return authorizationAfterRequest
    }

    func startRecognition(
        locale: String,
        onPartial: @escaping (String) -> Void,
        onFinal: @escaping (String) -> Void
    ) throws -> SpeechRecognitionTask {
        startCount += 1
        lastLocale = locale
        if let startError { throw startError }
        partialHandler = onPartial
        finalHandler = onFinal
        return FakeRecognitionTask()
    }

    func stopRecognition() {
        stopCount += 1
        partialHandler = nil
        finalHandler = nil
    }

    /// Deliver a partial as a real recognizer would.
    func deliverPartial(_ text: String) {
        partialHandler?(text)
    }

    /// Deliver a final as a real recognizer would.
    func deliverFinal(_ text: String) {
        finalHandler?(text)
    }
}

private final class FakeRecognitionTask: SpeechRecognitionTask {
    private(set) var cancelCount = 0
    func cancel() { cancelCount += 1 }
}

private final class FakeVAD: VoiceActivityDetecting {
    private(set) var resetCount = 0
    private(set) var processed: [(level: Float, timestampMs: Int64)] = []

    /// Force the activity result returned for the next sample.
    var forcedActive: Bool = false

    func processSample(level: Float, timestamp: MonotonicTimestamp) -> VoiceActivityResult {
        processed.append((level, timestamp.milliseconds))
        return VoiceActivityResult(isActive: forcedActive, level: level)
    }

    func reset() {
        resetCount += 1
    }
}

/// A monotonic clock tests advance explicitly; no wall time, no sleeps.
private final class SteppableClock: MonotonicClock {
    private var ms: Int64 = 0
    private let step: Int64

    init(start: Int64 = 0, step: Int64 = 100) {
        self.ms = start
        self.step = step
    }

    func now() -> MonotonicTimestamp {
        let t = MonotonicTimestamp(milliseconds: ms)
        ms += step
        return t
    }

    func advance(by deltaMs: Int64) {
        ms += deltaMs
    }
}

/// ID generator that produces a fixed sequence so tests can assert session IDs.
private final class FixedIDGenerator: VoiceSessionIDGenerator {
    private var next = 0
    func nextID() -> VoiceSessionID {
        next += 1
        return VoiceSessionID(value: next)
    }
}

// MARK: - Recorded event for assertions

private struct RecordedEvent {
    let sessionID: VoiceSessionID
    let seq: Int
    let timestampMs: Int64
    let event: VoiceEvent
}

/// Collects events into an array for deterministic assertion.
private final class EventRecorder {
    private(set) var events: [RecordedEvent] = []

    func handler() -> (VoiceSessionID, Int, MonotonicTimestamp, VoiceEvent) -> Void {
        return { [weak self] id, seq, ts, event in
            self?.events.append(RecordedEvent(
                sessionID: id, seq: seq, timestampMs: ts.milliseconds, event: event
            ))
        }
    }
}

// MARK: - Tests

final class VoiceSessionTests: XCTestCase {

    // Helper: build a session wired to fakes with default-authorized recognizer.
    private func makeSession(
        audio: FakeAudioSession = FakeAudioSession(),
        recognizer: FakeRecognizer = FakeRecognizer(),
        vad: FakeVAD = FakeVAD(),
        clock: SteppableClock = SteppableClock()
    ) -> (VoiceSession, FakeAudioSession, FakeRecognizer, FakeVAD, SteppableClock) {
        let session = VoiceSession(
            audioSession: audio,
            recognizer: recognizer,
            vad: vad,
            clock: clock,
            idGenerator: FixedIDGenerator()
        )
        return (session, audio, recognizer, vad, clock)
    }

    // 1. Permission states: not-determined→authorized, denied, restricted,
    //    unavailable locale.
    func testNotDeterminedToAuthorizedStartsSuccessfully() async throws {
        let (session, audio, recognizer, _, _) = makeSession()
        let recorder = EventRecorder()
        session.onEvent = recorder.handler()

        try await session.start(locale: "en-US")

        XCTAssertEqual(recognizer.requestAuthCount, 1)
        XCTAssertEqual(audio.configureCount, 1)
        XCTAssertEqual(audio.installTapCount, 1)
        XCTAssertEqual(recognizer.startCount, 1)
        XCTAssertEqual(recognizer.lastLocale, "en-US")
        XCTAssertNil(session.lastTerminal)
        try await session.stop()
    }

    func testDeniedPermissionFailsClosedWithoutAudioOrTap() async throws {
        let recognizer = FakeRecognizer(
            authorizationState: .denied,
            authorizationAfterRequest: .denied
        )
        let (session, audio, _, vad, _) = makeSession(recognizer: recognizer)
        let recorder = EventRecorder()
        session.onEvent = recorder.handler()

        await XCTAssertThrowsErrorAsync(try await session.start(locale: "en-US")) { error in
            XCTAssertEqual(error as? VoiceSessionStartError, .permissionDenied)
        }

        // No audio session configuration, no tap, no recognition start.
        XCTAssertEqual(audio.configureCount, 0)
        XCTAssertEqual(audio.installTapCount, 0)
        XCTAssertEqual(recognizer.startCount, 0)
        // VAD was reset during cleanup.
        XCTAssertGreaterThan(vad.resetCount, 0)
        XCTAssertEqual(session.lastTerminal, .permissionDenied)
        // No events emitted on a denied start.
        XCTAssertTrue(recorder.events.isEmpty)
    }

    func testRestrictedPermissionFailsClosed() async throws {
        let recognizer = FakeRecognizer(
            authorizationState: .restricted,
            authorizationAfterRequest: .restricted
        )
        let (session, audio, _, _, _) = makeSession(recognizer: recognizer)

        await XCTAssertThrowsErrorAsync(try await session.start(locale: "en-US")) { error in
            XCTAssertEqual(error as? VoiceSessionStartError, .permissionRestricted)
        }
        XCTAssertEqual(audio.configureCount, 0)
        XCTAssertEqual(audio.installTapCount, 0)
        XCTAssertEqual(session.lastTerminal, .permissionRestricted)
    }

    func testUnavailableLocaleFailsClosedAfterAuthorization() async throws {
        let recognizer = FakeRecognizer(
            authorizationState: .notDetermined,
            authorizationAfterRequest: .authorized,
            isAvailable: false
        )
        let (session, audio, _, _, _) = makeSession(recognizer: recognizer)

        await XCTAssertThrowsErrorAsync(try await session.start(locale: "en-US")) { error in
            XCTAssertEqual(error as? VoiceSessionStartError, .recognizerUnavailable)
        }
        // Authorized but unavailable: no audio session or tap.
        XCTAssertEqual(audio.configureCount, 0)
        XCTAssertEqual(audio.installTapCount, 0)
        XCTAssertEqual(recognizer.startCount, 0)
        XCTAssertEqual(session.lastTerminal, .recognizerUnavailable)
    }

    // 2. Audio-session failure (fake throws).
    func testAudioSessionConfigureFailureFailsClosedWithErrorEvent() async throws {
        struct Boom: Error {}
        let audio = FakeAudioSession()
        audio.configureError = Boom()
        let (session, _, recognizer, _, _) = makeSession(audio: audio)
        let recorder = EventRecorder()
        session.onEvent = recorder.handler()

        await XCTAssertThrowsErrorAsync(try await session.start(locale: "en-US"))

        XCTAssertEqual(audio.configureCount, 1)
        XCTAssertEqual(audio.installTapCount, 0)
        XCTAssertEqual(recognizer.startCount, 0)
        XCTAssertEqual(session.lastTerminal, .failed)
        // One structured error event, no transcript.
        XCTAssertEqual(recorder.events.count, 1)
        guard case .voiceError(let err) = recorder.events[0].event else {
            XCTFail("expected voiceError event"); return
        }
        XCTAssertFalse(err.message.contains("transcript"))
        XCTAssertFalse(err.id.isEmpty)
    }

    func testTapInstallFailureFailsClosedWithCleanup() async throws {
        struct Boom: Error {}
        let audio = FakeAudioSession()
        audio.tapError = Boom()
        let (session, _, recognizer, _, _) = makeSession(audio: audio)
        let recorder = EventRecorder()
        session.onEvent = recorder.handler()

        await XCTAssertThrowsErrorAsync(try await session.start(locale: "en-US"))

        // Configure succeeded, tap failed, recognition never started.
        XCTAssertEqual(audio.configureCount, 1)
        XCTAssertEqual(audio.installTapCount, 1)
        XCTAssertEqual(recognizer.startCount, 0)
        // Cleanup deactivated the configured session.
        XCTAssertEqual(audio.deactivateCount, 1)
        XCTAssertEqual(session.lastTerminal, .failed)
        XCTAssertEqual(recorder.events.count, 1)
    }

    // 3. Duplicate start (second start while running fails).
    func testDuplicateStartFailsWhileRunning() async throws {
        let (session, _, _, _, _) = makeSession()
        try await session.start(locale: "en-US")

        await XCTAssertThrowsErrorAsync(try await session.start(locale: "en-US")) { error in
            XCTAssertEqual(error as? VoiceSessionStartError, .alreadyRunning)
        }
        try await session.stop()
    }

    // 4. Stop idempotence (stop twice, no crash).
    func testStopIsIdempotent() async throws {
        let (session, audio, recognizer, _, _) = makeSession()
        try await session.start(locale: "en-US")

        session.stop()
        let firstDeactivate = audio.deactivateCount
        let firstRemoveTap = audio.removeTapCount
        let firstStop = recognizer.stopCount

        // Second stop is a no-op: no extra deactivate/remove/stop calls.
        session.stop()
        XCTAssertEqual(audio.deactivateCount, firstDeactivate)
        XCTAssertEqual(audio.removeTapCount, firstRemoveTap)
        XCTAssertEqual(recognizer.stopCount, firstStop)
        XCTAssertEqual(session.lastTerminal, .stopped)
    }

    // 5. No event after stopped session.
    func testNoEventAfterStoppedSession() async throws {
        let (session, audio, recognizer, _, _) = makeSession()
        let recorder = EventRecorder()
        session.onEvent = recorder.handler()
        try await session.start(locale: "en-US")

        session.stop()
        let countAfterStop = recorder.events.count

        // Driving levels, partials, and finals after stop emits nothing.
        audio.deliverLevels([0.5])
        recognizer.deliverPartial("late")
        recognizer.deliverFinal("late final")

        XCTAssertEqual(recorder.events.count, countAfterStop)
    }

    // 6. Partials and final recognition.
    func testPartialsAndFinalRecognitionAreEmitted() async throws {
        let (session, _, recognizer, _, _) = makeSession()
        let recorder = EventRecorder()
        session.onEvent = recorder.handler()
        try await session.start(locale: "en-US")

        recognizer.deliverPartial("hello")
        recognizer.deliverPartial("hello world")
        recognizer.deliverFinal("hello world")

        let kinds = recorder.events.map { $0.event.name }
        XCTAssertEqual(kinds, ["voice.partial", "voice.partial", "voice.final"])
        if case .voicePartial(let text) = recorder.events[0].event {
            XCTAssertEqual(text, "hello")
        } else { XCTFail() }
        if case .voiceFinal(let text) = recorder.events[2].event {
            XCTAssertEqual(text, "hello world")
        } else { XCTFail() }
        try await session.stop()
    }

    // 7. Monotonic sequence numbers.
    func testSequenceNumbersAreStrictlyIncreasingPerSession() async throws {
        let (session, audio, recognizer, _, _) = makeSession()
        let recorder = EventRecorder()
        session.onEvent = recorder.handler()
        try await session.start(locale: "en-US")

        recognizer.deliverPartial("a")
        audio.deliverLevels([0.1])
        recognizer.deliverFinal("a b")
        audio.deliverLevels([0.2, 0.3])

        let seqs = recorder.events.map { $0.seq }
        XCTAssertEqual(seqs, Array(1...5))
        // All events share the same session ID.
        let ids = Set(recorder.events.map { $0.sessionID.value })
        XCTAssertEqual(ids, [1])
        try await session.stop()
    }

    // 8. Input levels from tap.
    func testInputLevelsFromTapAreEmitted() async throws {
        let (session, audio, _, _, _) = makeSession()
        let recorder = EventRecorder()
        session.onEvent = recorder.handler()
        try await session.start(locale: "en-US")

        audio.deliverLevels([0.25, 0.75])
        audio.deliverLevels([0.5])

        let levelEvents = recorder.events.compactMap { event -> Float? in
            if case .voiceLevel(let level) = event.event { return level } else { return nil }
        }
        XCTAssertEqual(levelEvents, [0.25, 0.75, 0.5])
        try await session.stop()
    }

    // 9. Recognizer restart after a final.
    func testRecognizerRestartsAfterAFinal() async throws {
        let (session, _, recognizer, _, _) = makeSession()
        let recorder = EventRecorder()
        session.onEvent = recorder.handler()
        try await session.start(locale: "en-US")

        let startsBefore = recognizer.startCount
        recognizer.deliverFinal("first utterance")
        // A final triggers a restart: one stop then one new start.
        XCTAssertEqual(recognizer.startCount, startsBefore + 1)
        XCTAssertGreaterThan(recognizer.stopCount, 0)
        // The final event itself is emitted before the restart.
        XCTAssertTrue(recorder.events.contains { event in
            if case .voiceFinal("first utterance") = event.event { return true }
            return false
        })
        try await session.stop()
    }

    func testRestartFailureAfterFinalStopsCleanly() async throws {
        let recognizer = FakeRecognizer()
        let (session, audio, _, _, _) = makeSession(recognizer: recognizer)
        let recorder = EventRecorder()
        session.onEvent = recorder.handler()
        try await session.start(locale: "en-US")

        // First final triggers restart; make the next start throw.
        recognizer.startError = FakeRecognizer.Error.startFailed
        recognizer.deliverFinal("only utterance")

        XCTAssertEqual(session.lastTerminal, .failed)
        XCTAssertEqual(audio.deactivateCount, 1)
        XCTAssertEqual(audio.removeTapCount, 1)
        XCTAssertGreaterThan(recognizer.stopCount, 0)
        // A structured error event was emitted for the restart failure.
        XCTAssertTrue(recorder.events.contains { event in
            if case .voiceError = event.event { return true }
            return false
        })
    }

    // 10. Interruption handling.
    func testInterruptionEmitsInterruptedAndStopsCleanly() async throws {
        let (session, audio, recognizer, _, _) = makeSession()
        let recorder = EventRecorder()
        session.onEvent = recorder.handler()
        try await session.start(locale: "en-US")

        session.handleInterruption()

        XCTAssertEqual(session.lastTerminal, .interrupted)
        XCTAssertEqual(audio.removeTapCount, 1)
        XCTAssertEqual(audio.deactivateCount, 1)
        XCTAssertGreaterThan(recognizer.stopCount, 0)
        XCTAssertTrue(recorder.events.contains { event in
            if case .voiceInterrupted = event.event { return true }
            return false
        })
        // Idempotent: a second interruption does nothing.
        let countBefore = recorder.events.count
        session.handleInterruption()
        XCTAssertEqual(recorder.events.count, countBefore)
    }

    // 11. Route loss handling.
    func testRouteLossEmitsErrorAndStopsCleanly() async throws {
        let (session, audio, recognizer, _, _) = makeSession()
        let recorder = EventRecorder()
        session.onEvent = recorder.handler()
        try await session.start(locale: "en-US")

        session.handleRouteLoss()

        XCTAssertEqual(session.lastTerminal, .routeLost)
        XCTAssertEqual(audio.removeTapCount, 1)
        XCTAssertEqual(audio.deactivateCount, 1)
        XCTAssertGreaterThan(recognizer.stopCount, 0)
        let errorEvents = recorder.events.filter {
            if case .voiceError = $0.event { return true } else { return false }
        }
        XCTAssertEqual(errorEvents.count, 1)
        if case .voiceError(let err) = errorEvents[0].event {
            XCTAssertEqual(err.kind, "route_loss")
            XCTAssertFalse(err.message.contains("transcript"))
        } else { XCTFail() }
    }

    // 12. Background stop.
    func testBackgroundStopCleansUpWithoutEvents() async throws {
        let (session, audio, recognizer, _, _) = makeSession()
        let recorder = EventRecorder()
        session.onEvent = recorder.handler()
        try await session.start(locale: "en-US")

        let eventsBefore = recorder.events.count
        session.handleBackground()

        XCTAssertEqual(session.lastTerminal, .backgrounded)
        XCTAssertEqual(audio.removeTapCount, 1)
        XCTAssertEqual(audio.deactivateCount, 1)
        XCTAssertGreaterThan(recognizer.stopCount, 0)
        // Background stop emits no new events (foreground-only, silent stop).
        XCTAssertEqual(recorder.events.count, eventsBefore)
    }

    // 13. Cleanup of taps/tasks/session category on every terminal path.
    func testCleanupOnEveryTerminalPath() async throws {
        // Drive each terminal and assert tap removed, task stopped, session
        // deactivated exactly once. Reuse a fresh session per terminal.
        let terminals: [(name: String, apply: (VoiceSession) -> Void)] = [
            ("stopped",       { $0.stop() }),
            ("interrupted",   { $0.handleInterruption() }),
            ("routeLost",     { $0.handleRouteLoss() }),
            ("backgrounded",  { $0.handleBackground() }),
        ]
        for terminal in terminals {
            let (session, audio, recognizer, _, _) = makeSession()
            try await session.start(locale: "en-US")
            // Reset counts so we can assert exactly one cleanup per terminal.
            _ = audio.removeTapCount
            _ = audio.deactivateCount
            _ = recognizer.stopCount

            terminal.apply(session)

            XCTAssertEqual(audio.removeTapCount, 1, "removeTap for \(terminal.name)")
            XCTAssertEqual(audio.deactivateCount, 1, "deactivate for \(terminal.name)")
            XCTAssertGreaterThanOrEqual(recognizer.stopCount, 1, "stop for \(terminal.name)")
        }
    }

    func testFailedStartLeavesNoLiveTapOrTask() async throws {
        struct Boom: Error {}
        let recognizer = FakeRecognizer()
        recognizer.startError = Boom()
        let (session, audio, _, _, _) = makeSession(recognizer: recognizer)
        let recorder = EventRecorder()
        session.onEvent = recorder.handler()

        await XCTAssertThrowsErrorAsync(try await session.start(locale: "en-US"))

        // Configure + tap succeeded, recognition start failed: cleanup must
        // remove the tap and deactivate the session.
        XCTAssertEqual(audio.installTapCount, 1)
        XCTAssertEqual(audio.removeTapCount, 1)
        XCTAssertEqual(audio.deactivateCount, 1)
        XCTAssertEqual(session.lastTerminal, .failed)
    }

    // 14. VAD: activity from input levels with injected clock and threshold;
    //     tests drive explicit samples and time without sleeps.
    func testVADActivityFromInjectedSamplesAndClock() {
        let clock = SteppableClock(start: 0, step: 0)  // explicit timestamps
        let vad = ThresholdVoiceActivityDetector(
            clock: clock,
            activationThreshold: 0.3,
            deactivationThreshold: 0.15,
            holdMs: 300
        )

        // Below threshold: inactive.
        XCTAssertFalse(
            vad.processSample(level: 0.1, timestamp: MonotonicTimestamp(milliseconds: 0)).isActive
        )
        // Cross activation threshold: becomes active.
        XCTAssertTrue(
            vad.processSample(level: 0.4, timestamp: MonotonicTimestamp(milliseconds: 100)).isActive
        )
        // Below deactivation but within hold: still active.
        XCTAssertTrue(
            vad.processSample(level: 0.05, timestamp: MonotonicTimestamp(milliseconds: 200)).isActive
        )
        // Beyond hold with sub-threshold: inactive.
        XCTAssertFalse(
            vad.processSample(level: 0.05, timestamp: MonotonicTimestamp(milliseconds: 600)).isActive
        )
    }

    func testVADHysteresisKeepsActiveAcrossBriefDips() {
        let clock = SteppableClock(start: 0, step: 0)
        let vad = ThresholdVoiceActivityDetector(
            clock: clock,
            activationThreshold: 0.3,
            holdMs: 200
        )
        XCTAssertTrue(vad.processSample(level: 0.5, timestamp: MonotonicTimestamp(milliseconds: 0)).isActive)
        // Sub-threshold sample within hold remains active (hysteresis).
        XCTAssertTrue(vad.processSample(level: 0.1, timestamp: MonotonicTimestamp(milliseconds: 50)).isActive)
        XCTAssertTrue(vad.processSample(level: 0.4, timestamp: MonotonicTimestamp(milliseconds: 100)).isActive)
        XCTAssertFalse(vad.processSample(level: 0.1, timestamp: MonotonicTimestamp(milliseconds: 400)).isActive)
    }

    func testVADResetClearsActivity() {
        let clock = SteppableClock(start: 0, step: 0)
        let vad = ThresholdVoiceActivityDetector(
            clock: clock,
            activationThreshold: 0.3,
            holdMs: 300
        )
        XCTAssertTrue(vad.processSample(level: 0.5, timestamp: MonotonicTimestamp(milliseconds: 0)).isActive)
        vad.reset()
        XCTAssertFalse(vad.processSample(level: 0.1, timestamp: MonotonicTimestamp(milliseconds: 10)).isActive)
    }

    // 15. VAD: production threshold calibrates from a short ambient window.
    func testVADCalibrationFromAmbientWindowBoundedAboveNoiseFloor() {
        // Quiet ambient: peak 0.01. Threshold must be at least the floor.
        let quiet = VADCalibrator.calibrate(levels: [0.005, 0.01, 0.008])
        XCTAssertGreaterThanOrEqual(quiet.activationThreshold, VADCalibrator.minActivationThreshold)
        XCTAssertLessThanOrEqual(quiet.activationThreshold, VADCalibrator.maxActivationThreshold)
        XCTAssertEqual(quiet.deactivationThreshold, quiet.activationThreshold * 0.5, accuracy: 0.0001)

        // Loud ambient: peak 0.8. Threshold must be capped at the ceiling and
        // never exceed it even when ambient is loud.
        let loud = VADCalibrator.calibrate(levels: [0.6, 0.8, 0.7])
        XCTAssertLessThanOrEqual(loud.activationThreshold, VADCalibrator.maxActivationThreshold)
        XCTAssertGreaterThanOrEqual(loud.activationThreshold, VADCalibrator.minActivationThreshold)

        // Empty window: fall back to the floor.
        let empty = VADCalibrator.calibrate(levels: [])
        XCTAssertEqual(empty.activationThreshold, VADCalibrator.minActivationThreshold, accuracy: 0.0001)
    }

    func testCalibratedVADActivatesAboveCalibratedThreshold() {
        // Calibrate from a modest ambient window, then build a detector and
        // confirm a sample above the threshold activates.
        let calibration = VADCalibrator.calibrate(levels: [0.05, 0.08, 0.06])
        let clock = SteppableClock(start: 0, step: 0)
        let vad = ThresholdVoiceActivityDetector(
            clock: clock,
            activationThreshold: calibration.activationThreshold,
            deactivationThreshold: calibration.deactivationThreshold,
            holdMs: 300
        )
        // A sample well above the calibrated threshold activates.
        let activatingLevel: Float = calibration.activationThreshold * 2
        XCTAssertTrue(vad.processSample(
            level: activatingLevel,
            timestamp: MonotonicTimestamp(milliseconds: 0)
        ).isActive)
    }
}

// MARK: - Async throw assertion helper

private func XCTAssertThrowsErrorAsync<T>(
    _ expression: @autoclosure () async throws -> T,
    _ verify: (Error) -> Void = { _ in }
) async {
    do {
        _ = try await expression()
        XCTFail("Expected error, got success")
    } catch {
        verify(error)
    }
}
