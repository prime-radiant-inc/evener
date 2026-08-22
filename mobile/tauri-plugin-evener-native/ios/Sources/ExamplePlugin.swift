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
            throw KeychainError.decodeFailure
        }
        guard let value = String(data: data, encoding: .utf8) else {
            throw KeychainError.decodeFailure
        }
        return value
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

class EvenerNativePlugin: Plugin {
    /// Until Task 5 wires real pairing, production returns structured
    /// `pairing_unavailable`; no hardcoded fake profile or token.
    @objc public func scanAndPreviewPairing(_ invoke: Invoke) throws {
        _ = try invoke.parseArgs(ScanAndPreviewArgs.self)
        invoke.resolve([
            "version": ContractV1.version,
            "type": "error",
            "error": [
                "id": "scan-and-preview",
                "kind": "pairing_unavailable",
                "message": "Pairing is unavailable",
            ] as [String: String],
        ] as [String: Any])
    }
}

@_cdecl("init_plugin_evener_native")
func initPlugin() -> Plugin {
    return EvenerNativePlugin()
}
