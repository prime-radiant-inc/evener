// The bar in a running subagent's composer place (spec 9, ruling 30).
import { expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import { SubagentBar } from "./SubagentBar";

vi.mock("expo-symbols", () => ({ SymbolView: () => null }));
vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());

it("wraps Ask coordinator to stop it onto a second line rather than cut it off", () => {
	// Half of an iPhone 17 Pro's bar leaves about 143pt for the words, and at
	// 15pt they need about 180 (seen truncated on the simulator, phase 4 PR 9).
	const tree = render(<SubagentBar offer="ask" onStop={() => {}} onOpenCoordinator={() => {}} />);
	const label = tree.root.find(
		(node) => String(node.type) === "Text" && node.props.children === "Ask coordinator to stop it",
	);
	expect(label.props.numberOfLines).toBe(2);
	expect(label.props.style).toMatchObject({ flexShrink: 1, textAlign: "center" });
});
