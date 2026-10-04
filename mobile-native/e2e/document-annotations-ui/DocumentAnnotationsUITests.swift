import XCTest

final class DocumentAnnotationsUITests: XCTestCase {
    private let app = XCUIApplication(bundleIdentifier: "com.primeradiant.evener.native")

    override func setUpWithError() throws {
        continueAfterFailure = true
        app.launch()
        XCTAssertTrue(app.staticTexts["native-probe-ready"].waitForExistence(timeout: 30),
                      "Installed qualification entry must be ready before selection assertions")
    }

    private func receipt(_ name: String) {
        let screenshot = XCTAttachment(screenshot: app.screenshot())
        screenshot.name = name
        screenshot.lifetime = .keepAlways
        add(screenshot)
        let tree = XCTAttachment(string: app.debugDescription)
        tree.name = "\(name)-accessibility"
        tree.lifetime = .keepAlways
        add(tree)
    }

    // The receipt contains the actual installed accessibility frames and pixels.
    // Gesture inputs are measured from it, never from the original-source map.
    func testLayoutReceipt() {
        receipt("initial-layout")
        XCTAssertTrue(app.otherElements["native-probe-first-passage"].exists)
        XCTAssertTrue(app.otherElements["native-probe-second-passage"].exists)
    }

    private func point(_ name: String) throws -> XCUICoordinate {
        let env = ProcessInfo.processInfo.environment
        guard let x = env["ANNOTATION_\(name)_X"].flatMap(Double.init),
              let y = env["ANNOTATION_\(name)_Y"].flatMap(Double.init) else {
            throw NSError(domain: "DocumentAnnotationsGeometry", code: 1,
                          userInfo: [NSLocalizedDescriptionKey:
                          "Missing measured \(name) glyph/handle coordinates, this is not a selection RED"])
        }
        return app.coordinate(withNormalizedOffset: .zero)
            .withOffset(CGVector(dx: x, dy: y))
    }

    private var copyMenu: XCUIElement {
        app.descendants(matching: .any)
            .matching(NSPredicate(format: "label == %@", "Copy")).firstMatch
    }

    func testLongPressReceipt() throws {
        try point("FIRST").press(forDuration: 1.2)
        XCTAssertTrue(copyMenu.waitForExistence(timeout: 5),
                      "A genuine system selection menu must follow the glyph long press")
        receipt("long-press-selection")
    }

    func testCompletedOriginalSourceSelectionAcrossRows() throws {
        let first = try point("FIRST")
        let handle = try point("HANDLE")
        let second = try point("SECOND")
        first.press(forDuration: 1.2)
        XCTAssertTrue(copyMenu.waitForExistence(timeout: 5),
                      "The long press must create native selection before source assertions")
        receipt("first-selected")
        XCTAssertTrue(app.staticTexts["Captured source 18:22"].exists,
                      "Real native selection must report original UTF-16 [18,22)")
        XCTAssertTrue(app.staticTexts["Selection completed"].exists,
                      "Gesture end must emit native completed selection, without menu activation")
        handle.press(forDuration: 0.2, thenDragTo: second)
        receipt("handle-drag-across-rows")
        XCTAssertTrue(app.staticTexts["Captured source 18:77"].exists,
                      "Actual handle movement must extend selection across the second native row")
    }
}
