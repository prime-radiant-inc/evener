import Foundation

// MARK: - Timing / reporting protocols

/// Monotonic clock supplying milliseconds. Production uses a monotonic
/// source; tests inject a deterministic clock.
protocol BargeInClock {
    func now() -> Int64  // monotonic milliseconds
}

/// Voice-activity reporting surface. `isActive` is true while voice is
/// detected.
protocol BargeInVADReporting: AnyObject {
    var isActive: Bool { get }
}

/// Partial-recognition reporting surface used to detect a nonempty
/// recognized partial. Raw partial text never enters diagnostic events.
protocol BargeInPartialReporting: AnyObject {
    var hasNonemptyPartial: Bool { get }
    var currentPartial: String { get }
}

// MARK: - Speech queue + event sink (barge-in owned boundaries)

/// Minimal speech-queue surface the barge-in controller needs. This keeps
/// the controller decoupled from the concrete `SynthesisQueue` and lets
/// tests observe clear/stop ordering without a real synthesizer.
protocol BargeInSpeechQueue {
    func clear()
    func stop()
}

/// A barge-in event carries the current recognized partial (required for
/// UI state) but never propagates raw partial text into diagnostics.
struct BargeInEvent {
    let recognizedPartial: String
    let timestampMs: Int64
}

/// Diagnostic record for barge-in decisions. Raw partial text is omitted;
/// only the barge event itself carries it.
struct BargeInDiagnostic {
    let timestampMs: Int64
    /// Raw partial text is intentionally nil here. Kept as an optional so
    /// tests can assert it is never populated.
    var rawPartialText: String? { nil }
}

/// Sink that receives barge-in events and diagnostics.
protocol BargeInEventSink {
    func emitBarge(_ event: BargeInEvent)
    func emitDiagnostic(_ diag: BargeInDiagnostic)
}

// MARK: - Controller state machine

/// Barge-in state.
private enum BargeInState {
    case idle
    case synthesizing
    case awaitingFinal
}

/// Barge-in controller combining VAD + partial recognition.
///
/// Barge-in fires when BOTH conditions are true:
/// 1. VAD reports activity (voice detected)
/// 2. A nonempty recognition partial exists
///
/// Barge-in can only fire after synthesis has started. Before firing, the
/// queued speech is cleared. The barge event includes the current
/// recognized partial (required for UI state); diagnostics omit it. After
/// barge, the controller waits for final recognition before steering.
final class BargeInController {

    private static let bargeWindowMs: Int64 = 250

    private let clock: BargeInClock
    private let vad: BargeInVADReporting
    private let partial: BargeInPartialReporting
    private let speechQueue: BargeInSpeechQueue
    private let sink: BargeInEventSink

    private var state: BargeInState = .idle
    private var synthesisStartMs: Int64 = 0
    private var bargeFiredForCurrentSynthesis: Bool = false

    init(
        clock: BargeInClock,
        vad: BargeInVADReporting,
        partial: BargeInPartialReporting,
        speechQueue: BargeInSpeechQueue,
        sink: BargeInEventSink
    ) {
        self.clock = clock
        self.vad = vad
        self.partial = partial
        self.speechQueue = speechQueue
        self.sink = sink
    }

    /// Notify the controller that synthesis has started. Barge-in cannot
    /// fire before this call.
    func synthesisDidStart(atMs: Int64) {
        synthesisStartMs = atMs
        bargeFiredForCurrentSynthesis = false
        state = .synthesizing
    }

    /// Notify the controller that synthesis has stopped (cleared naturally
    /// or via stop). Returns the controller to idle if not awaiting final.
    func synthesisDidStop() {
        if state == .synthesizing {
            state = .idle
            bargeFiredForCurrentSynthesis = false
        }
    }

    /// Evaluate the combined VAD + partial signal against the current state.
    /// Idempotent within a single synthesis cycle: barge fires at most once
    /// per `synthesisDidStart`.
    func evaluate() {
        guard state == .synthesizing, !bargeFiredForCurrentSynthesis else { return }
        guard vad.isActive, partial.hasNonemptyPartial else { return }

        let nowMs = clock.now()
        let elapsed = nowMs - synthesisStartMs
        guard elapsed >= 0, elapsed <= BargeInController.bargeWindowMs else { return }

        fireBarge(atMs: nowMs)
    }

    /// Final recognition has been received; release the awaiting-final
    /// state and allow the next synthesis cycle to barge.
    func recognitionDidFinalize() {
        state = .idle
        bargeFiredForCurrentSynthesis = false
    }

    private func fireBarge(atMs: Int64) {
        // Clear queued speech before emitting the barge event.
        speechQueue.clear()
        speechQueue.stop()

        let recognized = partial.currentPartial
        sink.emitBarge(BargeInEvent(recognizedPartial: recognized, timestampMs: atMs))
        // Diagnostic omits the raw partial text by construction.
        sink.emitDiagnostic(BargeInDiagnostic(timestampMs: atMs))

        bargeFiredForCurrentSynthesis = true
        state = .awaitingFinal
    }
}
