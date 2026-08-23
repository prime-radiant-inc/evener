import Foundation

/// Delegate for `SynthesisQueue` lifecycle events. Only stable chunk
/// identifiers are reported here — never raw spoken text.
protocol SynthesisQueueDelegate: AnyObject {
    func speechStarted(chunkId: String)
    func speechFinished(chunkId: String)
}

/// FIFO synthesis queue with stable, caller-provided chunk identifiers.
///
/// Chunks are spoken in enqueue order. Each chunk retains its stable ID
/// through the start/finish lifecycle. Duplicate chunk IDs are ignored.
/// `stop()` and interruption clear the queue and reject late callbacks.
///
/// Start/finish ordering is strict: a chunk must be started before it can
/// be finished. Callbacks for unknown, duplicate-already-spoken, or
/// cleared chunks are ignored so the queue never forwards stale state.
final class SynthesisQueue {

    private struct Chunk {
        let chunkId: String
        let text: String
    }

    private let synthesizer: SpeechSynthesizing
    private let rate: Float

    private var pending: [Chunk] = []
    private var knownIds: Set<String> = []
    private var currentChunkId: String?
    private var currentStarted: Bool = false

    weak var delegate: SynthesisQueueDelegate?

    init(synthesizer: SpeechSynthesizing, rate: Float) {
        self.synthesizer = synthesizer
        self.rate = rate
        self.synthesizer.delegate = self
    }

    /// Enqueue a chunk for synthesis. Duplicate chunk IDs are ignored.
    /// When the queue is idle, the first chunk begins speaking immediately.
    func enqueue(chunkId: String, text: String) {
        guard !knownIds.contains(chunkId) else { return }
        knownIds.insert(chunkId)
        pending.append(Chunk(chunkId: chunkId, text: text))
        pump()
    }

    /// Stop current speech and clear the pending queue. Late callbacks
    /// for the previously-current chunk are ignored.
    func stop() {
        synthesizer.stopSpeaking()
        pending.removeAll()
        currentChunkId = nil
        currentStarted = false
    }

    /// Clear the pending queue and stop current speech without emitting
    /// a finish for the interrupted chunk.
    func handleInterruption() {
        synthesizer.stopSpeaking()
        pending.removeAll()
        currentChunkId = nil
        currentStarted = false
    }

    private func pump() {
        guard currentChunkId == nil, let next = pending.first else { return }
        pending.removeFirst()
        currentChunkId = next.chunkId
        currentStarted = false
        synthesizer.speak(
            SpeechUtterance(chunkId: next.chunkId, text: next.text, rate: rate)
        )
    }
}

extension SynthesisQueue: SpeechSynthesizingDelegate {

    func speechDidStart(chunkId: String) {
        guard chunkId == currentChunkId, !currentStarted else { return }
        currentStarted = true
        delegate?.speechStarted(chunkId: chunkId)
    }

    func speechDidFinish(chunkId: String) {
        guard chunkId == currentChunkId, currentStarted else { return }
        let finishedId = chunkId
        currentChunkId = nil
        currentStarted = false
        delegate?.speechFinished(chunkId: finishedId)
        pump()
    }

    func speechDidCancel(chunkId: String) {
        guard chunkId == currentChunkId else { return }
        currentChunkId = nil
        currentStarted = false
        pump()
    }
}
