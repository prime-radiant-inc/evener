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
		typeWholeText(app, composer, Self.draft, "the draft never landed whole") { $0 == Self.draft }

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
		typeWholeText(app, recovered, "qualify the readback", "the follow-up never landed whole") {
			$0 == "qualify the readback"
		}
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
	/// usable: swipe the New session sheet's form rows, then press its
	/// Cancel and see the sheet close back to the Board. The Board's own row
	/// gestures have their own regression checks; this is the sheet's side of
	/// the interplay.
	func testSheetGestureKeepsControlsUsable() {
		let app = XCUIApplication()
		onboardIfNeeded(app)
		let prompt = openNewSession(app)
		// The gesture swipes the form's own labels, not a control (a swipe
		// on a row can land as its press) and not the focused prompt (a swipe
		// over a first-responder text view can turn into a text selection,
		// and the selection holds the next tap). A nested label registers on
		// both its outer and inner text, so the query takes its first match:
		// a find that must be unique would refuse the doubled label.
		let anchor = app.staticTexts["Where"].firstMatch
		XCTAssertTrue(anchor.waitForExistence(timeout: tripwire), "the sheet's form never showed")
		anchor.swipeUp()
		anchor.swipeDown()

		// Cancel, checked by its effect like every other step: the first
		// press can be lost to a selection the swipe left behind.
		let deadline = Date().addingTimeInterval(tripwire)
		while prompt.waitForExistence(timeout: 2) && Date() < deadline {
			tap(app.buttons["Cancel"], "the sheet's Cancel vanished after swipes")
		}
		XCTAssertTrue(
			prompt.waitForNonExistence(timeout: tripwire),
			"the sheet never closed after Cancel"
		)
		XCTAssertTrue(
			app.buttons["new-session"].waitForExistence(timeout: tripwire),
			"the board never returned after the sheet closed"
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
			typeWholeText(app, address, hubOrigin, "the hub address never landed whole") { $0 == hubOrigin }
			let token = app.secureTextFields["hub-token"]
			tap(token, "the token field never appeared")
			typeWholeText(app, token, hubToken, "the hub token never landed whole") { $0.count == hubToken.count }
			// The form is dragged past the keyboard, the app's own
			// drag-to-dismiss, rather than pressing the secure field's
			// return key: on iOS that press can leave the keyboard's
			// autofill accessory window over the content, eating presses
			// that every check says landed. The drag rides a static label:
			// a swipe on a control can land as its press.
				dismissKeyboard(app, draggingOn: app.staticTexts["Token (optional)"].firstMatch)
			tap(app.buttons["hub-continue"], "the Continue row never appeared")
			// The board is Connect's own effect, checked like every other
			// step: the press can deaden in the settling window every fresh
			// screen has, and the app's own guard makes a repeat press safe.
			let board = app.buttons["new-session"]
			let shown = Date().addingTimeInterval(tripwire)
			while !board.waitForExistence(timeout: 4) && Date() < shown && app.buttons["Connect"].exists {
				tap(app.buttons["Connect"], "the review step never appeared")
			}
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
		let prompt = openNewSession(app)
		tap(prompt, "the sheet's prompt never became tappable")
		typeWholeText(app, prompt, promptText, "the session prompt never landed whole") { $0 == promptText }
		let browse = app.buttons
			.matching(NSPredicate(format: "label BEGINSWITH %@", "Browse folders")).firstMatch
		// The sheet's picker sits where the prompt's keyboard can cover it.
		dismissKeyboard(app, draggingOn: app.staticTexts["Where"].firstMatch)
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

	/// Opens the New session sheet, tapping again when the first tap lands
	/// inside the board's settling window: right after the board appears, the
	/// compose button can carry a valid frame at one instant and an invalid
	/// one at the next, and a tap synthesized in that window computes {-1, -1}
	/// and goes nowhere. The journey checks its own effect, the sheet's
	/// prompt, rather than pretending to predict the settled moment.
	private func openNewSession(_ app: XCUIApplication) -> XCUIElement {
		let prompt = app.descendants(matching: .any).matching(identifier: "session-prompt").firstMatch
		let deadline = Date().addingTimeInterval(tripwire)
		while !prompt.waitForExistence(timeout: 2) && Date() < deadline {
			tap(app.buttons["new-session"], "the board's compose button never appeared")
		}
		XCTAssertTrue(prompt.waitForExistence(timeout: 2), "the New session sheet never opened")
		return prompt
	}

	/// Types into a field and checks the text arrived whole, clearing and
	/// retyping when it did not. A busy app can drop synthesized keystrokes
	/// mid-burst, and a silently shortened address surfaces only much later,
	/// as a review with a malformed hub and an empty suggested name. Secure
	/// fields hide their text, so they are checked by their bullet count.
	private func typeWholeText(
		_ app: XCUIApplication,
		_ field: XCUIElement,
		_ text: String,
		_ what: String,
		landed: (String) -> Bool
	) {
		let deadline = Date().addingTimeInterval(tripwire)
		while Date() < deadline {
			field.typeText(text)
			if let value = field.value as? String, landed(value) {
				return
			}
			// Some keystroke in the burst was dropped: clear what arrived
			// and type again.
			let remove = app.keyboards.keys["delete"]
			for _ in 0..<(text.count + 4) where remove.exists {
				remove.tap()
			}
		}
		XCTFail("\(what): the field reads \(field.value ?? "nothing")")
	}

	/// Dismisses the keyboard by dragging the form, its own drag-to-dismiss,
	/// on a static label inside it. A multiline field takes a return key as
	/// text, so it cannot dismiss that way, and the drag must ride a label:
	/// a swipe that starts on a control can land as that control's press.
	/// Nested labels register on both their outer and inner text, so the
	/// anchor must already be resolved to a single match.
	/// Best effort, and only a few tries: a keyboard that stays up is judged
	/// where it blocks, by the next step's own wait.
	private func dismissKeyboard(_ app: XCUIApplication, draggingOn anchor: XCUIElement) {
		let keyboard = app.keyboards.firstMatch
		for _ in 0..<3 where keyboard.exists {
			if anchor.exists {
				anchor.swipeUp()
			}
			Thread.sleep(forTimeInterval: 0.5)
		}
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
		// The press is synthesized at the element's center rather than
		// through the tap's own visibility machinery: that machinery
		// refuses covered or mid-layout elements with a test-ending failure
		// even when the frame is real and the control enabled, while a
		// center press lands wherever the layout actually is, and the
		// effect checks around each step judge whether it worked.
		element.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
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
