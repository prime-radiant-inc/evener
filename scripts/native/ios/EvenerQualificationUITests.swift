import XCTest

/// The installed-app qualification journeys for the native migration.
/// Each test drives the real Release app on the run-owned simulator against
/// a real isolated hub whose provider answers every ordinary turn with the
/// fixed qualification fixture. The UI runner passes the hub's origin, its
/// fixture-only token, the run-owned workspace and the hub host's home
/// through the test-run environment; the app never reads a host path, and
/// nothing here publishes.
///
/// The tests share one app install, so the suite starts with the recovery
/// journey, which saves the hub; every later journey reuses that saved hub
/// and never onboards again.
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
	/// How long a negative wait may look before concluding the app did the
	/// right thing by NOT showing a surface (a credential prompt after a
	/// cold launch). Short because absence is proven by the app settling,
	/// not by waiting out a long window.
	private let absenceBound = TimeInterval(10)

	private var hubOrigin: String {
		ProcessInfo.processInfo.environment["EVENER_QUALIFICATION_HUB_ORIGIN"] ?? ""
	}

	private var hubToken: String {
		ProcessInfo.processInfo.environment["EVENER_QUALIFICATION_HUB_TOKEN"] ?? ""
	}

	private var workspace: String {
		ProcessInfo.processInfo.environment["EVENER_QUALIFICATION_WORKSPACE"] ?? ""
	}

	private var hubHome: String {
		ProcessInfo.processInfo.environment["EVENER_QUALIFICATION_HOME"] ?? ""
	}

	private static let draft = "QUALIFICATION_draft_d0"

	override func setUp() {
		continueAfterFailure = false
	}

	/// The complete recovery journey: onboarding by address and token, a real
	/// session whose completed response renders natively, an unsent draft,
	/// reactivation and a cold launch that both keep the saved hub without
	/// re-entering credentials, the exact draft surviving in the restored
	/// conversation, and an authenticated follow-up through the real hub.
	/// That follow-up after the cold launch is the credential-readback proof.
	func testSavedHubMarkdownAndDraftRecovery() {
		let app = XCUIApplication()
		onboardIfNeeded(app)
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
		tap(composer, "the composer never appeared")
		composer.typeText(Self.draft)

		// Reactivation keeps the hub: background, return, and the journey is
		// still where it was.
		XCUIDevice.shared.press(.home)
		app.activate()
		XCTAssertTrue(composer.waitForExistence(timeout: tripwire), "reactivation lost the conversation")

		// Cold launch: terminate and relaunch. Credentials must survive, so
		// the first-run onboarding must not reappear, and the restored
		// conversation carries the composer with the exact unsent draft.
		app.terminate()
		app.launch()
		XCTAssertFalse(
			app.buttons["add-hub:address"].waitForExistence(timeout: absenceBound),
			"cold launch asked for credentials again"
		)
		let recovered = composerField(app)
		XCTAssertEqual(recovered.value as? String, Self.draft, "the unsent draft did not survive the cold launch")
		attach(app, "recovered-draft")

		// The authenticated follow-up: the recovered credentials carry a new
		// turn through the real hub, and its response renders. The baseline
		// is sampled before sending, so the wait compares counts honestly.
		tap(recovered, "the composer never appeared")
		recovered.typeText("qualify the readback")
		let followUp = app.descendants(matching: .any)
			.matching(NSPredicate(format: "label CONTAINS %@", "QUALIFICATION_code_c9"))
		let baseline = followUp.count
		tap(app.buttons["Send"], "Send never became available")
		let deadline = Date().addingTimeInterval(tripwire)
		while Date() < deadline && followUp.count <= baseline {
			Thread.sleep(forTimeInterval: 0.25)
		}
		XCTAssertGreaterThan(
			followUp.count,
			baseline,
			"the follow-up turn never completed through the recovered credentials"
		)
		attach(app, "follow-up-response")

		// Leave the install where the later journeys expect it: the app
		// reopens its frontmost route on launch, so ending inside the
		// conversation would reopen the conversation over the Board and
		// hide the compose button from every later journey.
		tap(app.navigationBars.buttons.firstMatch, "the conversation never offered a way back")
		XCTAssertTrue(
			app.buttons["new-session"].waitForExistence(timeout: tripwire),
			"the board never reappeared after the conversation"
		)
	}

	/// Scheme activation only: evener-native://share must bring the app to
	/// the foreground from the background (warm) and from a terminated state
	/// (cold), and ordinary controls must keep working after activation.
	/// Delivery through Safari's share sheet is a separate, unclaimed journey.
	func testWarmAndColdShareSchemeActivation() {
		let app = XCUIApplication()
		app.launch()
		XCTAssertTrue(
			mainSurface(app).waitForExistence(timeout: tripwire),
			"the app never reached its own controls"
		)
		// Warm: the app is alive in the background when the scheme arrives.
		XCUIDevice.shared.press(.home)
		XCUIDevice.shared.system.open(URL(string: "evener-native://share")!)
		XCTAssertTrue(
			app.wait(for: .runningForeground, timeout: tripwire),
			"warm scheme activation left the app in the background"
		)
		XCTAssertTrue(mainSurface(app).waitForExistence(timeout: tripwire), "scheme activation replaced the app's controls")

		// Cold: the app is terminated, never launched, when the scheme
		// arrives; the URL delivery itself starts it.
		app.terminate()
		XCUIDevice.shared.system.open(URL(string: "evener-native://share")!)
		XCTAssertTrue(
			app.wait(for: .runningForeground, timeout: tripwire),
			"cold scheme activation left the app in the background"
		)
		let way = app.buttons["add-hub:address"]
		if way.waitForExistence(timeout: absenceBound) {
			tap(way, "the first-run address way disappeared before its tap")
			XCTAssertTrue(
				app.textFields["hub-address"].waitForExistence(timeout: tripwire),
				"ordinary controls stopped working after scheme activation"
			)
		} else {
			XCTAssertTrue(
				app.buttons["new-session"].waitForExistence(timeout: tripwire),
				"ordinary controls did not survive scheme activation"
			)
		}
	}

	/// A swipe inside an ordinary native sheet must leave its controls
	/// usable: scroll the New session sheet's form, then press its Cancel and
	/// see the sheet close back to the Board. The Board's own row gestures
	/// have their own regression checks; this is the sheet's side of the
	/// interplay.
	func testSheetGestureKeepsControlsUsable() {
		let app = XCUIApplication()
		onboardIfNeeded(app)
		tap(app.buttons["new-session"], "the board never appeared")
		let prompt = app.descendants(matching: .any).matching(identifier: "session-prompt").firstMatch
		XCTAssertTrue(prompt.waitForExistence(timeout: tripwire), "the New session sheet never opened")

		let sheet = prompt.firstMatch
		sheet.swipeUp()
		sheet.swipeDown()

		tap(app.buttons["Cancel"], "the sheet's Cancel vanished after swipes")
		XCTAssertTrue(
			prompt.waitForNonExistence(timeout: tripwire),
			"the sheet never closed after Cancel"
		)
		XCTAssertTrue(
			app.buttons["new-session"].waitForExistence(timeout: tripwire),
			"the sheet's Cancel stopped working after swipes"
		)
	}

	// MARK: - Journey steps

	/// The app's own main controls, whichever surface it restored to: the
	/// first-run address way or the board's compose button. Both carry stable
	/// identifiers; the board also shows a second control labeled New session
	/// when it is empty, so journeys never tap by that label.
	private func mainSurface(_ app: XCUIApplication) -> XCUIElement {
		return app.buttons
			.matching(NSPredicate(format: "identifier IN %@", ["add-hub:address", "new-session"])).firstMatch
	}

	/// Launches the app and, on a fresh install, saves the fixture hub
	/// through the ordinary address way. A later journey in the same install
	/// reuses the saved hub and goes straight to the board.
	private func onboardIfNeeded(_ app: XCUIApplication) {
		app.launch()
		// The app opens wherever it left off: the first-run pairing way on
		// a fresh install, the board on a saved one. Waiting on the way
		// alone would hold the full tripwire once the hub is saved, for a
		// screen that can never appear.
		let surface = mainSurface(app)
		XCTAssertTrue(surface.waitForExistence(timeout: tripwire), "the app never reached its own controls")
		if surface.identifier == "add-hub:address" {
			tap(surface, "the first-run address way disappeared before its tap")
			let address = app.textFields["hub-address"]
			tap(address, "the address field never appeared")
			address.typeText(hubOrigin)
			let token = app.secureTextFields["hub-token"]
			tap(token, "the token field never appeared")
			token.typeText(hubToken)
			// The token's return key takes the keyboard down before the
			// review appears: a keyboard left up covers the review's Connect
			// row, and a synthesized tap at that point lands on the keyboard
			// window, never reaching the app.
			token.typeText("\n")
			tap(app.buttons["hub-continue"], "the Continue row never appeared")
			tap(app.buttons["Connect"], "the review step never appeared")
		}
		XCTAssertTrue(
			app.buttons["new-session"].waitForExistence(timeout: tripwire),
			"the board never appeared after onboarding"
		)
	}

	/// A real session in the run-owned workspace: open the New session sheet,
	/// type the prompt, and choose the project through the ordinary picker —
	/// a fresh hub lists no recent projects, so the workspace is reached by
	/// browsing — then wait for Start to unlock and start.
	private func startSession(_ app: XCUIApplication, prompt promptText: String) {
		tap(app.buttons["new-session"], "the board's compose button never appeared")
		let prompt = app.descendants(matching: .any).matching(identifier: "session-prompt").firstMatch
		tap(prompt, "the New session sheet never opened")
		prompt.typeText(promptText)

		let browse = app.buttons
			.matching(NSPredicate(format: "label BEGINSWITH %@", "Browse folders")).firstMatch
		tap(browse, "the project picker never offered browsing")

		// The browse starts in the hub host's home; walk up to the filesystem
		// root, then down the workspace's own segments.
		let up = app.buttons["Up one folder"]
		let ups = hubHome.split(separator: "/").filter { !$0.isEmpty }.count
		for _ in 0..<ups {
			tap(up, "the browse never offered going up")
		}
		for segment in workspace.split(separator: "/").filter({ !$0.isEmpty }) {
			let folder = app.buttons[String(segment)]
			tap(folder, "the browse never listed \(segment)")
		}
		tap(app.buttons["Use this folder"], "the browse never offered the workspace")

		let start = app.buttons["Start"]
		XCTAssertTrue(start.waitForExistence(timeout: tripwire), "Start never appeared")
		waitForEnabled(start)
		tap(start, "Start never appeared")
	}

	/// Taps only once the control has a real frame. A control that registers
	/// in the accessibility tree during a screen push exists before layout
	/// gives it a frame, and a tap at that moment computes {-1, -1} and lands
	/// nowhere. Hittability is deliberately not the gate: the tap itself
	/// scrolls rows the keyboard or the fold covers into view, so waiting on
	/// hittability here would deadlock on exactly those rows. A disabled
	/// control is refused loudly instead of letting the press vanish.
	private func tap(_ element: XCUIElement, _ what: String) {
		XCTAssertTrue(element.waitForExistence(timeout: tripwire), what)
		let deadline = Date().addingTimeInterval(tripwire)
		while Date() < deadline && !(element.exists && element.frame.width > 0 && element.isEnabled) {
			Thread.sleep(forTimeInterval: 0.25)
		}
		XCTAssertTrue(element.exists && element.frame.width > 0, "\(what) never gained a real frame")
		XCTAssertTrue(element.isEnabled, "\(what) is not enabled")
		element.tap()
	}

	/// Waits for a control to become enabled using XCTest's own expectation
	/// over the element, so the wait resolves the moment it unlocks.
	private func waitForEnabled(_ element: XCUIElement) {
		let unlocked = expectation(for: NSPredicate(format: "isEnabled == true"), evaluatedWith: element)
		wait(for: [unlocked], timeout: tripwire)
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
}
