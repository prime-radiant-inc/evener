// The app group the extension and the app share (mirrors
// SHARE_APP_GROUP in mobile-native/src/share/appGroupSharePort.ts, and the
// entitlement both targets carry). The extension writes one JSON file per
// share here; the app reads it back and deletes it once the text is in a
// draft.
import UIKit
import UniformTypeIdentifiers

private let appGroup = "group.com.primeradiant.evener.native"
private let hostScheme = "evener-native"
private let sharePrefix = "share-"
private let maxPendingShares = 10

/// Keeps the app group small: a share older than the newest maxPendingShares
/// is dropped, so a share left behind by a crash cannot collect forever. This
/// runs off the main actor, so it is a free function rather than a method.
private func prunePendingShares(in container: URL) throws {
	let files = try FileManager.default.contentsOfDirectory(
		at: container,
		includingPropertiesForKeys: [.creationDateKey]
	).filter { $0.lastPathComponent.hasPrefix(sharePrefix) && $0.pathExtension == "json" }
	// Read each date once, then sort, rather than a filesystem read per
	// comparison.
	let dated = files.map { file in
		((try? file.resourceValues(forKeys: [.creationDateKey]))?.creationDate ?? .distantPast, file)
	}
	for (_, stale) in dated.sorted(by: { $0.0 < $1.0 }).dropLast(maxPendingShares) {
		try? FileManager.default.removeItem(at: stale)
	}
}

/// Takes the shared text or web link, saves it, and asks Evener to come
/// forward. The share sheet dismisses only when the save and the open have both
/// been attempted; if either fails the view stays with what happened, so the
/// person can still get the text into Evener by hand.
final class ShareViewController: UIViewController {
	private let status = UILabel()
	private let spinner = UIActivityIndicatorView(style: .medium)

	override func viewDidLoad() {
		super.viewDidLoad()
		view.backgroundColor = .systemBackground
		status.numberOfLines = 0
		status.textAlignment = .center
		status.font = .preferredFont(forTextStyle: .body)
		status.text = "Sending to Evener…"
		status.translatesAutoresizingMaskIntoConstraints = false
		spinner.translatesAutoresizingMaskIntoConstraints = false
		view.addSubview(spinner)
		view.addSubview(status)
		NSLayoutConstraint.activate([
			spinner.centerXAnchor.constraint(equalTo: view.centerXAnchor),
			spinner.centerYAnchor.constraint(equalTo: view.centerYAnchor, constant: -24),
			status.topAnchor.constraint(equalTo: spinner.bottomAnchor, constant: 16),
			status.leadingAnchor.constraint(equalTo: view.leadingAnchor, constant: 24),
			status.trailingAnchor.constraint(equalTo: view.trailingAnchor, constant: -24),
		])
		spinner.startAnimating()
		Task { await send() }
	}

	private func send() async {
		let text = await sharedContent()
		guard !text.isEmpty else {
			finish(failed: "There was nothing to send. Copy the text and share it again.")
			return
		}
		guard let container = FileManager.default.containerURL(
			forSecurityApplicationGroupIdentifier: appGroup
		) else {
			finish(failed: "Evener's share directory is unavailable. Reinstall Evener and try again.")
			return
		}
		let id = UUID().uuidString
		let file = container.appendingPathComponent("\(sharePrefix)\(id).json")
		let payload: [String: Any] = [
			"id": id,
			"text": text,
			"createdAt": ISO8601DateFormatter().string(from: Date()),
		]
		do {
			let data = try JSONSerialization.data(withJSONObject: payload)
			// The write and the prune are file work the share sheet does not
			// need to wait for on the main thread.
			try await Task.detached(priority: .userInitiated) {
				try data.write(to: file, options: .atomic)
				try prunePendingShares(in: container)
			}.value
		} catch {
			try? FileManager.default.removeItem(at: file)
			finish(failed: "Evener couldn't save this. Try sharing it again.")
			return
		}
		if openHostApp() {
			extensionContext?.completeRequest(returningItems: [], completionHandler: nil)
		} else {
			finish(failed: "Your text is saved. Open Evener to finish.")
		}
	}

	/// The share's text: the shared text and web link, text first, each on its
	/// own line. A share with neither is empty.
	private func sharedContent() async -> String {
		let items = (extensionContext?.inputItems as? [NSExtensionItem]) ?? []
		var parts: [String] = []
		for item in items {
			for provider in item.attachments ?? [] {
				if provider.hasItemConformingToTypeIdentifier(UTType.plainText.identifier) {
					if let text = await load(provider, UTType.plainText.identifier) as? String, !text.isEmpty {
						parts.append(text)
					}
				} else if provider.hasItemConformingToTypeIdentifier(UTType.url.identifier) {
					if let url = await load(provider, UTType.url.identifier) as? URL, !url.absoluteString.isEmpty {
						parts.append(url.absoluteString)
					}
				}
			}
		}
		return parts.joined(separator: "\n")
	}

	private func load(_ provider: NSItemProvider, _ type: String) async -> Any? {
		await withCheckedContinuation { continuation in
			provider.loadItem(forTypeIdentifier: type, options: nil) { value, _ in
				continuation.resume(returning: value)
			}
		}
	}

	/// Opens Evener through the responder chain. A share extension cannot use
	/// UIApplication directly and Apple documents no way to open its host, so
	/// this walks the chain to the application, as other apps' share
	/// extensions do (see the design's App Store risk note).
	private func openHostApp() -> Bool {
		guard let url = URL(string: "\(hostScheme)://share") else { return false }
		var responder: UIResponder? = self
		while let current = responder {
			if let application = current as? UIApplication {
				application.open(url, options: [:], completionHandler: nil)
				return true
			}
			responder = current.next
		}
		return false
	}

	private func finish(failed message: String) {
		spinner.stopAnimating()
		status.text = message
		let done = UIButton(type: .system)
		done.setTitle("Done", for: .normal)
		done.addTarget(self, action: #selector(close), for: .touchUpInside)
		done.translatesAutoresizingMaskIntoConstraints = false
		view.addSubview(done)
		NSLayoutConstraint.activate([
			done.topAnchor.constraint(equalTo: status.bottomAnchor, constant: 16),
			done.centerXAnchor.constraint(equalTo: view.centerXAnchor),
		])
	}

	@objc private func close() {
		extensionContext?.completeRequest(returningItems: [], completionHandler: nil)
	}
}
