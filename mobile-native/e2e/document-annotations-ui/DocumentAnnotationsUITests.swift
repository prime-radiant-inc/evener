import XCTest
import UIKit

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

    // Diagnostic only, not part of qualify or a preservation pass. The pause
    // permits inspecting RN's documented readonly attributedText with LLDB.
    func testMixedFontMetadataReceipt() {
        app.buttons["native-probe-code-comparison"].tap()
        for _ in 0..<6 { app.buttons["native-probe-next-case"].tap() }
        XCTAssertEqual(app.staticTexts["native-probe-code-case"].label, "Code case 6")
        receipt("mixed-font-metadata-before-inspection")
        print("MIXED_FONT_METADATA_READY")
        Thread.sleep(forTimeInterval: 60)
        receipt("mixed-font-metadata-after-inspection")
    }

    func testEmptyCodeGeometryReceipt() {
        app.buttons["native-probe-code-comparison"].tap()
        XCTAssertEqual(app.staticTexts["native-probe-code-case"].label, "Code case 0")
        receipt("empty-code-geometry-before-inspection")
        print("EMPTY_CODE_GEOMETRY_READY")
        Thread.sleep(forTimeInterval: 60)
        receipt("empty-code-geometry-after-inspection")
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

    private func selectBoundaryOrigin(_ name: String, source: String) throws -> Bool {
        app.buttons["native-probe-show-code"].tap()
        try point(name).press(forDuration: 1.2)
        XCTAssertTrue(copyMenu.waitForExistence(timeout: 5))
        receipt("\(name.lowercased())-boundary-origin")
        // RN Text has paragraph Copy but no adjustable native selection handles.
        // Fail on the missing native selection before requesting handle geometry.
        let captured = app.staticTexts[source].waitForExistence(timeout: 5)
        XCTAssertTrue(captured, "The boundary origin needs genuine mapped native selection before dragging its handle")
        XCTAssertTrue(app.staticTexts["Selection completed"].exists)
        return captured
    }

    func testCodeToProseBoundary() throws {
        guard try selectBoundaryOrigin("CODE", source: "Captured source 159:163") else { return }
        try point("CODE_HANDLE").press(forDuration: 0.2, thenDragTo: point("CODE_TO_FIRST"))
        receipt("code-to-prose-after-drag")
        XCTAssertTrue(app.staticTexts["Captured source 18:163"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["Selection completed"].exists)
    }

    func testTableToCodeBoundary() throws {
        guard try selectBoundaryOrigin("TABLE", source: "Captured source 135:139") else { return }
        try point("TABLE_HANDLE").press(forDuration: 0.2, thenDragTo: point("TABLE_TO_CODE"))
        receipt("table-to-code-after-drag")
        XCTAssertTrue(app.staticTexts["Captured source 135:163"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["Selection completed"].exists)
    }

    func testCodeToTableBoundary() throws {
        guard try selectBoundaryOrigin("CODE", source: "Captured source 159:163") else { return }
        try point("CODE_HANDLE").press(forDuration: 0.2, thenDragTo: point("CODE_TO_TABLE"))
        receipt("code-to-table-after-drag")
        XCTAssertTrue(app.staticTexts["Captured source 135:163"].waitForExistence(timeout: 5))
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

    // Inspect screenshot pixels, not geometry returned by the candidate layout.
    // The comparison fixture has dark glyphs on the unchanged pale Reader inset.
    private func glyphInkBounds(_ image: CGImage, frame: CGRect, scale: Double) throws -> CGRect {
        let pixelFrame = CGRect(x: frame.minX * scale, y: frame.minY * scale,
                                width: min(frame.width, app.frame.maxX - frame.minX) * scale,
                                height: frame.height * scale).integral
        let crop = try XCTUnwrap(image.cropping(to: pixelFrame))
        let width = crop.width, height = crop.height
        var bytes = [UInt8](repeating: 0, count: width * height * 4)
        try bytes.withUnsafeMutableBytes { buffer in
            let context = try XCTUnwrap(CGContext(data: buffer.baseAddress, width: width, height: height,
                bitsPerComponent: 8, bytesPerRow: width * 4, space: CGColorSpaceCreateDeviceRGB(),
                bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue))
            context.draw(crop, in: CGRect(x: 0, y: 0, width: CGFloat(width), height: CGFloat(height)))
        }
        var minX = width, minY = height, maxX = -1, maxY = -1
        for y in 0..<height {
            for x in 0..<width {
                let offset = (y * width + x) * 4
                if bytes[offset] < 128 && bytes[offset + 1] < 128 && bytes[offset + 2] < 128 {
                    minX = min(minX, x); minY = min(minY, y)
                    maxX = max(maxX, x); maxY = max(maxY, y)
                }
            }
        }
        XCTAssertGreaterThanOrEqual(maxX, minX, "The screenshot must contain actual drawn glyphs")
        return CGRect(x: CGFloat(minX), y: CGFloat(minY),
                      width: CGFloat(maxX - minX + 1), height: CGFloat(maxY - minY + 1))
    }

    func testCodePreservationAndLegacyCopy() throws {
        app.buttons["native-probe-code-comparison"].tap()
        let scaleLabel = app.staticTexts["native-probe-display-scale"].label
        let dimensions = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(scaleLabel.utf8)) as? [String: Double])
        let scale = try XCTUnwrap(dimensions["scale"])
        XCTAssertGreaterThan(scale, 0)
        let fontScale = try XCTUnwrap(dimensions["fontScale"])
        print("Code preservation actual fontScale: \(fontScale), screen scale: \(scale)")
        if ProcessInfo.processInfo.environment["ANNOTATION_REQUIRE_SCALED_CODE"] == "1" {
            XCTAssertGreaterThan(fontScale, 1, "The system category must drive real Reader scaling, not a test-only style")
        }
        let tolerance = 1 / scale
        for (index, text) in codeStrings.enumerated() {
            XCTAssertEqual(app.staticTexts["native-probe-code-case"].label, "Code case \(index)")
            let reference = app.staticTexts["native-probe-code-reference"]
            let candidate = app.staticTexts["native-probe-code-candidate"]
            // Containers stay exposed even when empty text has no VoiceOver node.
            // Identical unchanged padding makes their frames catch missing lines.
            let referenceContainer = app.otherElements["native-probe-code-reference-container"]
            let candidateContainer = app.otherElements["native-probe-code-candidate-container"]
            XCTAssertTrue(referenceContainer.exists, "RN container geometry, case \(index)")
            XCTAssertTrue(candidateContainer.exists, "Candidate container geometry, case \(index)")
            XCTAssertEqual(candidateContainer.frame.width, referenceContainer.frame.width, accuracy: tolerance)
            XCTAssertEqual(candidateContainer.frame.height, referenceContainer.frame.height, accuracy: tolerance)
            if !text.isEmpty {
                XCTAssertEqual(Array(reference.label.utf16), Array(text.utf16), "Unchanged RN VoiceOver text, case \(index)")
                XCTAssertEqual(Array(candidate.label.utf16), Array(text.utf16), "Candidate VoiceOver text, case \(index)")
                XCTAssertEqual(candidate.frame.width, reference.frame.width, accuracy: tolerance)
                XCTAssertEqual(candidate.frame.height, reference.frame.height, accuracy: tolerance)
                let screenshot = try XCTUnwrap(app.screenshot().image.cgImage)
                let referenceInk = try glyphInkBounds(screenshot, frame: reference.frame, scale: scale)
                let candidateInk = try glyphInkBounds(screenshot, frame: candidate.frame, scale: scale)
                XCTAssertEqual(candidateInk.minX, referenceInk.minX, accuracy: 1, "Drawn glyph left, case \(index)")
                XCTAssertEqual(candidateInk.maxX, referenceInk.maxX, accuracy: 1, "Drawn glyph right, case \(index)")
                XCTAssertEqual(candidateInk.minY, referenceInk.minY, accuracy: 1, "Drawn glyph top, case \(index)")
                XCTAssertEqual(candidateInk.maxY, referenceInk.maxY, accuracy: 1, "Drawn glyph bottom, case \(index)")
                for leaf in [reference, candidate] {
                    leaf.press(forDuration: 1.2)
                    XCTAssertTrue(copyMenu.waitForExistence(timeout: 5))
                    copyMenu.tap()
                    let copied = try XCTUnwrap(clipboardText())
                    XCTAssertEqual(Array(copied.utf16), Array(text.utf16), "Legacy RN paragraph Copy, case \(index)")
                }
            }
            app.buttons["native-probe-block-copy"].tap()
            let copied = try XCTUnwrap(clipboardText())
            XCTAssertEqual(Array(copied.utf16), Array(text.utf16), "Whole block Copy, case \(index)")
            receipt("code-comparison-\(index)-preservation")
            app.buttons["native-probe-next-case"].tap()
        }
    }
}
