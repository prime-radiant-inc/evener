import Foundation

// ---------------------------------------------------------------------------
// Audio session boundary
//
// `AudioSessionControlling` abstracts `AVAudioSession` + `AVAudioEngine` input
// tap management so `VoiceSession` and its tests never touch live audio. The
// production implementation configures `.playAndRecord` with `.voiceChat`,
// `.defaultToSpeaker`, `.allowBluetooth`, and supported Bluetooth A2DP, then
// installs a single input tap that reports per-buffer input levels. Tests
// inject a fake that records calls and can throw on demand.
// ---------------------------------------------------------------------------

/// Audio session boundary injected into `VoiceSession`.
protocol AudioSessionControlling: AnyObject {
    /// Configure the session for duplex voice chat:
    /// category `.playAndRecord`, mode `.voiceChat`, options `.defaultToSpeaker`
    /// and `.allowBluetooth`, plus the supported Bluetooth A2DP option for the
    /// deployment target. Throws when the category/mode cannot be set.
    func configureForVoiceChat() throws

    /// Deactivate the session and release its category. Idempotent.
    func deactivate() throws

    /// Install a single input tap that delivers per-buffer input levels to
    /// `onLevels`. Throws when a tap is already installed or the engine input
    /// is unavailable. Each call replaces any previously installed tap.
    func installTap(onLevels: @escaping ([Float]) -> Void) throws

    /// Remove the installed input tap, if any. Idempotent.
    func removeTap()
}
