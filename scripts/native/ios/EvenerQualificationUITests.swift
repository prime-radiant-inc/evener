import XCTest

/// The installed-app qualification journeys for the native migration.
/// Each test drives the real Release app on the run-owned simulator against
/// a real isolated hub whose provider answers every ordinary turn with the
/// fixed qualification fixture. The UI runner passes the hub's origin and
/// its fixture-only token through the test-run environment; the app never
/// reads a host path, and nothing here publishes.
///
/// The fixture markers (prose, emphasis, code) and the draft below are
/// supplied test data, not product copy: they identify rendered content,
/// never wording the app owns.
final class EvenerQualificationUITests: XCTestCase {
	/// A bounded failure ceiling around the existence waits below: every
	/// wait resolves the moment the app is ready, and this only caps how
	/// long a genuinely stuck journey may hold the run. It is never a
	/// pacing mechanism.
	private let tripwire = TimeInterval(120)

	private var hubOrigin: String {
		ProcessInfo.processInfo.environment["EVENER_QUALIFICATION_HUB_ORIGIN"] ?? ""
	}

	private var hubToken: String {
		ProcessInfo.processInfo.environment["EVENER_QUALIFICATION_HUB_TOKEN"] ?? ""
	}

	private static let draft = "QUALIFICATION_draft_d0"

	override func setUp() {
		continueAfterFailure = false
	}

	/// The complete recovery journey: onboarding by address and token, a real
	/// session whose completed response renders natively, an unsent draft,
	/// reactivation and a cold launch that both keep the saved hub without
	/// re-entering credentials, the exact draft surviving, and an
	/// authenticated follow-up through the real hub. That follow-up after the
	/// cold launch is the credential-readback proof.
	func testSavedHubMarkdownAndDraftRecovery() {
		let app = XCUIApplication()
		onboard(app)
		startSession(app, prompt: "qualify the renderer")

		let markdown = app.descendants(matching: .any).matching(identifier: "native-markdown").firstMatch
		XCTAssertTrue(markdown.waitForExistence(timeout: tripwire), "the fixture response never rendered")
		XCTAssertGreaterThan(markdown.frame.width, 0)
		XCTAssertGreaterThan(markdown.frame.height, 0)
		for marker in ["QUALIFICATION_prose_a7", "QUALIFICATION_emphasis_b8", "QUALIFICATION_code_c9"] {
			let rendered = app.descendants(matching: .any)
				.matching(NSPredicate(format: "label CONTAINS %@", marker)).firstMatch
			XCTAssertTrue(rendered.waitForExistence(timeout: tripwire), "native rendering lost \(marker)")
		}
		attach(app, "fixture-response")

		let composer = composerField(app)
		composer.tap()
		composer.typeText(Self.draft)

		// Reactivation keeps the hub: background, return, and the journey is
		// still where it was.
		XCUIDevice.shared.press(.home)
		app.activate()
		XCTAssertTrue(composer.waitForExistence(timeout: tripwire), "reactivation lost the conversation")

		// Cold launch: terminate and relaunch. Credentials must survive, so
		// the first-run onboarding must not reappear.
		app.terminate()
		app.launch()
		XCTAssertTrue(app.buttons["New session"].waitForExistence(timeout: tripwire), "cold launch lost the saved hub")
		XCTAssertFalse(app.buttons["add-hub:address"].exists, "cold launch asked for credentials again")

		let session = app.descendants(matching: .any)
			.matching(NSPredicate(format: "label CONTAINS %@", "Fake Session")).firstMatch
		XCTAssertTrue(session.waitForExistence(timeout: tripwire), "cold launch lost the session")
		session.tap()

		let recovered = composerField(app)
		XCTAssertTrue(recovered.waitForExistence(timeout: tripwire), "the recovered conversation never showed its composer")
		XCTAssertEqual(recovered.value as? String, Self.draft, "the unsent draft did not survive the cold launch")
		attach(app, "recovered-draft")

		// The authenticated follow-up: the recovered credentials carry a new
		// turn through the real hub, and its response renders.
		recovered.tap()
		recovered.typeText("qualify the readback")
		let send = app.buttons["Send"]
		XCTAssertTrue(send.waitForExistence(timeout: tripwire), "Send never became available")
		send.tap()
		let followUp = app.descendants(matching: .any)
			.matching(NSPredicate(format: "label CONTAINS %@", "QUALIFICATION_code_c9"))
		let firstResponse = followUp.count
		XCTAssertTrue(
			wait(for: NSPredicate(format: "count > %d", firstResponse), on: followUp, seconds: tripwire),
			"the follow-up turn never completed through the recovered credentials"
		)
		attach(app, "follow-up-response")
	}

	/// Scheme activation only: evener-native://share must bring the app to
	/// the foreground warm and cold, and ordinary controls must keep working
	/// after activation. Delivery through Safari's share sheet is a separate,
	/// unclaimed journey.
	func testWarmAndColdShareSchemeActivation() {
		let app = XCUIApplication()
		app.launch()
		XCTAssertTrue(
			app.buttons["add-hub:address"].waitForExistence(timeout: tripwire),
			"the app never reached its first-run controls"
		)
		XCUIDevice.shared.system.open(URL(string: "evener-native://share")!)
		XCTAssertTrue(
			app.waitForState(.runningForeground, timeout: tripwire),
			"warm scheme activation left the app in the background"
		)
		XCTAssertTrue(app.buttons["add-hub:address"].exists, "scheme activation replaced the app's controls")

		app.terminate()
		app.launch()
		XCTAssertTrue(
			app.buttons["add-hub:address"].waitForExistence(timeout: tripwire),
			"the cold launch never reached its first-run controls"
		)
		XCUIDevice.shared.system.open(URL(string: "evener-native://share")!)
		XCTAssertTrue(
			app.waitForState(.runningForeground, timeout: tripwire),
			"cold scheme activation left the app in the background"
		)
		let way = app.buttons["add-hub:address"]
		XCTAssertTrue(way.exists, "ordinary controls did not survive scheme activation")
		way.tap()
		XCTAssertTrue(app.textFields["hub-address"].waitForExistence(timeout: tripwire), "ordinary controls stopped working")
	}

	/// A swipe inside an ordinary native sheet must leave its controls
	/// usable: scroll the New session sheet's form, then press its Cancel and
	/// see the sheet dismiss. This complements the three upstream
	/// gesture-source regression checks.
	func testSheetGestureKeepsControlsUsable() {
		let app = XCUIApplication()
		onboard(app)
		let newSession = app.buttons["New session"]
		XCTAssertTrue(newSession.waitForExistence(timeout: tripwire), "the board never appeared")
		newSession.tap()
		let prompt = app.descendants(matching: .any).matching(identifier: "session-prompt").firstMatch
		XCTAssertTrue(prompt.waitForExistence(timeout: tripwire), "the New session sheet never opened")

		let sheet = prompt.firstMatch
		sheet.swipeUp()
		sheet.swipeDown()

		let cancel = app.buttons["Cancel"]
		XCTAssertTrue(cancel.waitForExistence(timeout: tripwire), "the sheet's Cancel vanished after swipes")
		cancel.tap()
		XCTAssertTrue(
			app.buttons["New session"].waitForExistence(timeout: tripwire),
			"the sheet's Cancel stopped working after swipes"
		)
	}

	// MARK: - Journey steps

	/// Ordinary address/token onboarding: enter the address way, type the
	/// fixture hub's origin and token, continue, and connect under the
	/// suggested name.
	private func onboard(_ app: XCUIApplication) {
		let way = app.buttons["add-hub:address"]
		XCTAssertTrue(way.waitForExistence(timeout: tripwire), "first run never offered the address way")
		way.tap()
		let address = app.textFields["hub-address"]
		XCTAssertTrue(address.waitForExistence(timeout: tripwire), "the address field never appeared")
		address.tap()
		address.typeText(hubOrigin)
		let token = app.secureTextFields["hub-token"]
		XCTAssertTrue(token.waitForExistence(timeout: tripwire), "the token field never appeared")
		token.tap()
		token.typeText(hubToken)
		let proceed = app.buttons["hub-continue"]
		XCTAssertTrue(proceed.waitForExistence(timeout: tripwire), "the Continue row never appeared")
		proceed.tap()
		let connect = app.buttons["Connect"]
		XCTAssertTrue(connect.waitForExistence(timeout: tripwire), "the review step never appeared")
		connect.tap()
		XCTAssertTrue(
			app.buttons["New session"].waitForExistence(timeout: tripwire),
			"the board never appeared after onboarding"
		)
	}

	/// A real session: open the New session sheet, type the prompt, and let
	/// Start carry it through the real hub.
	private func startSession(_ app: XCUIApplication, prompt promptText: String) {
		let newSession = app.buttons["New session"]
		XCTAssertTrue(newSession.waitForExistence(timeout: tripwire))
		newSession.tap()
		let prompt = app.descendants(matching: .any).matching(identifier: "session-prompt").firstMatch
		XCTAssertTrue(prompt.waitForExistence(timeout: tripwire), "the New session sheet never opened")
		prompt.tap()
		prompt.typeText(promptText)
		let start = app.buttons["Start"]
		XCTAssertTrue(start.waitForExistence(timeout: tripwire), "Start never appeared")
		start.tap()
	}

	/// The conversation's composer, addressed by its stable identifier. The
	/// multiline field surfaces as a text view.
	private func composerField(_ app: XCUIApplication) -> XCUIElement {
		let field = app.textViews["composer-message"]
		XCTAssertTrue(field.waitForExistence(timeout: tripwire), "the composer never appeared")
		return field
	}

	private func attach(_ app: XCUIApplication, _ name: String) {
		let attachment = XCTAttachment(screenshot: app.screenshot())
		attachment.name = name
		attachment.lifetime = .keepAlways
		add(attachment)
	}

	/// Waits for a predicate over an arbitrary query's snapshot count. The
	/// journey resolves as soon as the predicate holds; tripwire only bounds
	/// a genuinely stuck wait.
	private func wait(for predicate: NSPredicate, on query: XCUIElementQuery, seconds limit: TimeInterval) -> Bool {
		let deadline = Date().addingTimeInterval(limit)
		while Date() < deadline {
			if predicate.evaluate(with: query.count) { return true }
			Thread.sleep(forTimeInterval: 0.25)
		}
		return predicate.evaluate(with: query.count)
	}
}
