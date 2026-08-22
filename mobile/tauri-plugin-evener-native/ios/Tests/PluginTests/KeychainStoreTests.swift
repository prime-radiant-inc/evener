import Security
import XCTest
@testable import EvenerNativePlugin

final class KeychainStoreTests: XCTestCase {
    func testUsesFixedServiceAccountAndAccessibility() throws {
        let client = RecordingKeychainClient()
        let store = KeychainStore(client: client)

        try store.save(
            profileID: ProfileUUIDFixture.first,
            capability: "saved-capability-must-stay-native"
        )

        XCTAssertEqual(client.calls.count, 2)
        XCTAssertEqual(client.calls[0].operation, "delete")
        XCTAssertEqual(client.calls[1].operation, "set")
        for call in client.calls {
            XCTAssertEqual(call.service, "com.primeradiant.evener.hub")
            XCTAssertEqual(call.account, "profile:\(ProfileUUIDFixture.first)")
        }
        XCTAssertEqual(
            client.calls[1].accessibility as String?,
            kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly as String
        )
        XCTAssertEqual(
            String(data: try XCTUnwrap(client.calls[1].data), encoding: .utf8),
            "saved-capability-must-stay-native"
        )
    }

    func testLoadsAndDeletesTheActiveCapability() throws {
        let client = RecordingKeychainClient()
        client.value = Data("native-only".utf8)
        let store = KeychainStore(client: client)

        XCTAssertEqual(
            try store.load(profileID: ProfileUUIDFixture.first),
            "native-only"
        )
        try store.delete(profileID: ProfileUUIDFixture.first)

        XCTAssertEqual(client.calls.map(\.operation), ["get", "delete"])
        for call in client.calls {
            XCTAssertEqual(call.account, "profile:\(ProfileUUIDFixture.first)")
        }
    }

    func testTwoIndependentProfilesAreIsolated() throws {
        let client = RecordingKeychainClient()
        // Seed first profile
        client.stored["profile:\(ProfileUUIDFixture.first)"] = Data("alpha".utf8)
        // Seed second profile
        client.stored["profile:\(ProfileUUIDFixture.second)"] = Data("beta".utf8)
        let store = KeychainStore(client: client)

        XCTAssertEqual(try store.load(profileID: ProfileUUIDFixture.first), "alpha")
        XCTAssertEqual(try store.load(profileID: ProfileUUIDFixture.second), "beta")

        // Deleting the first must not affect the second
        try store.delete(profileID: ProfileUUIDFixture.first)

        // After delete, first is gone but second remains
        client.stored["profile:\(ProfileUUIDFixture.first)"] = nil
        client.value = nil
        XCTAssertThrowsError(try store.load(profileID: ProfileUUIDFixture.first))
        client.value = client.stored["profile:\(ProfileUUIDFixture.second)"]
        XCTAssertEqual(try store.load(profileID: ProfileUUIDFixture.second), "beta")
    }

    func testDeleteOnlyRemovesTheNamedProfile() throws {
        let client = RecordingKeychainClient()
        client.stored["profile:\(ProfileUUIDFixture.first)"] = Data("alpha".utf8)
        client.stored["profile:\(ProfileUUIDFixture.second)"] = Data("beta".utf8)
        let store = KeychainStore(client: client)

        try store.delete(profileID: ProfileUUIDFixture.first)

        // The delete call must target only the first profile
        let deleteCalls = client.calls.filter { $0.operation == "delete" }
        XCTAssertEqual(deleteCalls.count, 1)
        XCTAssertEqual(deleteCalls[0].account, "profile:\(ProfileUUIDFixture.first)")
    }

    func testFailureDescriptionNeverContainsCapability() throws {
        let client = RecordingKeychainClient()
        client.setError = KeychainError.unexpectedStatus(errSecNotAvailable)
        let store = KeychainStore(client: client)

        XCTAssertThrowsError(
            try store.save(
                profileID: ProfileUUIDFixture.first,
                capability: "must-never-be-rendered"
            )
        ) { error in
            XCTAssertFalse(String(describing: error).contains("must-never-be-rendered"))
            XCTAssertFalse(String(reflecting: error).contains("must-never-be-rendered"))
        }
    }
}

// Stable fixture UUIDs so tests are deterministic.
enum ProfileUUIDFixture {
    static let first = "11111111-1111-1111-1111-111111111111"
    static let second = "22222222-2222-2222-2222-222222222222"
}

private final class RecordingKeychainClient: KeychainClient {
    struct Call {
        let operation: String
        let service: String
        let account: String
        let accessibility: CFString?
        let data: Data?
    }

    var calls: [Call] = []
    var value: Data?
    var stored: [String: Data] = [:]
    var setError: Error?

    func get(service: String, account: String) throws -> Data? {
        calls.append(Call(operation: "get", service: service, account: account, accessibility: nil, data: nil))
        if let setError { throw setError }
        return stored[account] ?? value
    }

    func set(
        _ data: Data,
        service: String,
        account: String,
        accessibility: CFString
    ) throws {
        calls.append(
            Call(
                operation: "set",
                service: service,
                account: account,
                accessibility: accessibility,
                data: data
            )
        )
        if let setError { throw setError }
        stored[account] = data
    }

    func delete(service: String, account: String) throws {
        calls.append(Call(operation: "delete", service: service, account: account, accessibility: nil, data: nil))
        if let setError { throw setError }
        stored[account] = nil
    }
}
