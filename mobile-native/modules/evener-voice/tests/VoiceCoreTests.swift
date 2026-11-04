import XCTest
@testable import EvenerVoiceCore

final class VoiceCoreTests: XCTestCase {
    func testRenderedClockDoesNotCreditSilenceBetweenItemsAsSpeech() throws {
        var timeline = PlaybackTimeline()
        try timeline.append(delivery: "d", generation: 1, item: "one", providerSamples: 2400, outputSamples: 4800, outputRate: 48000)
        _ = timeline.providerEnded(item: "one")
        _ = timeline.drained(item: "one", outputEnd: 4800)
        try timeline.append(delivery: "d", generation: 1, item: "two", providerSamples: 2400, outputSamples: 4800, outputRate: 48000, outputStart: 48000)
        XCTAssertEqual(timeline.watermarks(renderedOutputSamples: 48000)["two"], 0)
        XCTAssertEqual(timeline.watermarks(renderedOutputSamples: 50400)["two"], 1200)
    }

    func testRFC3339FractionalGrantExpiry() {
        XCTAssertNotNil(voiceExpiry("2026-11-04T22:00:00.123456789Z"))
        XCTAssertNotNil(voiceExpiry("2026-11-04T22:00:00Z"))
        XCTAssertNil(voiceExpiry("not-a-timestamp"))
    }

    func testItemDrainDoesNotFinishDeliveryBeforeLaterItemArrives() throws {
        var timeline = PlaybackTimeline()
        try timeline.append(delivery: "d", generation: 2, item: "one", providerSamples: 240, outputSamples: 480, outputRate: 48000)
        XCTAssertNil(timeline.providerEnded(item: "one"))
        XCTAssertNotNil(timeline.drained(item: "one", outputEnd: 480))
        XCTAssertFalse(timeline.allDrained)
        try timeline.append(delivery: "d", generation: 2, item: "two", providerSamples: 240, outputSamples: 480, outputRate: 48000)
        timeline.endDelivery()
        XCTAssertFalse(timeline.allDrained)
        XCTAssertNil(timeline.providerEnded(item: "two"))
        XCTAssertNotNil(timeline.drained(item: "two", outputEnd: 960))
        XCTAssertTrue(timeline.allDrained)
    }

    func testQuietAndTalkAdmissionExcludePrePressAndWaitForDurableStart() {
        var voice = VoiceController()
        voice.arm(policy: VoicePolicy())
        voice.ready(generation: 4)
        XCTAssertEqual(voice.capture(Data(repeating: 1, count: 960), speech: false, at: 0), [])
        XCTAssertEqual(voice.talk(at: 10), [.cue, .send(.inputStart(1))])
        XCTAssertEqual(voice.capture(Data(repeating: 2, count: 960), speech: true, at: 9), [])
        XCTAssertEqual(voice.capture(Data(repeating: 3, count: 960), speech: true, at: 11), [])
        XCTAssertEqual(voice.started(sequence: 1), [.send(.audio(1, 0, Data(repeating: 3, count: 960)))])
        XCTAssertTrue(voice.cloudMicrophone)
        XCTAssertEqual(voice.capture(Data(repeating: 4, count: 960), speech: false, at: 611).last, .send(.inputEnd(1)))
        XCTAssertFalse(voice.cloudMicrophone)
    }

    func testPartialOverflowAbortsInsteadOfAdmittingTruncatedSpeech() {
        var voice = VoiceController()
        voice.arm(policy: VoicePolicy()); voice.ready(generation: 1)
        _ = voice.talk(at: 0)
        let actions = voice.capture(Data(repeating: 1, count: 96_002), speech: true, at: 1)
        XCTAssertEqual(actions, [.abort(1, "input_overflow")])
        XCTAssertEqual(voice.started(sequence: 1), [])
        XCTAssertFalse(voice.cloudMicrophone)
        XCTAssertEqual(voice.bufferedBytes, 0)
    }

    func testLocalInterruptFencesBufferedOldGenerationBeforeNetwork() {
        var voice = VoiceController()
        voice.arm(policy: VoicePolicy()); voice.ready(generation: 7)
        XCTAssertTrue(voice.acceptSpeech(generation: 7))
        let actions = voice.stopSpeaking(played: ["a": 1200])
        XCTAssertEqual(actions, [.flushOutput, .send(.interrupt(7, ["a": 1200]))])
        XCTAssertEqual(voice.minimumGeneration, 8)
        XCTAssertFalse(voice.acceptSpeech(generation: 7))
        voice.remoteStop(generation: 10)
        XCTAssertFalse(voice.acceptSpeech(generation: 9))
        XCTAssertTrue(voice.acceptSpeech(generation: 10))
    }

    func testDisarmClearsAllAudioAndDoesNotIssueSessionStop() {
        var voice = VoiceController()
        voice.arm(policy: VoicePolicy()); voice.ready(generation: 2)
        _ = voice.talk(at: 10)
        _ = voice.capture(Data(repeating: 1, count: 960), speech: true, at: 20)
        XCTAssertEqual(voice.disarm(), [.flushOutput, .stopCapture, .send(.disarm)])
        XCTAssertEqual(voice.bufferedBytes, 0)
        XCTAssertEqual(voice.state, .disarmed)
        XCTAssertEqual(voice.started(sequence: 1), [])
        XCTAssertFalse(voice.acceptSpeech(generation: 20))
    }

    func testTimelineUsesRenderedClockAndKeepsPerItemFIFOAcrossResampling() throws {
        var timeline = PlaybackTimeline()
        try timeline.append(delivery: "d", generation: 3, item: "a", providerSamples: 2400, outputSamples: 4800, outputRate: 48000)
        try timeline.append(delivery: "d", generation: 3, item: "b", providerSamples: 1200, outputSamples: 2400, outputRate: 48000)
        XCTAssertEqual(timeline.watermarks(renderedOutputSamples: 1200), ["a": 600, "b": 0])
        XCTAssertEqual(timeline.watermarks(renderedOutputSamples: 6000), ["a": 2400, "b": 600])
        XCTAssertEqual(timeline.providerEnded(item: "a"), nil)
        let drained = timeline.drained(item: "a", outputEnd: 4800)
        XCTAssertEqual(drained?.samples, 2400)
        XCTAssertEqual(drained?.complete, true)
        XCTAssertEqual(timeline.drained(item: "b", outputEnd: 7200), nil)
        XCTAssertEqual(timeline.providerEnded(item: "b")?.samples, 1200)
    }

    func testGeneratedDoneNeverMeansPlayedAndInterruptedItemsNeverComplete() throws {
        var timeline = PlaybackTimeline()
        try timeline.append(delivery: "d", generation: 1, item: "x", providerSamples: 24000, outputSamples: 44100, outputRate: 44100)
        XCTAssertNil(timeline.providerEnded(item: "x"))
        XCTAssertEqual(timeline.watermarks(renderedOutputSamples: 22050)["x"], 12000)
        timeline.clear()
        XCTAssertNil(timeline.drained(item: "x", outputEnd: 44100))
    }

    func testFollowupExpiresAndIdleInputIsNotSubmitted() {
        var voice = VoiceController()
        voice.arm(policy: VoicePolicy()); voice.ready(generation: 1)
        voice.outputDrained(at: 100)
        XCTAssertEqual(voice.tick(at: 12100), [])
        XCTAssertEqual(voice.state, .quiet)
        _ = voice.talk(at: 13000)
        XCTAssertEqual(voice.tick(at: 43000), [.abort(1, "input_idle")])
        XCTAssertEqual(voice.state, .quiet)
    }
}
