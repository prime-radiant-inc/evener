// The one piece of the retained-screen wiring whose contract the screens'
// own suites leave unpinned: the wall itself. It says what the screen needs
// and, on a fatal close, why it can't have it, and offers no Reconnect: the
// app reconnects on its own (spec principle 2). Everything else in
// retainedScreen is the screens' suites' net: useConnectionDisplay,
// useLiveReadiness and useRenderClient are pinned in
// connectionDisplay.test.ts, and their composed outcomes (banner retention,
// wall copy and conditions, re-key freshness, the modal status, the
// not-selected guard copy) through the screens' reconnect suites. Mirrors
// their mocking: ConnectionProvider is the only native edge.
import { expect, it, vi } from "vitest";
import { INCOMPATIBLE_VERSIONS } from "./connectionRecovery";
import { ConnectionWall } from "./retainedScreen";
import { nativeModuleMock, render, renderedText } from "./renderNative.testkit";

vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
}));
// retainedScreen imports the provider to compose the wiring; the wall itself
// never calls it, but the module must load without the native expo graph the
// provider pulls in - the same reason MarketplaceBrowser.removal mocks it.
vi.mock("./ConnectionProvider", () => ({ useConnection: () => ({}) }));

it("the wall says what the screen needs, and offers no Reconnect", () => {
	const tree = render(<ConnectionWall hubName="Work hub" purpose="manage plugins" />);
	expect(renderedText(tree)).toBe("Connect to Work hub to manage plugins.");
	expect(tree.root.findAllByProps({ accessibilityRole: "button" })).toHaveLength(0);
});

it("a fatal wall names the reason no retry can clear it", () => {
	const tree = render(<ConnectionWall hubName="Work hub" purpose="manage plugins" error={INCOMPATIBLE_VERSIONS} />);
	expect(renderedText(tree)).toContain(INCOMPATIBLE_VERSIONS);
	expect(tree.root.findAllByProps({ accessibilityRole: "button" })).toHaveLength(0);
});
