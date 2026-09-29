// The bar in a running subagent's composer place (spec 9, ruling 30).
import { expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import { SubagentBar } from "./SubagentBar";

vi.mock("expo-symbols", () => ({ SymbolView: () => null }));
vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());

// Side by side, "Ask coordinator to stop it" needs about 180pt of the 143pt
// half the bar leaves it even at the default text size, and "Open
// coordinator" is cut off at XXXL (device audit N9): the two stack, each the
// bar's full width, and every label keeps to one line.
it("stacks its buttons full width, each label on one line", () => {
	const tree = render(<SubagentBar offer="ask" onStop={() => {}} onOpenCoordinator={() => {}} />);
	const bar = tree.root.find((node) => String(node.type) === "View" && node.props.testID === "subagent-bar");
	expect(bar.props.style).toMatchObject({ flexDirection: "column" });
	const labels = tree.root.findAll((node) => String(node.type) === "Text");
	expect(labels.map((label) => [label.props.children, label.props.numberOfLines])).toEqual([
		["Ask coordinator to stop it", 1],
		["Open coordinator", 1],
	]);
	for (const button of tree.root.findAll(
		(node) => String(node.type) === "Pressable" && node.props.accessibilityRole === "button",
	)) {
		const style = typeof button.props.style === "function" ? button.props.style({ pressed: false }) : button.props.style;
		expect(style.flex ?? 0).toBe(0);
		expect(style.alignSelf ?? "stretch").toBe("stretch");
	}
});
