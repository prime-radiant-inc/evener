import XCTest
@testable import EvenerVoiceCore

final class WakeWordStreamTests: XCTestCase {
    func testThirdSubmitReportsGapDropsOldEpochAndResetsNextSubmit() {
        let detector = GatedWakeDetector()
        let receiver = DispatchQueue(label: "wake-stream-test")
        let stream = WakeWordStream(detector: detector, receiver: receiver)
        let fresh = expectation(description: "fresh epoch result")
        var results: [Int64] = []
        var failures: [String] = []
        receiver.sync {
            stream.received = { results.append($0.sampleOffset); fresh.fulfill() }
            stream.failed = { reason, samples in
                failures.append(reason)
                XCTAssertEqual(samples, 3)
            }
            stream.submit([1])
        }
        detector.waitUntilProcessing()
        // Both submits return while the first inference is still blocked.
        receiver.sync { stream.submit([2]); stream.submit([3, 3, 3]) }
        receiver.sync { XCTAssertEqual(failures, ["wake_capture_gap"]) }
        detector.proceed.signal()
        detector.waitUntilProcessing()
        // The first slot has been returned, but the old second block is still busy.
        receiver.sync { stream.submit([4]) }
        detector.proceed.signal()
        detector.waitUntilProcessing()
        detector.proceed.signal()
        wait(for: [fresh], timeout: 5)
        receiver.sync {
            XCTAssertEqual(results, [4], "both pre-gap results must be discarded")
            XCTAssertEqual(failures, ["wake_capture_gap"])
        }
        XCTAssertEqual(detector.resetsAtProcess, [1, 1, 2])
    }

    func testThrowingDetectorReportsFailureAndNextSubmitResets() {
        let detector = GatedWakeDetector(throwOnFirst: true)
        let receiver = DispatchQueue(label: "wake-stream-throw-test")
        let stream = WakeWordStream(detector: detector, receiver: receiver)
        let failed = expectation(description: "current epoch failure")
        let recovered = expectation(description: "result after reset")
        receiver.sync {
            stream.failed = { reason, samples in
                XCTAssertEqual(reason, "wake_inference_failed")
                XCTAssertEqual(samples, 2)
                failed.fulfill()
            }
            stream.received = { XCTAssertEqual($0.sampleOffset, 2); recovered.fulfill() }
            stream.submit([1, 1])
        }
        detector.waitUntilProcessing()
        detector.proceed.signal()
        wait(for: [failed], timeout: 5)
        receiver.sync { stream.submit([2]) }
        detector.waitUntilProcessing()
        detector.proceed.signal()
        wait(for: [recovered], timeout: 5)
        XCTAssertEqual(detector.resetsAtProcess, [1, 2])
    }

    func testExplicitResetDropsStaleFailureWithoutResettingFreshEpochAgain() {
        let detector = GatedWakeDetector(throwOnFirst: true)
        let receiver = DispatchQueue(label: "wake-stream-stale-failure-test")
        let stream = WakeWordStream(detector: detector, receiver: receiver)
        let fresh = expectation(description: "fresh result after explicit reset")
        receiver.sync {
            stream.failed = { reason, _ in XCTFail("stale failure was published: \(reason)") }
            stream.received = { XCTAssertEqual($0.sampleOffset, 2); fresh.fulfill() }
            stream.submit([1])
        }
        detector.waitUntilProcessing()
        receiver.sync { stream.reset(); stream.submit([2]) }
        detector.proceed.signal()
        detector.waitUntilProcessing()
        detector.proceed.signal()
        wait(for: [fresh], timeout: 5)
        XCTAssertEqual(detector.resetsAtProcess, [1, 2])
    }
}

/// Deliberately slow/throwing mechanics stub, not model-accuracy evidence.
/// Handshakes make queue saturation deterministic without sleeps or ONNX.
private final class GatedWakeDetector: WakeWordDetecting {
    let settings = WakeWordSettings(threshold: 0.99, releaseThreshold: 0.5, hopSamples: 1280, holdoffMs: 2500, embeddingFrames: 16)
    let proceed = DispatchSemaphore(value: 0)
    private let entered = DispatchSemaphore(value: 0)
    private let lock = NSLock()
    private let throwOnFirst: Bool
    private var resets = 0
    private var snapshots: [Int] = []
    var resetsAtProcess: [Int] { lock.lock(); defer { lock.unlock() }; return snapshots }
    init(throwOnFirst: Bool = false) { self.throwOnFirst = throwOnFirst }
    func reset() { lock.lock(); resets += 1; lock.unlock() }
    func waitUntilProcessing(file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertEqual(entered.wait(timeout: .now() + 5), .success, file: file, line: line)
    }
    func process(_ samples: [Float]) throws -> [WakeWordResult] {
        lock.lock()
        snapshots.append(resets)
        let first = snapshots.count == 1
        lock.unlock()
        entered.signal()
        guard proceed.wait(timeout: .now() + 5) == .success else { throw VoiceFailure("test_gate_timeout") }
        if throwOnFirst && first { throw VoiceFailure("test_inference_error") }
        return [WakeWordResult(state: .listening, score: 1, detected: true, sampleOffset: Int64(samples[0]))]
    }
}
