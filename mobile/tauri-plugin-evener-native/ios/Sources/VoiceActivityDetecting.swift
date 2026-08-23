import Foundation

// ---------------------------------------------------------------------------
// Voice activity detection boundary
//
// `VoiceActivityDetecting` abstracts VAD so `VoiceSession` and its tests are
// deterministic: no real audio, no sleeps. Production calibrates a threshold
// from a short ambient window; tests inject explicit samples, an injected
// monotonic clock, and a fixed threshold state.
// ---------------------------------------------------------------------------

/// Monotonic timestamp in milliseconds, sourced from an injectable clock so
/// tests never depend on wall time.
struct MonotonicTimestamp {
    let milliseconds: Int64
}

/// Result of processing one input level sample. `level` is the input level
/// used to reach the activity decision (echoed back for tests and events).
struct VoiceActivityResult {
    let isActive: Bool
    let level: Float
}

/// Voice activity detection boundary injected into `VoiceSession`.
protocol VoiceActivityDetecting: AnyObject {
    /// Process one input level sample at a monotonic timestamp. Returns the
    /// activity decision and the level that produced it. Pure with respect to
    /// the injected clock and threshold state; never sleeps.
    func processSample(level: Float, timestamp: MonotonicTimestamp) -> VoiceActivityResult

    /// Reset internal state (e.g. after a session stops or restarts).
    func reset()
}

// ---------------------------------------------------------------------------
// Deterministic threshold VAD
//
// A simple level-threshold detector with hysteresis and hold time. Activity
// rises when a level exceeds the activation threshold and falls only after
// `holdMs` of consecutive sub-threshold samples, measured against the
// injected monotonic clock. The production threshold is calibrated from a
// short ambient window; tests set an explicit threshold and drive samples.
// ---------------------------------------------------------------------------

/// A monotonic clock injecting milliseconds. Production reads
/// `mach_absolute_time`-derived monotonic time; tests advance a counter.
protocol MonotonicClock {
    func now() -> MonotonicTimestamp
}

/// Deterministic level-threshold VAD with activation threshold, hysteresis
/// (deactivation threshold), and hold time.
final class ThresholdVoiceActivityDetector: VoiceActivityDetecting {
    private let clock: MonotonicClock
    private let activationThreshold: Float
    private let deactivationThreshold: Float
    private let holdMs: Int64

    private var active = false
    private var lastActiveTimestampMs: Int64 = 0
    private var initialized = false

    /// Create a detector with an explicit threshold set. `deactivation`
    /// defaults to half of `activation` when nil.
    init(
        clock: MonotonicClock,
        activationThreshold: Float,
        deactivationThreshold: Float? = nil,
        holdMs: Int64 = 300
    ) {
        self.clock = clock
        self.activationThreshold = activationThreshold
        self.deactivationThreshold = deactivationThreshold ?? (activationThreshold * 0.5)
        self.holdMs = holdMs
    }

    func processSample(level: Float, timestamp: MonotonicTimestamp) -> VoiceActivityResult {
        let nowMs = timestamp.milliseconds

        if !active {
            if level >= activationThreshold {
                active = true
                lastActiveTimestampMs = nowMs
            }
        } else {
            if level >= deactivationThreshold {
                lastActiveTimestampMs = nowMs
            } else if nowMs - lastActiveTimestampMs >= holdMs {
                active = false
            }
        }

        return VoiceActivityResult(isActive: active, level: level)
    }

    func reset() {
        active = false
        lastActiveTimestampMs = 0
        initialized = false
    }
}

// ---------------------------------------------------------------------------
// Ambient calibration
//
// Production calibrates the activation threshold from a short ambient window of
// input levels. The threshold sits above the observed ambient noise floor by a
// configured margin, bounded to sane defaults so silence never floors the
// threshold at zero and loud ambient never pushes it beyond a usable ceiling.
// ---------------------------------------------------------------------------

/// Calibrated VAD threshold produced from an ambient window.
struct VADCalibration {
    let activationThreshold: Float
    let deactivationThreshold: Float
}

enum VADCalibrator {
    /// Default floor/ceiling for the activation threshold regardless of ambient.
    static let minActivationThreshold: Float = 0.02
    static let maxActivationThreshold: Float = 0.5

    /// Margin above the ambient noise floor. 3 dB ≈ 1.41x in linear amplitude.
    static let ambientMargin: Float = 1.41

    /// Calibrate from `levels` collected during a short ambient window.
    /// Returns a bounded activation threshold and its hysteresis partner.
    /// An empty window falls back to the floor threshold.
    static func calibrate(levels: [Float]) -> VADCalibration {
        guard !levels.isEmpty else {
            return VADCalibration(
                activationThreshold: minActivationThreshold,
                deactivationThreshold: minActivationThreshold * 0.5
            )
        }

        let peak = levels.max() ?? 0
        let floor = max(peak, minActivationThreshold)
        let raised = floor * ambientMargin
        let activation = min(max(raised, minActivationThreshold), maxActivationThreshold)
        return VADCalibration(
            activationThreshold: activation,
            deactivationThreshold: activation * 0.5
        )
    }
}
