// swift-tools-version: 5.9
import PackageDescription
var targets: [Target] = [
    .target(name: "EvenerVoiceCore", path: "core"),
    .testTarget(name: "EvenerVoiceCoreTests", dependencies: ["EvenerVoiceCore"], path: "tests")
]
#if os(macOS)
targets += [
    .target(name: "EvenerVoiceTransport", dependencies: ["EvenerVoiceCore"], path: "ios", exclude: ["EvenerVoiceModule.swift", "SileroVAD.swift", "VoiceAudioDevice.swift", "VoiceService.swift"], sources: ["VoiceSocket.swift", "VoiceAuthorization.swift"]),
    .testTarget(name: "EvenerVoiceTransportTests", dependencies: ["EvenerVoiceTransport", "EvenerVoiceCore"], path: "transport-tests")
]
#endif
let package = Package(name: "EvenerVoiceCore", platforms: [.macOS(.v12)], products: [.library(name: "EvenerVoiceCore", targets: ["EvenerVoiceCore"])], targets: targets)
