import Foundation

/// The comparable projection of a bridge snapshot (#5684): one field for
/// every value the JS side can observe, with the wake score's outside-trial
/// exclusion applied by the service before the facts are built, and the
/// trials payload compared by the ledger's revision instead of its JSON. A
/// field the snapshot exposes but the facts omit would silently swallow that
/// field's updates, so the snapshot is built from the facts, never beside
/// them.
public struct VoiceSnapshotFacts: Equatable {
	public var state: String
	public var capturing: Bool
	public var cloudMicrophone: Bool
	public var route: String
	public var speakerConsentRequired: Bool
	public var error: String
	public var interpreted: String
	public var custody: String
	public var canonical: String
	public var generated: String
	public var bindingId: String
	public var wakeState: String
	public var wakePhrase: String
	public var wakeModelId: String
	public var wakeScore: Float?
	public var lastDetectionScore: Float?
	public var detections: Int
	public var wakeError: String
	public var trialsRevision: Int
}

/// One coalescing publisher's diff gate (#5684): a freshly built snapshot is
/// handed to the bridge listener only when its facts differ from the last
/// ones emitted, so a per-block capture burst or a burst of socket
/// acknowledgements that changes nothing the JS side can see costs no bridge
/// event, while every visible change still emits exactly one.
public struct VoiceSnapshotGate {
	private var lastEmitted: VoiceSnapshotFacts?

	public init() {}

	public mutating func shouldEmit(_ facts: VoiceSnapshotFacts) -> Bool {
		guard facts != lastEmitted else { return false }
		lastEmitted = facts
		return true
	}
}
