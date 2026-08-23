import Foundation

// ---------------------------------------------------------------------------
// VoiceSession
//
// Coordinates the audio session, speech recognizer, and voice activity detector
// behind injectable protocols, emitting an ordered stream of voice events with
// a monotonic voice session ID, monotonic sequence numbers, and monotonic
// timestamps from an injectable clock.
//
// Guarantees:
// - One live session at a time; a second `start` while running fails closed.
// - `stop()` is idempotent and removes taps, cancels recognition tasks, and
//   deactivates the audio session category on every terminal path.
// - No events are emitted after a stopped session.
// - Audio and transcript are never stored to disk; recognition text appears
//   only in transient event callbacks.
// - Errors are structured (opaque ID/kind/message) and never carry transcript.
// ---------------------------------------------------------------------------

/// Structured voice error emitted via `voiceError`. Carries only an opaque ID,
/// a stable kind, and a redacted message — never transcript or audio.
struct VoiceErrorEvent: Equatable {
    let id: String
    let kind: String
    let message: String
}

/// Ordered voice event delivered to the `VoiceSession` callback stream. Each
/// carries the voice session ID, a strictly-increasing per-session sequence
/// number, and a monotonic timestamp.
enum VoiceEvent: Equatable {
    case voiceLevel(level: Float)
    case voicePartial(text: String)
    case voiceFinal(text: String)
    case voiceError(error: VoiceErrorEvent)
    case voiceInterrupted

    var name: String {
        switch self {
        case .voiceLevel: return "voice.level"
        case .voicePartial: return "voice.partial"
        case .voiceFinal: return "voice.final"
        case .voiceError: return "voice.error"
        case .voiceInterrupted: return "voice.interrupted"
        }
    }
}

/// The kind of terminal that ended a voice session, for test assertions.
enum VoiceSessionTerminal {
    case stopped
    case interrupted
    case routeLost
    case backgrounded
    case failed
    case permissionDenied
    case permissionRestricted
    case recognizerUnavailable
}

/// Monotonic voice session identity. Sequence numbers and timestamps are
/// scoped to a single session and reset on the next `start`.
struct VoiceSessionID: Equatable, Hashable {
    let value: Int
}

/// Injectable source of fresh, strictly-increasing voice session IDs.
protocol VoiceSessionIDGenerator {
    func nextID() -> VoiceSessionID
}

/// Default generator: an atomic incrementing counter shared across sessions.
final class DefaultVoiceSessionIDGenerator: VoiceSessionIDGenerator {
    private let lock = NSLock()
    private var counter = 0

    func nextID() -> VoiceSessionID {
        lock.lock()
        counter += 1
        let id = VoiceSessionID(value: counter)
        lock.unlock()
        return id
    }
}

/// Reasons `start` can fail before any audio is touched.
enum VoiceSessionStartError: Error, Equatable {
    case alreadyRunning
    case permissionDenied
    case permissionRestricted
    case recognizerUnavailable
}

/// `VoiceSession` coordinates audio capture, speech recognition, and voice
/// activity detection. All collaborators are injected; production wires the
/// real `AVAudioSession`/`SFSpeechRecognizer` boundaries, tests inject fakes.
final class VoiceSession {
    private let audioSession: AudioSessionControlling
    private let recognizer: SpeechRecognizing
    private let vad: VoiceActivityDetecting
    private let clock: MonotonicClock
    private let idGenerator: VoiceSessionIDGenerator

    private let lock = NSLock()
    private var state = State()

    /// Terminal events observed for test/audit assertions. Not emitted after
    /// a stopped session; cleared on the next `start`.
    private(set) var lastTerminal: VoiceSessionTerminal?

    private struct State {
        var running: Bool = false
        var sessionID: VoiceSessionID?
        var sequence: Int = 0
        var recognitionTask: SpeechRecognitionTask?
        var tapInstalled: Bool = false
        var sessionConfigured: Bool = false
    }

    /// Callback invoked for every ordered voice event while running.
    var onEvent: ((VoiceSessionID, Int, MonotonicTimestamp, VoiceEvent) -> Void)?

    init(
        audioSession: AudioSessionControlling,
        recognizer: SpeechRecognizing,
        vad: VoiceActivityDetecting,
        clock: MonotonicClock,
        idGenerator: VoiceSessionIDGenerator = DefaultVoiceSessionIDGenerator()
    ) {
        self.audioSession = audioSession
        self.recognizer = recognizer
        self.vad = vad
        self.clock = clock
        self.idGenerator = idGenerator
    }

    // MARK: - Lifecycle

    /// Begin a voice session for `locale`. Throws `VoiceSessionStartError` on
    /// permission/availability/duplicate-start failures; never leaves a live
    /// audio task or tap on a failed start. On success, emits events to
    /// `onEvent` with strictly increasing per-session sequence numbers.
    func start(locale: String) async throws {
        lock.lock()
        if state.running {
            lock.unlock()
            throw VoiceSessionStartError.alreadyRunning
        }
        // Reserve the ID up front so cleanup knows which session it owns.
        let sessionID = idGenerator.nextID()
        state.sessionID = sessionID
        state.sequence = 0
        lock.unlock()

        // 1. Authorization: request, then observe the resulting state.
        let auth = await recognizer.requestAuthorization()
        switch auth {
        case .authorized:
            break
        case .denied:
            failStart(sessionID, .permissionDenied)
            throw VoiceSessionStartError.permissionDenied
        case .restricted:
            failStart(sessionID, .permissionRestricted)
            throw VoiceSessionStartError.permissionRestricted
        case .notDetermined:
            failStart(sessionID, .permissionDenied)
            throw VoiceSessionStartError.permissionDenied
        }

        // 2. Availability for the requested locale.
        guard recognizer.isAvailable else {
            failStart(sessionID, .recognizerUnavailable)
            throw VoiceSessionStartError.recognizerUnavailable
        }

        // 3. Audio session configuration.
        do {
            try audioSession.configureForVoiceChat()
            lock.lock()
            state.sessionConfigured = true
            lock.unlock()
        } catch {
            emit(sessionID, .voiceError(error: Self.audioSessionError(underlying: error)))
            failStart(sessionID, .failed)
            throw error
        }

        // 4. Install the input tap and feed levels to VAD + events.
        do {
            try audioSession.installTap { [weak self] levels in
                self?.handleLevels(levels)
            }
            lock.lock()
            state.tapInstalled = true
            lock.unlock()
        } catch {
            emit(sessionID, .voiceError(error: Self.audioSessionError(underlying: error)))
            cleanup(sessionID, terminal: .failed)
            throw error
        }

        // 5. Start recognition.
        let task: SpeechRecognitionTask
        do {
            task = try recognizer.startRecognition(
                locale: locale,
                onPartial: { [weak self] text in self?.handlePartial(text) },
                onFinal: { [weak self] text in self?.handleFinal(text) }
            )
        } catch {
            emit(sessionID, .voiceError(error: Self.recognitionError(underlying: error)))
            cleanup(sessionID, terminal: .failed)
            throw error
        }

        lock.lock()
        state.running = true
        state.recognitionTask = task
        lock.unlock()
        vad.reset()
    }

    /// Stop the current voice session. Idempotent: a second stop while idle is
    /// a no-op. Removes the tap, cancels the recognition task, and deactivates
    /// the audio session category. No events are emitted after stop.
    func stop() {
        lock.lock()
        let running = state.running
        let sessionID = state.sessionID
        lock.unlock()
        guard running, let sessionID else { return }
        cleanup(sessionID, terminal: .stopped)
    }

    // MARK: - Interruption / route / background

    /// Audio interruption (e.g. incoming call). Emits `voiceInterrupted`,
    /// stops cleanly, and performs full cleanup. Idempotent.
    func handleInterruption() {
        lock.lock()
        let running = state.running
        let sessionID = state.sessionID
        lock.unlock()
        guard running, let sessionID else { return }
        emit(sessionID, .voiceInterrupted)
        cleanup(sessionID, terminal: .interrupted)
    }

    /// Audio route loss. Fails closed: emits a structured error and stops.
    func handleRouteLoss() {
        lock.lock()
        let running = state.running
        let sessionID = state.sessionID
        lock.unlock()
        guard running, let sessionID else { return }
        emit(sessionID, .voiceError(error: Self.routeLossError))
        cleanup(sessionID, terminal: .routeLost)
    }

    /// App backgrounded. Voice is foreground-only; stop cleanly with cleanup.
    func handleBackground() {
        lock.lock()
        let running = state.running
        let sessionID = state.sessionID
        lock.unlock()
        guard running, let sessionID else { return }
        cleanup(sessionID, terminal: .backgrounded)
    }

    // MARK: - Internals

    private func handleLevels(_ levels: [Float]) {
        lock.lock()
        guard state.running, let sessionID = state.sessionID else {
            lock.unlock()
            return
        }
        lock.unlock()

        for level in levels {
            let timestamp = clock.now()
            let result = vad.processSample(level: level, timestamp: timestamp)
            // Emit the input level for every sample; VAD activity is consumed
            // by the synthesis/barge-in controller (Task 3), not here.
            emit(sessionID, .voiceLevel(level: result.level))
        }
    }

    private func handlePartial(_ text: String) {
        lock.lock()
        guard state.running, let sessionID = state.sessionID else {
            lock.unlock()
            return
        }
        lock.unlock()
        emit(sessionID, .voicePartial(text: text))
    }

    private func handleFinal(_ text: String) {
        lock.lock()
        guard state.running, let sessionID = state.sessionID else {
            lock.unlock()
            return
        }
        let shouldRestart = state.running
        lock.unlock()

        emit(sessionID, .voiceFinal(text: text))

        // Apple speech recognition delivers one final per task; restart so the
        // next utterance is recognized without manual intervention.
        guard shouldRestart else { return }
        restartRecognition(sessionID: sessionID)
    }

    /// Restart recognition after a final result. Cleans up the prior task on
    /// failure and emits a structured error without leaking the tap/session.
    private func restartRecognition(sessionID: VoiceSessionID) {
        lock.lock()
        let running = state.running
        lock.unlock()
        guard running else { return }

        // Stop the prior task before starting a new one.
        recognizer.stopRecognition()
        lock.lock()
        state.recognitionTask = nil
        lock.unlock()

        do {
            let task = try recognizer.startRecognition(
                locale: Self.recognitionLocale,
                onPartial: { [weak self] text in self?.handlePartial(text) },
                onFinal: { [weak self] text in self?.handleFinal(text) }
            )
            lock.lock()
            state.recognitionTask = task
            lock.unlock()
        } catch {
            emit(sessionID, .voiceError(error: Self.recognitionError(underlying: error)))
            cleanup(sessionID, terminal: .failed)
        }
    }

    /// Tear down a failed `start` before the session was marked running.
    private func failStart(_ sessionID: VoiceSessionID, _ terminal: VoiceSessionTerminal) {
        cleanup(sessionID, terminal: terminal)
    }

    /// Idempotent full cleanup for any terminal path. Cancels the recognition
    /// task, removes the tap, deactivates the audio session category, and
    /// clears running state. Safe to call when nothing is installed.
    private func cleanup(_ sessionID: VoiceSessionID, terminal: VoiceSessionTerminal) {
        lock.lock()
        let task = state.recognitionTask
        let tapInstalled = state.tapInstalled
        let configured = state.sessionConfigured
        state.running = false
        state.recognitionTask = nil
        state.tapInstalled = false
        state.sessionConfigured = false
        state.sessionID = nil
        state.sequence = 0
        lock.unlock()

        recognizer.stopRecognition()
        task?.cancel()
        if tapInstalled { audioSession.removeTap() }
        if configured {
            try? audioSession.deactivate()
        }
        vad.reset()
        lastTerminal = terminal
    }

    /// Emit an event with the next sequence number and a fresh monotonic
    /// timestamp. Emits only while `sessionID` is still the owned, active
    /// session; `cleanup` clears the owned session so no event escapes after
    /// a stop. Sequence numbers are strictly increasing per session.
    private func emit(_ sessionID: VoiceSessionID, _ event: VoiceEvent) {
        lock.lock()
        guard state.sessionID == sessionID else {
            lock.unlock()
            return
        }
        state.sequence += 1
        let seq = state.sequence
        lock.unlock()

        let timestamp = clock.now()
        onEvent?(sessionID, seq, timestamp, event)
    }

    // MARK: - Error shaping

    private static let recognitionLocale = "en-US"

    private static func audioSessionError(underlying: Error) -> VoiceErrorEvent {
        VoiceErrorEvent(
            id: opaqueID(),
            kind: "audio_session",
            message: "Audio session unavailable"
        )
    }

    private static func recognitionError(underlying: Error) -> VoiceErrorEvent {
        VoiceErrorEvent(
            id: opaqueID(),
            kind: "recognition",
            message: "Speech recognition failed"
        )
    }

    private static let routeLossError = VoiceErrorEvent(
        id: "route-loss",
        kind: "route_loss",
        message: "Audio route lost"
    )

    private static func opaqueID() -> String {
        UUID().uuidString
    }
}
