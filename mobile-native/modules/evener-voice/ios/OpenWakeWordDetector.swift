import Foundation
import CryptoKit
import onnxruntime_objc
#if canImport(EvenerVoiceCore)
import EvenerVoiceCore
#endif

private final class WakeModelBundle: NSObject {}

/// Paths are relative to the manifest, not a training-lane artifact convention.
struct WakeWordModels: Codable {
    struct Asset: Codable {
        let file: String
        let sha256: String
        func verifiedURL(relativeTo manifest: URL) throws -> URL {
            let url = URL(fileURLWithPath: file, relativeTo: manifest.deletingLastPathComponent()).standardizedFileURL
            let bytes = try Data(contentsOf: url)
            guard SHA256.hash(data: bytes).map({ String(format: "%02x", $0) }).joined() == sha256 else { throw VoiceFailure("wake_model_invalid") }
            return url
        }
    }
    let id: String
    let phrase: String
    let settings: WakeWordSettings
    let melspectrogram: Asset
    let embedding: Asset
    let classifier: Asset

    static func bundledManifest() throws -> URL {
        let bundle = Bundle(for: WakeModelBundle.self)
        guard let resource = bundle.url(forResource: "EvenerVoiceModels", withExtension: "bundle"),
              let models = Bundle(url: resource), let path = models.url(forResource: "wake-models", withExtension: "json") else { throw VoiceFailure("wake_model_missing") }
        return path
    }
    static func read(_ url: URL) throws -> WakeWordModels {
        let value = try JSONDecoder().decode(Self.self, from: Data(contentsOf: url))
        try value.settings.validate()
        return value
    }
}

/// openWakeWord v0.6 streaming features, CPU ONNX Runtime 1.20.0.
/// Unlike upstream's random initial embeddings, we wait for real context (about
/// two seconds). No classification is possible against fabricated startup audio.
final class OpenWakeWordDetector: WakeWordDetecting {
    let settings: WakeWordSettings
    let models: WakeWordModels
    private let environment: ORTEnv
    private let mel: WakeONNXModel
    private let embedding: WakeONNXModel
    private let classifier: WakeONNXModel
    private var pending: [Float] = []
    private var raw: [Float] = []
    private var mels: [Float] = []
    private var features: [Float] = []
    private var samplesSeen: Int64 = 0
    private var trigger = WakeWordTrigger()
    private let melBins = 32
    private let melWindow = 76
    private let embeddingWidth = 96
    private let melContextSamples = 480 // three 10 ms mel hops

    init(manifestURL: URL) throws {
        models = try WakeWordModels.read(manifestURL)
        settings = models.settings
        environment = try ORTEnv(loggingLevel: .error)
        mel = try WakeONNXModel(environment, path: models.melspectrogram.verifiedURL(relativeTo: manifestURL))
        embedding = try WakeONNXModel(environment, path: models.embedding.verifiedURL(relativeTo: manifestURL))
        classifier = try WakeONNXModel(environment, path: models.classifier.verifiedURL(relativeTo: manifestURL))
    }
    func reset() {
        pending.removeAll(); raw.removeAll(); mels.removeAll(); features.removeAll()
        samplesSeen = 0; trigger = WakeWordTrigger()
    }
    func process(_ samples: [Float]) throws -> [WakeWordResult] {
        pending.append(contentsOf: samples)
        var results: [WakeWordResult] = []
        while pending.count >= settings.hopSamples {
            // Models expect PCM16 magnitudes, not normalized Float microphone data.
            raw.append(contentsOf: pending.prefix(settings.hopSamples).map { Float(Int16(max(-32768, min(32767, $0 * 32768)))) })
            pending.removeFirst(settings.hopSamples)
            raw = Array(raw.suffix(settings.hopSamples + melContextSamples))
            let spectrum = try mel.run(raw, shape: [1, NSNumber(value: raw.count)])
            guard spectrum.count % melBins == 0 else { throw VoiceFailure("wake_mel_shape_invalid") }
            mels.append(contentsOf: spectrum.map { $0 / 10 + 2 })
            let strides = settings.hopSamples / WakeWordSettings.embeddingStride
            for step in 0..<strides {
                samplesSeen += Int64(WakeWordSettings.embeddingStride)
                let end = mels.count - (strides - step - 1) * 8 * melBins
                guard end >= melWindow * melBins else {
                    results.append(WakeWordResult(state: .warming, score: nil, detected: false, sampleOffset: samplesSeen)); continue
                }
                let window = Array(mels[(end - melWindow * melBins)..<end])
                let vector = try embedding.run(window, shape: [1, 76, 32, 1])
                guard vector.count == embeddingWidth else { throw VoiceFailure("wake_embedding_shape_invalid") }
                features.append(contentsOf: vector)
                features = Array(features.suffix(settings.embeddingFrames * embeddingWidth))
                guard features.count == settings.embeddingFrames * embeddingWidth else {
                    results.append(WakeWordResult(state: .warming, score: nil, detected: false, sampleOffset: samplesSeen)); continue
                }
                let prediction = try classifier.run(features, shape: [1, NSNumber(value: settings.embeddingFrames), 96])
                // ARM's optimized sigmoid produced -5.9604645e-08 for real
                // negative audio. Accept only one Float ULP of endpoint roundoff.
                guard prediction.count == 1, let rawScore = prediction.first, rawScore.isFinite,
                      rawScore >= -Float.ulpOfOne, rawScore <= 1 + Float.ulpOfOne else { throw VoiceFailure("wake_score_invalid") }
                let score = max(0, min(1, rawScore))
                results.append(trigger.evaluate(score: score, sampleOffset: samplesSeen, settings: settings))
            }
            mels = Array(mels.suffix(melWindow * melBins))
        }
        return results
    }
}

private final class WakeONNXModel {
    private let session: ORTSession
    private let input: String
    private let output: String
    init(_ environment: ORTEnv, path: URL) throws {
        let options = try ORTSessionOptions()
        try options.setIntraOpNumThreads(1)
        session = try ORTSession(env: environment, modelPath: path.path, sessionOptions: options)
        let inputs = try session.inputNames(), outputs = try session.outputNames()
        guard inputs.count == 1, outputs.count == 1 else { throw VoiceFailure("wake_model_shape_invalid") }
        input = inputs[0]; output = outputs[0]
    }
    func run(_ values: [Float], shape: [NSNumber]) throws -> [Float] {
        let data = values.withUnsafeBytes { NSMutableData(bytes: $0.baseAddress!, length: $0.count) }
        let tensor = try ORTValue(tensorData: data, elementType: .float, shape: shape)
        let result = try session.run(withInputs: [input: tensor], outputNames: [output], runOptions: nil)
        guard let value = result[output] else { throw VoiceFailure("wake_output_missing") }
        let bytes = try value.tensorData() as Data
        guard bytes.count % 4 == 0 else { throw VoiceFailure("wake_output_invalid") }
        return bytes.withUnsafeBytes { data in (0..<(data.count / 4)).map { data.loadUnaligned(fromByteOffset: $0 * 4, as: Float.self) } }
    }
}
