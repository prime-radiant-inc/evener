import XCTest
@testable import EvenerVoiceCore

final class VoiceRecoveryTests: XCTestCase {
    func testRecoveryRetiresIncomingSpeechWithoutReplayingAfterReady() {
        var voice = VoiceController()
        voice.arm(policy: VoicePolicy()); voice.ready(generation: 4)
        // VoiceService.pause interrupts the prior output before entering recovery.
        XCTAssertEqual(voice.stopSpeaking(played: [:]), [.flushOutput, .send(.interrupt(4, [:]))])
        XCTAssertEqual(voice.recovering(advanceGeneration: false), [.flushOutput])
        XCTAssertEqual(voice.state, .recovering)
        XCTAssertEqual(voice.admitSpeech(generation: 9, speakerConsent: true),
                       .rejected([.flushOutput, .send(.interrupt(9, [:]))]))
        XCTAssertEqual(voice.minimumGeneration, 10)
        XCTAssertEqual(voice.state, .recovering)
        XCTAssertFalse(voice.cloudMicrophone)
        XCTAssertEqual(voice.admitSpeech(generation: 9, speakerConsent: true), .rejected([]))
        voice.remoteStop(generation: 9)
        XCTAssertEqual(voice.minimumGeneration, 10)
        XCTAssertEqual(voice.state, .recovering)
        XCTAssertEqual(voice.recovering(advanceGeneration: false), [.flushOutput])
        XCTAssertEqual(voice.recovering(advanceGeneration: false), [.flushOutput])
        XCTAssertEqual(voice.admitSpeech(generation: 11, speakerConsent: true),
                       .rejected([.flushOutput, .send(.interrupt(11, [:]))]))
        XCTAssertEqual(voice.minimumGeneration, 12)
        XCTAssertEqual(voice.state, .recovering)
        voice.remoteStop(generation: 10)
        voice.ready(generation: 10)
        XCTAssertEqual(voice.minimumGeneration, 12)
        XCTAssertEqual(voice.admitSpeech(generation: 9, speakerConsent: true), .rejected([]))
        XCTAssertEqual(voice.admitSpeech(generation: 11, speakerConsent: true), .rejected([]))
        XCTAssertEqual(voice.admitSpeech(generation: 12, speakerConsent: true), .accepted)
        XCTAssertEqual(voice.state, .speaking)
        XCTAssertEqual(voice.sequence, 0)
    }

    func testStaleAndDisarmedSpeechRemainNoOpsDuringRecovery() {
        var voice = VoiceController()
        for consent in [false, true] {
            XCTAssertEqual(voice.admitSpeech(generation: 9, speakerConsent: consent), .rejected([]))
        }
        XCTAssertEqual(voice.minimumGeneration, 0)
        XCTAssertEqual(voice.state, .disarmed)
        voice.arm(policy: VoicePolicy()); voice.ready(generation: 10)
        _ = voice.recovering(advanceGeneration: false)
        for consent in [false, true] {
            XCTAssertEqual(voice.admitSpeech(generation: 9, speakerConsent: consent), .rejected([]))
        }
        XCTAssertEqual(voice.minimumGeneration, 10)
        XCTAssertEqual(voice.state, .recovering)
        _ = voice.disarm()
        XCTAssertEqual(voice.admitSpeech(generation: 12, speakerConsent: true), .rejected([]))
        XCTAssertEqual(voice.minimumGeneration, 11)
        XCTAssertEqual(voice.state, .disarmed)
    }
}
