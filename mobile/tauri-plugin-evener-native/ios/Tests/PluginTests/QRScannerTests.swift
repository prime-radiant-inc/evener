import XCTest
@testable import EvenerNativePlugin

final class QRScannerTests: XCTestCase {
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
