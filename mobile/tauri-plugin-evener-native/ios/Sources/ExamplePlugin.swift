import Foundation
import Security
import SwiftRs
import Tauri
import UIKit
import WebKit

// ---------------------------------------------------------------------------
// Contract V1
// ---------------------------------------------------------------------------

enum ContractV1 {
    static let version: Int = 1

    // -- Decodable wrappers with strict field rejection --------------------

    struct DecodedCommand: Decodable {
        let kind: String

        enum CodingKeys: String, CodingKey {
            case kind = "type"
        }
    }

    struct DecodedResponse: Decodable {
        let kind: String

        enum CodingKeys: String, CodingKey {
            case kind = "type"
        }
    }

    struct DecodedEvent: Decodable {
        let kind: String

        enum CodingKeys: String, CodingKey {
            case kind = "type"
        }
    }

    struct Fixture: Decodable {
        let bridgeVersion: Int
        let commands: [DecodedCommand]
        let responses: [DecodedResponse]
        let events: [DecodedEvent]
    }

    // -- Decode helpers ----------------------------------------------------

    static func decodeFixture(_ data: Data) throws -> Fixture {
        let decoder = JSONDecoder()
        return try decoder.decode(Fixture.self, from: data)
    }

    static func decodeEvent(_ data: Data) throws -> DecodedEvent {
        try decodeStrict(data, type: DecodedEvent.self, label: "event")
    }

    static func decodeResponse(_ data: Data) throws -> DecodedResponse {
        try decodeStrict(data, type: DecodedResponse.self, label: "response")
    }

    /// Strict decode: reject unknown version, unknown type, and extra fields.
    private static func decodeStrict<T: Decodable>(
        _ data: Data,
        type: T.Type,
        label: String
    ) throws -> T {
        guard let object = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            throw ContractError.invalidJSON
        }
        guard let version = object["version"] as? Int, version == Self.version else {
            throw ContractError.unsupportedVersion
        }
        guard let typeValue = object["type"] as? String else {
            throw ContractError.missingType
        }

        // Reject unknown discriminators per category
        switch label {
        case "event":
            let allowed: Set<String> = [
                "lifecycle.changed", "speech.partial", "speech.final", "barge.in",
            ]
            if !allowed.contains(typeValue) { throw ContractError.unknownType }
        case "response":
            let allowed: Set<String> = [
                "secure.state", "secure.updated", "secure.deleted",
                "pairing.preview", "permission.status", "speech.ready",
                "speech.stopped", "synthesis.started", "synthesis.stopped",
                "haptic.completed", "contentSize.value", "error",
            ]
            if !allowed.contains(typeValue) { throw ContractError.unknownType }
            // Reject secret-bearing extra fields on responses
            if object["capability"] != nil { throw ContractError.secretField }
            if let error = object["error"] as? [String: Any] {
                let allowedErrorKeys: Set<String> = ["id", "kind", "message"]
                for key in error.keys {
                    if !allowedErrorKeys.contains(key) {
                        throw ContractError.secretField
                    }
                }
            }
        default:
            break
        }

        let decoder = JSONDecoder()
        return try decoder.decode(T.self, from: data)
    }
}

enum ContractError: Error {
    case invalidJSON
    case unsupportedVersion
    case missingType
    case unknownType
    case secretField
}

// ---------------------------------------------------------------------------
// Keychain — multi-profile secure storage
// ---------------------------------------------------------------------------

protocol KeychainClient {
    func get(service: String, account: String) throws -> Data?
    func set(_ data: Data, service: String, account: String, accessibility: CFString) throws
    func delete(service: String, account: String) throws
}

enum KeychainError: Error {
    case unexpectedStatus(OSStatus)
    case decodeFailure
    case notFound
}

struct KeychainStore {
    static let service = "com.primeradiant.evener.hub"

    private let client: KeychainClient

    init(client: KeychainClient) {
        self.client = client
    }

    private func account(for profileID: String) -> String {
        "profile:\(profileID)"
    }

    func save(profileID: String, capability: String) throws {
        let account = account(for: profileID)
        let data = Data(capability.utf8)

        // Delete any existing item first (upsert semantics)
        try client.delete(service: Self.service, account: account)
        try client.set(
            data,
            service: Self.service,
            account: account,
            accessibility: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        )
    }

    func load(profileID: String) throws -> String {
        let account = account(for: profileID)
        guard let data = try client.get(service: Self.service, account: account) else {
            throw KeychainError.notFound
        }
        guard let value = String(data: data, encoding: .utf8) else {
            throw KeychainError.decodeFailure
        }
        return value
    }

    /// Load the capability for a profile, returning nil when not present.
    /// Used internally by the Rust bridge to retrieve the token for HTTP
    /// bearer auth and rollback compensation. The capability never crosses
    /// to JavaScript.
    func loadCapability(profileID: String) -> String? {
        let account = account(for: profileID)
        guard let data = try? client.get(service: Self.service, account: account) else {
            return nil
        }
        return String(data: data, encoding: .utf8)
    }

    func delete(profileID: String) throws {
        let account = account(for: profileID)
        try client.delete(service: Self.service, account: account)
    }
}

// ---------------------------------------------------------------------------
// QR Scanner — raw text travels Swift→Rust only
// ---------------------------------------------------------------------------

enum QRScanError: Error {
    case cameraUnavailable
    case cancelled
    case permissionDenied
}

protocol QRScanning {
    func scan(completion: @escaping (Result<String, Error>) -> Void)
}

final class QRScanCoordinator {
    private let scanner: QRScanning

    init(scanner: QRScanning) {
        self.scanner = scanner
    }

    func scan(completion: @escaping (Result<String, Error>) -> Void) {
        scanner.scan(completion: completion)
    }
}

// ---------------------------------------------------------------------------
// Native plugin
// ---------------------------------------------------------------------------

class ScanAndPreviewArgs: Decodable {
    // No payload — JavaScript sends only the command, no scanned text
}

class SecureGetArgs: Decodable {
    let profileId: String
}

class EvenerNativePlugin: Plugin {
    private let keychain: KeychainStore
    private var scanCoordinator: QRScanCoordinator?

    override init() {
        self.keychain = KeychainStore(client: SystemKeychainClient())
        self.scanCoordinator = nil
        super.init()
    }

    /// Test initializer: inject a fake keychain client and scanner.
    init(keychainClient: KeychainClient, scanner: QRScanning? = nil) {
        self.keychain = KeychainStore(client: keychainClient)
        if let scanner = scanner {
            self.scanCoordinator = QRScanCoordinator(scanner: scanner)
        } else {
            self.scanCoordinator = nil
        }
        super.init()
    }

    /// Scan a QR code and return the raw text to Rust via a private internal
    /// response type. The raw text never crosses to JavaScript — Rust wraps
    /// it in SensitiveScannedCode and feeds it through the installed
    /// PreviewCoordinator, returning only {previewId, origin} to JS.
    ///
    /// When no scanner is available (e.g. no camera on this device), returns
    /// an "unavailable" result so Rust can return pairing_unavailable.
    @objc public func scanAndPreviewPairing(_ invoke: Invoke) throws {
        _ = try invoke.parseArgs(ScanAndPreviewArgs.self)

        guard let coordinator = scanCoordinator else {
            // No scanner configured. Return unavailable so Rust returns
            // structured pairing_unavailable, not a hardcoded fake.
            invoke.resolve([
                "version": ContractV1.version,
                "type": "unavailable",
                "error": [
                    "id": "scan-and-preview",
                    "kind": "pairing_unavailable",
                    "message": "Pairing scan is unavailable on this platform",
                ] as [String: String],
            ] as [String: Any])
            return
        }

        coordinator.scan { result in
            switch result {
            case .success(let scannedText):
                // Return the raw scanned text to Rust. This is the
                // Swift→Rust transport channel only. Rust wraps it in
                // SensitiveScannedCode before any handler sees it.
                invoke.resolve([
                    "version": ContractV1.version,
                    "type": "scanned",
                    "scanned": scannedText,
                ] as [String: Any])
            case .failure(let error):
                let kind: String
                let message: String
                if let scanError = error as? QRScanError {
                    switch scanError {
                    case .cameraUnavailable:
                        kind = "pairing_unavailable"
                        message = "Camera unavailable"
                    case .cancelled:
                        kind = "pairing_unavailable"
                        message = "Scan cancelled"
                    case .permissionDenied:
                        kind = "permission_denied"
                        message = "Camera permission denied"
                    }
                } else {
                    kind = "scanner"
                    message = "Scan failed"
                }
                invoke.resolve([
                    "version": ContractV1.version,
                    "type": "error",
                    "error": [
                        "id": "scan-and-preview",
                        "kind": kind,
                        "message": message,
                    ] as [String: String],
                ] as [String: Any])
            }
        }
    }

    /// Internal: return the stored Keychain capability for a profile.
    /// The capability never crosses to JavaScript — this is a Rust↔Swift
    /// internal bridge command used for HTTP bearer auth and rollback
    /// compensation.
    @objc public func secureGetCapability(_ invoke: Invoke) throws {
        let args = try invoke.parseArgs(SecureGetArgs.self)
        let capability = keychain.loadCapability(profileID: args.profileId)
        invoke.resolve([
            "capability": capability as Any,
        ] as [String: Any])
    }

    @objc public func secureGet(_ invoke: Invoke) throws {
        let args = try invoke.parseArgs(SecureGetArgs.self)
        let present = keychain.loadCapability(profileID: args.profileId) != nil
        invoke.resolve([
            "version": ContractV1.version,
            "present": present,
        ] as [String: Any])
    }

    @objc public func secureSet(_ invoke: Invoke) throws {
        let args = try invoke.parseArgs(SecureSetArgs.self)
        try keychain.save(profileID: args.profileId, capability: args.capability)
        invoke.resolve([
            "version": ContractV1.version,
            "stored": true,
        ] as [String: Any])
    }

    @objc public func secureDelete(_ invoke: Invoke) throws {
        let args = try invoke.parseArgs(SecureGetArgs.self)
        try keychain.delete(profileID: args.profileId)
        invoke.resolve([
            "version": ContractV1.version,
            "deleted": true,
        ] as [String: Any])
    }
}

class SecureSetArgs: Decodable {
    let profileId: String
    let capability: String
}

// ---------------------------------------------------------------------------
// System Keychain client (production)
// ---------------------------------------------------------------------------

struct SystemKeychainClient: KeychainClient {
    func get(service: String, account: String) throws -> Data? {
        var query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var item: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &item)
        switch status {
        case errSecSuccess:
            return item as? Data
        case errSecItemNotFound:
            return nil
        default:
            throw KeychainError.unexpectedStatus(status)
        }
    }

    func set(_ data: Data, service: String, account: String, accessibility: CFString) throws {
        var query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        // Delete existing first (upsert semantics handled by KeychainStore)
        SecItemDelete(query as CFDictionary)

        query[kSecValueData as String] = data
        query[kSecAttrAccessible as String] = accessibility
        let status = SecItemAdd(query as CFDictionary, nil)
        if status != errSecSuccess {
            throw KeychainError.unexpectedStatus(status)
        }
    }

    func delete(service: String, account: String) throws {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        let status = SecItemDelete(query as CFDictionary)
        if status != errSecSuccess && status != errSecItemNotFound {
            throw KeychainError.unexpectedStatus(status)
        }
    }
}

@_cdecl("init_plugin_evener_native")
func initPlugin() -> Plugin {
    return EvenerNativePlugin()
}
