import XCTest
@testable import EvenerVoiceCore

final class VoiceConsentTests: XCTestCase {
    func testConsentRejectionRetiresIncomingGenerationAndAllowsOnlyFreshSpeech() {
        var voice = VoiceController()
        voice.arm(policy: VoicePolicy()); voice.ready(generation: 4)
        // A route change already stopped the last output. The next delivery's
        // generation is newer than that local fence while consent is pending.
        _ = voice.stopSpeaking(played: [:])
        XCTAssertEqual(voice.minimumGeneration, 5)
        XCTAssertEqual(voice.admitSpeech(generation: 9, speakerConsent: false),
                       .rejected([.flushOutput, .send(.interrupt(9, [:]))]))
        XCTAssertEqual(voice.minimumGeneration, 10)
        XCTAssertEqual(voice.state, .quiet)
        XCTAssertFalse(voice.cloudMicrophone)

        // Neither duplicate delivery nor later consent can replay rejected audio.
        XCTAssertEqual(voice.admitSpeech(generation: 9, speakerConsent: false), .rejected([]))
        XCTAssertEqual(voice.admitSpeech(generation: 9, speakerConsent: true), .rejected([]))
        voice.remoteStop(generation: 10)
        XCTAssertEqual(voice.admitSpeech(generation: 11, speakerConsent: true), .accepted)
        XCTAssertEqual(voice.state, .speaking)
        XCTAssertEqual(voice.minimumGeneration, 11)
        XCTAssertEqual(voice.admitSpeech(generation: 9, speakerConsent: false), .rejected([]))
        XCTAssertEqual(voice.minimumGeneration, 11)
        XCTAssertEqual(voice.state, .speaking)
    }

    func testConsentRetirementDoesNotAbortAdmittedInput() {
        var voice = VoiceController()
        voice.arm(policy: VoicePolicy()); voice.ready(generation: 4)
        _ = voice.talk(at: 0); _ = voice.started(sequence: 1)
        XCTAssertTrue(voice.cloudMicrophone)
        XCTAssertEqual(voice.admitSpeech(generation: 9, speakerConsent: false),
                       .rejected([.flushOutput, .send(.interrupt(9, [:]))]))
        XCTAssertTrue(voice.cloudMicrophone)
        XCTAssertEqual(voice.state, .conversing)
        let pcm = Data(repeating: 1, count: 960)
        XCTAssertEqual(voice.capture(pcm, speech: true, at: 1), [.send(.audio(1, 0, pcm))])
    }

    func testConsentRetirementPreservesRecoveryAndDisarmFences() {
        var voice = VoiceController()
        XCTAssertEqual(voice.admitSpeech(generation: 9, speakerConsent: false), .rejected([]))
        XCTAssertEqual(voice.minimumGeneration, 0)
        voice.arm(policy: VoicePolicy())
        XCTAssertEqual(voice.admitSpeech(generation: 9, speakerConsent: false),
                       .rejected([.flushOutput, .send(.interrupt(9, [:]))]))
        XCTAssertEqual(voice.minimumGeneration, 10)
        XCTAssertEqual(voice.state, .recovering)
        XCTAssertEqual(voice.admitSpeech(generation: 11, speakerConsent: true), .rejected([]))
        voice.ready(generation: 10)
        XCTAssertEqual(voice.admitSpeech(generation: 9, speakerConsent: true), .rejected([]))
        XCTAssertEqual(voice.admitSpeech(generation: 11, speakerConsent: true), .accepted)
    }
}
