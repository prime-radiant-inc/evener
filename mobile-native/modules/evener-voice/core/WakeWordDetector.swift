import Foundation

/// Model-independent boundary. Input is mono 16 kHz Float PCM in [-1, 1].
/// The caller owns serialization and must never call inference on the audio tap.
public protocol WakeWordDetecting: AnyObject {
    var settings: WakeWordSettings { get }
    func process(_ samples: [Float]) throws -> [WakeWordResult]
    func reset()
}

public struct WakeWordSettings: Codable {
    public var threshold: Float
    public var releaseThreshold: Float
    /// Batch size, a multiple of openWakeWord's trained 80 ms embedding stride.
    public var hopSamples: Int
    public var holdoffMs: Int
    public var embeddingFrames: Int
    public static let sampleRate = 16000
    public static let embeddingStride = 1280
    public func validate() throws {
        guard threshold > 0, threshold <= 1, releaseThreshold >= 0, releaseThreshold < threshold,
              hopSamples >= Self.embeddingStride, hopSamples <= Self.sampleRate,
              hopSamples % Self.embeddingStride == 0, holdoffMs >= 0,
              embeddingFrames > 0, embeddingFrames <= 120 else { throw VoiceFailure("wake_configuration_invalid") }
    }
}

public struct WakeWordResult {
    public enum State: String { case warming, listening, holdoff }
    public let state: State
    public let score: Float?
    public let detected: Bool
    public let sampleOffset: Int64
}

/// Hysteresis and sample-clock holdoff prevent a sustained phrase from retriggering.
public struct WakeWordTrigger {
    private var released = true
    private var nextAllowed: Int64 = 0
    public init() {}
    public mutating func evaluate(score: Float, sampleOffset: Int64, settings: WakeWordSettings) -> WakeWordResult {
        if score < settings.releaseThreshold { released = true }
        let detected = released && sampleOffset >= nextAllowed && score >= settings.threshold
        if detected {
            released = false
            nextAllowed = sampleOffset + Int64(settings.holdoffMs) * Int64(WakeWordSettings.sampleRate) / 1000
        }
        return WakeWordResult(state: !released || sampleOffset < nextAllowed ? .holdoff : .listening,
                              score: score, detected: detected, sampleOffset: sampleOffset)
    }
}
