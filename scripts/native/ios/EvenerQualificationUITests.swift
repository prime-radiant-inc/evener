import XCTest

/// The installed-app qualification journeys for the native migration.
/// Each test drives the real Release app on the run-owned simulator against
/// a real isolated hub whose provider answers every ordinary turn with the
/// fixed qualification fixture. The UI runner passes the hub's origin, its
/// fixture-only token and run-owned workspace through the test-run
/// environment. The hub resolves the path supplied to its directory picker, and
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
	/// session whose completed response renders in the web workspace, an unsent draft,
	/// reactivation and a cold launch that both keep the saved hub without
	/// re-entering credentials, the exact draft surviving in the restored
	/// conversation, and an authenticated follow-up through the real hub.
	/// That follow-up after the cold launch is the credential-readback proof.
	func testSavedHubMarkdownAndDraftRecovery() {
		let app = XCUIApplication()
		onboardIfNeeded(app)
		startSession(app, prompt: "qualify the renderer")

		let web = app.webViews.firstMatch
		XCTAssertTrue(web.waitForExistence(timeout: tripwire), "the workspace never opened")
		XCTAssertGreaterThan(web.frame.width, 0)
		XCTAssertGreaterThan(web.frame.height, 0)
		for marker in ["QUALIFICATION_prose_a7", "QUALIFICATION_emphasis_b8", "QUALIFICATION_code_c9"] {
			let rendered = web.descendants(matching: .any)
				.matching(NSPredicate(format: "label CONTAINS %@", marker)).firstMatch
			XCTAssertTrue(rendered.waitForExistence(timeout: tripwire), "the workspace lost \(marker)")
		}
		attach(app, "fixture-response")

		let composer = composerField(app)
		tap(composer, "the composer never appeared")
		composer.typeText(Self.draft)
		XCTAssertEqual(composer.value as? String, Self.draft)

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
		// The same web composer must reopen from the saved hub without onboarding.
		let recovered = composerField(app)
		// The composer mounts before its restored draft loads from the web store, so
		// the draft's arrival is its own bounded wait; a single read could
		// race the restore and fail a draft that did survive.
		let restoreDeadline = Date().addingTimeInterval(tripwire)
		while Date() < restoreDeadline && (recovered.value as? String) != Self.draft {
			Thread.sleep(forTimeInterval: 0.25)
		}
		XCTAssertEqual(recovered.value as? String, Self.draft, "the unsent draft did not survive the cold launch")
		attach(app, "recovered-draft")

		// The authenticated follow-up: the recovered credentials carry a new
		// turn through the real hub, and its response renders. The baseline
		// is sampled only after the restored first response is on screen:
		// a still-loading transcript would otherwise raise the count by
		// itself and pass a follow-up that never completed. The verdict is
		// the loop's own observation: the transcript scrolls older matches
		// out, so the count can fall again after the rise, and a second query
		// can report the old count and fail a follow-up that did complete.
		let followUp = app.webViews.firstMatch.descendants(matching: .any)
			.matching(NSPredicate(format: "label CONTAINS %@", "QUALIFICATION_code_c9"))
		XCTAssertTrue(
			followUp.firstMatch.waitForExistence(timeout: tripwire),
			"the restored first response never rendered"
		)
		let baseline = followUp.count
		tap(app.buttons["Send"], "Send never became available")
		let deadline = Date().addingTimeInterval(tripwire)
		var followedUp = false
		while Date() < deadline {
			if followUp.count > baseline {
				followedUp = true
				break
			}
			Thread.sleep(forTimeInterval: 0.25)
		}
		XCTAssertTrue(
			followedUp,
			"the follow-up turn never completed through the recovered credentials"
		)
		attach(app, "follow-up-response")

		showSessions(app)
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
		let surface = mainSurface(app)
		XCTAssertTrue(surface.waitForExistence(timeout: tripwire))
		if surface.identifier == "add-hub:address" {
			tap(surface, "the first-run address way disappeared")
			XCTAssertTrue(app.textFields["hub-address"].waitForExistence(timeout: tripwire))
		} else {
			showSessions(app)
			tap(app.webViews.buttons["New session"].firstMatch, "scheme activation lost the workspace controls")
			XCTAssertTrue(app.webViews.descendants(matching: .any)["Prompt"].waitForExistence(timeout: tripwire))
		}
	}

	/// A swipe inside an ordinary native sheet must leave its controls
	/// usable: swipe the New session sheet's form rows, then press its
	/// Cancel and see the sheet close back to the Board. The Board's own row
	/// gestures have their own regression checks; this is the sheet's side of
	/// the interplay.
	func testSheetGestureKeepsControlsUsable() throws {
		let app = XCUIApplication()
		onboardIfNeeded(app)
		showSessions(app)
		tap(app.webViews.buttons["Hubs on this iPhone"].firstMatch, "the iPhone settings never appeared")
		app.buttons["iphone-device-work"].tap()
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
		// press can be lost to a selection the swipe left behind, and a
		// retained draft riding in the prompt makes Cancel ask before
		// discarding — the journey answers Keep draft, which closes the
		// sheet with the draft kept, and never presses into the asking
		// alert.
		let deadline = Date().addingTimeInterval(tripwire)
		while prompt.waitForExistence(timeout: 2) && Date() < deadline {
			let keep = app.buttons["Keep draft"].firstMatch
			if keep.exists {
				tap(keep, "the draft alert never offered keeping it")
				continue
			}
			tap(app.buttons["Cancel"], "the sheet's Cancel vanished after swipes")
			if keep.waitForExistence(timeout: 2) {
				tap(keep, "the draft alert never offered keeping it")
			}
		}
		XCTAssertTrue(
			prompt.waitForNonExistence(timeout: tripwire),
			"the sheet never closed after Cancel"
		)
		XCTAssertTrue(
			app.buttons["new-session"].waitForExistence(timeout: tripwire),
			"the board never returned after the sheet closed"
		)
		let hubName = try XCTUnwrap(URL(string: hubOrigin)?.host)
		tap(app.navigationBars.buttons[hubName], "the saved hub menu was unreachable")
		app.buttons["iphone-workspace"].tap()
		XCTAssertTrue(app.webViews.firstMatch.waitForExistence(timeout: tripwire))
	}

	// MARK: - Journey steps

	/// The initial route is either native onboarding or the paired hub's web view.
	private func mainSurface(_ app: XCUIApplication) -> XCUIElement {
		app.descendants(matching: .any).matching(NSPredicate(
			format: "identifier == %@ OR elementType == %d", "add-hub:address", XCUIElement.ElementType.webView.rawValue
		)).firstMatch
	}

	/// A cold-opened session has a Sessions drawer. A session reached through another
	/// screen has Back until that parent screen can show the drawer.
	private func showSessions(_ app: XCUIApplication) {
		let web = app.webViews.firstMatch
		let hubSettings = web.buttons["Hubs on this iPhone"]
		let opener = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "Sessions")).firstMatch
		let back = app.buttons["Back"].firstMatch
		let ready = expectation(for: NSPredicate { _, _ in
			hubSettings.isHittable || opener.isHittable || back.isHittable
		}, evaluatedWith: nil)
		wait(for: [ready], timeout: tripwire)
		if hubSettings.isHittable { return }
		if !opener.isHittable && back.isHittable { back.tap() }
		if !hubSettings.isHittable { tap(opener, "the Sessions drawer was unreachable") }
		XCTAssertTrue(hubSettings.waitForExistence(timeout: tripwire))
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
			answerSavePasswordSheet(app)
			tap(app.buttons["hub-continue"], "the Continue row never appeared")
			// The board is Connect's own effect, checked like every other
			// step: the press can deaden in the settling window every fresh
			// screen has, and the app's own guard makes a repeat press safe.
			// iOS's save-password sheet can lie over the review and swallow
			// the press until it is answered, so each iteration answers it
			// before judging whether Connect is on screen: the sheet hides
			// the covered review from the query, so a check that reads
			// Connect.exists first exits while the review is still there.
			let board = app.webViews.firstMatch
			let shown = Date().addingTimeInterval(tripwire)
			while Date() < shown && !board.waitForExistence(timeout: 4) {
				answerSavePasswordSheet(app)
				let connect = app.buttons["Connect"]
				// The sheet is a separate process, so answering it does not
				// guarantee the review is visible in the same instant. Wait
				// for Connect inside the iteration; the outer deadline still
				// bounds the loop when the review genuinely never appears.
				guard connect.waitForExistence(timeout: 4) else { break }
				tap(connect, "the review step never appeared")
			}
		}
		XCTAssertTrue(
			app.webViews.firstMatch.waitForExistence(timeout: tripwire),
			"the workspace never appeared after onboarding"
		)
	}

	/// Start a session in the fixture workspace using the hub's mobile launcher.
	private func startSession(_ app: XCUIApplication, prompt promptText: String) {
		showSessions(app)
		let web = app.webViews.firstMatch
		tap(web.buttons["New session"].firstMatch, "the Sessions drawer never offered a new session")
		let controls = web.descendants(matching: .any)
		let directory = controls.matching(NSPredicate(format: "label BEGINSWITH %@", "Working directory:")).firstMatch
		tap(directory, "the launcher never offered the workspace picker")
		let workspace = ProcessInfo.processInfo.environment["EVENER_QUALIFICATION_WORKSPACE"] ?? ""
		XCTAssertTrue(workspace.hasPrefix("/"), "the runner must supply its isolated workspace")
		let path = web.textFields["Path"].firstMatch
		tap(path, "the directory picker has no path field")
		path.typeText(workspace)
		XCTAssertEqual(path.value as? String, workspace)
		tap(web.buttons["Go"].firstMatch, "the directory picker could not navigate to the workspace")
		tap(web.buttons["Use this folder"].firstMatch, "the directory picker could not select the workspace")
		XCTAssertTrue(directory.label.hasSuffix(workspace), "the launcher did not select the fixture workspace")

		controls.matching(NSPredicate(format: "label CONTAINS[c] %@", "change model")).firstMatch.tap()
		let model = controls.matching(
			NSPredicate(format: "label CONTAINS[c] %@ OR label CONTAINS[c] %@", "fake-test-model", "Fake Test Model")
		).firstMatch
		tap(model, "the fixture model was unavailable")
		let prompt = web.descendants(matching: .any)["Prompt"].firstMatch
		tap(prompt, "the launcher prompt was unavailable")
		prompt.typeText(promptText)
		XCTAssertEqual(prompt.value as? String, promptText)
		let start = web.buttons["Start"].firstMatch
		waitForEnabled(start)
		tap(start, "the launcher could not start the session")
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
			answerSavePasswordSheet(app)
			tap(app.buttons["new-session"], "the board's compose button never appeared")
		}
		XCTAssertTrue(prompt.waitForExistence(timeout: 2), "the New session sheet never opened")
		return prompt
	}

	/// Types into a field and checks the text arrived whole, clearing and
	/// retyping when it did not. A busy app can drop synthesized keystrokes
	/// mid-burst, and a silently shortened address surfaces only much later,
	/// as a review with a malformed hub and an empty suggested name. The
	/// clear is judged by the field reading empty, never by a tap count: a
	/// restored draft can leave the field holding far more than the text
	/// just typed, and the deletes can drop under load the same way typing
	/// does. A restored draft can park the caret anywhere - and a delete
	/// only removes what is before it - so each burst first taps the
	/// field's own bottom-right corner, where a tap places the caret at the
	/// end of the last line. An empty field reads as its own placeholder,
	/// so that reading counts as empty too — no journey types its
	/// placeholder into a field. Secure fields hide their text, so they are
	/// checked by their bullet count. The verdict is always the field's own
	/// reading: every burst is read back before the deadline is consulted,
	/// because on a slow app a single burst can outlast the whole bound and
	/// still land whole.
	private func typeWholeText(
		_ app: XCUIApplication,
		_ field: XCUIElement,
		_ text: String,
		_ what: String,
		landed: (String) -> Bool
	) {
		let deadline = Date().addingTimeInterval(tripwire)
		while true {
			if let value = field.value as? String, landed(value) {
				return
			}
			guard Date() < deadline else { break }
			// Clear what the field holds — an empty field costs one read —
			// then type the whole text again.
			let remove = app.keyboards.keys["delete"]
			var clears = 0
			while clears < 12 && Date() < deadline {
				let held = field.value as? String ?? ""
				if held.isEmpty || held == (field.placeholderValue ?? "") {
					break
				}
				field.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.9)).tap()
				for _ in 0..<24 where remove.exists && Date() < deadline {
					remove.tap()
				}
				clears += 1
			}
			guard Date() < deadline else { break }
			field.typeText(text)
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

	/// Answers iOS's save-password sheet when the autofill agent raises it
	/// over the content. The token field reads as a login to iOS, so around
	/// the form's continuation the agent — a separate process — lays a
	/// "Save Password?" sheet over the review that silently swallows every
	/// press beneath it until it is answered, while each covered row still
	/// reads valid, enabled, and framed. "Not Now" is the ordinary answer;
	/// the journey never saves credentials outside the app's own storage.
	/// Best effort: a sheet that is absent, or that vanishes before the
	/// answer lands, just leaves the next press to the caller's own effect
	/// check.
	private func answerSavePasswordSheet(_ app: XCUIApplication) {
		let notNow = app.buttons["Not Now"].firstMatch
		guard notNow.exists else { return }
		let settled = Date().addingTimeInterval(2)
		while Date() < settled && !(notNow.exists && notNow.frame.width > 0 && notNow.isEnabled) {
			Thread.sleep(forTimeInterval: 0.25)
		}
		guard notNow.exists && notNow.frame.width > 0 && notNow.isEnabled else { return }
		notNow.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
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

/// WebKit exposes the hub's multiline conversation composer as an editable field.
	private func composerField(_ app: XCUIApplication) -> XCUIElement {
		let field = app.webViews.descendants(matching: .any)["Message"].firstMatch
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
