import Foundation

/// Loopback WebSocket peer for scripted voice delivery and client controls.
final class VoiceWebSocketTestPeer {
    private let process = Process()
    let port: String

    init(scenario: String) throws {
        process.executableURL = URL(fileURLWithPath: "/usr/bin/python3")
        process.arguments = ["-u", "-c", #"""
import base64, hashlib, http.server, json, struct
class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    def send_event(self, value):
        payload = json.dumps(value).encode()
        if len(payload) < 126: header = bytes([0x81, len(payload)])
        elif len(payload) <= 65535: header = bytes([0x81, 126]) + struct.pack('!H', len(payload))
        else: header = bytes([0x81, 127]) + struct.pack('!Q', len(payload))
        self.wfile.write(header + payload)
        self.wfile.flush()
    def receive_event(self):
        header = self.rfile.read(2)
        if not header or header[0] & 15 == 8: return None
        length = header[1] & 127
        if length == 126: length = struct.unpack('!H', self.rfile.read(2))[0]
        if length == 127: length = struct.unpack('!Q', self.rfile.read(8))[0]
        mask = self.rfile.read(4)
        payload = self.rfile.read(length)
        return json.loads(bytes(value ^ mask[index % 4] for index, value in enumerate(payload)))
    def do_GET(self):
        assert self.path == '/api/voice/socket'
        key = self.headers['Sec-WebSocket-Key'] + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11'
        self.send_response(101)
        self.send_header('Upgrade', 'websocket')
        self.send_header('Connection', 'Upgrade')
        self.send_header('Sec-WebSocket-Accept', base64.b64encode(hashlib.sha1(key.encode()).digest()).decode())
        self.end_headers()
        scenario(self)
    def log_message(self, *args): pass
"""# + "\ndef scenario(self):\n" + scenario + #"""

server = http.server.HTTPServer(('127.0.0.1', 0), Handler)
print(server.server_port, flush=True)
server.serve_forever()
"""#]
        let output = Pipe(); process.standardOutput = output
        try process.run()
        port = String(data: output.fileHandleForReading.availableData, encoding: .utf8)!.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    func stop() {
        process.terminate()
        process.waitUntilExit()
    }
}
