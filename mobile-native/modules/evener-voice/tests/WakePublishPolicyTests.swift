import XCTest
@testable import EvenerVoiceCore

final class WakePublishPolicyTests: XCTestCase {
    func testArmedQuietScoreChangeAloneDoesNotRepublish() {
        XCTAssertFalse(WakePublishPolicy.shouldPublish(
            trialRunning: false, state: "listening", publishedState: "listening",
            error: "", publishedError: ""))
    }

    func testStateChangeRepublishes() {
        XCTAssertTrue(WakePublishPolicy.shouldPublish(
            trialRunning: false, state: "holdoff", publishedState: "listening",
            error: "", publishedError: ""))
    }

    func testErrorChangeRepublishesOnce() {
        XCTAssertTrue(WakePublishPolicy.shouldPublish(
            trialRunning: false, state: "listening", publishedState: "listening",
            error: "capture_gap", publishedError: ""))
        XCTAssertFalse(WakePublishPolicy.shouldPublish(
            trialRunning: false, state: "listening", publishedState: "listening",
            error: "capture_gap", publishedError: "capture_gap"))
    }

    func testRunningTrialRepublishesForLiveScores() {
        XCTAssertTrue(WakePublishPolicy.shouldPublish(
            trialRunning: true, state: "listening", publishedState: "listening",
            error: "", publishedError: ""))
    }
}
