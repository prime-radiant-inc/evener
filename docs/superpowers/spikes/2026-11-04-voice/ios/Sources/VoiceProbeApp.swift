import SwiftUI
import UniformTypeIdentifiers

// The application delegate, not any scene or view, retains the native service.
@MainActor
final class ProbeAppDelegate: NSObject, UIApplicationDelegate {
    let probe = AudioProbe()
}

@main
struct VoiceProbeApp: App {
    @UIApplicationDelegateAdaptor(ProbeAppDelegate.self) private var delegate
    var body: some Scene {
        WindowGroup { ProbeView(probe: delegate.probe) }
    }
}

private struct EvidenceDocument: FileDocument {
    static var readableContentTypes: [UTType] { [.json] }
    var data: Data
    init(data: Data) { self.data = data }
    init(configuration: ReadConfiguration) throws { data = configuration.file.regularFileContents ?? Data() }
    func fileWrapper(configuration: WriteConfiguration) throws -> FileWrapper { FileWrapper(regularFileWithContents: data) }
}

private struct ProbeView: View {
    @ObservedObject var probe: AudioProbe
    @Environment(\.scenePhase) private var scenePhase
    @State private var exporting = false
    @State private var document = EvidenceDocument(data: Data())

    var body: some View {
        NavigationStack {
            Form {
                Section("Throwaway audio probe") {
                    Text("No wake detector, ASR, network, or raw microphone export. Arm explicitly enables the microphone until Disarm.")
                    Text("Lock/background survival and echo cancellation require a real iPhone test.")
                    Text("State: \(probe.phase)")
                    Text("Last event: \(probe.lastEvent)")
                    Button("Arm microphone") { probe.arm() }
                        .disabled(probe.isArmed || probe.phase == "permission_pending")
                    Button("Disarm", role: .destructive) { probe.disarm() }
                    Button("Play synthetic speech") { probe.playSpeech() }.disabled(!probe.isArmed)
                    Button("Stop playback, keep listening") { probe.stopPlayback() }
                }
                Section("Counters, not speech detection") {
                    TimelineView(.periodic(from: .now, by: 1)) { _ in
                        let capture = probe.capture.snapshot()
                        VStack(alignment: .leading) {
                            Text("Capture buffers: \(capture.buffers)")
                            Text("Capture frames: \(capture.frames)")
                            Text("Maximum callback gap: \(capture.maximumCallbackGapSeconds, specifier: "%.3f") s")
                            Text("Playback generation: \(probe.generation)")
                            Text("Voice processing enabled: \(probe.voiceProcessingEnabled ? "yes" : "no")")
                        }
                        .monospacedDigit()
                    }
                }
                Section("Nonprivate evidence") {
                    Button("Export JSON, no audio") {
                        do {
                            document = EvidenceDocument(data: try probe.evidence())
                            exporting = true
                        } catch { probe.record("export_encoding_failed") }
                    }
                    Text("Interruptions, engine changes, and headset changes disarm this probe. Re-arm explicitly. Backgrounding alone does not disarm.")
                }
            }
            .navigationTitle("Voice probe")
        }
        .fileExporter(isPresented: $exporting, document: document, contentType: .json, defaultFilename: "voice-probe-evidence") { result in
            switch result {
            case .success: probe.record("export_complete")
            case .failure: probe.record("export_failed_or_cancelled")
            }
        }
        .onAppear {
            probe.record("view_appeared")
            probe.runDisarmedSmokeIfRequested()
        }
        .onChange(of: scenePhase) { _, value in probe.record("scene_phase", ["value": String(describing: value)]) }
    }
}
