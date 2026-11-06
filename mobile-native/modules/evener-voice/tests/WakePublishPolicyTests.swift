import XCTest
@testable import EvenerVoiceCore

final class WakePublishPolicyTests: XCTestCase {
    func testUnchangedWakeStateAndErrorDoesNotRepublish() {
        XCTAssertFalse(WakePublishPolicy.shouldPublish(
            trialRunning: false, wakeState: "listening", publishedWakeState: "listening",
            wakeError: "", publishedWakeError: ""))
    }

    func testWakeStateChangeRepublishes() {
        XCTAssertTrue(WakePublishPolicy.shouldPublish(
            trialRunning: false, wakeState: "holdoff", publishedWakeState: "listening",
            wakeError: "", publishedWakeError: ""))
    }

    func testWakeErrorChangeRepublishes() {
        XCTAssertTrue(WakePublishPolicy.shouldPublish(
            trialRunning: false, wakeState: "listening", publishedWakeState: "listening",
            wakeError: "capture_gap", publishedWakeError: ""))
    }

    func testRunningTrialRepublishesForLiveScores() {
        XCTAssertTrue(WakePublishPolicy.shouldPublish(
            trialRunning: true, wakeState: "listening", publishedWakeState: "listening",
            wakeError: "", publishedWakeError: ""))
    }
}
