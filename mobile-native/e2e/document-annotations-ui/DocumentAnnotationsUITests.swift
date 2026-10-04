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
        app.buttons["native-probe-show-code"].tap()
        receipt("table-and-reader-code-layout")
        app.buttons["native-probe-code-comparison"].tap()
        for index in 0..<9 {
            receipt("code-comparison-\(index)-layout")
            app.buttons["native-probe-next-case"].tap()
        }
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

    func testTableOriginalSourceSelection() throws {
        app.buttons["native-probe-show-code"].tap()
        try point("TABLE").press(forDuration: 1.2)
        XCTAssertTrue(copyMenu.waitForExistence(timeout: 5))
        receipt("table-selected")
        XCTAssertTrue(app.staticTexts["Captured source 135:139"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["Selection completed"].exists)
    }

    func testMarkdownCodeOriginalSourceSelection() throws {
        app.buttons["native-probe-show-code"].tap()
        try point("CODE").press(forDuration: 1.2)
        XCTAssertTrue(copyMenu.waitForExistence(timeout: 5))
        receipt("reader-code-selected")
        XCTAssertTrue(app.staticTexts["Captured source 159:163"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["Selection completed"].exists)
    }

    func testProseToCodeBoundary() throws {
        try point("FIRST").press(forDuration: 1.2)
        XCTAssertTrue(copyMenu.waitForExistence(timeout: 5))
        receipt("prose-to-code-before-drag")
        try point("HANDLE").press(forDuration: 0.2, thenDragTo: point("FIRST_TO_CODE"))
        receipt("prose-to-code-after-drag")
        XCTAssertTrue(app.staticTexts["Captured source 18:163"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["Selection completed"].exists)
    }

    private var clipboardRead = 0

    private func clipboardText() throws -> String? {
        clipboardRead += 1
        app.buttons["native-probe-read-clipboard"].tap()
        let field = app.staticTexts["native-probe-clipboard"]
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        let read = NSPredicate { _, _ in
            guard let result = try? JSONSerialization.jsonObject(with: Data(field.label.utf8)) as? [String: Any] else { return false }
            return result["read"] as? Int == self.clipboardRead
        }
        XCTAssertEqual(XCTWaiter.wait(for: [XCTNSPredicateExpectation(predicate: read, object: field)], timeout: 5), .completed)
        let result = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(field.label.utf8)) as? [String: Any])
        return result["text"] as? String
    }

    // Literal expected values, not data returned by the new parser/layout/model.
    private let codeStrings = [
        "", "same()", "same()\n", "same()\n\n", "\nsame()\n\nnext()\n",
        "\tfirst\tsecond\n", "😀 e\u{0301} שלום same()\n",
        "0123456789012345678901234567890123456789012345678901234567890123456789\n",
        "same()\r\n\r\n"
    ]

    func testCodePreservationAndLegacyCopy() throws {
        app.buttons["native-probe-code-comparison"].tap()
        let scaleLabel = app.staticTexts["native-probe-display-scale"].label
        let dimensions = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(scaleLabel.utf8)) as? [String: Double])
        let scale = try XCTUnwrap(dimensions["scale"])
        XCTAssertGreaterThan(scale, 0)
        let tolerance = 1 / scale
        for (index, text) in codeStrings.enumerated() {
            XCTAssertEqual(app.staticTexts["native-probe-code-case"].label, "Code case \(index)")
            let reference = app.staticTexts["native-probe-code-reference"]
            let candidate = app.staticTexts["native-probe-code-candidate"]
            // Empty text may not create a VoiceOver element. Its geometry remains in the receipt.
            if !text.isEmpty {
                XCTAssertEqual(reference.label, text, "Unchanged RN VoiceOver text, case \(index)")
                XCTAssertEqual(candidate.label, text, "Candidate VoiceOver text, case \(index)")
                XCTAssertEqual(candidate.frame.width, reference.frame.width, accuracy: tolerance)
                XCTAssertEqual(candidate.frame.height, reference.frame.height, accuracy: tolerance)
                for leaf in [reference, candidate] {
                    leaf.press(forDuration: 1.2)
                    XCTAssertTrue(copyMenu.waitForExistence(timeout: 5))
                    copyMenu.tap()
                    XCTAssertEqual(try clipboardText(), text, "Legacy RN paragraph Copy, case \(index)")
                }
            }
            app.buttons["native-probe-block-copy"].tap()
            XCTAssertEqual(try clipboardText(), text, "Whole block Copy, case \(index)")
            receipt("code-comparison-\(index)-preservation")
            app.buttons["native-probe-next-case"].tap()
        }
    }
}
