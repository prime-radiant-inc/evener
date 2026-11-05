import Foundation
import XCTest
import EvenerVoiceCore
@testable import EvenerVoiceTransport

final class VoicePlaybackTransportTests: XCTestCase {
    func testWirePCMReachesProductionPlaybackValidation() async throws {
        let peer = Process()
        peer.executableURL = URL(fileURLWithPath: "/usr/bin/python3")
        peer.arguments = ["-u", "-c", #"""
import base64, hashlib, http.server, json, struct
class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    def do_GET(self):
        assert self.path == '/api/voice/socket'
        key = self.headers['Sec-WebSocket-Key'] + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11'
        self.send_response(101)
        self.send_header('Upgrade', 'websocket')
        self.send_header('Connection', 'Upgrade')
        self.send_header('Sec-WebSocket-Accept', base64.b64encode(hashlib.sha1(key.encode()).digest()).decode())
        self.end_headers()
        for count in [48000, 48002, 65536, 0, 1, 48001, 65535, 65537, 65538]:
            pcm = (b'\x00\x80' * ((count + 1) // 2))[:count]
            payload = json.dumps({'type': 'audio', 'deliveryId': 'd', 'generation': 7, 'itemId': 'a', 'sampleRate': 24000, 'pcm': base64.b64encode(pcm).decode()}).encode()
            if len(payload) < 126: header = bytes([0x81, len(payload)])
            elif len(payload) <= 65535: header = bytes([0x81, 126]) + struct.pack('!H', len(payload))
            else: header = bytes([0x81, 127]) + struct.pack('!Q', len(payload))
            self.wfile.write(header + payload)
            self.wfile.flush()
        self.rfile.read(2) # Keep the peer open until the client closes.
    def log_message(self, *args): pass
server = http.server.HTTPServer(('127.0.0.1', 0), Handler)
print(server.server_port, flush=True)
server.serve_forever()
"""#]
        let output = Pipe(); peer.standardOutput = output
        try peer.run()
        defer { peer.terminate(); peer.waitUntilExit() }
        let port = String(data: output.fileHandleForReading.availableData, encoding: .utf8)!.trimmingCharacters(in: .whitespacesAndNewlines)
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
