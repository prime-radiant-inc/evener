import Foundation

public enum WakeTrialLabel: String, Codable { case positive, negative, falseActivation }
public struct WakeTrialAttempt: Codable {
    public let id: String
    public let modelId: String
    public let phrase: String
    public let settings: WakeWordSettings
    public let startedAt: Double
    public var running = true
    public var interrupted = false
    public var label: WakeTrialLabel?
    public var detections = 0
    public var lastScore: Float?
    public var scoredSamples: Int64 = 0
    public var droppedSamples: Int64 = 0
}

/// Counts survive JS suspension. Only scores/counts are persisted, never PCM.
public final class WakeTrialLedger {
    // Keep the latest qualification runs without unbounded storage/bridge payloads.
    private static let maximumAttempts = 256
    private let defaults: UserDefaults
    private let key = "evener.voice.wakeTrials.v1"
    public private(set) var attempts: [WakeTrialAttempt]
    /// Bumped by every mutation, never by reads: the snapshot diff compares
    /// this instead of the trials payload (#5684), and the JSON cache keys on
    /// it (#5683).
    public private(set) var revision = 0
    // The bridge payload encoder, injectable so a test can count encodes.
    private let encode: ([WakeTrialAttempt]) -> [[String: Any]]
    private var cachedJSON: [[String: Any]]?
    public var running: Bool { attempts.last?.running == true }
    public init(defaults: UserDefaults = .standard,
                encode: (([WakeTrialAttempt]) -> [[String: Any]])? = nil) {
        self.defaults = defaults
        self.encode = encode ?? Self.encodeAttemptsJSON
        attempts = defaults.data(forKey: key).flatMap { try? JSONDecoder().decode([WakeTrialAttempt].self, from: $0) } ?? []
        // A process restart is not evidence that the microphone kept running.
        interrupt()
        if attempts.count > Self.maximumAttempts { save() }
    }
    public func start(modelId: String, phrase: String, settings: WakeWordSettings, at: Double) throws {
        guard !running, attempts.last?.label != nil || attempts.isEmpty else { throw VoiceFailure("wake_trial_unmarked") }
        attempts.append(WakeTrialAttempt(id: UUID().uuidString, modelId: modelId, phrase: phrase, settings: settings, startedAt: at))
        mutated(); save()
    }
    public func record(_ result: WakeWordResult) {
        guard running, result.score != nil || result.detected else { return }
        let index = attempts.count - 1
        if let score = result.score {
            attempts[index].lastScore = score
            attempts[index].scoredSamples += Int64(WakeWordSettings.embeddingStride)
        }
        if result.detected { attempts[index].detections += 1; save() }
        mutated()
    }
    public func dropped(_ samples: Int) {
        guard running else { return }
        attempts[attempts.count - 1].droppedSamples += Int64(samples)
        mutated()
    }
    public func finish(_ label: WakeTrialLabel) throws {
        guard !attempts.isEmpty, attempts.last?.label == nil else { throw VoiceFailure("wake_trial_missing") }
        attempts[attempts.count - 1].running = false
        attempts[attempts.count - 1].label = label
        mutated(); save()
    }
    public func interrupt() {
        guard running else { return }
        attempts[attempts.count - 1].running = false
        attempts[attempts.count - 1].interrupted = true
        mutated(); save()
    }
    public func save() {
        if attempts.count > Self.maximumAttempts { attempts.removeFirst(attempts.count - Self.maximumAttempts); mutated() }
        defaults.set(try? JSONEncoder().encode(attempts), forKey: key)
    }
    private func mutated() {
        revision += 1
        cachedJSON = nil
    }
    /// The bridge payload, cached between mutations (#5683). Armed-quiet
    /// snapshot builds read it with nothing mutating in between, so the cache
    /// holds; while a trial runs, records mutate at inference rate and the
    /// twice-a-second publishes re-encode no more often than main did.
    public var json: [[String: Any]] {
        if let cached = cachedJSON { return cached }
        let payload = encode(attempts)
        cachedJSON = payload
        return payload
    }

    private static func encodeAttemptsJSON(_ attempts: [WakeTrialAttempt]) -> [[String: Any]] {
        guard let data = try? JSONEncoder().encode(attempts) else { return [] }
        return (try? JSONSerialization.jsonObject(with: data)) as? [[String: Any]] ?? []
    }
}
