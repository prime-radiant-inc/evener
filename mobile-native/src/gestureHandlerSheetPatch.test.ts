import { describe, expect, it } from "vitest";
import { patchAddedLines } from "./packagePatch.testkit";

// A swipe row inside a sheet must not fire its press when the swipe lets go
// (issue #2952). Every sheet is a native-stack formSheet, which UIKit presents
// outside React Native's RCTSurfaceView. react-native-gesture-handler 2.32
// cancels a JS responder (a Pressable's touch) by toggling the React touch
// handler it finds where its root recognizer sits, and it looks for that spot
// by walking up to an RCTSurfaceView. Inside a formSheet the walk finds none,
// so no root recognizer is installed, and the Pressable's press survives the
// swipe. On iOS, GestureHandlerRootView is a plain View and changes nothing.
//
// mobile-native ships upstream's fix (react-native-gesture-handler PR 4306,
// released in 2.33.0, outside Expo SDK 57's ~2.32.0 pin) as a patch. The native
// gate has no iOS toolchain to compile or run it, so this test guards the patch
// itself: it reads the committed diff and checks the three parts of that fix.
describe("react-native-gesture-handler sheet touch-cancellation patch", () => {
	const manager = () => patchAddedLines("react-native-gesture-handler", "2.32.0", "apple/RNGestureHandlerManager.mm");

	it("installs the root recognizer on a modally presented screen", () => {
		// The walk up from a handler's view stops at a modal RNSScreenView, the
		// view react-native-screens attaches the sheet's own touch handler to.
		expect(manager()).toContain('screenViewClass = NSClassFromString(@"RNSScreenView");');
		expect(manager()).toContain('[[view valueForKey:@"isModal"] boolValue]');
		expect(manager()).toContain("!RNGHIsScreensTouchHandlerHost(touchHandlerView)");
	});

	it("retries once when the view isn't connected to its root yet", () => {
		expect(manager()).toContain("registerViewWithGestureRecognizerAttachedIfNeeded:childView isRetry:YES");
	});

	it("cancels the touch handler the screen reports when none sits on its view", () => {
		expect(manager()).toContain('[viewWithTouchHandler valueForKey:@"touchHandler"]');
	});
});
