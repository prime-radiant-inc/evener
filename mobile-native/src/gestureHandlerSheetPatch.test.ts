import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// Native-stack sheets sit outside RCTSurfaceView. Their root recognizer must
// cancel the screen's touch handler so releasing a swipe cannot press a row.
// These installed-source checks complement the real simulator sheet journey.
describe("react-native-gesture-handler sheet touch cancellation", () => {
	const manager = readFileSync(
		fileURLToPath(
			new URL("../node_modules/react-native-gesture-handler/apple/RNGestureHandlerManager.mm", import.meta.url),
		),
		"utf8",
	);

	it("starts the root search at the child before walking its ancestors", () => {
		expect(manager).toMatch(/RNGHUIView \*touchHandlerView = childView;/);
		expect(manager).toMatch(/touchHandlerView = touchHandlerView\.superview;/);
	});

	it("installs the root recognizer on a modally presented screen", () => {
		expect(manager).toContain('screenViewClass = NSClassFromString(@"RNSScreenView");');
		expect(manager).toContain('[[view valueForKey:@"isModal"] boolValue]');
		expect(manager).toContain("!RNGHIsScreensTouchHandlerHost(touchHandlerView)");
		expect(manager).toContain("[touchHandlerView addGestureRecognizer:recognizer]");
	});

	it("retries once on the main queue when children mount before their root", () => {
		expect(manager).toMatch(
			/if \(touchHandlerView == nil\) \{[\s\S]*?if \(!isRetry\) \{\s*dispatch_async\(dispatch_get_main_queue\(\), \^\{\s*\[self registerViewWithGestureRecognizerAttachedIfNeeded:childView isRetry:YES\];/,
		);
		expect(manager.match(/registerViewWithGestureRecognizerAttachedIfNeeded:childView isRetry:YES/g)).toHaveLength(1);
	});

	it("cancels the touch handler the screen reports when none sits on its view", () => {
		expect(manager).toMatch(
			/if \(touchHandler == nil && \[viewWithTouchHandler respondsToSelector:[^\n]+\) \{[\s\S]*?\[viewWithTouchHandler valueForKey:@"touchHandler"\]/,
		);
		expect(manager).toMatch(/touchHandler = screenTouchHandler;/);
		expect(manager).toMatch(/\[touchHandler setEnabled:NO\];\s*\[touchHandler setEnabled:YES\];/);
	});
});
