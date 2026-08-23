import XCTest
@testable import EvenerNativePlugin

// MARK: - Fakes

final class FakeBargeInClock: BargeInClock {
    private(set) var nowMs: Int64 = 0

    func now() -> Int64 { nowMs }

    @discardableResult
    func advance(by ms: Int64) -> Int64 {
        nowMs += ms
        return nowMs
    }
}

final class FakeBargeInVAD: BargeInVADReporting {
    var isActive: Bool = false
}

final class FakeBargeInPartial: BargeInPartialReporting {
    var currentPartial: String = ""

    var hasNonemptyPartial: Bool { !currentPartial.isEmpty }
}

final class RecordingSynthesisQueue: SynthesisQueueDelegate {
    private(set) var startedChunkIds: [String] = []
    private(set) var finishedChunkIds: [String] = []

    func speechStarted(chunkId: String) { startedChunkIds.append(chunkId) }
    func speechFinished(chunkId: String) { finishedChunkIds.append(chunkId) }
}

final class FakeBargeInSpeechQueue: BargeInSpeechQueue {
    /// Mirrors the BargeInSpeechQueue protocol the controller depends on.
    var clearCallCount = 0
    var stopCallCount = 0

    func clear() { clearCallCount += 1 }
    func stop() { stopCallCount += 1 }
}

final class RecordingBargeInSink: BargeInEventSink {
    private(set) var bargeEvents: [BargeInEvent] = []
    private(set) var diagnosticEvents: [BargeInDiagnostic] = []

    var onBarge: ((BargeInEvent) -> Void)?

    func emitBarge(_ event: BargeInEvent) {
        bargeEvents.append(event)
        onBarge?(event)
    }

    func emitDiagnostic(_ diag: BargeInDiagnostic) {
        diagnosticEvents.append(diag)
    }
}

// MARK: - Tests

final class BargeInControllerTests: XCTestCase {

    private func makeController(
        clock: FakeBargeInClock = FakeBargeInClock(),
        vad: FakeBargeInVAD = FakeBargeInVAD(),
        partial: FakeBargeInPartial = FakeBargeInPartial(),
        queue: FakeBargeInSpeechQueue = FakeBargeInSpeechQueue(),
        sink: RecordingBargeInSink = RecordingBargeInSink()
    ) -> (
        controller: BargeInController,
        clock: FakeBargeInClock,
        vad: FakeBargeInVAD,
        partial: FakeBargeInPartial,
        queue: FakeBargeInSpeechQueue,
        sink: RecordingBargeInSink
    ) {
        let c = BargeInController(
            clock: clock,
            vad: vad,
            partial: partial,
            speechQueue: queue,
            sink: sink
        )
        return (c, clock, vad, partial, queue, sink)
    }

    // 1. VAD alone does nothing
    func testVADAloneDoesNothing() {
        let (controller, _, vad, partial, queue, sink) = makeController()
        controller.synthesisDidStart(atMs: 0)

        partial.currentPartial = ""
        vad.isActive = true
        controller.evaluate()

        XCTAssertEqual(queue.stopCallCount, 0)
        XCTAssertEqual(queue.clearCallCount, 0)
        XCTAssertTrue(sink.bargeEvents.isEmpty)
    }

    // 2. Partial alone does nothing
    func testPartialAloneDoesNothing() {
        let (controller, _, vad, partial, queue, sink) = makeController()
        controller.synthesisDidStart(atMs: 0)

        vad.isActive = false
        partial.currentPartial = "hello"
        controller.evaluate()

        XCTAssertEqual(queue.stopCallCount, 0)
        XCTAssertEqual(queue.clearCallCount, 0)
        XCTAssertTrue(sink.bargeEvents.isEmpty)
    }

    // 3. Combined VAD + partial signals stop once within 250ms
    func testCombinedSignalsStopOnceWithin250ms() {
        let clock = FakeBargeInClock()
        let (controller, _, vad, partial, queue, sink) = makeController(clock: clock)

        controller.synthesisDidStart(atMs: 0)
        vad.isActive = true
        partial.currentPartial = "stop now"

        // Evaluate well within the 250ms window.
        clock.advance(by: 100)
        controller.evaluate()

        XCTAssertEqual(queue.stopCallCount, 1)
        XCTAssertEqual(sink.bargeEvents.count, 1)

        // Re-evaluating within the same window must NOT fire a second stop.
        clock.advance(by: 50)
        controller.evaluate()
        XCTAssertEqual(queue.stopCallCount, 1)
        XCTAssertEqual(sink.bargeEvents.count, 1)
    }

    // 4. Clears queue before barge event
    func testClearsQueueBeforeBargeEvent() {
        let clock = FakeBargeInClock()
        let queue = FakeBargeInSpeechQueue()

        // Record the order: clear must precede the barge event emission.
        var callOrder: [String] = []
        let orderQueue = OrderingSpeechQueue(
            realClear: { callOrder.append("clear"); queue.clear() },
            realStop: { callOrder.append("stop"); queue.stop() }
        )

        let vad = FakeBargeInVAD()
        vad.isActive = true
        let partial = FakeBargeInPartial()
        partial.currentPartial = "stop"

        let c = BargeInController(
            clock: clock,
            vad: vad,
            partial: partial,
            speechQueue: orderQueue,
            sink: OrderingSink(
                onBarge: { _ in callOrder.append("barge") },
                onDiagnostic: { _ in callOrder.append("diag") }
            )
        )

        c.synthesisDidStart(atMs: 0)
        clock.advance(by: 100)
        c.evaluate()

        // Expect: clear, then stop, then barge, then diagnostic.
        XCTAssertEqual(callOrder, ["clear", "stop", "barge", "diag"])
    }

    // 5. Pre-synthesis partial cannot trigger barge-in (synthesis must have started)
    func testPreSynthesisPartialCannotTriggerBargeIn() {
        let (controller, _, vad, partial, queue, sink) = makeController()
        // No synthesisDidStart call yet.
        vad.isActive = true
        partial.currentPartial = "hello"
        controller.evaluate()

        XCTAssertEqual(queue.stopCallCount, 0)
        XCTAssertEqual(queue.clearCallCount, 0)
        XCTAssertTrue(sink.bargeEvents.isEmpty)
    }

    // 6. Barge event includes current recognized partial
    func testBargeEventIncludesCurrentPartial() {
        let clock = FakeBargeInClock()
        let (controller, _, vad, partial, _, sink) = makeController(clock: clock)

        controller.synthesisDidStart(atMs: 0)
        vad.isActive = true
        partial.currentPartial = "stop talking please"

        clock.advance(by: 80)
        controller.evaluate()

        XCTAssertEqual(sink.bargeEvents.count, 1)
        XCTAssertEqual(sink.bargeEvents.first?.recognizedPartial, "stop talking please")
    }

    // 7. Diagnostics do not include raw partial text
    func testDiagnosticsDoNotIncludeRawPartialText() {
        let clock = FakeBargeInClock()
        let (controller, _, vad, partial, _, sink) = makeController(clock: clock)

        controller.synthesisDidStart(atMs: 0)
        vad.isActive = true
        partial.currentPartial = "secret recognized content"

        clock.advance(by: 80)
        controller.evaluate()

        // Barge event carries the partial (required for UI state).
        XCTAssertEqual(sink.bargeEvents.first?.recognizedPartial, "secret recognized content")
        // Diagnostics must not embed the raw partial text.
        for diag in sink.diagnosticEvents {
            XCTAssertNil(diag.rawPartialText)
            let encoded = String(describing: diag)
            XCTAssertFalse(encoded.contains("secret recognized content"),
                           "diagnostic must not leak raw partial text")
        }
    }

    // 8. After barge: waits for final recognition before steering
    func testAfterBargeWaitsForFinalRecognition() {
        let clock = FakeBargeInClock()
        let (controller, _, vad, partial, queue, sink) = makeController(clock: clock)

        controller.synthesisDidStart(atMs: 0)
        vad.isActive = true
        partial.currentPartial = "hi"

        clock.advance(by: 80)
        controller.evaluate()
        XCTAssertEqual(sink.bargeEvents.count, 1)
        // Controller is now in "awaiting final" state; further evaluate() must not re-barge.
        let stopsBefore = queue.stopCallCount
        clock.advance(by: 200)
        controller.evaluate()
        XCTAssertEqual(queue.stopCallCount, stopsBefore)
        XCTAssertEqual(sink.bargeEvents.count, 1)

        // A new partial change while awaiting final must not re-trigger.
        partial.currentPartial = "hi there"
        controller.evaluate()
        XCTAssertEqual(sink.bargeEvents.count, 1)

        // Final recognition releases the awaiting state and resets for next round.
        controller.recognitionDidFinalize()
        // Now a fresh combined signal after a new synthesis start can barge again.
        // Use the clock's current time as the synthesis start so elapsed is non-negative.
        let secondStart = clock.now()
        controller.synthesisDidStart(atMs: secondStart)
        partial.currentPartial = "again"
        vad.isActive = true
        clock.advance(by: 100)
        controller.evaluate()
        XCTAssertEqual(sink.bargeEvents.count, 2)
    }
}

// MARK: - Ordering helpers for test 4

private final class OrderingSpeechQueue: BargeInSpeechQueue {
    let realClear: () -> Void
    let realStop: () -> Void
    init(realClear: @escaping () -> Void, realStop: @escaping () -> Void) {
        self.realClear = realClear
        self.realStop = realStop
    }
    func clear() { realClear() }
    func stop() { realStop() }
}

private final class OrderingSink: BargeInEventSink {
    let onBarge: (BargeInEvent) -> Void
    let onDiagnostic: (BargeInDiagnostic) -> Void
    init(onBarge: @escaping (BargeInEvent) -> Void, onDiagnostic: @escaping (BargeInDiagnostic) -> Void) {
        self.onBarge = onBarge
        self.onDiagnostic = onDiagnostic
    }
    func emitBarge(_ event: BargeInEvent) { onBarge(event) }
    func emitDiagnostic(_ diag: BargeInDiagnostic) { onDiagnostic(diag) }
}
