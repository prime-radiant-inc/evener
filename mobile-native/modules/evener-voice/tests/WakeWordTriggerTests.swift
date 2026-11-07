import XCTest
@testable import EvenerVoiceCore

final class WakeWordTriggerTests: XCTestCase {
    private let settings = WakeWordSettings(threshold: 0.99, releaseThreshold: 0.5, hopSamples: 1280, holdoffMs: 2500, embeddingFrames: 16)

    func testThresholdIsInclusiveAndSustainedScoreDoesNotRetrigger() {
        var trigger = WakeWordTrigger()
        let below = trigger.evaluate(score: 0.98, sampleOffset: 0, settings: settings)
        XCTAssertFalse(below.detected)
        XCTAssertEqual(below.state, .listening)
        let hit = trigger.evaluate(score: 0.99, sampleOffset: 1280, settings: settings)
        XCTAssertTrue(hit.detected)
        XCTAssertEqual(hit.state, .holdoff)
        XCTAssertEqual(hit.score, 0.99)
        XCTAssertEqual(hit.sampleOffset, 1280)
        XCTAssertFalse(trigger.evaluate(score: 1, sampleOffset: 100_000, settings: settings).detected)
        XCTAssertEqual(trigger.evaluate(score: 1, sampleOffset: 101_280, settings: settings).state, .holdoff)
    }

    func testReleaseMustBeStrictlyBelowHysteresisThreshold() {
        var trigger = WakeWordTrigger()
        XCTAssertTrue(trigger.evaluate(score: 1, sampleOffset: 0, settings: settings).detected)
        XCTAssertFalse(trigger.evaluate(score: 0.5, sampleOffset: 40_000, settings: settings).detected)
        XCTAssertFalse(trigger.evaluate(score: 1, sampleOffset: 41_280, settings: settings).detected)
        let released = trigger.evaluate(score: 0.49, sampleOffset: 42_560, settings: settings)
        XCTAssertEqual(released.state, .listening)
        XCTAssertFalse(released.detected)
        XCTAssertTrue(trigger.evaluate(score: 0.99, sampleOffset: 43_840, settings: settings).detected)
    }

    func testReleaseDoesNotBypassSampleClockHoldoff() {
        var trigger = WakeWordTrigger()
        XCTAssertTrue(trigger.evaluate(score: 1, sampleOffset: 0, settings: settings).detected)
        XCTAssertEqual(trigger.evaluate(score: 0, sampleOffset: 1280, settings: settings).state, .holdoff)
        XCTAssertFalse(trigger.evaluate(score: 1, sampleOffset: 39_999, settings: settings).detected)
        XCTAssertTrue(trigger.evaluate(score: 1, sampleOffset: 40_000, settings: settings).detected)
    }

    func testZeroHoldoffStillRequiresRelease() {
        var immediate = settings
        immediate.holdoffMs = 0
        var trigger = WakeWordTrigger()
        XCTAssertTrue(trigger.evaluate(score: 1, sampleOffset: 0, settings: immediate).detected)
        XCTAssertFalse(trigger.evaluate(score: 1, sampleOffset: 1280, settings: immediate).detected)
        XCTAssertEqual(trigger.evaluate(score: 0, sampleOffset: 2560, settings: immediate).state, .listening)
        XCTAssertTrue(trigger.evaluate(score: 1, sampleOffset: 3840, settings: immediate).detected)
    }
}
