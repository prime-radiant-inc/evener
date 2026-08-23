import XCTest
@testable import EvenerNativePlugin

final class ContractTests: XCTestCase {
    func testDecodesEveryV1FixtureVariant() throws {
        let url = try XCTUnwrap(Bundle.module.url(forResource: "contract-v1", withExtension: "json"))
        let fixture = try ContractV1.decodeFixture(Data(contentsOf: url))

        XCTAssertEqual(ContractV1.version, 1)
        XCTAssertEqual(fixture.bridgeVersion, ContractV1.version)
        XCTAssertEqual(
            fixture.commands.map(\.kind),
            [
                "secure.get",
                "secure.set",
                "secure.delete",
                "pairing.scanAndPreview",
                "permission.request",
                "speech.start",
                "speech.stop",
                "synthesis.speak",
                "synthesis.stop",
                "haptic.perform",
                "clipboard.paste",
                "contentSize.get",
                "voice.permissions",
                "voice.start",
                "voice.stop",
                "voice.speak",
                "voice.stopSpeaking",
                "voice.setRate",
            ]
        )
        XCTAssertEqual(
            fixture.responses.map(\.kind),
            [
                "secure.state",
                "secure.updated",
                "secure.deleted",
                "pairing.preview",
                "permission.status",
                "speech.ready",
                "speech.stopped",
                "synthesis.started",
                "synthesis.stopped",
                "haptic.completed",
                "clipboard.pasted",
                "contentSize.value",
                "voice.permissions",
                "voice.ready",
                "voice.stopped",
                "voice.queued",
                "voice.speakingStopped",
                "voice.rateSet",
                "error",
            ]
        )
        XCTAssertEqual(
            fixture.events.map(\.kind),
            [
                "lifecycle.changed", "speech.partial", "speech.final", "barge.in",
                "voice.level", "voice.partial", "voice.final",
                "voice.speechStarted", "voice.speechFinished",
                "voice.bargeIn", "voice.interrupted", "voice.error",
            ]
        )
    }

    func testRejectsUnknownVersionAndDiscriminator() throws {
        XCTAssertThrowsError(
            try ContractV1.decodeEvent(json(#"{"version":2,"type":"lifecycle.changed","state":"active"}"#))
        )
        XCTAssertThrowsError(
            try ContractV1.decodeEvent(json(#"{"version":1,"type":"lifecycle.future","state":"active"}"#))
        )
    }

    func testRejectsUnknownAndSecretBearingResponseFields() throws {
        XCTAssertThrowsError(
            try ContractV1.decodeResponse(json(#"{"version":1,"type":"secure.state","present":true,"capability":"secret"}"#))
        )
        XCTAssertThrowsError(
            try ContractV1.decodeResponse(json(#"{"version":1,"type":"error","error":{"id":"e","kind":"internal","message":"redacted","token":"secret"}}"#))
        )
    }

    private func json(_ value: String) -> Data {
        Data(value.utf8)
    }
}
