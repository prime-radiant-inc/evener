import Foundation
import XCTest

final class WakeWordTests: XCTestCase {
    private func detector() throws -> OpenWakeWordDetector {
        // Preserve the preview regression tests without shipping its ONNX files.
        try OpenWakeWordDetector(manifestURL: previewManifest())
    }
    private func previewManifest() -> URL {
        Bundle(for: WakeWordTests.self).resourceURL!.appendingPathComponent("PreviewWakeModels/wake-models.json")
    }
    private func fixture(_ name: String) throws -> [Float] {
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().appendingPathComponent("fixtures")
        let bytes = try Data(contentsOf: root.appendingPathComponent(name))
        // The committed fixtures have canonical 44-byte PCM WAV headers.
        XCTAssertEqual(String(data: bytes.prefix(4), encoding: .ascii), "RIFF")
        XCTAssertEqual(String(data: bytes[8..<12], encoding: .ascii), "WAVE")
        let rate = bytes.withUnsafeBytes { $0.loadUnaligned(fromByteOffset: 24, as: UInt32.self).littleEndian }
        XCTAssertEqual(rate, 16000)
        XCTAssertEqual(bytes.withUnsafeBytes { $0.loadUnaligned(fromByteOffset: 22, as: UInt16.self).littleEndian }, 1)
        XCTAssertEqual(bytes.withUnsafeBytes { $0.loadUnaligned(fromByteOffset: 34, as: UInt16.self).littleEndian }, 16)
        XCTAssertEqual(String(data: bytes[36..<40], encoding: .ascii), "data")
        let speech: [Float] = bytes.dropFirst(44).withUnsafeBytes { data in
            (0..<(data.count / 2)).map { Float(Int16(littleEndian: data.loadUnaligned(fromByteOffset: $0 * 2, as: Int16.self))) / 32768 }
        }
        let silence = [Float](repeating: 0, count: WakeWordSettings.sampleRate * 3)
        return silence + speech + silence
    }
    private func run(_ detector: OpenWakeWordDetector, samples: [Float], chunk: Int) throws -> [WakeWordResult] {
        var result: [WakeWordResult] = []
        for start in stride(from: 0, to: samples.count, by: chunk) {
            result += try detector.process(Array(samples[start..<min(start + chunk, samples.count)]))
        }
        return result
    }
    func testPackagedCustomHeyEvenerAndNegativeAtFixedThreshold() throws {
        let engine = try OpenWakeWordDetector(manifestURL: WakeWordModels.bundledManifest())
        XCTAssertEqual(engine.models.phrase, "Hey Evener.")
        XCTAssertEqual(engine.settings.threshold, 0.99)
        XCTAssertEqual(engine.settings.releaseThreshold, 0.5)
        XCTAssertEqual(engine.settings.hopSamples, 1280)
        XCTAssertEqual(engine.settings.holdoffMs, 2500)
        XCTAssertEqual(engine.settings.embeddingFrames, 16)
        let positive = try run(engine, samples: fixture("hey-evener.wav"), chunk: 317)
        engine.reset()
        let negative = try run(engine, samples: fixture("negative-hey-jane.wav"), chunk: 1280)
        let positivePeak = positive.compactMap(\.score).max() ?? 0
        let negativePeak = negative.compactMap(\.score).max() ?? 1
        print("CUSTOM HEY EVENER: threshold=\(engine.settings.threshold), detections=\(positive.filter(\.detected).count), peak=\(positivePeak)")
        print("CUSTOM NEGATIVE: detections=\(negative.filter(\.detected).count), peak=\(negativePeak)")
        XCTAssertEqual(positive.filter(\.detected).count, 1, "custom peak=\(positivePeak)")
        XCTAssertGreaterThanOrEqual(positivePeak, 0.99)
        XCTAssertEqual(negative.filter(\.detected).count, 0, "negative peak=\(negativePeak)")
        XCTAssertLessThan(negativePeak, 0.99)
        XCTAssertTrue(negative.contains { $0.score != nil }, "all three custom models must run")
        XCTAssertEqual(positive.first?.state, .warming)
        XCTAssertEqual(positive.filter(\.detected).first?.state, .holdoff)
        XCTAssertEqual(positive.last?.state, .listening)
        XCTAssertEqual(negative.last?.state, .listening)
    }
    func testSpokenWakeActivatesOnceAndUsesTalkAdmission() throws {
        let engine = try detector()
        let results = try run(engine, samples: fixture("alexa.wav"), chunk: 317)
        XCTAssertEqual(results.first?.state, .warming)
        XCTAssertTrue(results.contains { $0.state == .listening })
        let detections = results.filter(\.detected)
        XCTAssertEqual(detections.count, 1)
        XCTAssertGreaterThan(detections.first?.score ?? 0, 0.5)
        XCTAssertEqual(detections.first?.state, .holdoff)
        XCTAssertEqual(results.last?.state, .listening)
        var controller = VoiceController()
        controller.arm(policy: VoicePolicy()); controller.ready(generation: 1)
        XCTAssertTrue(controller.capture(Data(repeating: 0, count: 480), speech: true, at: 0).isEmpty)
        XCTAssertFalse(controller.cloudMicrophone)
        for result in detections {
            XCTAssertEqual(controller.wake(at: Double(result.sampleOffset) / 16), [.cue, .send(.inputStart(1))])
        }
        XCTAssertEqual(controller.state, .conversing)
        XCTAssertFalse(controller.cloudMicrophone, "wake must still wait for input_started")
        let evidence: [String: Any] = ["state": controller.state.rawValue, "cloudMicrophone": controller.cloudMicrophone,
            "wake": ["state": controller.state == .quiet ? detections[0].state.rawValue : "paused", "phrase": engine.models.phrase, "modelId": engine.models.id,
                     "score": NSNull(), "lastDetectionScore": detections[0].score!,
                     "detections": detections.count, "error": "", "trials": []]]
        try writeEvidence(evidence, name: "wake-activation.json")
        _ = controller.started(sequence: 1)
        XCTAssertTrue(controller.cloudMicrophone)
        XCTAssertTrue(controller.wake(at: 10000).isEmpty, "one phrase cannot reopen an active input")
        _ = controller.disarm()
        XCTAssertTrue(controller.wake(at: 12000).isEmpty)
        print("REAL WAKE: detections=\(detections.count), peak=\(results.compactMap(\.score).max() ?? 0)")
    }
    func testSpokenNegativeNeverActivates() throws {
        let results = try run(detector(), samples: fixture("negative-hey-jane.wav"), chunk: 1280)
        XCTAssertTrue(results.contains { $0.score != nil }, "all three models must actually run")
        XCTAssertEqual(results.filter(\.detected).count, 0)
        XCTAssertLessThan(results.compactMap(\.score).max() ?? 1, 0.5)
        XCTAssertEqual(results.last?.state, .listening)
        print("REAL NEGATIVE: detections=0, peak=\(results.compactMap(\.score).max() ?? 1)")
    }
    func testChunkingResetAndSecondPhrase() throws {
        let engine = try detector(), samples = try fixture("alexa.wav")
        let contiguous = try run(engine, samples: samples, chunk: 1280)
        engine.reset()
        let fragmented = try run(engine, samples: samples, chunk: 511)
        XCTAssertEqual(contiguous.map(\.detected), fragmented.map(\.detected))
        XCTAssertEqual(contiguous.compactMap(\.score), fragmented.compactMap(\.score))
        let second = try run(engine, samples: samples, chunk: 1024)
        XCTAssertEqual(second.filter(\.detected).count, 1)
    }
    func testRealTrialCountsPersistAndRestartDoesNotCountDowntime() throws {
        let suite = "evener.wake-tests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        let ledger = WakeTrialLedger(defaults: defaults), engine = try detector()
        try ledger.start(modelId: engine.models.id, phrase: engine.models.phrase, settings: engine.settings, at: 0)
        let positive = try run(engine, samples: fixture("alexa.wav"), chunk: 1280)
        positive.forEach(ledger.record)
        XCTAssertEqual(ledger.attempts.last?.detections, 1)
        XCTAssertEqual(ledger.attempts.last?.scoredSamples, Int64(positive.filter { $0.score != nil }.count * 1280))
        try ledger.finish(.positive)
        let restored = WakeTrialLedger(defaults: defaults)
        XCTAssertEqual(restored.attempts.last?.label, .positive)
        XCTAssertEqual(restored.attempts.last?.detections, 1)
        engine.reset()
        try restored.start(modelId: engine.models.id, phrase: engine.models.phrase, settings: engine.settings, at: 1)
        try run(engine, samples: fixture("negative-hey-jane.wav"), chunk: 1280).forEach(restored.record)
        restored.save()
        let afterRestart = WakeTrialLedger(defaults: defaults)
        XCTAssertFalse(afterRestart.running)
        XCTAssertEqual(afterRestart.attempts.last?.detections, 0)
        XCTAssertEqual(afterRestart.attempts.last?.interrupted, true)
        XCTAssertEqual(afterRestart.attempts.last?.scoredSamples, restored.attempts.last?.scoredSamples)
        try afterRestart.finish(.negative)
        engine.reset()
        try afterRestart.start(modelId: engine.models.id, phrase: engine.models.phrase, settings: engine.settings, at: 2)
        try run(engine, samples: fixture("negative-hey-jane.wav"), chunk: 1280).forEach(afterRestart.record)
        try afterRestart.finish(.negative)
        try writeEvidence(afterRestart.json, name: "wake-trials.json")
    }
    private func writeEvidence(_ value: Any, name: String) throws {
        // Each check-apple run owns a new output directory. JS tests replay these
        // real detector/controller receipts, not a fake classifier implementation.
        let output = Bundle(for: WakeWordTests.self).bundleURL.deletingLastPathComponent().appendingPathComponent(name)
        try JSONSerialization.data(withJSONObject: value, options: [.sortedKeys, .prettyPrinted]).write(to: output)
    }
    func testModelManifestControlsBatchHopAndThreshold() throws {
        let original = previewManifest()
        var json = try JSONSerialization.jsonObject(with: Data(contentsOf: original)) as! [String: Any]
        for key in ["melspectrogram", "embedding", "classifier"] {
            var asset = json[key] as! [String: Any]
            asset["file"] = original.deletingLastPathComponent().appendingPathComponent(asset["file"] as! String).path
            json[key] = asset
        }
        var settings = json["settings"] as! [String: Any]
        settings["hopSamples"] = 2560
        json["settings"] = settings
        let path = FileManager.default.temporaryDirectory.appendingPathComponent("wake-test-\(UUID().uuidString).json")
        defer { try? FileManager.default.removeItem(at: path) }
        try JSONSerialization.data(withJSONObject: json).write(to: path)
        let batched = try OpenWakeWordDetector(manifestURL: path)
        XCTAssertEqual(try run(batched, samples: fixture("alexa.wav"), chunk: 317).filter(\.detected).count, 1)
        settings["threshold"] = 1.0
        json["settings"] = settings
        try JSONSerialization.data(withJSONObject: json).write(to: path)
        let stricter = try OpenWakeWordDetector(manifestURL: path)
        XCTAssertEqual(try run(stricter, samples: fixture("alexa.wav"), chunk: 317).filter(\.detected).count, 0)
    }
}
