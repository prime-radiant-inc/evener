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

    func testNewOutputKeepsBargeInAfterPreviousFollowupDeadline() {
        var voice = VoiceController()
        voice.arm(policy: VoicePolicy()); voice.ready(generation: 1)
        XCTAssertTrue(voice.acceptSpeech(generation: 2))
        voice.outputDrained(at: 0)
        _ = voice.tick(at: 10000)
        XCTAssertTrue(voice.acceptSpeech(generation: 3))
        _ = voice.tick(at: 12000)
        // VoiceService's immediate local stop depends on this state.
        XCTAssertEqual(voice.state, .speaking)
        let pcm = Data(repeating: 1, count: 1024)
        XCTAssertEqual(voice.capture(pcm, speech: true, at: 12001), [.cue, .send(.inputStart(1))])
        XCTAssertEqual(voice.started(sequence: 1), [.send(.audio(1, 0, pcm))])
    }

    func testFollowupWindowStartsWhenLatestOutputDrains() {
        var voice = VoiceController()
        voice.arm(policy: VoicePolicy()); voice.ready(generation: 1)
        XCTAssertTrue(voice.acceptSpeech(generation: 2))
        voice.outputDrained(at: 0)
        _ = voice.tick(at: 10000)
        XCTAssertTrue(voice.acceptSpeech(generation: 3))
        _ = voice.tick(at: 40000)
        XCTAssertEqual(voice.state, .speaking)
        voice.outputDrained(at: 40000)
        _ = voice.tick(at: 51999)
        XCTAssertEqual(voice.state, .conversing)
        _ = voice.tick(at: 52000)
        XCTAssertEqual(voice.state, .quiet)
        XCTAssertEqual(voice.capture(Data(repeating: 1, count: 1024), speech: true, at: 52001), [])
    }

    func testRejectedSpeechPreservesFollowupDeadline() {
        var voice = VoiceController()
        voice.arm(policy: VoicePolicy()); voice.ready(generation: 1)
        XCTAssertTrue(voice.acceptSpeech(generation: 3))
        voice.outputDrained(at: 100)
        _ = voice.tick(at: 10000)
        XCTAssertFalse(voice.acceptSpeech(generation: 2))
        XCTAssertEqual(voice.state, .conversing)
        var beforeExpiry = voice
        XCTAssertEqual(beforeExpiry.capture(Data(repeating: 1, count: 1024), speech: true, at: 12099), [.cue, .send(.inputStart(1))])
        _ = voice.tick(at: 12100)
        XCTAssertEqual(voice.state, .quiet)
        XCTAssertEqual(voice.capture(Data(repeating: 1, count: 1024), speech: true, at: 12100), [])
    }

    func testCaptureSampleBudgetIncludesBufferedFramesAndResetsPerUtterance() {
        var voice = VoiceController()
        voice.arm(policy: VoicePolicy()); voice.ready(generation: 1)
        let pcm = Data((0..<1024).map { UInt8($0 % 256) })
        // Each 1024-frame hardware tap at 48 kHz supplies 512 mono PCM16
        // provider samples at 24 kHz. Admission may arrive after buffering.
        for (turn, pendingFrames) in [0, 45].enumerated() {
            let sequence = turn + 1
            let start = turn * 40000
            XCTAssertEqual(voice.talk(at: Double(start)), [.cue, .send(.inputStart(sequence))])
            var actions: [VoiceAction] = []
            for frame in 0..<1500 {
                if frame == pendingFrames { actions += voice.started(sequence: sequence) }
                let at = Double(start + 1 + frame * 1024 * 1000 / 48000)
                let captured = voice.capture(pcm, speech: true, at: at)
                actions += captured
                if captured.contains(.send(.inputEnd(sequence))) { break }
            }
            let audio = actions.compactMap { action -> (Int, Int, Data)? in
                if case let .send(.audio(sequence, frame, pcm)) = action { return (sequence, frame, pcm) }
                return nil
            }
            // The hub rejects an entire input above 720000 samples. A bounded
            // final partial frame must precede input_end, with no frame gaps.
            XCTAssertEqual(audio.map { $0.0 }, Array(repeating: sequence, count: 1407))
            XCTAssertEqual(audio.map { $0.1 }, Array(0..<1407))
            XCTAssertEqual(audio.map { $0.2.count / 2 }, Array(repeating: 512, count: 1406) + [128])
            XCTAssertEqual(audio.reduce(0) { $0 + $1.2.count / 2 }, 720000)
            XCTAssertTrue(audio.allSatisfy { $0.2.count % 2 == 0 })
            XCTAssertEqual(audio.last?.2, Data(pcm.prefix(256)))
            XCTAssertEqual(actions.count, audio.count + 1)
            XCTAssertEqual(actions.last, .send(.inputEnd(sequence)))
            XCTAssertFalse(voice.cloudMicrophone)
            XCTAssertEqual(voice.bufferedBytes, 0)
            XCTAssertEqual(voice.capture(pcm, speech: true, at: Double(start + 31000)), [])
        }
    }

    func testSampleBudgetBeforeAdmissionAbandonsUnacknowledgedInput() {
        var policy = VoicePolicy()
        policy.maxUtteranceMs = 60
        var voice = VoiceController()
        voice.arm(policy: policy); voice.ready(generation: 1)
        _ = voice.talk(at: 0)
        let pcm = Data(repeating: 1, count: 1024)
        XCTAssertEqual(voice.capture(pcm, speech: true, at: 1), [])
        XCTAssertEqual(voice.capture(pcm, speech: true, at: 22), [])
        XCTAssertEqual(voice.capture(pcm, speech: true, at: 43), [.abort(1, "input_start_timeout")])
        XCTAssertEqual(voice.started(sequence: 1), [])
        XCTAssertEqual(voice.bufferedBytes, 0)
        XCTAssertEqual(voice.state, .quiet)
    }

    func testSampleBudgetDoesNotSubmitSilentInput() {
        var voice = VoiceController()
        voice.arm(policy: VoicePolicy()); voice.ready(generation: 1)
        _ = voice.talk(at: 0); _ = voice.started(sequence: 1)
        var actions: [VoiceAction] = []
        for frame in 0..<1500 {
            let at = Double(1 + frame * 1024 * 1000 / 48000)
            actions += voice.capture(Data(repeating: 0, count: 1024), speech: false, at: at)
        }
        actions += voice.tick(at: 32000)
        let audio = actions.compactMap { action -> Data? in
            if case let .send(.audio(_, _, pcm)) = action { return pcm }
            return nil
        }
        XCTAssertEqual(audio.reduce(0) { $0 + $1.count / 2 }, 720000)
        XCTAssertFalse(actions.contains(.send(.inputEnd(1))))
        XCTAssertEqual(actions.filter { if case .abort = $0 { return true }; return false }, [.abort(1, "input_idle")])
        XCTAssertEqual(voice.state, .quiet)
    }

    func testReadyAcknowledgesFreshAndQuietReconnectWithoutClaimingCapture() {
        var voice = VoiceController()
        voice.arm(policy: VoicePolicy())
        XCTAssertEqual(voice.synchronizeReady(generation: 1), [.send(.interrupt(0, [:]))])
        XCTAssertEqual(voice.state, .recovering)
        voice.ready(generation: 1)
        XCTAssertEqual(voice.state, .quiet)
        _ = voice.recovering()
        XCTAssertEqual(voice.synchronizeReady(generation: 2), [.send(.interrupt(1, [:]))])
        XCTAssertEqual(voice.state, .recovering)
        voice.ready(generation: 2)
        XCTAssertEqual(voice.state, .quiet)
    }

    func testReadyAcknowledgesRetainedFenceAndNewerDurableGeneration() {
        var voice = VoiceController()
        voice.arm(policy: VoicePolicy()); voice.ready(generation: 7)
        _ = voice.stopSpeaking(played: [:])
        _ = voice.recovering()
        XCTAssertEqual(voice.synchronizeReady(generation: 3), [.send(.interrupt(8, [:]))])
        XCTAssertEqual(voice.minimumGeneration, 9)
        XCTAssertEqual(voice.synchronizeReady(generation: 20), [.send(.interrupt(19, [:]))])
        XCTAssertEqual(voice.minimumGeneration, 20)
        voice.ready(generation: 20)
        XCTAssertFalse(voice.acceptSpeech(generation: 19))
        XCTAssertTrue(voice.acceptSpeech(generation: 20))
    }

    func testRepeatedReadyAcknowledgementKeepsCurrentSpeechEligible() {
        var voice = VoiceController()
        voice.arm(policy: VoicePolicy()); voice.ready(generation: 6)
        XCTAssertTrue(voice.acceptSpeech(generation: 6))
        XCTAssertEqual(voice.synchronizeReady(generation: 6), [.send(.interrupt(5, [:]))])
        XCTAssertEqual(voice.synchronizeReady(generation: 6), [.send(.interrupt(5, [:]))])
        XCTAssertEqual(voice.minimumGeneration, 6)
        XCTAssertEqual(voice.state, .speaking)
        XCTAssertTrue(voice.acceptSpeech(generation: 6))
    }
}
