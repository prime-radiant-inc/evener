import Foundation

/// The armed worker tick's repeating interval (#5683): fast while a stream is
/// active - captured conversation or provider playback - and slow while
/// merely listening armed, where every window the tick watches (renew,
/// reconnect, recovery, trial save, the wake republish throttle) is
/// seconds-scale. A disarmed service runs no tick at all; that is the
/// service's timer lifecycle, not this policy's answer.
public enum VoiceTickPolicy {
	public static func intervalMilliseconds(state: VoiceState) -> Int {
		switch state {
		case .conversing, .speaking: 100
		case .quiet, .recovering: 500
		case .disarmed: 500
		}
	}
}
