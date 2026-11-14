import XCTest
@testable import EvenerVoiceCore

final class VoiceSnapshotGateTests: XCTestCase {
	private func facts(
		generated: String = "",
		detections: Int = 0,
		wakeScore: Float? = nil,
		trialsRevision: Int = 3
	) -> VoiceSnapshotFacts {
		VoiceSnapshotFacts(
			state: "quiet", capturing: true, cloudMicrophone: false, route: "iPhone Speaker",
			speakerConsentRequired: false, error: "", interpreted: "", custody: "", canonical: "",
			generated: generated, bindingId: "binding", wakeState: "listening", wakePhrase: "Hey Evener.",
			wakeModelId: "model", wakeScore: wakeScore, lastDetectionScore: nil, detections: detections,
			wakeError: "", trialsRevision: trialsRevision
		)
	}

	func testAPerBlockBurstOfIdenticalSnapshotsEmitsOneBridgeEvent() {
		var gate = VoiceSnapshotGate()
		var emissions = 0
		// A conversing capture block that sends audio but changes nothing the
		// JS side can see, over and over: one bridge event, not one per block (#5684).
		for _ in 0..<25 where gate.shouldEmit(facts()) { emissions += 1 }
		XCTAssertEqual(emissions, 1)
	}

	func testEachVisibleChangeEmitsExactlyOnce() {
		var gate = VoiceSnapshotGate()
		var emissions = 0
		if gate.shouldEmit(facts()) { emissions += 1 }
		if gate.shouldEmit(facts()) { emissions += 1 }
		if gate.shouldEmit(facts(generated: "hello")) { emissions += 1 }
		if gate.shouldEmit(facts(generated: "hello")) { emissions += 1 }
		if gate.shouldEmit(facts(generated: "hello", detections: 1)) { emissions += 1 }
		if gate.shouldEmit(facts(trialsRevision: 4)) { emissions += 1 }
		if gate.shouldEmit(facts()) { emissions += 1 }
		XCTAssertEqual(emissions, 5)
	}

	func testTrialScoreChangeEmitsWhenNothingElseDiffers() {
		var gate = VoiceSnapshotGate()
		var emissions = 0
		if gate.shouldEmit(facts(wakeScore: 0.4)) { emissions += 1 }
		if gate.shouldEmit(facts(wakeScore: 0.4)) { emissions += 1 }
		if gate.shouldEmit(facts(wakeScore: 0.9)) { emissions += 1 }
		XCTAssertEqual(emissions, 2)
	}

	func testSocketAcknowledgementsThatChangeNothingEmitNothing() {
		var gate = VoiceSnapshotGate()
		var emissions = 0
		if gate.shouldEmit(facts()) { emissions += 1 }
		// A spoken reply's audio acknowledgements, none of which change the snapshot.
		for _ in 0..<50 where gate.shouldEmit(facts()) { emissions += 1 }
		XCTAssertEqual(emissions, 1)
	}
}
