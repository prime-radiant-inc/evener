import Foundation
#if SWIFT_PACKAGE
import EvenerVoiceCore
#endif
#if canImport(UIKit)
import UIKit
#endif

/// One foreground request. Never owned by VoiceService and never stores credentials.
final class VoiceAuthorization: NSObject, URLSessionTaskDelegate {
    static func request(origin: String, path: String, token: String, body: String) async throws -> [String: Any] {
        guard path == "/api/voice/arm" || path == "/api/voice/disarm" || path == "/api/voice/status" else { throw VoiceFailure("authorization_path_invalid") }
        let pinned = try VoiceTransport.validate(origin)
        var components = URLComponents(url: pinned, resolvingAgainstBaseURL: false)!
        components.path = path
        var request = URLRequest(url: components.url!)
        request.httpMethod = "POST"
        request.httpBody = body.data(using: .utf8)
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.timeoutInterval = 30
        let configuration = URLSessionConfiguration.ephemeral
        configuration.httpCookieStorage = nil; configuration.urlCache = nil
        let session = URLSession(configuration: configuration, delegate: VoiceAuthorization(), delegateQueue: nil)
        defer { session.invalidateAndCancel() }
        #if canImport(UIKit)
        guard await MainActor.run(body: { UIApplication.shared.applicationState == .active }) else { throw VoiceFailure("foreground_required") }
        #endif
        let (data, response) = try await session.data(for: request)
        guard let response = response as? HTTPURLResponse, data.count <= 65536,
              let body = String(data: data, encoding: .utf8) else { throw VoiceFailure("authorization_response_invalid") }
        guard !(300..<400).contains(response.statusCode) else { throw VoiceFailure("authorization_redirect_rejected") }
        return ["origin": pinned.absoluteString.trimmingCharacters(in: CharacterSet(charactersIn: "/")), "status": response.statusCode, "body": body]
    }
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) { completionHandler(nil) }
}
