import XCTest
@testable import EvenerNativePlugin
import Tauri

final class QRScannerTests: XCTestCase {
    func testProductionPluginInstallsConcreteScanner() {
        let plugin = EvenerNativePlugin()
        XCTAssertTrue(plugin.hasScannerForTesting)
    }

    func testForwardsScannedTextOnlyToNativeRustCompletion() throws {
        let scanner = FakeQRScanner()
        let coordinator = QRScanCoordinator(scanner: scanner)
        var result: Result<String, Error>?

        coordinator.scan { result = $0 }
        scanner.complete(.success("https://hub.example.test/auth?token=native-only"))

        XCTAssertEqual(
            try XCTUnwrap(result).get(),
            "https://hub.example.test/auth?token=native-only"
        )
    }

    func testForwardsScannerFailureWithoutAddingScannedData() throws {
        let scanner = FakeQRScanner()
        let coordinator = QRScanCoordinator(scanner: scanner)
        var result: Result<String, Error>?

        coordinator.scan { result = $0 }
        scanner.complete(.failure(QRScanError.cameraUnavailable))

        XCTAssertThrowsError(try XCTUnwrap(result).get())
    }
}

private final class FakeQRScanner: QRScanning {
    private var completion: ((Result<String, Error>) -> Void)?

    func scan(completion: @escaping (Result<String, Error>) -> Void) {
        self.completion = completion
    }

    func complete(_ result: Result<String, Error>) {
        completion?(result)
    }
}

// ---------------------------------------------------------------------------
// Plugin scan integration: injected scanner returns raw text to Rust
// ---------------------------------------------------------------------------

final class PluginScanIntegrationTests: XCTestCase {
    func testScanAndPreviewReturnsScannedTextToRust() throws {
        let scanner = FakePluginScanner()
        let plugin = EvenerNativePlugin(
            keychainClient: NoOpKeychainClient(),
            scanner: scanner
        )

        let (invoke, capturedResponse) = makeInvoke(args: "{}")
        try plugin.scanAndPreviewPairing(invoke)

        // The scanner hasn't completed yet; no response should be captured.
        XCTAssertNil(capturedResponse.value)

        // Complete the scan with raw text.
        scanner.complete(.success("https://hub.example.test/auth?token=native-only"))

        // The plugin should have resolved with a "scanned" result carrying
        // the raw text — this is the Swift→Rust transport channel only.
        let json = try XCTUnwrap(capturedResponse.value)
        XCTAssertTrue(json.contains("\"scanned\""))
        XCTAssertTrue(json.contains("native-only"))
    }

    func testScanAndPreviewReturnsUnavailableWhenNoScanner() throws {
        let plugin = EvenerNativePlugin(
            keychainClient: NoOpKeychainClient(),
            scanner: nil
        )

        let (invoke, capturedResponse) = makeInvoke(args: "{}")
        try plugin.scanAndPreviewPairing(invoke)

        let json = try XCTUnwrap(capturedResponse.value)
        XCTAssertTrue(json.contains("\"unavailable\""))
        XCTAssertTrue(json.contains("pairing_unavailable"))
    }

    func testScanAndPreviewReturnsErrorOnScanFailure() throws {
        let scanner = FakePluginScanner()
        let plugin = EvenerNativePlugin(
            keychainClient: NoOpKeychainClient(),
            scanner: scanner
        )

        let (invoke, capturedResponse) = makeInvoke(args: "{}")
        try plugin.scanAndPreviewPairing(invoke)
        scanner.complete(.failure(QRScanError.cameraUnavailable))

        let json = try XCTUnwrap(capturedResponse.value)
        XCTAssertTrue(json.contains("\"error\""))
        XCTAssertTrue(json.contains("pairing_unavailable"))
    }

    func testScanAndPreviewMapsPermissionDenied() throws {
        let scanner = FakePluginScanner()
        let plugin = EvenerNativePlugin(
            keychainClient: NoOpKeychainClient(),
            scanner: scanner
        )
        let (invoke, capturedResponse) = makeInvoke(args: "{}")
        try plugin.scanAndPreviewPairing(invoke)
        scanner.complete(.failure(QRScanError.permissionDenied))

        XCTAssertTrue(try XCTUnwrap(capturedResponse.value).contains("permission_denied"))
    }

    func testScanAndPreviewMapsUserCancellationWithoutRawText() throws {
        let scanner = FakePluginScanner()
        let plugin = EvenerNativePlugin(
            keychainClient: NoOpKeychainClient(),
            scanner: scanner
        )
        let (invoke, capturedResponse) = makeInvoke(args: "{}")
        try plugin.scanAndPreviewPairing(invoke)
        scanner.complete(.failure(QRScanError.cancelled))

        let json = try XCTUnwrap(capturedResponse.value)
        XCTAssertTrue(json.contains("Scan cancelled"))
        XCTAssertFalse(json.contains("scanned"))
    }

    func testSecureGetCapabilityReturnsStoredToken() throws {
        let keychainClient = RecordingKeychainClientPlugin()
        keychainClient.stored["profile:11111111-1111-1111-1111-111111111111"] =
            Data("native-only-capability".utf8)
        let plugin = EvenerNativePlugin(keychainClient: keychainClient)

        let args = "{\"profileId\":\"11111111-1111-1111-1111-111111111111\"}"
        let (invoke, capturedResponse) = makeInvoke(args: args)
        try plugin.secureGetCapability(invoke)

        let json = try XCTUnwrap(capturedResponse.value)
        XCTAssertTrue(json.contains("native-only-capability"))
    }

    func testSecureGetCapabilityReturnsNilWhenNotPresent() throws {
        let keychainClient = RecordingKeychainClientPlugin()
        let plugin = EvenerNativePlugin(keychainClient: keychainClient)

        let args = "{\"profileId\":\"11111111-1111-1111-1111-111111111111\"}"
        let (invoke, capturedResponse) = makeInvoke(args: args)
        try plugin.secureGetCapability(invoke)

        let json = try XCTUnwrap(capturedResponse.value)
        // When capability is nil, the JSON should not contain it or contain null.
        XCTAssertTrue(json.contains("null") || !json.contains("capability"))
    }

    func testScanResultNeverContainsRawTextInUnavailableResponse() throws {
        let plugin = EvenerNativePlugin(
            keychainClient: NoOpKeychainClient(),
            scanner: nil
        )

        let (invoke, capturedResponse) = makeInvoke(args: "{}")
        try plugin.scanAndPreviewPairing(invoke)

        let json = try XCTUnwrap(capturedResponse.value)
        // The unavailable response must not have a "scanned" field.
        XCTAssertFalse(json.contains("raw-secret"))
        XCTAssertFalse(json.contains("token=native-only"))
    }
}

// ---------------------------------------------------------------------------
// Test helpers for plugin integration tests
// ---------------------------------------------------------------------------

private final class FakePluginScanner: QRScanning {
    private var completion: ((Result<String, Error>) -> Void)?

    func scan(completion: @escaping (Result<String, Error>) -> Void) {
        self.completion = completion
    }

    func complete(_ result: Result<String, Error>) {
        completion?(result)
    }
}

private final class NoOpKeychainClient: KeychainClient {
    func get(service: String, account: String) throws -> Data? { nil }
    func set(_ data: Data, service: String, account: String, accessibility: CFString) throws {}
    func delete(service: String, account: String) throws {}
}

private final class RecordingKeychainClientPlugin: KeychainClient {
    var stored: [String: Data] = [:]

    func get(service: String, account: String) throws -> Data? {
        stored[account]
    }
    func set(_ data: Data, service: String, account: String, accessibility: CFString) throws {
        stored[account] = data
    }
    func delete(service: String, account: String) throws {
        stored[account] = nil
    }
}

/// Helper: create a real `Invoke` with a captured response callback.
private func makeInvoke(args: String) -> (Invoke, CapturedResponse) {
    let captured = CapturedResponse()
    let invoke = Invoke(
        command: "test",
        callback: 1,
        error: 0,
        sendResponse: { _, json in
            captured.value = json
        },
        sendChannelData: { _, _ in },
        data: args
    )
    return (invoke, captured)
}

private final class CapturedResponse {
    var value: String?
}
