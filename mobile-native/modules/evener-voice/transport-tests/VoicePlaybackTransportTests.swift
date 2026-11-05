import Foundation
import XCTest
import EvenerVoiceCore
@testable import EvenerVoiceTransport

final class VoicePlaybackTransportTests: XCTestCase {
    func testWirePCMReachesProductionPlaybackValidation() async throws {
        let peer = try VoiceWebSocketTestPeer(scenario: #"""
        for count in [48000, 48002, 65536, 0, 1, 48001, 65535, 65537, 65538]:
            pcm = (b'\x00\x80' * ((count + 1) // 2))[:count]
            self.send_event({'type': 'audio', 'deliveryId': 'd', 'generation': 7, 'itemId': 'a', 'sampleRate': 24000, 'pcm': base64.b64encode(pcm).decode()})
        self.rfile.read(2) # Keep the peer open until the client closes.
"""#)
        defer { peer.stop() }
        let port = peer.port
        let queue = DispatchQueue(label: "voice-pcm-transport-test")
        let socket = VoiceSocket(queue: queue)
        defer { queue.sync { socket.close() } }
        let received = expectation(description: "all PCM packets decoded")
        received.expectedFulfillmentCount = 9
        var decodedBytes: [Int] = []
        var acceptedSamples: [Int] = []
        var rejectedBytes: [Int] = []
        try queue.sync {
            socket.failed = { XCTFail("Loopback voice connection failed") }
            socket.received = { event in
                defer { received.fulfill() }
                XCTAssertEqual(event["sampleRate"] as? Int, 24000)
                guard let encoded = event["pcm"] as? String, let pcm = Data(base64Encoded: encoded) else { return XCTFail("PCM was not decoded") }
                decodedBytes.append(pcm.count)
                do {
                    let packet = try VoicePlaybackPacket(pcm)
                    var samples = [Float](repeating: .nan, count: packet.sampleCount)
                    samples.withUnsafeMutableBufferPointer { packet.copySamples(to: $0) }
                    XCTAssertTrue(samples.allSatisfy { $0 == -1 })
                    acceptedSamples.append(packet.sampleCount)
                } catch {
                    XCTAssertEqual((error as? VoiceFailure)?.code, "audio_packet_invalid")
                    rejectedBytes.append(pcm.count)
                }
            }
            try socket.connect(origin: "http://127.0.0.1:\(port)", path: "/api/voice/socket", grant: "fixture-only", binding: "binding", bindingEpoch: "epoch", device: "device")
        }
        await fulfillment(of: [received], timeout: 5)
        queue.sync {
            XCTAssertEqual(decodedBytes, [48000, 48002, 65536, 0, 1, 48001, 65535, 65537, 65538])
            XCTAssertEqual(acceptedSamples, [24000, 24001, 32768])
            XCTAssertEqual(rejectedBytes, [0, 1, 48001, 65535, 65537, 65538])
        }
    }
}
