import Foundation

/// submit/reset run on the voice worker, inference on its own serial queue.
/// The non-waiting two-block bound prevents slow inference from accumulating
/// microphone RAM or delaying capture/VAD/playout. A gap invalidates context.
final class WakeWordStream {
    private let inference = DispatchQueue(label: "com.primeradiant.evener.wake", qos: .userInitiated)
    private let slots = DispatchSemaphore(value: 2)
    private let detector: WakeWordDetecting
    private let receiver: DispatchQueue
    private var epoch = 0
    private var needsReset = true
    var received: ((WakeWordResult) -> Void)?
    var failed: ((String, Int) -> Void)?
    init(detector: WakeWordDetecting, receiver: DispatchQueue) {
        self.detector = detector; self.receiver = receiver
    }
    func reset() { epoch += 1; needsReset = true }
    func submit(_ samples: [Float]) {
        guard slots.wait(timeout: .now()) == .success else {
            reset(); failed?("wake_capture_gap", samples.count); return
        }
        let generation = epoch, restart = needsReset
        needsReset = false
        inference.async {
            defer { self.slots.signal() }
            do {
                if restart { self.detector.reset() }
                let results = try self.detector.process(samples)
                self.receiver.async {
                    guard self.epoch == generation else { return }
                    results.forEach { self.received?($0) }
                }
            } catch {
                self.receiver.async {
                    guard self.epoch == generation else { return }
                    self.reset(); self.failed?("wake_inference_failed", samples.count)
                }
            }
        }
    }
}
