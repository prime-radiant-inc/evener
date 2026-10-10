// The bar in a running subagent's composer place (spec 9, ruling 30).
import { expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import { SubagentBar } from "./SubagentBar";

vi.mock("expo-symbols", () => ({ SymbolView: () => null }));
vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());

// Side by side, "Ask coordinator to stop it" needs about 180pt of the 143pt
// half the bar leaves it even at the default text size, and "Open
// coordinator" is cut off at XXXL (device audit N9): the two stack, each the
// bar's full width. A label wraps rather than truncates, so at the largest
// sizes no button hides its verb behind an ellipsis.
it("stacks its buttons full width, and wraps a label rather than cut it off", () => {
	const tree = render(<SubagentBar offer="ask" onStop={() => {}} onOpenCoordinator={() => {}} />);
	const bar = tree.root.find((node) => String(node.type) === "View" && node.props.testID === "subagent-bar");
	expect(bar.props.style).toMatchObject({ flexDirection: "column" });
	const labels = tree.root.findAll((node) => String(node.type) === "Text");
	expect(labels.map((label) => [label.props.children, label.props.numberOfLines])).toEqual([
		["Ask coordinator to stop it", undefined],
		["Open coordinator", undefined],
	]);
});

it("wraps and centers the stop it requested, as it does the buttons", () => {
	const tree = render(<SubagentBar offer="requested" onStop={() => {}} onOpenCoordinator={() => {}} />);
	const requested = tree.root.find((node) => String(node.type) === "Text" && node.props.children === "Stop requested");
	expect(requested.props.numberOfLines).toBeUndefined();
	expect(requested.props.style).toMatchObject({ textAlign: "center" });
});
