import XCTest
@testable import EvenerNativePlugin
import Tauri
import UIKit

final class QRScannerTests: XCTestCase {
    func testProductionPluginInstallsConcreteScanner() {
        let plugin = EvenerNativePlugin()
        XCTAssertTrue(plugin.hasScannerForTesting)
    }

    func testForwardsScannedTextOnlyToNativeRustCompletion() throws {
        let scanner = FakeQRScanner()
        let coordinator = QRScanCoordinator(scanner: scanner)
        var result: Result<String, Error>?

        coordinator.scan { result = $0 }
        scanner.complete(.success("https://hub.example.test/auth?token=native-only"))

        XCTAssertEqual(
            try XCTUnwrap(result).get(),
            "https://hub.example.test/auth?token=native-only"
        )
    }

    func testForwardsScannerFailureWithoutAddingScannedData() throws {
        let scanner = FakeQRScanner()
        let coordinator = QRScanCoordinator(scanner: scanner)
        var result: Result<String, Error>?

        coordinator.scan { result = $0 }
        scanner.complete(.failure(QRScanError.cameraUnavailable))

        XCTAssertThrowsError(try XCTUnwrap(result).get())
    }

    func testSystemScannerAuthorizedSuccessAndDoubleCompletionCleanup() throws {
        let permission = FakeCameraPermission(state: .authorized)
        let sessionFactory = FakeQRScanSessionFactory()
        let presenter = FakeQRScanPresenter()
        let scanner = SystemQRScanner(
            permission: permission,
            sessionFactory: sessionFactory,
            presenter: presenter
        )
        var results: [Result<String, Error>] = []

        scanner.scan { results.append($0) }
        let session = try XCTUnwrap(sessionFactory.session)
        XCTAssertEqual(session.startCount, 1)
        XCTAssertEqual(presenter.presentCount, 1)

        session.emitCode("https://hub.example.test/auth?token=native-only")
        presenter.cancel()
        session.emitFailure(QRScanError.cameraUnavailable)

        XCTAssertEqual(results.count, 1)
        XCTAssertEqual(
            try results[0].get(),
            "https://hub.example.test/auth?token=native-only"
        )
        XCTAssertEqual(session.stopCount, 1)
        XCTAssertEqual(presenter.dismissCount, 1)

        // Completion cleanup releases the active operation so the same
        // production scanner type can start another scan.
        scanner.scan { _ in }
        XCTAssertEqual(sessionFactory.makeCount, 2)
    }

    func testSystemScannerRejectsNewScanUntilDeferredDismissalCompletes() throws {
        let permission = FakeCameraPermission(state: .authorized)
        let sessionFactory = FakeQRScanSessionFactory()
        let presenter = FakeQRScanPresenter(automaticallyCompletesDismissal: false)
        let scanner = SystemQRScanner(
            permission: permission,
            sessionFactory: sessionFactory,
            presenter: presenter
        )
        var firstResults: [Result<String, Error>] = []
        var secondResults: [Result<String, Error>] = []

        scanner.scan { firstResults.append($0) }
        let firstSession = try XCTUnwrap(sessionFactory.session)
        firstSession.emitCode("first")

        XCTAssertEqual(firstSession.stopCount, 1)
        XCTAssertEqual(presenter.dismissCount, 1)
        XCTAssertTrue(firstResults.isEmpty, "the operation must remain active until UIKit finishes dismissal")

        scanner.scan { secondResults.append($0) }

        XCTAssertEqual(sessionFactory.makeCount, 1, "a dismissing scanner still owns the modal lifetime")
        XCTAssertEqual(secondResults.count, 1)
        XCTAssertThrowsError(try secondResults[0].get())

        presenter.completeDismissal()

        XCTAssertEqual(firstResults.count, 1)
        XCTAssertEqual(try firstResults[0].get(), "first")

        scanner.scan { _ in }
        XCTAssertEqual(sessionFactory.makeCount, 2, "the next scan is admitted after dismissal completion")
    }

    func testSystemScannerMarshalsAuthorizedScanLifecycleToMainThread() throws {
        let started = expectation(description: "capture session started")
        let completed = expectation(description: "scan completed")
        let permission = FakeCameraPermission(state: .authorized)
        let sessionFactory = ThreadRecordingQRScanSessionFactory(started: started)
        let presenter = ThreadRecordingQRScanPresenter()
        let scanner = SystemQRScanner(
            permission: permission,
            sessionFactory: sessionFactory,
            presenter: presenter
        )
        let completionThread = ThreadObservation()

        DispatchQueue.global(qos: .userInitiated).async {
            XCTAssertFalse(Thread.isMainThread, "test must enter the real scanner off-main")
            scanner.scan { result in
                completionThread.recordCurrentThread()
                if case .failure(let error) = result {
                    XCTFail("expected scan success, got \(error)")
                }
                completed.fulfill()
            }
        }

        wait(for: [started], timeout: 1)
        let session = try XCTUnwrap(sessionFactory.session)
        XCTAssertTrue(sessionFactory.makeWasMain, "session creation must run on main")
        XCTAssertTrue(presenter.presentWasMain, "native presentation must run on main")
        XCTAssertTrue(session.startWasMain, "capture start must be owned from main")

        DispatchQueue.global(qos: .userInitiated).async {
            session.emitCode("https://hub.example.test/auth?token=native-only")
        }

        wait(for: [completed], timeout: 1)
        XCTAssertTrue(session.stopWasMain, "terminal cleanup must return to main")
        XCTAssertTrue(presenter.dismissWasMain, "native dismissal must run on main")
        XCTAssertTrue(completionThread.wasMain, "terminal completion must run on main")
    }

    func testSystemScannerMarshalsBackgroundPermissionGrantLifecycleToMainThread() throws {
        let started = expectation(description: "capture session started")
        let completed = expectation(description: "scan completed")
        let permissionDelivered = expectation(description: "permission delivered from background")
        let permission = FakeCameraPermission(state: .notDetermined)
        let sessionFactory = ThreadRecordingQRScanSessionFactory(started: started)
        let presenter = ThreadRecordingQRScanPresenter()
        let scanner = SystemQRScanner(
            permission: permission,
            sessionFactory: sessionFactory,
            presenter: presenter
        )
        let completionThread = ThreadObservation()

        scanner.scan { result in
            completionThread.recordCurrentThread()
            if case .failure(let error) = result {
                XCTFail("expected scan success, got \(error)")
            }
            completed.fulfill()
        }

        DispatchQueue.global(qos: .userInitiated).async {
            XCTAssertFalse(Thread.isMainThread)
            permission.resolve(true)
            permissionDelivered.fulfill()
        }

        wait(for: [permissionDelivered, started], timeout: 1)
        let session = try XCTUnwrap(sessionFactory.session)
        XCTAssertTrue(sessionFactory.makeWasMain, "session creation must return to main")
        XCTAssertTrue(presenter.presentWasMain, "presentation must return to main")
        XCTAssertTrue(session.startWasMain, "capture start must return to main")

        session.emitCode("granted-off-main")

        wait(for: [completed], timeout: 1)
        XCTAssertTrue(session.stopWasMain)
        XCTAssertTrue(presenter.dismissWasMain)
        XCTAssertTrue(completionThread.wasMain)
    }

    func testSystemScannerAcceptsDuplicatePermissionCompletionOnlyOnce() throws {
        let permission = FakeCameraPermission(state: .notDetermined)
        let sessionFactory = FakeQRScanSessionFactory()
        let presenter = FakeQRScanPresenter()
        let scanner = SystemQRScanner(
            permission: permission,
            sessionFactory: sessionFactory,
            presenter: presenter
        )
        var results: [Result<String, Error>] = []

        scanner.scan { results.append($0) }
        permission.resolve(true)
        permission.resolve(true)

        XCTAssertEqual(sessionFactory.makeCount, 1)
        XCTAssertEqual(presenter.presentCount, 1)

        try XCTUnwrap(sessionFactory.session).emitCode("once")

        XCTAssertEqual(results.count, 1)
        XCTAssertEqual(try results[0].get(), "once")
    }

    func testSystemScannerSerializesConcurrentScanEntries() throws {
        let started = expectation(description: "one capture session started")
        let bothEntriesReturned = expectation(description: "both scan entries returned")
        bothEntriesReturned.expectedFulfillmentCount = 2
        let bothResultsDelivered = expectation(description: "both scan results delivered")
        bothResultsDelivered.expectedFulfillmentCount = 2
        let gate = DispatchSemaphore(value: 0)
        let permission = FakeCameraPermission(state: .authorized)
        let sessionFactory = ThreadRecordingQRScanSessionFactory(started: started)
        let presenter = ThreadRecordingQRScanPresenter()
        let scanner = SystemQRScanner(
            permission: permission,
            sessionFactory: sessionFactory,
            presenter: presenter
        )
        let results = LockedScanResults()

        for entry in 0..<2 {
            DispatchQueue.global(qos: .userInitiated).async {
                gate.wait()
                scanner.scan { result in
                    results.record(entry: entry, result: result)
                    bothResultsDelivered.fulfill()
                }
                bothEntriesReturned.fulfill()
            }
        }
        gate.signal()
        gate.signal()

        wait(for: [bothEntriesReturned, started], timeout: 1)
        XCTAssertEqual(sessionFactory.makeCount, 1)
        XCTAssertEqual(presenter.presentCount, 1)
        XCTAssertEqual(results.failureCount, 1, "exactly one concurrent entry must be rejected")

        try XCTUnwrap(sessionFactory.session).emitCode("winner")
        wait(for: [bothResultsDelivered], timeout: 1)

        XCTAssertEqual(results.count, 2)
        XCTAssertEqual(results.successCount, 1)
        XCTAssertEqual(results.failureCount, 1)
    }

    func testSystemScannerDeniedNeverBuildsOrPresentsSession() throws {
        let permission = FakeCameraPermission(state: .denied)
        let sessionFactory = FakeQRScanSessionFactory()
        let presenter = FakeQRScanPresenter()
        let scanner = SystemQRScanner(
            permission: permission,
            sessionFactory: sessionFactory,
            presenter: presenter
        )
        var result: Result<String, Error>?

        scanner.scan { result = $0 }

        XCTAssertThrowsError(try XCTUnwrap(result).get()) { error in
            XCTAssertTrue(error is QRScanError)
        }
        XCTAssertEqual(sessionFactory.makeCount, 0)
        XCTAssertEqual(presenter.presentCount, 0)
    }

    func testSystemScannerPermissionRequestDenialCleansUp() throws {
        let permission = FakeCameraPermission(state: .notDetermined)
        let sessionFactory = FakeQRScanSessionFactory()
        let presenter = FakeQRScanPresenter()
        let scanner = SystemQRScanner(
            permission: permission,
            sessionFactory: sessionFactory,
            presenter: presenter
        )
        var result: Result<String, Error>?

        scanner.scan { result = $0 }
        XCTAssertEqual(permission.requestCount, 1)
        permission.resolve(false)

        XCTAssertThrowsError(try XCTUnwrap(result).get())
        XCTAssertEqual(sessionFactory.makeCount, 0)
        XCTAssertEqual(presenter.dismissCount, 0)
    }

    func testSystemScannerSetupFailureCompletesOnceWithoutPresentation() throws {
        let permission = FakeCameraPermission(state: .authorized)
        let sessionFactory = FakeQRScanSessionFactory()
        sessionFactory.error = QRScanError.cameraUnavailable
        let presenter = FakeQRScanPresenter()
        let scanner = SystemQRScanner(
            permission: permission,
            sessionFactory: sessionFactory,
            presenter: presenter
        )
        var results: [Result<String, Error>] = []

        scanner.scan { results.append($0) }

        XCTAssertEqual(results.count, 1)
        XCTAssertThrowsError(try results[0].get())
        XCTAssertEqual(presenter.presentCount, 0)
        XCTAssertEqual(presenter.dismissCount, 0)
    }

    func testSystemScannerCancelStopsSessionAndDismissesOnce() throws {
        let permission = FakeCameraPermission(state: .authorized)
        let sessionFactory = FakeQRScanSessionFactory()
        let presenter = FakeQRScanPresenter()
        let scanner = SystemQRScanner(
            permission: permission,
            sessionFactory: sessionFactory,
            presenter: presenter
        )
        var results: [Result<String, Error>] = []

        scanner.scan { results.append($0) }
        let session = try XCTUnwrap(sessionFactory.session)
        presenter.cancel()
        presenter.cancel()

        XCTAssertEqual(results.count, 1)
        XCTAssertThrowsError(try results[0].get())
        XCTAssertEqual(session.stopCount, 1)
        XCTAssertEqual(presenter.dismissCount, 1)
    }

    func testPresentationResolverRecursesThroughNestedContainersAndPresentation() {
        let final = UIViewController()
        let presentingLeaf = PresentedViewController(presented: final)
        let innerNavigation = UINavigationController(rootViewController: presentingLeaf)
        let tabs = UITabBarController()
        tabs.viewControllers = [UIViewController(), innerNavigation]
        tabs.selectedIndex = 1
        let outerNavigation = UINavigationController(rootViewController: tabs)

        XCTAssertTrue(QRScanPresentationResolver.topViewController(from: outerNavigation) === final)
    }

    func testPresentationResolverSelectsVisibleAttachedKeyWindowFromForegroundScene() {
        let backgroundRoot = UIViewController()
        let detachedRoot = UIViewController()
        let hiddenRoot = UIViewController()
        let fallbackRoot = UIViewController()
        let keyRoot = UIViewController()
        let scenes = [
            QRScanSceneCandidate(
                identifier: "background",
                activationState: .background,
                windows: [.init(rootViewController: backgroundRoot, isKeyWindow: true, isHidden: false, alpha: 1, isAttached: true)]
            ),
            QRScanSceneCandidate(
                identifier: "foreground-a",
                activationState: .foregroundActive,
                windows: [
                    .init(rootViewController: detachedRoot, isKeyWindow: true, isHidden: false, alpha: 1, isAttached: false),
                    .init(rootViewController: hiddenRoot, isKeyWindow: true, isHidden: true, alpha: 1, isAttached: true),
                    .init(rootViewController: fallbackRoot, isKeyWindow: false, isHidden: false, alpha: 1, isAttached: true),
                ]
            ),
            QRScanSceneCandidate(
                identifier: "foreground-b",
                activationState: .foregroundActive,
                windows: [.init(rootViewController: keyRoot, isKeyWindow: true, isHidden: false, alpha: 1, isAttached: true)]
            ),
        ]

        XCTAssertTrue(QRScanPresentationResolver.rootViewController(from: scenes) === keyRoot)
    }

    func testPresentationResolverUsesDeterministicVisibleWindowFallback() {
        let transparentRoot = UIViewController()
        let fallbackRoot = UIViewController()
        let laterRoot = UIViewController()
        let scenes = [
            QRScanSceneCandidate(
                identifier: "foreground-b",
                activationState: .foregroundActive,
                windows: [.init(rootViewController: laterRoot, isKeyWindow: false, isHidden: false, alpha: 1, isAttached: true)]
            ),
            QRScanSceneCandidate(
                identifier: "foreground-a",
                activationState: .foregroundActive,
                windows: [
                    .init(rootViewController: transparentRoot, isKeyWindow: true, isHidden: false, alpha: 0, isAttached: true),
                    .init(rootViewController: fallbackRoot, isKeyWindow: false, isHidden: false, alpha: 1, isAttached: true),
                ]
            ),
        ]

        XCTAssertTrue(QRScanPresentationResolver.rootViewController(from: scenes) === fallbackRoot)
    }
}

private final class FakeCameraPermission: CameraPermissionProviding {
    private let lock = NSLock()
    private var storedState: CameraPermissionState
    private var storedRequestCount = 0
    private var completion: ((Bool) -> Void)?

    init(state: CameraPermissionState) {
        storedState = state
    }

    var state: CameraPermissionState {
        lock.lock()
        defer { lock.unlock() }
        return storedState
    }

    var requestCount: Int {
        lock.lock()
        defer { lock.unlock() }
        return storedRequestCount
    }

    func request(completion: @escaping (Bool) -> Void) {
        lock.lock()
        storedRequestCount += 1
        self.completion = completion
        lock.unlock()
    }

    func resolve(_ granted: Bool) {
        lock.lock()
        storedState = granted ? .authorized : .denied
        let completion = completion
        lock.unlock()
        completion?(granted)
    }
}

private final class FakeQRScanSession: QRScanSession {
    var startCount = 0
    var stopCount = 0
    var onCode: ((String) -> Void)?
    var onFailure: ((Error) -> Void)?

    func installPreview(in view: UIView) {}
    func start() { startCount += 1 }
    func stop() { stopCount += 1 }
    func emitCode(_ code: String) { onCode?(code) }
    func emitFailure(_ error: Error) { onFailure?(error) }
}

private final class FakeQRScanSessionFactory: QRScanSessionBuilding {
    var makeCount = 0
    var error: Error?
    var session: FakeQRScanSession?

    func makeSession(
        onCode: @escaping (String) -> Void,
        onFailure: @escaping (Error) -> Void
    ) throws -> QRScanSession {
        makeCount += 1
        if let error { throw error }
        let session = FakeQRScanSession()
        session.onCode = onCode
        session.onFailure = onFailure
        self.session = session
        return session
    }
}

private final class FakeQRScanPresenter: QRScanPresenting {
    var presentCount = 0
    var dismissCount = 0
    private let automaticallyCompletesDismissal: Bool
    private var onCancel: (() -> Void)?
    private var dismissalCompletion: (() -> Void)?

    init(automaticallyCompletesDismissal: Bool = true) {
        self.automaticallyCompletesDismissal = automaticallyCompletesDismissal
    }

    func present(session: QRScanSession, onCancel: @escaping () -> Void) throws {
        presentCount += 1
        self.onCancel = onCancel
    }

    func dismiss(completion: @escaping () -> Void) {
        dismissCount += 1
        if automaticallyCompletesDismissal {
            completion()
        } else {
            dismissalCompletion = completion
        }
    }

    func completeDismissal() {
        let completion = dismissalCompletion
        dismissalCompletion = nil
        completion?()
    }

    func cancel() { onCancel?() }
}

private final class PresentedViewController: UIViewController {
    private let storedPresentedViewController: UIViewController

    init(presented: UIViewController) {
        storedPresentedViewController = presented
        super.init(nibName: nil, bundle: nil)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { nil }

    override var presentedViewController: UIViewController? {
        storedPresentedViewController
    }
}

private final class LockedScanResults {
    private let lock = NSLock()
    private var results: [(Int, Result<String, Error>)] = []

    var count: Int {
        lock.lock()
        defer { lock.unlock() }
        return results.count
    }

    var successCount: Int {
        lock.lock()
        defer { lock.unlock() }
        return results.reduce(into: 0) { count, entry in
            if case .success = entry.1 { count += 1 }
        }
    }

    var failureCount: Int {
        lock.lock()
        defer { lock.unlock() }
        return results.reduce(into: 0) { count, entry in
            if case .failure = entry.1 { count += 1 }
        }
    }

    func record(entry: Int, result: Result<String, Error>) {
        lock.lock()
        results.append((entry, result))
        lock.unlock()
    }
}

private final class ThreadObservation {
    private let lock = NSLock()
    private var value = false

    var wasMain: Bool {
        lock.lock()
        defer { lock.unlock() }
        return value
    }

    func recordCurrentThread() {
        lock.lock()
        value = Thread.isMainThread
        lock.unlock()
    }
}

private final class ThreadRecordingQRScanSession: QRScanSession {
    private let lock = NSLock()
    private let started: XCTestExpectation
    private var _startWasMain = false
    private var _stopWasMain = false
    var onCode: ((String) -> Void)?

    init(started: XCTestExpectation) {
        self.started = started
    }

    var startWasMain: Bool {
        lock.lock()
        defer { lock.unlock() }
        return _startWasMain
    }

    var stopWasMain: Bool {
        lock.lock()
        defer { lock.unlock() }
        return _stopWasMain
    }

    func installPreview(in view: UIView) {}

    func start() {
        lock.lock()
        _startWasMain = Thread.isMainThread
        lock.unlock()
        started.fulfill()
    }

    func stop() {
        lock.lock()
        _stopWasMain = Thread.isMainThread
        lock.unlock()
    }

    func emitCode(_ code: String) { onCode?(code) }
}

private final class ThreadRecordingQRScanSessionFactory: QRScanSessionBuilding {
    private let lock = NSLock()
    private let started: XCTestExpectation
    private var _makeWasMain = false
    private var _makeCount = 0
    private var _session: ThreadRecordingQRScanSession?

    init(started: XCTestExpectation) {
        self.started = started
    }

    var makeWasMain: Bool {
        lock.lock()
        defer { lock.unlock() }
        return _makeWasMain
    }

    var session: ThreadRecordingQRScanSession? {
        lock.lock()
        defer { lock.unlock() }
        return _session
    }

    var makeCount: Int {
        lock.lock()
        defer { lock.unlock() }
        return _makeCount
    }

    func makeSession(
        onCode: @escaping (String) -> Void,
        onFailure: @escaping (Error) -> Void
    ) throws -> QRScanSession {
        let session = ThreadRecordingQRScanSession(started: started)
        session.onCode = onCode
        lock.lock()
        _makeCount += 1
        _makeWasMain = Thread.isMainThread
        _session = session
        lock.unlock()
        return session
    }
}

private final class ThreadRecordingQRScanPresenter: QRScanPresenting {
    private let lock = NSLock()
    private var _presentWasMain = false
    private var _dismissWasMain = false
    private var _presentCount = 0

    var presentWasMain: Bool {
        lock.lock()
        defer { lock.unlock() }
        return _presentWasMain
    }

    var dismissWasMain: Bool {
        lock.lock()
        defer { lock.unlock() }
        return _dismissWasMain
    }

    var presentCount: Int {
        lock.lock()
        defer { lock.unlock() }
        return _presentCount
    }

    func present(session: QRScanSession, onCancel: @escaping () -> Void) throws {
        lock.lock()
        _presentCount += 1
        _presentWasMain = Thread.isMainThread
        lock.unlock()
    }

    func dismiss(completion: @escaping () -> Void) {
        lock.lock()
        _dismissWasMain = Thread.isMainThread
        lock.unlock()
        completion()
    }
}

private final class FakeQRScanner: QRScanning {
    private var completion: ((Result<String, Error>) -> Void)?

    func scan(completion: @escaping (Result<String, Error>) -> Void) {
        self.completion = completion
    }

    func complete(_ result: Result<String, Error>) {
        completion?(result)
    }
}

// ---------------------------------------------------------------------------
// Plugin scan integration: injected scanner returns raw text to Rust
// ---------------------------------------------------------------------------

final class PluginScanIntegrationTests: XCTestCase {
    func testScanAndPreviewReturnsScannedTextToRust() throws {
        let scanner = FakePluginScanner()
        let plugin = EvenerNativePlugin(
            keychainClient: NoOpKeychainClient(),
            scanner: scanner
        )

        let (invoke, capturedResponse) = makeInvoke(args: "{}")
        try plugin.scanAndPreviewPairing(invoke)

        // The scanner hasn't completed yet; no response should be captured.
        XCTAssertNil(capturedResponse.value)

        // Complete the scan with raw text.
        scanner.complete(.success("https://hub.example.test/auth?token=native-only"))

        // The plugin should have resolved with a "scanned" result carrying
        // the raw text — this is the Swift→Rust transport channel only.
        let json = try XCTUnwrap(capturedResponse.value)
        XCTAssertTrue(json.contains("\"scanned\""))
        XCTAssertTrue(json.contains("native-only"))
    }

    func testScanAndPreviewReturnsUnavailableWhenNoScanner() throws {
        let plugin = EvenerNativePlugin(
            keychainClient: NoOpKeychainClient(),
            scanner: nil
        )

        let (invoke, capturedResponse) = makeInvoke(args: "{}")
        try plugin.scanAndPreviewPairing(invoke)

        let json = try XCTUnwrap(capturedResponse.value)
        XCTAssertTrue(json.contains("\"unavailable\""))
        XCTAssertTrue(json.contains("pairing_unavailable"))
    }

    func testScanAndPreviewReturnsErrorOnScanFailure() throws {
        let scanner = FakePluginScanner()
        let plugin = EvenerNativePlugin(
            keychainClient: NoOpKeychainClient(),
            scanner: scanner
        )

        let (invoke, capturedResponse) = makeInvoke(args: "{}")
        try plugin.scanAndPreviewPairing(invoke)
        scanner.complete(.failure(QRScanError.cameraUnavailable))

        let json = try XCTUnwrap(capturedResponse.value)
        XCTAssertTrue(json.contains("\"error\""))
        XCTAssertTrue(json.contains("pairing_unavailable"))
    }

    func testScanAndPreviewMapsPermissionDenied() throws {
        let scanner = FakePluginScanner()
        let plugin = EvenerNativePlugin(
            keychainClient: NoOpKeychainClient(),
            scanner: scanner
        )
        let (invoke, capturedResponse) = makeInvoke(args: "{}")
        try plugin.scanAndPreviewPairing(invoke)
        scanner.complete(.failure(QRScanError.permissionDenied))

        XCTAssertTrue(try XCTUnwrap(capturedResponse.value).contains("permission_denied"))
    }

    func testScanAndPreviewMapsUserCancellationWithoutRawText() throws {
        let scanner = FakePluginScanner()
        let plugin = EvenerNativePlugin(
            keychainClient: NoOpKeychainClient(),
            scanner: scanner
        )
        let (invoke, capturedResponse) = makeInvoke(args: "{}")
        try plugin.scanAndPreviewPairing(invoke)
        scanner.complete(.failure(QRScanError.cancelled))

        let json = try XCTUnwrap(capturedResponse.value)
        XCTAssertTrue(json.contains("Scan cancelled"))
        XCTAssertFalse(json.contains("scanned"))
    }

    func testSecureGetCapabilityReturnsStoredToken() throws {
        let keychainClient = RecordingKeychainClientPlugin()
        keychainClient.stored["profile:11111111-1111-1111-1111-111111111111"] =
            Data("native-only-capability".utf8)
        let plugin = EvenerNativePlugin(keychainClient: keychainClient)

        let args = "{\"profileId\":\"11111111-1111-1111-1111-111111111111\"}"
        let (invoke, capturedResponse) = makeInvoke(args: args)
        try plugin.secureGetCapability(invoke)

        let json = try XCTUnwrap(capturedResponse.value)
        XCTAssertTrue(json.contains("native-only-capability"))
    }

    func testSecureGetCapabilityReturnsNilWhenNotPresent() throws {
        let keychainClient = RecordingKeychainClientPlugin()
        let plugin = EvenerNativePlugin(keychainClient: keychainClient)

        let args = "{\"profileId\":\"11111111-1111-1111-1111-111111111111\"}"
        let (invoke, capturedResponse) = makeInvoke(args: args)
        try plugin.secureGetCapability(invoke)

        let json = try XCTUnwrap(capturedResponse.value)
        // When capability is nil, the JSON should not contain it or contain null.
        XCTAssertTrue(json.contains("null") || !json.contains("capability"))
    }

    func testScanResultNeverContainsRawTextInUnavailableResponse() throws {
        let plugin = EvenerNativePlugin(
            keychainClient: NoOpKeychainClient(),
            scanner: nil
        )

        let (invoke, capturedResponse) = makeInvoke(args: "{}")
        try plugin.scanAndPreviewPairing(invoke)

        let json = try XCTUnwrap(capturedResponse.value)
        // The unavailable response must not have a "scanned" field.
        XCTAssertFalse(json.contains("raw-secret"))
        XCTAssertFalse(json.contains("token=native-only"))
    }
}

// ---------------------------------------------------------------------------
// Test helpers for plugin integration tests
// ---------------------------------------------------------------------------

private final class FakePluginScanner: QRScanning {
    private var completion: ((Result<String, Error>) -> Void)?

    func scan(completion: @escaping (Result<String, Error>) -> Void) {
        self.completion = completion
    }

    func complete(_ result: Result<String, Error>) {
        completion?(result)
    }
}

private final class NoOpKeychainClient: KeychainClient {
    func get(service: String, account: String) throws -> Data? { nil }
    func set(_ data: Data, service: String, account: String, accessibility: CFString) throws {}
    func delete(service: String, account: String) throws {}
}

private final class RecordingKeychainClientPlugin: KeychainClient {
    var stored: [String: Data] = [:]

    func get(service: String, account: String) throws -> Data? {
        stored[account]
    }
    func set(_ data: Data, service: String, account: String, accessibility: CFString) throws {
        stored[account] = data
    }
    func delete(service: String, account: String) throws {
        stored[account] = nil
    }
}

/// Helper: create a real `Invoke` with a captured response callback.
private func makeInvoke(args: String) -> (Invoke, CapturedResponse) {
    let captured = CapturedResponse()
    let invoke = Invoke(
        command: "test",
        callback: 1,
        error: 0,
        sendResponse: { _, json in
            captured.value = json
        },
        sendChannelData: { _, _ in },
        data: args
    )
    return (invoke, captured)
}

private final class CapturedResponse {
    var value: String?
}
