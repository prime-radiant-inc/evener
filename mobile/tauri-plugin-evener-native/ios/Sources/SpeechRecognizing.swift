import Foundation

// ---------------------------------------------------------------------------
// Speech recognition boundary
//
// `SpeechRecognizing` abstracts `SFSpeechRecognizer` so the voice session and
// its tests never touch live speech recognition. Production wires the real
// `SFSpeechRecognizer`; tests inject a fake that reports deterministic
// authorization, availability, and partial/final results.
// ---------------------------------------------------------------------------

/// Speech recognition authorization states mirror `SFSpeechRecognizerAuthorizationStatus`.
enum SpeechAuthorizationState {
    case notDetermined
    case denied
    case restricted
    case authorized
}

/// An opaque, cancellable recognition task. The production implementation wraps
/// `SFSpeechRecognitionTask`; tests wrap a fake. Cancellation is idempotent.
protocol SpeechRecognitionTask: AnyObject {
    func cancel()
}

/// Speech recognition boundary injected into `VoiceSession`.
protocol SpeechRecognizing: AnyObject {
    /// The current authorization state, without prompting. Production returns
    /// `SFSpeechRecognizer.authorizationStatus()`; tests return a fixed value.
    var authorizationState: SpeechAuthorizationState { get }

    /// Whether recognition is available for the requested locale on this device.
    /// `SFSpeechRecognizer.isAvailable` can flip at runtime; production reads it
    /// per `startRecognition`. Tests return a fixed value.
    var isAvailable: Bool { get }

    /// Asynchronously request authorization. Production forwards to
    /// `SFSpeechRecognizer.requestAuthorization`; tests complete immediately.
    func requestAuthorization() async -> SpeechAuthorizationState

    /// Begin recognition for `locale`. `onPartial` and `onFinal` deliver the
    /// recognized text; both never persist audio or transcript to disk. Throws
    /// when recognition cannot start. Returns a cancellable task handle.
    func startRecognition(
        locale: String,
        onPartial: @escaping (String) -> Void,
        onFinal: @escaping (String) -> Void
    ) throws -> SpeechRecognitionTask

    /// Stop the current recognition task, if any. Idempotent. Production cancels
    /// the active `SFSpeechRecognitionTask`; tests record the call.
    func stopRecognition()
}
