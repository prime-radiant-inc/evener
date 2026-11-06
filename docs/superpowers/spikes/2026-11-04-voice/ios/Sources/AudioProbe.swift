import AVFoundation
import Combine
import Foundation
import UIKit

private enum ProbeTime {
    private static let start = ContinuousClock.now

    static func seconds() -> Double {
        let duration = start.duration(to: ContinuousClock.now).components
        return Double(duration.seconds) + Double(duration.attoseconds) / 1e18
    }
}

// No microphone samples leave the tap. This lock protects counters only, never PCM.
// A production real-time path should use a bounded, nonblocking handoff instead.
final class CaptureCounters: @unchecked Sendable {
    struct Snapshot: Codable {
        var buffers: UInt64 = 0
        var frames: UInt64 = 0
        var firstCallbackSecondsSinceProbeStart: Double?
        var lastCallbackSecondsSinceProbeStart: Double?
        var maximumCallbackGapSeconds: Double = 0
    }
    private let lock = NSLock()
    private var value = Snapshot()

    func received(frames: AVAudioFrameCount) {
        let now = ProbeTime.seconds() // ContinuousClock includes system sleep.
        lock.lock()
        defer { lock.unlock() }
        if let last = value.lastCallbackSecondsSinceProbeStart {
            value.maximumCallbackGapSeconds = max(value.maximumCallbackGapSeconds, now - last)
        }
        if value.firstCallbackSecondsSinceProbeStart == nil { value.firstCallbackSecondsSinceProbeStart = now }
        value.lastCallbackSecondsSinceProbeStart = now
        value.buffers += 1
        value.frames += UInt64(frames)
    }

    func snapshot() -> Snapshot {
        lock.lock()
        defer { lock.unlock() }
        return value
    }
}

// Only synthesized, nonprivate PCM crosses to the main actor. AVSpeechSynthesizer
// owns its callback buffer, so copy it before that callback returns.
final class SynthesizedPCM: @unchecked Sendable {
    let buffer: AVAudioPCMBuffer
    init?(_ source: AVAudioPCMBuffer) {
        guard let copy = AVAudioPCMBuffer(pcmFormat: source.format, frameCapacity: source.frameLength) else { return nil }
        copy.frameLength = source.frameLength
        let src = UnsafeMutableAudioBufferListPointer(source.mutableAudioBufferList)
        let dst = UnsafeMutableAudioBufferListPointer(copy.mutableAudioBufferList)
        for index in src.indices {
            memcpy(dst[index].mData!, src[index].mData!, Int(src[index].mDataByteSize))
        }
        buffer = copy
    }
}

@MainActor
final class AudioProbe: ObservableObject {
    struct Event: Codable {
        let secondsSinceProbeStart: Double
        let kind: String
        let fields: [String: String]
    }

    private struct ObservedNotification: Sendable {
        let interruptionType: UInt?
        let interruptionOptions: UInt
        let routeReason: UInt
        let engineIdentity: ObjectIdentifier?

        init(_ note: Notification) {
            interruptionType = (note.userInfo?[AVAudioSessionInterruptionTypeKey] as? NSNumber)?.uintValue
            interruptionOptions = (note.userInfo?[AVAudioSessionInterruptionOptionKey] as? NSNumber)?.uintValue ?? 0
            routeReason = (note.userInfo?[AVAudioSessionRouteChangeReasonKey] as? NSNumber)?.uintValue ?? 0
            engineIdentity = (note.object as? AVAudioEngine).map(ObjectIdentifier.init)
        }
    }

    @Published private(set) var phase = "disarmed"
    @Published private(set) var lastEvent = "created"
    private(set) var capture = CaptureCounters()
    private var engine: AVAudioEngine?
    private var player: AVAudioPlayerNode?
    private var playbackFormat: AVAudioFormat?
    private var synthesizer: AVSpeechSynthesizer?
    private var speechChunks: [AVAudioPCMBuffer] = []
    private var observers: [NSObjectProtocol] = []
    private var fence = PlaybackFence()
    private var permissionGeneration = 0
    private var scheduled = 0
    private var completed = 0
    private var staleCallbacks = 0
    private var events: [Event] = []
    private var smokeRan = false
    private let session = AVAudioSession.sharedInstance()

    var isArmed: Bool { phase == "armed" }
    var generation: UInt64 { fence.generation }
    var voiceProcessingEnabled: Bool { engine?.inputNode.isVoiceProcessingEnabled ?? false }

    init() {
        observe(AVAudioSession.interruptionNotification) { probe, note in
            let type = note.interruptionType
            let options = note.interruptionOptions
            probe.record("interruption", ["type": String(type ?? 0), "options": String(options)])
            if type == AVAudioSession.InterruptionType.began.rawValue {
                probe.disarm(reason: "interruption, explicit re-arm required")
            }
        }
        observe(AVAudioSession.routeChangeNotification) { probe, note in
            let reason = note.routeReason
            probe.record("route_change", ["reason": String(reason)])
            // Configuration/category notifications during Arm are informational.
            // Changed hardware formats require a fresh, stopped-engine graph.
            if probe.isArmed && (reason == AVAudioSession.RouteChangeReason.oldDeviceUnavailable.rawValue ||
                                 reason == AVAudioSession.RouteChangeReason.newDeviceAvailable.rawValue) {
                probe.disarm(reason: "hardware route changed, explicit re-arm required")
            }
        }
        observe(AVAudioSession.mediaServicesWereResetNotification) { probe, _ in
            probe.disarm(reason: "media services reset, explicit re-arm required")
        }
        observe(.AVAudioEngineConfigurationChange) { probe, note in
            guard let identity = note.engineIdentity, identity == probe.engine.map(ObjectIdentifier.init) else { return }
            probe.record("engine_configuration_change")
            // AVAudioEngine.h forbids deallocation within this notification.
            Task { @MainActor [weak probe] in
                guard let probe, probe.isArmed, probe.engine.map(ObjectIdentifier.init) == identity else { return }
                probe.disarm(reason: "engine configuration changed, explicit re-arm required")
            }
        }
        for (name, kind) in [
            (UIApplication.didEnterBackgroundNotification, "application_background"),
            (UIApplication.willEnterForegroundNotification, "application_foreground"),
            (UIApplication.protectedDataWillBecomeUnavailableNotification, "protected_data_unavailable"),
            (UIApplication.protectedDataDidBecomeAvailableNotification, "protected_data_available")
        ] {
            observe(name) { probe, _ in probe.record(kind) }
        }
        record("created", ["microphone_audio_export": "never", "wake_and_asr": "not_implemented"])
    }

    private func observe(_ name: Notification.Name, action: @escaping @MainActor (AudioProbe, ObservedNotification) -> Void) {
        observers.append(NotificationCenter.default.addObserver(forName: name, object: nil, queue: .main) { [weak self] note in
            let snapshot = ObservedNotification(note)
            MainActor.assumeIsolated {
                guard let self else { return }
                action(self, snapshot)
            }
        })
    }

    func record(_ kind: String, _ fields: [String: String] = [:]) {
        events.append(Event(secondsSinceProbeStart: ProbeTime.seconds(), kind: kind, fields: fields))
        if events.count > 256 { events.removeFirst(events.count - 256) }
        lastEvent = kind
    }

    func arm() {
        guard !isArmed && phase != "permission_pending" else { return }
        guard UIApplication.shared.applicationState == .active else {
            record("arm_refused_not_foreground")
            return
        }
        phase = "permission_pending"
        permissionGeneration += 1
        let request = permissionGeneration
        AVAudioApplication.requestRecordPermission { [weak self] granted in
            Task { @MainActor in
                guard let self, self.permissionGeneration == request else { return }
                guard granted else {
                    self.phase = "permission_denied"
                    self.record("microphone_permission_denied")
                    return
                }
                // Do not start a delayed permission request after the user locks.
                guard UIApplication.shared.applicationState == .active else {
                    self.disarm(reason: "permission completed outside foreground")
                    return
                }
                self.startGraph()
            }
        }
    }

    private func startGraph() {
        phase = "configuring"
        do {
            try session.setCategory(.playAndRecord, mode: .voiceChat, options: [.defaultToSpeaker, .allowBluetoothHFP])
            try session.setActive(true)
            let newEngine = AVAudioEngine()
            engine = newEngine
            // voiceChat alone is NOT AEC. Enable VPIO while stopped, before
            // querying final formats, connecting the player, or installing the tap.
            try newEngine.inputNode.setVoiceProcessingEnabled(true)
            guard newEngine.inputNode.isVoiceProcessingEnabled else { throw ProbeError.voiceProcessingUnavailable }
            let inputFormat = newEngine.inputNode.outputFormat(forBus: 0)
            guard inputFormat.sampleRate > 0, inputFormat.channelCount > 0,
                  let outputFormat = AVAudioFormat(standardFormatWithSampleRate: inputFormat.sampleRate, channels: 1)
            else { throw ProbeError.invalidFormat }
            let newPlayer = AVAudioPlayerNode()
            player = newPlayer
            playbackFormat = outputFormat
            newEngine.attach(newPlayer)
            newEngine.connect(newPlayer, to: newEngine.mainMixerNode, format: outputFormat)
            let counters = CaptureCounters()
            capture = counters
            newEngine.inputNode.installTap(onBus: 0, bufferSize: 1024, format: inputFormat) { buffer, _ in
                counters.received(frames: buffer.frameLength)
            }
            newEngine.prepare()
            try newEngine.start()
            phase = "armed"
            record("graph_started", ["voice_processing": "true", "input_rate": String(inputFormat.sampleRate),
                                     "input_channels": String(inputFormat.channelCount), "pcm_rate": String(outputFormat.sampleRate)])
        } catch {
            recordError("graph_failed", error)
            disarm(reason: "graph failed")
            phase = "failed"
        }
    }

    func stopPlayback(reason: String = "user_stop") {
        fence.invalidate() // Invalidate BEFORE stop invokes any completion callback.
        synthesizer?.stopSpeaking(at: .immediate)
        synthesizer = nil
        speechChunks.removeAll()
        player?.stop()
        record("playback_stop", ["generation": String(fence.generation), "reason": reason])
    }

    func disarm(reason: String = "user_disarm") {
        permissionGeneration += 1
        stopPlayback(reason: reason)
        engine?.stop()
        // Only the successful graph installs a tap. Resetting/discarding the
        // entire engine also handles a partial construction failure.
        engine = nil
        player = nil
        playbackFormat = nil
        phase = "disarmed"
        do { try session.setActive(false, options: .notifyOthersOnDeactivation) }
        catch { recordError("deactivate_failed", error) }
        record("disarmed", ["reason": reason])
    }

    func playSpeech() {
        guard isArmed else { return }
        stopPlayback(reason: "new_synthetic_utterance")
        let generation = fence.generation
        let synth = AVSpeechSynthesizer()
        synthesizer = synth
        let utterance = AVSpeechUtterance(string: "This is a local synthetic speech probe. Press stop while I am speaking. No wake word or speech recognition is running.")
        utterance.voice = AVSpeechSynthesisVoice(language: "en-US")
        record("synthesis_requested", ["generation": String(generation)])
        synth.write(utterance) { [weak self] audio in
            guard let pcm = audio as? AVAudioPCMBuffer else { return }
            let chunk = pcm.frameLength == 0 ? nil : SynthesizedPCM(pcm)
            Task { @MainActor in self?.receiveSpeech(chunk, generation: generation) }
        }
    }

    private func receiveSpeech(_ chunk: SynthesizedPCM?, generation: UInt64) {
        guard fence.accepts(generation) else {
            staleCallbacks += 1
            record("stale_synthesis_rejected", ["generation": String(generation)])
            return
        }
        guard isArmed else { return }
        if let chunk {
            speechChunks.append(chunk.buffer)
            return
        }
        do {
            // Synthesize the fixed utterance entirely, then convert once and
            // play PCM through the SAME engine as the microphone's VPIO unit.
            guard let first = speechChunks.first, let format = playbackFormat,
                  let player, let converter = AVAudioConverter(from: first.format, to: format)
            else { throw ProbeError.noSyntheticPCM }
            let frames = speechChunks.reduce(AVAudioFrameCount(0)) { $0 + $1.frameLength }
            guard let joined = AVAudioPCMBuffer(pcmFormat: first.format, frameCapacity: frames) else { throw ProbeError.invalidFormat }
            joined.frameLength = frames
            let destination = UnsafeMutableAudioBufferListPointer(joined.mutableAudioBufferList)
            var frameOffset = 0
            for buffer in speechChunks {
                guard buffer.format == first.format else { throw ProbeError.invalidFormat }
                let source = UnsafeMutableAudioBufferListPointer(buffer.mutableAudioBufferList)
                for index in source.indices {
                    let bytesPerFrame = Int(first.format.streamDescription.pointee.mBytesPerFrame)
                    memcpy(destination[index].mData!.advanced(by: frameOffset * bytesPerFrame), source[index].mData!, Int(source[index].mDataByteSize))
                }
                frameOffset += Int(buffer.frameLength)
            }
            speechChunks.removeAll()
            let capacity = AVAudioFrameCount(ceil(Double(frames) * format.sampleRate / first.format.sampleRate)) + 4096
            guard let output = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: capacity) else { throw ProbeError.invalidFormat }
            var supplied = false
            var conversionError: NSError?
            let status = converter.convert(to: output, error: &conversionError) { _, inputStatus in
                if supplied { inputStatus.pointee = .endOfStream; return nil }
                supplied = true
                inputStatus.pointee = .haveData
                return joined
            }
            if let conversionError { throw conversionError }
            guard status != .error, output.frameLength > 0 else { throw ProbeError.noSyntheticPCM }
            player.scheduleBuffer(output, completionCallbackType: .dataPlayedBack) { [weak self] _ in
                Task { @MainActor in
                    guard let self else { return }
                    guard self.fence.accepts(generation) else {
                        self.staleCallbacks += 1
                        self.record("stale_completion_rejected", ["generation": String(generation)])
                        return
                    }
                    self.completed += 1
                    self.record("playback_complete", ["generation": String(generation)])
                }
            }
            scheduled += 1
            player.play()
            record("pcm_scheduled", ["frames": String(output.frameLength), "rate": String(format.sampleRate), "generation": String(generation)])
        } catch { recordError("synthesis_failed", error) }
    }

    private func recordError(_ kind: String, _ error: Error) {
        let error = error as NSError
        // No localized error text, paths, device names, route UIDs, or transcripts.
        record(kind, ["domain": error.domain, "code": String(error.code)])
    }

    func evidence() throws -> Data {
        #if targetEnvironment(simulator)
        let runtime = "ios_simulator"
        #else
        let runtime = "physical_ios"
        #endif
        struct Evidence: Encodable {
            let schema: Int
            let runtime: String
            let os: String
            let phase: String
            let engineRunning: Bool
            let voiceProcessingEnabled: Bool
            let generation: UInt64
            let scheduled: Int
            let completed: Int
            let staleCallbacks: Int
            let capture: CaptureCounters.Snapshot
            let inputPorts: [String]
            let outputPorts: [String]
            let sessionSampleRate: Double
            let events: [Event]
        }
        let document = Evidence(schema: 1, runtime: runtime, os: UIDevice.current.systemVersion, phase: phase,
                                engineRunning: engine?.isRunning ?? false, voiceProcessingEnabled: voiceProcessingEnabled,
                                generation: fence.generation, scheduled: scheduled, completed: completed,
                                staleCallbacks: staleCallbacks, capture: capture.snapshot(),
                                inputPorts: session.currentRoute.inputs.map { $0.portType.rawValue },
                                outputPorts: session.currentRoute.outputs.map { $0.portType.rawValue },
                                sessionSampleRate: session.sampleRate, events: events)
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        return try encoder.encode(document)
    }

    // Test mode is deliberately disarmed. It cannot call Arm or request permission.
    // It exercises the real stop and stale-callback path, not audio hardware.
    func runDisarmedSmokeIfRequested() {
        guard !smokeRan, ProcessInfo.processInfo.arguments.contains("--disarmed-smoke") else { return }
        smokeRan = true
        let oldGeneration = fence.generation
        stopPlayback(reason: "disarmed_smoke")
        receiveSpeech(nil, generation: oldGeneration)
        guard engine == nil, !isArmed, capture.snapshot().frames == 0, staleCallbacks == 1 else { exit(1) }
        record("disarmed_smoke_passed")
        do {
            let directory = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
            let file = directory.appendingPathComponent("disarmed-smoke.json")
            try evidence().write(to: file, options: .withoutOverwriting)
            print("DISARMED_SMOKE_PASSED, no microphone permission requested, no audio graph initialized")
            exit(0)
        } catch {
            print("DISARMED_SMOKE_EXPORT_FAILED")
            exit(1)
        }
    }

    private enum ProbeError: Error { case voiceProcessingUnavailable, invalidFormat, noSyntheticPCM }
}
