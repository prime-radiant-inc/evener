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
	public init(
		state: String, capturing: Bool, cloudMicrophone: Bool, route: String, speakerConsentRequired: Bool,
		error: String, interpreted: String, custody: String, canonical: String, generated: String,
		bindingId: String, wakeState: String, wakePhrase: String, wakeModelId: String,
		wakeScore: Float?, lastDetectionScore: Float?, detections: Int, wakeError: String, trialsRevision: Int
	) {
		self.state = state
		self.capturing = capturing
		self.cloudMicrophone = cloudMicrophone
		self.route = route
		self.speakerConsentRequired = speakerConsentRequired
		self.error = error
		self.interpreted = interpreted
		self.custody = custody
		self.canonical = canonical
		self.generated = generated
		self.bindingId = bindingId
		self.wakeState = wakeState
		self.wakePhrase = wakePhrase
		self.wakeModelId = wakeModelId
		self.wakeScore = wakeScore
		self.lastDetectionScore = lastDetectionScore
		self.detections = detections
		self.wakeError = wakeError
		self.trialsRevision = trialsRevision
	}
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
