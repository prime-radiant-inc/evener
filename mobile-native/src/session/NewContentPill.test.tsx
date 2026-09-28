import { describe, expect, it, vi } from "vitest";
import { render, textOf } from "../renderNative.testkit";
import { NewContentPill } from "./NewContentPill";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

const ACCENT_INK = "#0064C2";

describe("the new-content pill", () => {
	it("says how many new rows arrived below, in accent ink", () => {
		const tree = render(<NewContentPill count={3} onPress={() => {}} />);
		const pill = tree.root.findAll((node) => String(node.type) === "Pressable")[0];
		expect(pill.props.accessibilityLabel).toBe("3 new below, scroll to the end");
		const text = tree.root.findAll((node) => String(node.type) === "Text")[0];
		expect(textOf(text)).toBe("↓ 3 new");
		expect(text.props.style).toMatchObject({ color: ACCENT_INK, fontSize: 15, lineHeight: 20 });
	});

	it("renders nothing when nothing is new", () => {
		expect(render(<NewContentPill count={0} onPress={() => {}} />).toJSON()).toBeNull();
	});

	it("scrolls to the end when pressed", () => {
		const onPress = vi.fn();
		const tree = render(<NewContentPill count={1} onPress={onPress} />);
		tree.root.findAll((node) => String(node.type) === "Pressable")[0].props.onPress();
		expect(onPress).toHaveBeenCalledTimes(1);
	});
});
