import XCTest
@testable import EvenerVoiceCore

final class PlaybackBudgetTests: XCTestCase {
    func testStreamingPastThirtySecondsRetainsPlayedEvidenceUntilProviderEnds() throws {
        for rate in [24000, 44100, 48000] {
            var timeline = PlaybackTimeline()
            // VoiceAudioDevice schedules each chunk's dataPlayedBack callback
            // independently of VoiceService receiving speech_end. Keep one
            // provider item open for 60 seconds while every chunk drains.
            for second in 0..<60 {
                // Include output gaps, which must not earn provider credit.
                let start = second * (rate + 100)
                do {
                    try timeline.append(delivery: "stream", generation: 7, item: "reply", providerSamples: 24000, outputSamples: rate, outputRate: Double(rate), outputStart: start)
                } catch {
                    XCTFail("append failed at second \(second + 1), rate \(rate), after \(second) drained chunks: \(error)")
                    throw error
                }
                XCTAssertEqual(timeline.watermarks(renderedOutputSamples: start), ["reply": second * 24000])
                XCTAssertEqual(timeline.watermarks(renderedOutputSamples: start + rate / 2), ["reply": second * 24000 + 12000])
                XCTAssertNil(timeline.drained(item: "reply", outputEnd: start + rate))
                XCTAssertEqual(timeline.watermarks(renderedOutputSamples: 0), ["reply": (second + 1) * 24000])
                XCTAssertFalse(timeline.allDrained)
            }
            XCTAssertEqual(timeline.providerEnded(item: "reply"), PlayedReceipt(delivery: "stream", generation: 7, item: "reply", samples: 1440000, complete: true))
            XCTAssertNil(timeline.providerEnded(item: "reply"))
            XCTAssertNil(timeline.drained(item: "reply", outputEnd: timeline.scheduledOutputSamples))
            XCTAssertFalse(timeline.allDrained)
            timeline.endDelivery()
            XCTAssertTrue(timeline.allDrained)
            XCTAssertEqual(timeline.watermarks(renderedOutputSamples: 0), ["reply": 1440000])
        }
    }

    func testThirtySecondsOutstandingStillRejectsAnotherSample() throws {
        var timeline = PlaybackTimeline()
        for second in 0..<30 {
            try timeline.append(delivery: "d", generation: 1, item: "a", providerSamples: 24000, outputSamples: 48000, outputRate: 48000, outputStart: second * 96000)
        }
        XCTAssertEqual(timeline.watermarks(renderedOutputSamples: 0), ["a": 0])
        // Provider completion is not a drain and must not release capacity.
        XCTAssertNil(timeline.providerEnded(item: "a"))
        let end = timeline.scheduledOutputSamples
        XCTAssertThrowsError(try timeline.append(delivery: "d", generation: 1, item: "b", providerSamples: 1, outputSamples: 2, outputRate: 48000)) { error in
            guard case PlaybackError.overflow = error else { return XCTFail("unexpected error: \(error)") }
        }
        XCTAssertEqual(timeline.scheduledOutputSamples, end)
        XCTAssertEqual(timeline.drained(item: "a", outputEnd: end)?.samples, 720000)
        try timeline.append(delivery: "d", generation: 1, item: "b", providerSamples: 1, outputSamples: 2, outputRate: 48000)
    }

    func testDrainReleasesOnlyPlayedBuffersBeforeProviderCompletion() throws {
        var timeline = PlaybackTimeline()
        for _ in 0..<30 {
            try timeline.append(delivery: "d", generation: 2, item: "a", providerSamples: 24000, outputSamples: 44100, outputRate: 44100)
        }
        for second in 1...5 {
            XCTAssertNil(timeline.drained(item: "a", outputEnd: second * 44100))
        }
        // Repeated/older callbacks cannot release additional capacity.
        XCTAssertNil(timeline.drained(item: "a", outputEnd: 44100))
        for _ in 0..<5 {
            try timeline.append(delivery: "d", generation: 2, item: "a", providerSamples: 24000, outputSamples: 44100, outputRate: 44100)
        }
        XCTAssertThrowsError(try timeline.append(delivery: "d", generation: 2, item: "a", providerSamples: 1, outputSamples: 2, outputRate: 44100)) { error in
            guard case PlaybackError.overflow = error else { return XCTFail("unexpected error: \(error)") }
        }
        XCTAssertEqual(timeline.watermarks(renderedOutputSamples: 0), ["a": 120000])
        XCTAssertNil(timeline.providerEnded(item: "a"))
        timeline.endDelivery()
        XCTAssertFalse(timeline.allDrained)
        XCTAssertEqual(timeline.drained(item: "a", outputEnd: 35 * 44100), PlayedReceipt(delivery: "d", generation: 2, item: "a", samples: 840000, complete: true))
        XCTAssertNil(timeline.drained(item: "a", outputEnd: 35 * 44100))
        XCTAssertTrue(timeline.allDrained)
    }
}
