import AVFoundation
import ExpoModulesCore
import Foundation
import UIKit

public final class EvenerVoiceAppDelegateSubscriber: ExpoAppDelegateSubscriber {
    public func application(_ application: UIApplication, didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        _ = VoiceService.shared
        return true
    }
    public func applicationWillTerminate(_ application: UIApplication) { VoiceService.shared.disarm() }
}

public final class EvenerVoiceModule: Module {
    public func definition() -> ModuleDefinition {
        Name("EvenerVoice")
        Events("state")
        OnStartObserving { VoiceService.shared.observe { [weak self] state in self?.sendEvent("state", state) } }
        OnStopObserving { VoiceService.shared.observe(nil) }
        AsyncFunction("deviceId") { () -> String in
            let key = "evener.voice.installation"
            if let value = UserDefaults.standard.string(forKey: key) { return value }
            let value = UUID().uuidString
            UserDefaults.standard.set(value, forKey: key)
            return value
        }
        AsyncFunction("validateTransport") { (origin: String) in _ = try VoiceTransport.validate(origin) }
        AsyncFunction("authorize") { (origin: String, path: String, token: String, body: String) async throws in
            try await VoiceAuthorization.request(origin: origin, path: path, token: token, body: body)
        }
        AsyncFunction("arm") { (configuration: String) async throws in
            guard await MainActor.run(body: { UIApplication.shared.applicationState == .active }) else { throw VoiceFailure("foreground_required") }
            let allowed = await withCheckedContinuation { continuation in
                AVAudioSession.sharedInstance().requestRecordPermission { continuation.resume(returning: $0) }
            }
            guard allowed else { throw VoiceFailure("microphone_denied") }
            guard await MainActor.run(body: { UIApplication.shared.applicationState == .active }) else { throw VoiceFailure("foreground_required") }
            try await VoiceService.shared.arm(configuration)
        }
        Function("talk") { VoiceService.shared.talk() }
        Function("stopSpeaking") { VoiceService.shared.stopSpeaking() }
        Function("disarm") { VoiceService.shared.disarm() }
        Function("allowSpeaker") { VoiceService.shared.allowSpeaker() }
        AsyncFunction("snapshot") { await VoiceService.shared.snapshot() }
    }
}
