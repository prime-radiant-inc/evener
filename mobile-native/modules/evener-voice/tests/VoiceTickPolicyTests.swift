import XCTest
@testable import EvenerVoiceCore

final class VoiceTickPolicyTests: XCTestCase {
	func testArmedQuietTicksAtTheSlowCadence() {
		XCTAssertEqual(VoiceTickPolicy.intervalMilliseconds(state: .quiet), 500)
	}

	func testRecoveringTicksAtTheSlowCadence() {
		XCTAssertEqual(VoiceTickPolicy.intervalMilliseconds(state: .recovering), 500)
	}

	func testAConversationTicksAtTheFastCadence() {
		XCTAssertEqual(VoiceTickPolicy.intervalMilliseconds(state: .conversing), 100)
	}

	func testProviderPlaybackTicksAtTheFastCadence() {
		XCTAssertEqual(VoiceTickPolicy.intervalMilliseconds(state: .speaking), 100)
	}
}
