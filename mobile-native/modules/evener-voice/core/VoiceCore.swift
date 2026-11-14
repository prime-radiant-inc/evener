import Foundation

public struct VoiceFailure: Error { public let code: String; public init(_ code: String) { self.code = code } }

/// A complete mono 24 kHz PCM16LE packet, before hardware-rate conversion.
public struct VoicePlaybackPacket {
    private let pcm: Data
    public var sampleCount: Int { pcm.count / 2 }
    public init(_ pcm: Data) throws {
        // Match the hub's decoded PCM byte cap, not one second of audio.
        guard !pcm.isEmpty, pcm.count % 2 == 0, pcm.count <= 64 * 1024 else { throw VoiceFailure("audio_packet_invalid") }
        self.pcm = pcm
    }
    public func copySamples(to channel: UnsafeMutableBufferPointer<Float>) {
        precondition(channel.count >= sampleCount)
        pcm.withUnsafeBytes { bytes in
            for i in 0..<sampleCount { channel[i] = Float(Int16(littleEndian: bytes.loadUnaligned(fromByteOffset: i * 2, as: Int16.self))) / 32768 }
        }
    }
}

public func voiceExpiry(_ value: String) -> Date? {
    let formatter = ISO8601DateFormatter()
    formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    if let date = formatter.date(from: value) { return date }
    formatter.formatOptions = [.withInternetDateTime]
    return formatter.date(from: value)
}

public struct VoicePolicy: Codable, Equatable {
    public var trailingSilenceMs = 600
    public var maxUtteranceMs = 30000
    public var followupMs = 12000
    public var idleInputMs = 30000
    public init() {}
}

public enum VoiceState: String { case disarmed, recovering, quiet, conversing, speaking }
public enum VoiceMessage: Equatable {
    case inputStart(Int), audio(Int, Int, Data), inputEnd(Int)
    case interrupt(Int, [String: Int]), disarm
    public var json: [String: Any] {
        switch self {
        case .inputStart(let sequence): return ["type": "input_start", "sequence": sequence]
        case .audio(let sequence, let frame, let pcm): return ["type": "audio", "sequence": sequence, "frame": frame, "pcm": pcm.base64EncodedString()]
        case .inputEnd(let sequence): return ["type": "input_end", "sequence": sequence]
        case .interrupt(let generation, let samples): return ["type": "interrupt", "generation": generation, "playedSamples": samples]
        case .disarm: return ["type": "disarm"]
        }
    }
}
public enum VoiceAction: Equatable {
    case cue, flushOutput, stopCapture, send(VoiceMessage), abort(Int, String)
}
public enum VoiceSpeechAdmission: Equatable {
    case accepted, rejected([VoiceAction])
}

/// Serialized by the native service's worker, never the audio callback or JS.
public struct VoiceController {
    public private(set) var state: VoiceState = .disarmed
    public private(set) var minimumGeneration = 0
    public private(set) var sequence = 0
    public private(set) var policy = VoicePolicy()
    private var active = false
    private var admitted = false
    private var frame = 0
    private var inputSamples = 0
    private var startedAt: Double = 0
    private var lastSpeech: Double?
    private var followupUntil: Double?
    private var pending: [Data] = []
    public private(set) var bufferedBytes = 0
    public var cloudMicrophone: Bool { active && admitted }
    public var armed: Bool { state != .disarmed }
    public init() {}
    public mutating func arm(policy: VoicePolicy) {
        self = VoiceController()
        self.policy = policy
        state = .recovering
    }
    public mutating func ready(generation: Int) {
        guard armed else { return }
        observeGeneration(generation)
        state = .quiet
    }
    public mutating func observeGeneration(_ generation: Int) { minimumGeneration = max(minimumGeneration, generation) }
    public mutating func synchronizeReady(generation: Int) -> [VoiceAction] {
        guard armed, generation > 0 else { return [] }
        observeGeneration(generation)
        // Acknowledge the playback fence even on a fresh connection. Capture
        // readiness is separate, and the current generation remains eligible.
        return [.send(.interrupt(minimumGeneration - 1, [:]))]
    }
    public mutating func wake(at now: Double) -> [VoiceAction] {
        guard state == .quiet else { return [] }
        return talk(at: now)
    }
    public mutating func talk(at now: Double) -> [VoiceAction] {
        guard state == .quiet || state == .conversing || state == .speaking, !active else { return [] }
        sequence += 1; frame = 0; active = true; admitted = false
        startedAt = now; lastSpeech = nil; followupUntil = nil
        pending.removeAll(); bufferedBytes = 0
        state = .conversing
        return [.cue, .send(.inputStart(sequence))]
    }
    public mutating func started(sequence: Int) -> [VoiceAction] {
        guard active, !admitted, self.sequence == sequence else { return [] }
        admitted = true
        let actions = pending.map { pcm -> VoiceAction in
            defer { frame += 1 }
            return .send(.audio(sequence, frame, pcm))
        }
        pending.removeAll(); bufferedBytes = 0
        return actions
    }
    public mutating func capture(_ pcm: Data, speech: Bool, at now: Double) -> [VoiceAction] {
        capture({ pcm }, speech: speech, at: now)
    }
    /// The device's per-block entry (#5683): the block's PCM is built only
    /// when this call pulls it, so an armed-quiet path that cannot consume
    /// audio never pays for the conversion that produced it. A nil pull means
    /// the device produced no block; it is dropped without touching turn
    /// state, and the device reports the failure on its own channel once
    /// this call returns.
    public mutating func capture(_ supply: () -> Data?, speech: Bool, at now: Double) -> [VoiceAction] {
        guard armed, state != .recovering else { return [] }
        guard active else {
            // Follow-up onset owns only the current frame. A block that
            // cannot start a turn is never pulled (#5683).
            guard speech, state == .speaking || (followupUntil.map { now < $0 } ?? false) else { return [] }
            guard let onset = supply() else { return [] }
            let actions = talk(at: now)
            return actions + capture(onset, speech: speech, at: now)
        }
        guard now >= startedAt else { return [] }
        guard let pcm = supply() else { return [] }
        if speech { lastSpeech = now }
        var actions: [VoiceAction] = []
        // Include pending audio in the hub's 24 kHz mono PCM16 budget. Tap
        // timestamps mark block starts, so elapsed time alone can overshoot.
        let maxSamples = policy.maxUtteranceMs * 24
        let samples = min(pcm.count / 2, maxSamples - inputSamples)
        let trimmed = Data(pcm.prefix(samples * 2))
        if admitted {
            actions.append(.send(.audio(sequence, frame, trimmed))); frame += 1
        } else {
            guard bufferedBytes + trimmed.count <= 96000 else { return abort(reason: "input_overflow") }
            pending.append(trimmed); bufferedBytes += trimmed.count
        }
        inputSamples += samples
        if inputSamples == maxSamples, lastSpeech == nil { return actions + abort(reason: "input_idle") }
        if let lastSpeech, now - lastSpeech >= Double(policy.trailingSilenceMs) || now - startedAt >= Double(policy.maxUtteranceMs) || inputSamples == maxSamples {
            // Acknowledgement must precede input_end too. An unacknowledged partial turn is discarded.
            if admitted { actions.append(.send(.inputEnd(sequence))); clearInput() }
            else { return abort(reason: "input_start_timeout") }
        }
        return actions
    }
    public mutating func tick(at now: Double) -> [VoiceAction] {
        if active, lastSpeech == nil, now - startedAt >= Double(policy.idleInputMs) { return abort(reason: "input_idle") }
        if !active, let until = followupUntil, now >= until { followupUntil = nil; state = .quiet }
        return []
    }
    public mutating func abort(reason: String) -> [VoiceAction] {
        guard active else { return [] }
        let result: [VoiceAction] = [.abort(sequence, reason)]
        clearInput(); followupUntil = nil
        if armed { state = .quiet }
        return result
    }
    private mutating func clearInput() {
        active = false; admitted = false; pending.removeAll(); bufferedBytes = 0; inputSamples = 0
    }
    public mutating func admitSpeech(generation: Int, speakerConsent: Bool) -> VoiceSpeechAdmission {
        guard armed, generation >= minimumGeneration else { return .rejected([]) }
        guard speakerConsent, state != .recovering else {
            // The hub must retire this unplayed delivery before selecting another.
            // Fence its incoming generation, not the previous local output's.
            observeGeneration(generation)
            return .rejected(stopSpeaking(played: [:]))
        }
        return acceptSpeech(generation: generation) ? .accepted : .rejected([])
    }
    public mutating func acceptSpeech(generation: Int) -> Bool {
        guard armed, state != .recovering, generation >= minimumGeneration else { return false }
        minimumGeneration = generation; followupUntil = nil; state = .speaking
        return true
    }
    public mutating func stopSpeaking(played: [String: Int]) -> [VoiceAction] {
        guard armed else { return [] }
        let retired = minimumGeneration
        minimumGeneration += 1
        if state == .speaking { state = .conversing }
        return [.flushOutput, .send(.interrupt(retired, played))]
    }
    public mutating func remoteStop(generation: Int) {
        minimumGeneration = max(minimumGeneration, generation)
        if state == .speaking { state = .quiet }
    }
    public mutating func outputDrained(at now: Double) {
        guard armed else { return }
        followupUntil = now + Double(policy.followupMs); state = .conversing
    }
    public mutating func recovering(advanceGeneration: Bool = true) -> [VoiceAction] {
        guard armed else { return [] }
        let actions = abort(reason: "capture_interrupted")
        state = .recovering
        if advanceGeneration { minimumGeneration += 1 }
        followupUntil = nil
        return actions + [.flushOutput]
    }
    public mutating func disarm() -> [VoiceAction] {
        clearInput(); followupUntil = nil
        minimumGeneration += 1; state = .disarmed
        return [.flushOutput, .stopCapture, .send(.disarm)]
    }
}

public struct PlayedReceipt: Equatable {
    public let delivery: String
    public let generation: Int
    public let item: String
    public let samples: Int
    public let complete: Bool
}
public enum PlaybackError: Error { case invalid, overflow, itemOrder }
/// Maps the physical player's rendered clock to the provider's per-item 24 kHz timeline.
/// Scheduling and provider completion alone never advance the rendered watermark.
public struct PlaybackTimeline {
    private struct Chunk {
        var delivery: String; var generation: Int; var item: String
        var start: Int; var end: Int; var providerSamples: Int
    }
    private var chunks: [Chunk] = []
    private var ended: Set<String> = []
    private var completed: Set<String> = []
    private var deliveryEnded = false
    private var drainedEnd = 0
    public private(set) var scheduledOutputSamples = 0
    public init() {}
    public mutating func append(delivery: String, generation: Int, item: String, providerSamples: Int, outputSamples: Int, outputRate: Double, outputStart: Int? = nil) throws {
        guard providerSamples > 0, outputSamples > 0, outputRate > 0, !ended.contains(item) else { throw PlaybackError.invalid }
        guard chunks.count < 8192, ended.count < 128 else { throw PlaybackError.overflow }
        // Physical drains release queue capacity even while the provider item
        // remains open. Keep drained chunks for cumulative played evidence and
        // provider-gated receipts, but charge only buffers not yet fully drained.
        guard chunks.filter({ $0.end > drainedEnd }).reduce(providerSamples, { $0 + $1.providerSamples }) <= 720000 else { throw PlaybackError.overflow }
        if chunks.contains(where: { $0.item == item }), chunks.last?.item != item { throw PlaybackError.itemOrder }
        let start = max(scheduledOutputSamples, outputStart ?? scheduledOutputSamples)
        chunks.append(Chunk(delivery: delivery, generation: generation, item: item, start: start, end: start + outputSamples, providerSamples: providerSamples))
        scheduledOutputSamples = start + outputSamples
    }
    public func watermarks(renderedOutputSamples: Int) -> [String: Int] {
        var result: [String: Int] = [:]
        for chunk in chunks {
            let rendered = min(chunk.end - chunk.start, max(0, max(drainedEnd, renderedOutputSamples) - chunk.start))
            result[chunk.item, default: 0] += Int(Int64(rendered) * Int64(chunk.providerSamples) / Int64(chunk.end - chunk.start))
        }
        return result
    }
    public mutating func providerEnded(item: String) -> PlayedReceipt? { ended.insert(item); return receipt(item: item) }
    public mutating func drained(item: String, outputEnd: Int) -> PlayedReceipt? {
        drainedEnd = max(drainedEnd, outputEnd)
        return receipt(item: item)
    }
    private mutating func receipt(item: String) -> PlayedReceipt? {
        guard ended.contains(item), !completed.contains(item), let last = chunks.last(where: { $0.item == item }), drainedEnd >= last.end else { return nil }
        completed.insert(item)
        return PlayedReceipt(delivery: last.delivery, generation: last.generation, item: item, samples: watermarks(renderedOutputSamples: drainedEnd)[item] ?? 0, complete: true)
    }
    public mutating func endDelivery() { deliveryEnded = true }
    public var allDrained: Bool { deliveryEnded && chunks.allSatisfy { completed.contains($0.item) } }
    public var isEmpty: Bool {
        chunks.isEmpty
    }
    public mutating func clear() { self = PlaybackTimeline() }
}
