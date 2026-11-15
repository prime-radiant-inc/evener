import AVFoundation
import Foundation

/// One voice-processed engine owns both microphone and provider playback.
final class VoiceAudioDevice {
    private let worker: DispatchQueue
    private let slots = DispatchSemaphore(value: 8)
    private var engine = AVAudioEngine()
    private var player = AVAudioPlayerNode()
    private var input24: AVAudioConverter?
    private var input16: AVAudioConverter?
    private var outputConverter: AVAudioConverter?
    private var outputFormat: AVAudioFormat?
    private var detector: SileroVAD?
    private var wakeStream: WakeWordStream?
    private(set) var wakeListening = false
    private(set) var wakeModels: WakeWordModels?
    private var engineGeneration = 0
    private var outputGeneration = 0
    private var overflow: DispatchSourceUserDataAdd?
    private var timeline = PlaybackTimeline()
    private var itemInfo: [String: (String, Int)] = [:]
    private var outputOrigin: AVAudioFramePosition = 0
    private var observers: [NSObjectProtocol] = []
    private(set) var capturing = false
    private(set) var running = false
    private(set) var lastCaptureAt: Double = 0
    private(set) var route = ""
    var capture: (((() -> Data?), Bool, Double) -> Void)?
    var failed: ((String) -> Void)?
    var interrupted: (() -> Void)?
    var recover: ((_ publicRoute: Bool) -> Void)?
    var played: ((PlayedReceipt) -> Void)?
    var outputDrained: (() -> Void)?
    var wakeResult: ((WakeWordResult) -> Void)?
    var wakeFailed: ((String, Int) -> Void)?
    init(worker: DispatchQueue) {
        self.worker = worker
        let center = NotificationCenter.default
        observers.append(center.addObserver(forName: AVAudioSession.interruptionNotification, object: nil, queue: nil) { [weak self] note in
            guard let self else { return }
            self.player.stop()
            let kind = (note.userInfo?[AVAudioSessionInterruptionTypeKey] as? NSNumber)?.uintValue
            let options = (note.userInfo?[AVAudioSessionInterruptionOptionKey] as? NSNumber)?.uintValue ?? 0
            self.worker.async {
                if kind == AVAudioSession.InterruptionType.began.rawValue { self.interrupted?() }
                else if options & AVAudioSession.InterruptionOptions.shouldResume.rawValue != 0 { self.recover?(false) }
            }
        })
        observers.append(center.addObserver(forName: AVAudioSession.routeChangeNotification, object: nil, queue: nil) { [weak self] note in
            guard let self else { return }
            let previous = note.userInfo?[AVAudioSessionRouteChangePreviousRouteKey] as? AVAudioSessionRouteDescription
            let current = AVAudioSession.sharedInstance().currentRoute
            if let previous, previous.outputs.map(\.uid) == current.outputs.map(\.uid), previous.inputs.map(\.uid) == current.inputs.map(\.uid) { return }
            // Stop before queueing any graph rebuild. Never let an old headset tail continue on speaker.
            self.player.stop()
            let wasPrivate = previous?.outputs.contains { $0.portType != .builtInSpeaker && $0.portType != .builtInReceiver } ?? false
            self.worker.async {
                self.recover?(wasPrivate)
            }
        })
        observers.append(center.addObserver(forName: AVAudioSession.mediaServicesWereResetNotification, object: nil, queue: nil) { [weak self] _ in
            self?.worker.async { self?.recover?(false) }
        })
    }
    deinit { observers.forEach(NotificationCenter.default.removeObserver) }

    func start() throws {
        stop()
        let session = AVAudioSession.sharedInstance()
        try session.setCategory(.playAndRecord, mode: .voiceChat, options: [.defaultToSpeaker])
        try session.setActive(true)
        guard session.currentRoute.inputs.allSatisfy({ $0.portType == .builtInMic }),
              session.currentRoute.outputs.allSatisfy({ $0.portType == .builtInSpeaker || $0.portType == .builtInReceiver }),
              !session.currentRoute.inputs.isEmpty else { throw VoiceFailure("route_unqualified") }
        route = session.currentRoute.outputs.map(\.portName).joined(separator: ", ")
        // One engine for the device's lifetime: replacing it here deallocates the
        // previous AVAudioEngine, and its AURemoteIO teardown can deadlock the
        // audio RPC and abort the app.
        #if !targetEnvironment(simulator)
        // Voice processing is a device path; the simulator's AURemoteIO can time
        // out just initializing it.
        try engine.inputNode.setVoiceProcessingEnabled(true)
        #endif
        engine.prepare()
        let hardwareInput = engine.inputNode.outputFormat(forBus: 0)
        let hardwareOutput = engine.outputNode.inputFormat(forBus: 0)
        guard hardwareInput.sampleRate > 0, hardwareInput.channelCount > 0,
              hardwareOutput.sampleRate > 0,
              let mono24 = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 24000, channels: 1, interleaved: false),
              let mono16 = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 16000, channels: 1, interleaved: false),
              let output = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: hardwareOutput.sampleRate, channels: 1, interleaved: false) else { throw VoiceFailure("audio_format_invalid") }
        input24 = AVAudioConverter(from: hardwareInput, to: mono24)
        input16 = AVAudioConverter(from: hardwareInput, to: mono16)
        outputConverter = AVAudioConverter(from: mono24, to: output)
        outputFormat = output
        detector = try SileroVAD()
        let wake = try OpenWakeWordDetector(manifestURL: WakeWordModels.bundledManifest())
        wakeModels = wake.models
        let stream = WakeWordStream(detector: wake, receiver: worker)
        let wakeGeneration = engineGeneration
        stream.received = { [weak self] result in
            guard let self, self.wakeListening, self.engineGeneration == wakeGeneration else { return }
            self.wakeResult?(result)
        }
        stream.failed = { [weak self] reason, samples in
            guard let self, self.wakeListening, self.engineGeneration == wakeGeneration else { return }
            self.wakeFailed?(reason, samples)
        }
        wakeStream = stream
        engine.attach(player)
        engine.connect(player, to: engine.mainMixerNode, format: output)
        let generation = engineGeneration
        let drops = DispatchSource.makeUserDataAddSource(queue: worker)
        drops.setEventHandler { [weak self] in
            guard let self, self.engineGeneration == generation else { return }
            self.wakeStream?.reset()
            self.failed?("capture_overflow")
        }
        drops.resume(); overflow = drops
        let buffers = try (0..<8).map { _ -> AVAudioPCMBuffer in
            guard let buffer = AVAudioPCMBuffer(pcmFormat: hardwareInput, frameCapacity: 8192) else { throw VoiceFailure("audio_buffer_missing") }
            return buffer
        }
        var nextBuffer = 0 // The input tap is serial, as is the FIFO consumer.
        engine.inputNode.installTap(onBus: 0, bufferSize: 1024, format: hardwareInput) { [weak self] buffer, time in
            guard let self else { return }
            guard buffer.frameLength <= 8192 else { drops.add(data: 1); return }
            guard self.slots.wait(timeout: .now()) == .success else { drops.add(data: 1); return }
            let copy = buffers[nextBuffer]
            nextBuffer = (nextBuffer + 1) % buffers.count
            copy.frameLength = buffer.frameLength
            let source = UnsafeMutableAudioBufferListPointer(buffer.mutableAudioBufferList)
            let target = UnsafeMutableAudioBufferListPointer(copy.mutableAudioBufferList)
            for index in source.indices {
                if let from = source[index].mData, let to = target[index].mData { memcpy(to, from, Int(source[index].mDataByteSize)) }
            }
            let at = time.isHostTimeValid ? AVAudioTime.seconds(forHostTime: time.hostTime) * 1000 : ProcessInfo.processInfo.systemUptime * 1000
            self.worker.async {
                defer { self.slots.signal() }
                guard self.running, generation == self.engineGeneration else { return }
                do {
                    let vad = try self.convert(copy, with: self.input16)
                    guard let vadSamples = vad.floatChannelData?[0], let detector = self.detector else { throw VoiceFailure("audio_format_invalid") }
                    let mono16 = Array(UnsafeBufferPointer(start: vadSamples, count: Int(vad.frameLength)))
                    let speech = try detector.process(mono16)
                    if self.wakeListening { self.wakeStream?.submit(mono16) }
                    self.capturing = true
                    self.lastCaptureAt = ProcessInfo.processInfo.systemUptime * 1000
                    // The hub PCM conversion is pulled, not pushed (#5683): an
                    // armed-quiet block that captures nothing never converts to
                    // 24 kHz and never builds PCM bytes. The pull stays pure - a
                    // failed conversion stashes its code and pulls nil, which
                    // the controller drops; the failure is reported only after
                    // capture returns, because the failed handler re-enters the
                    // controller (abort, pause, publish) and must not overlap
                    // the mutating call that pulled.
                    var pullFailed: String?
                    self.capture?({
                        do {
                            let input = try self.convert(copy, with: self.input24)
                            guard let samples = input.floatChannelData?[0] else { throw VoiceFailure("audio_format_invalid") }
                            var pcm = [Int16](repeating: 0, count: Int(input.frameLength))
                            for i in pcm.indices { pcm[i] = Int16(max(-32768, min(32767, samples[i] * 32767))).littleEndian }
                            return pcm.withUnsafeBytes { Data($0) }
                        } catch {
                            pullFailed = self.failureCode(error)
                            return nil
                        }
                    }, speech, at)
                    if let code = pullFailed { self.failed?(code) }
                } catch { self.failed?(self.failureCode(error)) }
            }
        }
        engine.prepare(); try engine.start()
        lastCaptureAt = ProcessInfo.processInfo.systemUptime * 1000
        running = true
    }
    func stop() {
        engineGeneration += 1; capturing = false; running = false
        wakeListening = false; wakeStream?.reset(); wakeStream = nil
        flushOutput()
        engine.stop()
        engine.inputNode.removeTap(onBus: 0)
        overflow?.cancel(); overflow = nil
        detector = nil; input24 = nil; input16 = nil; outputConverter = nil
    }
    @discardableResult func setWakeListening(_ enabled: Bool) -> Bool {
        guard enabled != wakeListening else { return false }
        wakeListening = enabled
        wakeStream?.reset()
        return true
    }
    func resetWake() { wakeStream?.reset() }
    func deactivate() {
        stop()
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
    }
    func flushOutput() {
        outputGeneration += 1
        player.stop(); timeline.clear(); itemInfo.removeAll(); outputOrigin = 0
        outputConverter?.reset()
    }
    func watermark() -> [String: Int] {
        guard let time = player.lastRenderTime, let rendered = player.playerTime(forNodeTime: time), let format = outputFormat else { return timeline.watermarks(renderedOutputSamples: 0) }
        // The render callback runs ahead of the speaker. Subtract measured presentation latency.
        let latency = Int(engine.outputNode.presentationLatency * format.sampleRate)
        return timeline.watermarks(renderedOutputSamples: max(0, Int(rendered.sampleTime - outputOrigin) - latency))
    }
    func reportProgress() {
        // An empty timeline owes no progress (#5683): skip the player render
        // clock queries that idle time would otherwise pay every 200 ms.
        guard !timeline.isEmpty else { return }
        for (item, samples) in watermark() {
            if let (delivery, generation) = itemInfo[item] { played?(PlayedReceipt(delivery: delivery, generation: generation, item: item, samples: samples, complete: false)) }
        }
    }
    func enqueue(pcm: Data, delivery: String, generation: Int, item: String) throws {
        guard capturing else { throw VoiceFailure("audio_packet_invalid") }
        let packet = try VoicePlaybackPacket(pcm)
        guard let format = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 24000, channels: 1, interleaved: false),
              let source = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: AVAudioFrameCount(packet.sampleCount)), let channel = source.floatChannelData?[0] else { throw VoiceFailure("audio_packet_invalid") }
        source.frameLength = source.frameCapacity
        packet.copySamples(to: UnsafeMutableBufferPointer(start: channel, count: Int(source.frameLength)))
        let output = try convert(source, with: outputConverter)
        let rendered = player.lastRenderTime.flatMap { player.playerTime(forNodeTime: $0) }
        let clock = rendered.map { Int($0.sampleTime - outputOrigin) } ?? 0
        let schedulingRoom = player.isPlaying ? Int(AVAudioSession.sharedInstance().ioBufferDuration * output.format.sampleRate) : 0
        let start = max(timeline.scheduledOutputSamples, clock + schedulingRoom)
        try timeline.append(delivery: delivery, generation: generation, item: item, providerSamples: Int(source.frameLength), outputSamples: Int(output.frameLength), outputRate: output.format.sampleRate, outputStart: start)
        itemInfo[item] = (delivery, generation)
        let end = timeline.scheduledOutputSamples
        let outputEpoch = outputGeneration
        player.scheduleBuffer(output, at: AVAudioTime(sampleTime: AVAudioFramePosition(start) + outputOrigin, atRate: output.format.sampleRate), options: [], completionCallbackType: .dataPlayedBack) { [weak self] _ in
            guard let self else { return }
            self.worker.async {
                guard self.outputGeneration == outputEpoch else { return }
                if let receipt = self.timeline.drained(item: item, outputEnd: end) { self.played?(receipt) }
                self.didDrain()
            }
        }
        if !player.isPlaying { player.play() }
    }
    func providerEnded(item: String) {
        if let receipt = timeline.providerEnded(item: item) { played?(receipt) }
        didDrain()
    }
    func deliveryEnded() { timeline.endDelivery(); didDrain() }
    private func didDrain() {
        if timeline.allDrained { flushOutput(); outputDrained?() }
    }
    func cue() {
        // Local activation cue shares the output graph and never enters provider history.
        guard capturing, let format = outputFormat, let buffer = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: AVAudioFrameCount(format.sampleRate * 0.06)), let samples = buffer.floatChannelData?[0] else { return }
        // Cue must precede provider frames. It is not a SpeechDelivery and is not reported as heard.
        if player.isPlaying { return }
        buffer.frameLength = buffer.frameCapacity
        for i in 0..<Int(buffer.frameLength) { samples[i] = Float(sin(2 * .pi * 880 * Double(i) / format.sampleRate)) * 0.025 }
        outputOrigin += AVAudioFramePosition(buffer.frameLength)
        player.scheduleBuffer(buffer)
        player.play()
    }
    private func failureCode(_ error: Error) -> String {
        (error as? VoiceFailure)?.code ?? "audio_processing_failed"
    }
    private func convert(_ source: AVAudioPCMBuffer, with converter: AVAudioConverter?) throws -> AVAudioPCMBuffer {
        guard let converter, let target = AVAudioPCMBuffer(pcmFormat: converter.outputFormat, frameCapacity: AVAudioFrameCount(ceil(Double(source.frameLength) * converter.outputFormat.sampleRate / source.format.sampleRate)) + 32) else { throw VoiceFailure("audio_converter_missing") }
        var supplied = false
        var error: NSError?
        let result = converter.convert(to: target, error: &error) { _, status in
            if supplied { status.pointee = .noDataNow; return nil }
            supplied = true; status.pointee = .haveData; return source
        }
        guard result != .error, error == nil else { throw VoiceFailure("audio_conversion_failed") }
        return target
    }
}
