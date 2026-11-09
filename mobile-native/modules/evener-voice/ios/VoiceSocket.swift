import Foundation
import Darwin
#if SWIFT_PACKAGE
import EvenerVoiceCore
#endif

/// Resolve every plaintext address against the tailnet boundary, then connect to
/// one literal IP. That URL stays pinned for the grant's whole lifetime.
enum VoiceTransport {
    static func validate(_ origin: String) throws -> URL {
        guard let url = URL(string: origin), let host = url.host, url.user == nil, url.password == nil,
              url.query == nil, url.fragment == nil, url.path.isEmpty || url.path == "/",
              url.scheme == "https" || url.scheme == "http" else { throw VoiceFailure("transport_invalid") }
        if url.scheme == "https" { return url }
        let literal = host.trimmingCharacters(in: CharacterSet(charactersIn: "[]"))
        var hints = addrinfo()
        hints.ai_socktype = SOCK_STREAM
        var result: UnsafeMutablePointer<addrinfo>?
        guard getaddrinfo(literal, nil, &hints, &result) == 0, let first = result else { throw VoiceFailure("transport_dns_failed") }
        defer { freeaddrinfo(first) }
        var next: UnsafeMutablePointer<addrinfo>? = first
        var pinned: String?
        while let entry = next {
            let info = entry.pointee
            var buffer = [CChar](repeating: 0, count: Int(NI_MAXHOST))
            guard getnameinfo(info.ai_addr, info.ai_addrlen, &buffer, socklen_t(buffer.count), nil, 0, NI_NUMERICHOST) == 0 else { throw VoiceFailure("transport_dns_failed") }
            let address = String(cString: buffer).lowercased()
            let v4 = address.split(separator: ".").compactMap { Int($0) }
            let privateAddress = address == "::1" || address.hasPrefix("fd7a:115c:a1e0:") ||
                (v4.count == 4 && (v4[0] == 127 || (v4[0] == 100 && (64...127).contains(v4[1]))))
            guard privateAddress else { throw VoiceFailure("transport_requires_tls") }
            if pinned == nil { pinned = address }
            next = info.ai_next
        }
        guard let pinned, var components = URLComponents(url: url, resolvingAgainstBaseURL: false) else { throw VoiceFailure("transport_invalid") }
        components.host = pinned.contains(":") ? "[\(pinned)]" : pinned
        guard let resolved = components.url else { throw VoiceFailure("transport_invalid") }
        return resolved
    }
}

final class VoiceSocket: NSObject, URLSessionTaskDelegate {
    private let queue: DispatchQueue
    private var session: URLSession?
    private var socket: URLSessionWebSocketTask?
    private var outbound: [String] = []
    private var outboundBytes = 0
    private var sending = false
    private var epoch = 0
    var received: (([String: Any]) -> Void)?
    var failed: (() -> Void)?
    init(queue: DispatchQueue) { self.queue = queue }
    func connect(origin: String, path: String, grant: String, binding: String, bindingEpoch: String, device: String) throws {
        close()
        guard path == "/api/voice/socket" else { throw VoiceFailure("socket_path_invalid") }
        let originURL = try VoiceTransport.validate(origin)
        var components = URLComponents(url: originURL, resolvingAgainstBaseURL: false)!
        components.scheme = originURL.scheme == "https" ? "wss" : "ws"
        components.path = path
        var request = URLRequest(url: components.url!)
        request.setValue("Bearer \(grant)", forHTTPHeaderField: "Authorization")
        request.setValue(binding, forHTTPHeaderField: "X-Evener-Voice-Binding")
        request.setValue(bindingEpoch, forHTTPHeaderField: "X-Evener-Voice-Epoch")
        request.setValue(device, forHTTPHeaderField: "X-Evener-Voice-Device")
        let config = URLSessionConfiguration.ephemeral
        config.httpCookieStorage = nil; config.urlCache = nil
        let session = URLSession(configuration: config, delegate: self, delegateQueue: nil)
        self.session = session
        let socket = session.webSocketTask(with: request)
        socket.maximumMessageSize = 128 * 1024
        self.socket = socket
        socket.resume()
        receive(socket, epoch: epoch)
    }
    func send(_ object: [String: Any]) {
        guard socket != nil, let data = try? JSONSerialization.data(withJSONObject: object), let value = String(data: data, encoding: .utf8) else { return }
        guard outboundBytes + data.count <= 128 * 1024 else { close(); failed?(); return }
        outbound.append(value); outboundBytes += data.count
        drain()
    }
    private func drain() {
        guard !sending, let socket, let value = outbound.first else { return }
        sending = true
        let stamp = epoch
        socket.send(.string(value)) { [weak self] error in
            guard let self else { return }
            self.queue.async {
                guard stamp == self.epoch else { return }
                if error != nil { self.close(); self.failed?(); return }
                self.outbound.removeFirst(); self.outboundBytes -= value.utf8.count
                self.sending = false; self.drain()
            }
        }
    }
    private func receive(_ socket: URLSessionWebSocketTask, epoch stamp: Int) {
        socket.receive { [weak self] result in
            guard let self else { return }
            self.queue.async {
                guard stamp == self.epoch else { return }
                do {
                    let message = try result.get()
                    let data: Data
                    switch message { case .string(let text): data = Data(text.utf8); case .data(let value): data = value; @unknown default: throw VoiceFailure("socket_message_invalid") }
                    guard let object = try JSONSerialization.jsonObject(with: data) as? [String: Any] else { throw VoiceFailure("socket_message_invalid") }
                    self.received?(object)
                    if stamp == self.epoch { self.receive(socket, epoch: stamp) }
                } catch { self.close(); self.failed?() }
            }
        }
    }
    func revokeAndClose() {
        guard let retiring = socket, let retiringSession = session else { close(); return }
        epoch += 1
        socket = nil; session = nil
        outbound.removeAll(); outboundBytes = 0; sending = false
        // Do not tear down the task before its revocation can leave the device.
        // This old connection is detached from any subsequent Arm operation.
        retiring.send(.string("{\"type\":\"disarm\"}")) { _ in
            retiring.cancel(with: .normalClosure, reason: nil)
            retiringSession.invalidateAndCancel()
        }
        queue.asyncAfter(deadline: .now() + 1) {
            retiring.cancel(with: .goingAway, reason: nil)
            retiringSession.invalidateAndCancel()
        }
    }
    func close() {
        epoch += 1
        socket?.cancel(with: .goingAway, reason: nil); socket = nil
        session?.invalidateAndCancel(); session = nil
        outbound.removeAll(); outboundBytes = 0; sending = false
    }
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) { completionHandler(nil) }
}
