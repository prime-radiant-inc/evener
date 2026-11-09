import Foundation
import CryptoKit
import onnxruntime_objc

private final class VoiceModelBundle: NSObject {}

/// Direct upstream 16 kHz wrapper: 512 new samples plus 64 recurrent context samples.
/// Called only on the audio worker. State is reset on every engine generation.
final class SileroVAD {
    private let environment: ORTEnv
    private let session: ORTSession
    private var state = [Float](repeating: 0, count: 256)
    private var context = [Float](repeating: 0, count: 64)
    private var pending: [Float] = []
    private(set) var speaking = false

    init() throws {
        let bundle = Bundle(for: VoiceModelBundle.self)
        guard let resource = bundle.url(forResource: "EvenerVoiceModels", withExtension: "bundle"),
              let models = Bundle(url: resource),
              let path = models.url(forResource: "silero_vad", withExtension: "onnx") else { throw VoiceFailure("vad_model_missing") }
        let bytes = try Data(contentsOf: path)
        guard SHA256.hash(data: bytes).map({ String(format: "%02x", $0) }).joined() == "6b99cbfd39246b6706f98ec13c7c50c6b299181f2474fa05cbc8046acc274396" else { throw VoiceFailure("vad_model_invalid") }
        environment = try ORTEnv(loggingLevel: .error)
        let options = try ORTSessionOptions()
        try options.setIntraOpNumThreads(1)
        session = try ORTSession(env: environment, modelPath: path.path, sessionOptions: options)
    }
    func process(_ samples: [Float]) throws -> Bool {
        pending.append(contentsOf: samples)
        while pending.count >= 512 {
            let frame = Array(pending.prefix(512)); pending.removeFirst(512)
            let input = context + frame
            var rate: Int64 = 16000
            let audio = try tensor(input, shape: [1, 576])
            let recurrent = try tensor(state, shape: [2, 1, 128])
            let sampleRate = try ORTValue(tensorData: NSMutableData(bytes: &rate, length: 8), elementType: .int64, shape: [])
            let outputs = try session.run(withInputs: ["input": audio, "state": recurrent, "sr": sampleRate], outputNames: ["output", "stateN"], runOptions: nil)
            guard let score = outputs["output"], let nextState = outputs["stateN"] else { throw VoiceFailure("vad_output_invalid") }
            let probability = try score.tensorData() as Data
            let stateBytes = try nextState.tensorData() as Data
            guard probability.count == 4, stateBytes.count == 1024 else { throw VoiceFailure("vad_shape_invalid") }
            speaking = probability.withUnsafeBytes { $0.loadUnaligned(as: Float.self) } >= 0.5
            state = stateBytes.withUnsafeBytes { bytes in (0..<256).map { bytes.loadUnaligned(fromByteOffset: $0 * 4, as: Float.self) } }
            context = Array(frame.suffix(64))
        }
        return speaking
    }
    private func tensor(_ values: [Float], shape: [NSNumber]) throws -> ORTValue {
        let data = values.withUnsafeBytes { NSMutableData(bytes: $0.baseAddress!, length: $0.count) }
        return try ORTValue(tensorData: data, elementType: .float, shape: shape)
    }
}
