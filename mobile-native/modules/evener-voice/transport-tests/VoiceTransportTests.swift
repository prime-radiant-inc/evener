import Foundation
import XCTest
import EvenerVoiceCore
@testable import EvenerVoiceTransport

final class VoiceTransportTests: XCTestCase {
    func testPlaintextResolutionPinsLoopbackAndRejectsPublicOrLAN() throws {
        let pinned = try VoiceTransport.validate("http://localhost:9180")
        XCTAssertNotEqual(pinned.host, "localhost")
        XCTAssertTrue(["127.0.0.1", "::1", "[::1]"].contains(pinned.host ?? ""))
        XCTAssertEqual(pinned.port, 9180)
        XCTAssertEqual(try VoiceTransport.validate("http://100.100.1.2:9180").host, "100.100.1.2")
        for origin in ["http://192.168.1.1", "http://10.0.0.1", "http://8.8.8.8"] {
            XCTAssertThrowsError(try VoiceTransport.validate(origin)) { XCTAssertEqual(($0 as? VoiceFailure)?.code, "transport_requires_tls") }
        }
        XCTAssertEqual(try VoiceTransport.validate("https://example.com").host, "example.com")
    }

    func testAuthorizationRejectsRealHTTPRedirectWithoutForwardingCredential() async throws {
        // This is an actual loopback HTTP peer, not a mocked URLSession or controller.
        let peer = Process()
        peer.executableURL = URL(fileURLWithPath: "/usr/bin/python3")
        peer.arguments = ["-u", "-c", #"""
import http.server, json
hits = 0
class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        global hits
        self.rfile.read(int(self.headers.get('Content-Length', '0')))
        if self.path in ['/api/voice/arm', '/api/voice/socket']:
            self.send_response(307)
            self.send_header('Location', 'http://127.0.0.1:%d/forbidden' % self.server.server_port)
            self.end_headers()
        else:
            if self.path == '/forbidden': hits += 1
            self.send_response(200)
            self.end_headers()
            self.wfile.write(json.dumps({'redirectedRequests': hits}).encode())
    do_GET = do_POST
    def log_message(self, *args): pass
server = http.server.HTTPServer(('127.0.0.1', 0), Handler)
print(server.server_port, flush=True)
server.serve_forever()
"""#]
        let output = Pipe(); peer.standardOutput = output
        try peer.run()
        defer { peer.terminate(); peer.waitUntilExit() }
        let port = String(data: output.fileHandleForReading.availableData, encoding: .utf8)!.trimmingCharacters(in: .whitespacesAndNewlines)
        let origin = "http://127.0.0.1:\(port)"
        do {
            _ = try await VoiceAuthorization.request(origin: origin, path: "/api/voice/arm", token: "fixture-only", body: "{}")
            XCTFail("A real redirect response must not be followed")
        } catch { XCTAssertEqual((error as? VoiceFailure)?.code, "authorization_redirect_rejected") }
        let queue = DispatchQueue(label: "voice-transport-test")
        let socket = VoiceSocket(queue: queue)
        let rejected = expectation(description: "redirected voice socket is rejected")
        try queue.sync {
            socket.failed = { rejected.fulfill() }
            try socket.connect(origin: origin, path: "/api/voice/socket", grant: "fixture-only", binding: "binding", bindingEpoch: "epoch", device: "device")
        }
        await fulfillment(of: [rejected], timeout: 5)
        queue.sync { socket.close() }
        let response = try await VoiceAuthorization.request(origin: origin, path: "/api/voice/disarm", token: "fixture-only", body: "{}")
        XCTAssertEqual(response["status"] as? Int, 200)
        XCTAssertEqual(response["origin"] as? String, origin)
        let body = try JSONSerialization.jsonObject(with: Data((response["body"] as! String).utf8)) as! [String: Int]
        XCTAssertEqual(body["redirectedRequests"], 0)
    }
}
