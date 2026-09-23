// The one piece of the retained-screen wiring whose contract the screens'
// own suites leave unpinned: the wall's Reconnect action firing the
// connection's retry. The suites pin the wall's copy ("to manage
// providers.", in the fatal and re-key tests) and they press
// ConnectionStatus's action inside modals, but no test presses the WALL's
// own action - the glue ConnectionWall owns. Everything else in
// retainedScreen is the screens' suites' net: useConnectionDisplay,
// useLiveReadiness and useRenderClient are pinned in
// connectionDisplay.test.ts, and their composed outcomes (banner retention,
// wall copy and conditions, re-key freshness, the modal status, the
// not-selected guard copy) through the screens' reconnect suites. Mirrors
// their mocking: ConnectionProvider is the only native edge.
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { ConnectionWall } from "./retainedScreen";
import { nativeModuleMock, render, renderedText } from "./renderNative.testkit";

vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
}));
// retainedScreen imports the provider to compose the wiring; the wall itself
// never calls it, but the module must load without the native expo graph the
// provider pulls in - the same reason MarketplaceBrowser.removal mocks it.
vi.mock("./ConnectionProvider", () => ({ useConnection: () => ({}) }));

it("the wall's Reconnect action fires the connection's own retry", () => {
	const retry = vi.fn();
	const tree = render(
		<ConnectionWall
			hubName="Work hub"
			purpose="manage plugins"
			onReconnect={retry}
		/>,
	);
	expect(renderedText(tree)).toContain(
		"Connect to Work hub to manage plugins.",
	);
	const reconnect = tree.root.findByProps({ accessibilityLabel: "Reconnect" });
	act(() => {
		reconnect.props.onPress();
	});
	expect(retry).toHaveBeenCalledOnce();
});

it("a fatal wall names the reason its retry cannot clear yet", () => {
	// A fatal close (a protocol mismatch) is what the connection's own
	// compatibility message describes, and its last word is the reconnect
	// the wall offers - the way back once the app and hub are updated
	// together (the fatal-retry flow ProvidersScreen's suite pins). Without
	// the message, that retry reads as ineffective: the wall never said why
	// nothing reconnected. The reason renders beside the action.
	const retry = vi.fn();
	const tree = render(
		<ConnectionWall
			hubName="Work hub"
			purpose="manage plugins"
			error="This app and hub need compatible versions. Update them together, then reconnect."
			onReconnect={retry}
		/>,
	);
	expect(renderedText(tree)).toContain(
		"This app and hub need compatible versions. Update them together, then reconnect.",
	);
	expect(
		tree.root.findAllByProps({ accessibilityLabel: "Reconnect" }),
	).toHaveLength(1);
});
