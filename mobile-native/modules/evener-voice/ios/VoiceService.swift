import AVFoundation
import Foundation
import UIKit

/// Owned by the application subscriber, not a module instance, screen, or JS AppState.
final class VoiceService {
    static let shared = VoiceService()
    private let worker = DispatchQueue(label: "com.primeradiant.evener.voice")
    private lazy var audio = VoiceAudioDevice(worker: worker)
    private lazy var socket = VoiceSocket(queue: worker)
    private var controller = VoiceController()
    private var configuration: [String: Any]?
    private var listener: (([String: Any]) -> Void)?
    private var timer: DispatchSourceTimer?
    private var tickInterval = 0
    private var gate = VoiceSnapshotGate()
    private var expiresAt = Date.distantPast
    private var renewAt = Date.distantFuture
    private var reconnectAt = Date.distantFuture
    private var reconnectAttempt = 0
    private var recoverAudioAt = Date.distantFuture
    private var connected = false
    private var speakerConsent = true
    private var error: String?
    private var interpreted = ""
    private var custody = ""
    private var canonical = ""
    private var generated = ""
    private var delivery = ""
    private var activeGeneration = 0
    private var lastProgress = 0.0
    private let wakeTrials = WakeTrialLedger()
    private var wakeState = "paused"
    private var wakeScore: Float?
    private var wakeError = ""
    private var wakeDetections = 0
    private var lastWakeScore: Float?
    private var lastWakeActivation = -Double.infinity
    private var lastWakePublish = 0.0
    private var publishedWakeState = "paused"
    private var publishedWakeError = ""
    private var lastTrialSave = 0.0
    private init() {
        audio.wakeResult = { [weak self] result in self?.heardWake(result) }
        audio.wakeFailed = { [weak self] reason, samples in
            guard let self else { return }
            let changed = self.wakeError != reason
            self.wakeState = "warming"; self.wakeError = reason
            self.wakeTrials.dropped(samples)
            if changed { self.publish() }
        }
        audio.capture = { [weak self] supply, speech, at in
            guard let self else { return }
            if self.connected, self.controller.state == .recovering {
                self.controller.ready(generation: self.controller.minimumGeneration); self.publish()
            }
            if speech, self.controller.state == .speaking {
                self.perform(self.controller.stopSpeaking(played: self.audio.watermark()))
                self.controller.outputDrained(at: at)
            }
            self.perform(self.controller.capture(supply, speech: speech, at: at))
        }
        audio.failed = { [weak self] reason in
            guard let self else { return }
            self.error = reason
            self.perform(self.controller.abort(reason: reason))
            if reason != "capture_overflow" {
                self.pause(); self.recoverAudioAt = Date().addingTimeInterval(2)
            }
            self.publish()
        }
        audio.interrupted = { [weak self] in self?.pause() }
        audio.recover = { [weak self] publicRoute in self?.recoverAudio(publicRoute: publicRoute) }
        audio.played = { [weak self] receipt in
            self?.socket.send(["type": "played", "deliveryId": receipt.delivery, "generation": receipt.generation, "itemId": receipt.item, "samples": receipt.samples, "complete": receipt.complete])
        }
        audio.outputDrained = { [weak self] in
            guard let self else { return }
            self.controller.outputDrained(at: self.now); self.publish()
        }
        socket.received = { [weak self] event in self?.receive(event) }
        socket.failed = { [weak self] in self?.connectionLost() }
    }
    private var now: Double { ProcessInfo.processInfo.systemUptime * 1000 }
    func observe(_ listener: (([String: Any]) -> Void)?) {
        // A new listener receives the current snapshot even when it matches the last one emitted.
        worker.async { self.listener = listener; self.gate = VoiceSnapshotGate(); self.publish() }
    }
    // The service and its completions stay on the worker. The Expo adapter owns Swift tasks.
    func snapshot(completion: @escaping ([String: Any]) -> Void) { worker.async { completion(self.state()) } }
    private var visibleWakeState: String { audio.wakeListening ? wakeState : "paused" }
    // The facts are the snapshot's single source (#5684): a field the snapshot
    // exposes but the facts omit would silently swallow that field's updates,
    // and a field the facts carry but the snapshot drops would publish diffs
    // the JS side can never see.
    private func snapshotFacts() -> VoiceSnapshotFacts {
        VoiceSnapshotFacts(state: controller.state.rawValue, capturing: audio.capturing, cloudMicrophone: controller.cloudMicrophone,
                           route: audio.route, speakerConsentRequired: !speakerConsent, error: error ?? "",
                           interpreted: interpreted, custody: custody, canonical: canonical, generated: generated,
                           bindingId: configuration?["bindingId"] as? String ?? "",
                           wakeState: visibleWakeState, wakePhrase: audio.wakeModels?.phrase ?? "",
                           wakeModelId: audio.wakeModels?.id ?? "",
                           wakeScore: wakeTrials.running ? wakeScore : nil, lastDetectionScore: lastWakeScore,
                           detections: wakeDetections, wakeError: wakeError, trialsRevision: wakeTrials.revision)
    }
    private func state(from facts: VoiceSnapshotFacts, trials: [[String: Any]]) -> [String: Any] {
        ["state": facts.state, "capturing": facts.capturing, "cloudMicrophone": facts.cloudMicrophone,
         "route": facts.route, "speakerConsentRequired": facts.speakerConsentRequired, "error": facts.error,
         "interpreted": facts.interpreted, "custody": facts.custody, "canonical": facts.canonical,
         "generated": facts.generated, "bindingId": facts.bindingId,
         "wake": ["state": facts.wakeState, "phrase": facts.wakePhrase,
                  "modelId": facts.wakeModelId, "score": (facts.wakeScore as Any?) ?? NSNull(),
                  "lastDetectionScore": (facts.lastDetectionScore as Any?) ?? NSNull(), "detections": facts.detections,
                  "error": facts.wakeError, "trials": trials]]
    }
    private func state() -> [String: Any] { state(from: snapshotFacts(), trials: wakeTrials.json) }
    private func publish() {
        if audio.setWakeListening(connected && audio.capturing && controller.state == .quiet) {
            wakeState = "warming"; wakeScore = nil
        }
        publishedWakeState = visibleWakeState
        publishedWakeError = wakeError
        lastWakePublish = now
        scheduleTick()
        let facts = snapshotFacts()
        // Coalesce (#5684): a capture burst or a burst of socket messages that
        // changes nothing the JS side can see costs no bridge event.
        guard gate.shouldEmit(facts) else { return }
        listener?(state(from: facts, trials: wakeTrials.json))
    }
    private func heardWake(_ result: WakeWordResult) {
        guard connected, controller.state == .quiet else { return }
        wakeState = result.state.rawValue; wakeScore = result.score; wakeError = ""
        wakeTrials.record(result)
        guard result.detected else { return }
        wakeDetections += 1; lastWakeScore = result.score
        // Trial mode is local-only, even on a false activation. Normal wake uses
        // exactly Talk's input_start/cue/acknowledgement path, never a JS RPC.
        if !wakeTrials.running, let settings = audio.wakeModels?.settings,
           now - lastWakeActivation >= Double(settings.holdoffMs) {
            lastWakeActivation = now
            perform(controller.wake(at: now))
        }
        publish()
    }
    func startWakeTrial(completion: @escaping (Result<Void, Error>) -> Void) {
        worker.async {
            do {
                guard self.connected, self.audio.capturing, self.controller.state == .quiet,
                      let models = self.audio.wakeModels else { throw VoiceFailure("wake_trial_requires_quiet") }
                try self.wakeTrials.start(modelId: models.id, phrase: models.phrase, settings: models.settings, at: Date().timeIntervalSince1970)
                self.audio.resetWake(); self.wakeState = "warming"; self.wakeScore = nil
                self.publish(); completion(.success(()))
            } catch { completion(.failure(error)) }
        }
    }
    func finishWakeTrial(_ label: String, completion: @escaping (Result<Void, Error>) -> Void) {
        worker.async {
            do {
                guard let label = WakeTrialLabel(rawValue: label) else { throw VoiceFailure("wake_trial_label_invalid") }
                try self.wakeTrials.finish(label)
                self.audio.resetWake(); self.wakeState = "warming"; self.wakeScore = nil
                self.publish(); completion(.success(()))
            } catch { completion(.failure(error)) }
        }
    }
    func arm(_ value: String, completion: @escaping (Result<Void, Error>) -> Void) {
        worker.async {
            do {
                guard let data = value.data(using: .utf8), let config = try JSONSerialization.jsonObject(with: data) as? [String: Any],
                      let policyJSON = config["policy"], let expiry = config["expiresAt"] as? String,
                      let date = voiceExpiry(expiry), date > Date(),
                      config["sampleRate"] as? Int == 24000 else { throw VoiceFailure("grant_invalid") }
                let policy = try JSONDecoder().decode(VoicePolicy.self, from: JSONSerialization.data(withJSONObject: policyJSON))
                guard policy.trailingSilenceMs > 0, policy.maxUtteranceMs <= 30000, policy.followupMs > 0, policy.idleInputMs > 0 else { throw VoiceFailure("policy_invalid") }
                self.disarmNow()
                self.configuration = config; self.expiresAt = date
                self.renewAt = date.addingTimeInterval(-60)
                self.speakerConsent = true; self.error = nil
                self.interpreted = ""; self.custody = ""; self.canonical = ""; self.generated = ""
                self.controller.arm(policy: policy)
                self.startTick()
                try self.audio.start()
                try self.connect()
                self.publish(); completion(.success(()))
            } catch {
                self.disarmNow(); self.error = (error as? VoiceFailure)?.code ?? "arm_failed"
                self.publish(); completion(.failure(error))
            }
        }
    }
    private func connect() throws {
        guard let config = configuration, let origin = config["origin"] as? String, let path = config["socketPath"] as? String,
              let grant = config["grant"] as? String, let binding = config["bindingId"] as? String,
              let epoch = config["epoch"] as? String, let device = config["deviceId"] as? String else { throw VoiceFailure("grant_invalid") }
        try socket.connect(origin: origin, path: path, grant: grant, binding: binding, bindingEpoch: epoch, device: device)
        reconnectAt = Date.distantFuture
    }
    func talk() {
        let pressed = now
        worker.async {
            guard !self.wakeTrials.running else { return }
            if self.controller.state == .speaking { self.perform(self.controller.stopSpeaking(played: self.audio.watermark())) }
            self.perform(self.controller.talk(at: pressed))
        }
    }
    func stopSpeaking() { worker.async { self.perform(self.controller.stopSpeaking(played: self.audio.watermark())) } }
    func disarm() { worker.async { self.disarmNow(); self.publish() } }
    private func disarmNow() {
        wakeTrials.interrupt()
        perform(controller.disarm())
        socket.close(); configuration = nil; connected = false
        audio.deactivate(); reconnectAt = .distantFuture; renewAt = .distantFuture; recoverAudioAt = .distantFuture
        stopTick()
    }
    // The armed worker's timer lifecycle (#5683): the tick runs only while
    // armed, fast while a stream is live and slow while merely listening.
    private func startTick() {
        let timer = DispatchSource.makeTimerSource(queue: worker)
        timer.setEventHandler { [weak self] in self?.tick() }
        self.timer = timer; tickInterval = 0
        scheduleTick()
        timer.resume()
    }
    private func stopTick() {
        timer?.cancel(); timer = nil; tickInterval = 0
    }
    private func scheduleTick() {
        let milliseconds = VoiceTickPolicy.intervalMilliseconds(state: controller.state)
        guard milliseconds != tickInterval else { return }
        tickInterval = milliseconds
        timer?.schedule(deadline: .now(), repeating: .milliseconds(milliseconds))
    }
    func allowSpeaker() { worker.async { self.speakerConsent = true; self.publish() } }
    private func perform(_ actions: [VoiceAction]) {
        for action in actions {
            switch action {
            case .cue: audio.cue()
            case .flushOutput: audio.flushOutput()
            case .stopCapture: audio.stop()
            case .send(.disarm): socket.revokeAndClose()
            case .send(let message): socket.send(message.json)
            case .abort(let sequence, let reason): socket.send(["type": "input_abort", "sequence": sequence, "reason": reason]); error = reason
            }
        }
        if !actions.isEmpty { publish() }
    }
    private func pause() {
        guard controller.armed else { return }
        perform(controller.stopSpeaking(played: audio.watermark()))
        perform(controller.recovering(advanceGeneration: false)); audio.stop(); publish()
    }
    private func recoverAudio(publicRoute: Bool) {
        guard controller.armed else { return }
        if publicRoute { speakerConsent = false }
        pause()
        do {
            try audio.start()
            error = nil; recoverAudioAt = .distantFuture
        } catch { self.error = (error as? VoiceFailure)?.code ?? "capture_paused"; recoverAudioAt = Date().addingTimeInterval(5) }
        publish()
    }
    private func connectionLost() {
        guard controller.armed else { return }
        perform(controller.recovering()); connected = false
        error = "voice_disconnected"
        reconnectAt = Date().addingTimeInterval(min(30, pow(2, Double(min(reconnectAttempt, 5)))))
        reconnectAttempt += 1; publish()
    }
    private func tick() {
        guard controller.armed else { return }
        if Date() >= expiresAt { disarmNow(); error = "grant_expired"; publish(); return }
        if audio.running, now - audio.lastCaptureAt > 1500 {
            pause(); error = "capture_gap"; recoverAudioAt = Date().addingTimeInterval(1)
        }
        if Date() >= recoverAudioAt { recoverAudio(publicRoute: false) }
        if Date() >= reconnectAt {
            do { try connect() } catch { connectionLost() }
        }
        if connected, Date() >= renewAt { socket.send(["type": "renew"]); renewAt = Date().addingTimeInterval(30) }
        let previous = controller.state
        perform(controller.tick(at: now))
        if previous != controller.state { publish() }
        if audio.wakeListening, now - lastWakePublish >= 500,
           WakePublishPolicy.shouldPublish(trialRunning: wakeTrials.running, wakeState: visibleWakeState,
                                           publishedWakeState: publishedWakeState,
                                           wakeError: wakeError, publishedWakeError: publishedWakeError) {
            publish()
        }
        if wakeTrials.running, now - lastTrialSave >= 5000 { lastTrialSave = now; wakeTrials.save() }
        if now - lastProgress >= 200 { audio.reportProgress(); lastProgress = now }
    }
    private func receive(_ event: [String: Any]) {
        guard controller.armed, let type = event["type"] as? String else { return }
        switch type {
        case "ready":
            guard let generation = event["generation"] as? Int, generation > 0,
                  event["bindingId"] as? String == configuration?["bindingId"] as? String,
                  event["epoch"] as? String == configuration?["epoch"] as? String else { socket.close(); connectionLost(); return }
            connected = true; reconnectAttempt = 0; error = nil
            perform(controller.synchronizeReady(generation: generation))
            if audio.capturing { controller.ready(generation: generation) }
        case "input_started":
            if let sequence = event["sequence"] as? Int { perform(controller.started(sequence: sequence)) }
        case "intent":
            interpreted = event["text"] as? String ?? ""; custody = event["custody"] as? String ?? ""
        case "speech_start":
            guard let generation = event["generation"] as? Int, let id = event["deliveryId"] as? String else { return }
            switch controller.admitSpeech(generation: generation, speakerConsent: speakerConsent && !wakeTrials.running) {
            case .accepted: break
            case .rejected(let actions): perform(actions); return
            }
            audio.flushOutput(); delivery = id; activeGeneration = generation
            canonical = event["canonicalText"] as? String ?? ""; generated = ""
        case "audio", "speech_end", "speech_text":
            guard let generation = event["generation"] as? Int, generation >= controller.minimumGeneration,
                  generation == activeGeneration, event["deliveryId"] as? String == delivery,
                  let item = event["itemId"] as? String, speakerConsent else { return }
            if type == "speech_end" { audio.providerEnded(item: item) }
            else if type == "speech_text" { generated += event["text"] as? String ?? "" }
            else {
                guard event["sampleRate"] as? Int == 24000, let encoded = event["pcm"] as? String,
                      let pcm = Data(base64Encoded: encoded) else { return }
                do { try audio.enqueue(pcm: pcm, delivery: delivery, generation: generation, item: item) }
                catch { perform(controller.stopSpeaking(played: audio.watermark())); self.error = "playback_failed" }
            }
        case "delivery_end":
            guard event["deliveryId"] as? String == delivery,
                  let generation = event["generation"] as? Int,
                  generation == activeGeneration, generation >= controller.minimumGeneration else { return }
            audio.deliveryEnded()
        case "stop":
            guard let generation = event["generation"] as? Int, generation >= controller.minimumGeneration else { return }
            audio.flushOutput(); controller.remoteStop(generation: generation)
        case "renewed":
            if let expiry = event["expiresAt"] as? String, let date = voiceExpiry(expiry) { expiresAt = date; renewAt = date.addingTimeInterval(-60) }
        case "error":
            error = event["code"] as? String ?? "voice_error"
            if event["recoverable"] as? Bool != true { let reason = error; disarmNow(); error = reason }
            else { perform(controller.abort(reason: "provider_error")) }
        default: break
        }
        publish()
    }
}
