import XCTest
@testable import EvenerNativePlugin

// MARK: - Fakes

final class FakeSpeechSynthesizer: SpeechSynthesizing {
    weak var delegate: SpeechSynthesizingDelegate?
    private(set) var spoken: [SpeechUtterance] = []
    private(set) var stopCount = 0
    private(set) var isSpeaking = false
    var rate: Float = 0.0

    func speak(_ utterance: SpeechUtterance) {
        spoken.append(utterance)
        rate = utterance.rate
        isSpeaking = true
    }

    func stopSpeaking() {
        stopCount += 1
        isSpeaking = false
    }

    /// Test driver: simulate the synthesizer reporting speech start.
    func emitStart(chunkId: String) {
        delegate?.speechDidStart(chunkId: chunkId)
    }

    /// Test driver: simulate the synthesizer reporting speech finish.
    func emitFinish(chunkId: String) {
        isSpeaking = false
        delegate?.speechDidFinish(chunkId: chunkId)
    }

    /// Test driver: simulate the synthesizer reporting speech cancel.
    func emitCancel(chunkId: String) {
        isSpeaking = false
        delegate?.speechDidCancel(chunkId: chunkId)
    }
}

final class RecordingSynthesisQueueDelegate: SynthesisQueueDelegate {
    private(set) var startedChunkIds: [String] = []
    private(set) var finishedChunkIds: [String] = []

    func speechStarted(chunkId: String) {
        startedChunkIds.append(chunkId)
    }

    func speechFinished(chunkId: String) {
        finishedChunkIds.append(chunkId)
    }
}

// MARK: - Tests

final class SynthesisQueueTests: XCTestCase {

    // 1. FIFO chunk ordering
    func testFIFOChunkOrdering() {
        let synth = FakeSpeechSynthesizer()
        let queue = SynthesisQueue(synthesizer: synth, rate: 0.5)
        let recorder = RecordingSynthesisQueueDelegate()
        queue.delegate = recorder

        queue.enqueue(chunkId: "a", text: "alpha")
        queue.enqueue(chunkId: "b", text: "bravo")
        queue.enqueue(chunkId: "c", text: "charlie")

        // The first speak happens immediately on enqueue (idle queue).
        XCTAssertEqual(synth.spoken.map(\.chunkId), ["a"])
        // Start then finish a -> b should start next.
        synth.emitStart(chunkId: "a")
        synth.emitFinish(chunkId: "a")
        XCTAssertEqual(synth.spoken.map(\.chunkId), ["a", "b"])
        // Start then finish b -> c should start next.
        synth.emitStart(chunkId: "b")
        synth.emitFinish(chunkId: "b")
        XCTAssertEqual(synth.spoken.map(\.chunkId), ["a", "b", "c"])
        // Start then finish c -> queue idle.
        synth.emitStart(chunkId: "c")
        synth.emitFinish(chunkId: "c")
        XCTAssertEqual(synth.spoken.map(\.chunkId), ["a", "b", "c"])
    }

    // 2. Rate application to utterances
    func testRateAppliedToUtterances() {
        let synth = FakeSpeechSynthesizer()
        let queue = SynthesisQueue(synthesizer: synth, rate: 0.42)

        queue.enqueue(chunkId: "a", text: "alpha")

        XCTAssertEqual(synth.spoken.count, 1)
        XCTAssertEqual(synth.spoken.first?.rate, 0.42)
        XCTAssertEqual(synth.rate, 0.42)
    }

    // 3. Stable chunk IDs preserved through start/finish
    func testStableChunkIDsPreservedThroughStartFinish() {
        let synth = FakeSpeechSynthesizer()
        let queue = SynthesisQueue(synthesizer: synth, rate: 0.5)
        let recorder = RecordingSynthesisQueueDelegate()
        queue.delegate = recorder

        queue.enqueue(chunkId: "chunk-1", text: "alpha")
        queue.enqueue(chunkId: "chunk-2", text: "bravo")

        synth.emitStart(chunkId: "chunk-1")
        synth.emitFinish(chunkId: "chunk-1")
        synth.emitStart(chunkId: "chunk-2")
        synth.emitFinish(chunkId: "chunk-2")

        XCTAssertEqual(recorder.startedChunkIds, ["chunk-1", "chunk-2"])
        XCTAssertEqual(recorder.finishedChunkIds, ["chunk-1", "chunk-2"])
    }

    // 4. Start/finish ordering (started before finished for each chunk)
    func testStartBeforeFinishOrdering() {
        let synth = FakeSpeechSynthesizer()
        let queue = SynthesisQueue(synthesizer: synth, rate: 0.5)
        let recorder = RecordingSynthesisQueueDelegate()
        queue.delegate = recorder

        queue.enqueue(chunkId: "a", text: "alpha")

        // Finish before start must be ignored (late/out-of-order callback).
        synth.emitFinish(chunkId: "a")
        XCTAssertEqual(recorder.finishedChunkIds, [])

        synth.emitStart(chunkId: "a")
        XCTAssertEqual(recorder.startedChunkIds, ["a"])
        XCTAssertEqual(recorder.finishedChunkIds, [])

        synth.emitFinish(chunkId: "a")
        XCTAssertEqual(recorder.finishedChunkIds, ["a"])
    }

    // 5. Stop clears queue
    func testStopClearsQueue() {
        let synth = FakeSpeechSynthesizer()
        let queue = SynthesisQueue(synthesizer: synth, rate: 0.5)
        let recorder = RecordingSynthesisQueueDelegate()
        queue.delegate = recorder

        queue.enqueue(chunkId: "a", text: "alpha")
        queue.enqueue(chunkId: "b", text: "bravo")
        queue.enqueue(chunkId: "c", text: "charlie")

        XCTAssertEqual(synth.spoken.count, 1)
        queue.stop()
        XCTAssertEqual(synth.stopCount, 1)

        // Finishing the previously-current chunk after stop is a late callback.
        synth.emitFinish(chunkId: "a")
        XCTAssertEqual(recorder.finishedChunkIds, [])
        // Nothing further is spoken.
        XCTAssertEqual(synth.spoken.count, 1)
    }

    // 6. Duplicate chunk ID ignored
    func testDuplicateChunkIDIgnored() {
        let synth = FakeSpeechSynthesizer()
        let queue = SynthesisQueue(synthesizer: synth, rate: 0.5)

        queue.enqueue(chunkId: "dup", text: "alpha")
        // Duplicate while still in flight is ignored.
        queue.enqueue(chunkId: "dup", text: "bravo")

        XCTAssertEqual(synth.spoken.count, 1)
        XCTAssertEqual(synth.spoken.first?.chunkId, "dup")
        XCTAssertEqual(synth.spoken.first?.text, "alpha")
    }

    // 7. Interruption clears queue
    func testInterruptionClearsQueue() {
        let synth = FakeSpeechSynthesizer()
        let queue = SynthesisQueue(synthesizer: synth, rate: 0.5)
        let recorder = RecordingSynthesisQueueDelegate()
        queue.delegate = recorder

        queue.enqueue(chunkId: "a", text: "alpha")
        queue.enqueue(chunkId: "b", text: "bravo")

        XCTAssertEqual(synth.spoken.count, 1)
        queue.handleInterruption()
        XCTAssertEqual(synth.stopCount, 1)

        // Late callback for the interrupted chunk is ignored.
        synth.emitFinish(chunkId: "a")
        XCTAssertEqual(recorder.finishedChunkIds, [])
        XCTAssertEqual(synth.spoken.count, 1)
    }

    // 8. Late delegate callbacks ignored (callback after stop does nothing)
    func testLateDelegateCallbacksIgnored() {
        let synth = FakeSpeechSynthesizer()
        let queue = SynthesisQueue(synthesizer: synth, rate: 0.5)
        let recorder = RecordingSynthesisQueueDelegate()
        queue.delegate = recorder

        queue.enqueue(chunkId: "a", text: "alpha")
        synth.emitStart(chunkId: "a")
        XCTAssertEqual(recorder.startedChunkIds, ["a"])

        queue.stop()

        // A finish arriving after stop must not be forwarded.
        synth.emitFinish(chunkId: "a")
        synth.emitStart(chunkId: "a")
        synth.emitCancel(chunkId: "a")
        XCTAssertEqual(recorder.finishedChunkIds, [])
        XCTAssertEqual(recorder.startedChunkIds, ["a"])
    }
}
