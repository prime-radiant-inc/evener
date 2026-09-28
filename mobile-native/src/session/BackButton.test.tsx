import { describe, expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import { render, renderedText, textOf } from "../renderNative.testkit";
import { BackButton } from "./BackButton";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const palette = paletteFor("light");

function button(tree: ReturnType<typeof render>) {
	return tree.root.findAll((node) => String(node.type) === "Pressable")[0];
}

describe("Back with who else needs you (spec 13.2)", () => {
	it("carries the count of the others that need you, in amber", () => {
		const tree = render(<BackButton count={4} onPress={() => {}} />);
		expect(button(tree).props.accessibilityLabel).toBe("Back, 4 others need you");
		const count = tree.root.findAll((node) => String(node.type) === "Text")[0];
		expect(textOf(count)).toBe("4");
		expect(count.props.style).toMatchObject({
			color: palette.attentionInk,
			fontWeight: "600",
			fontVariant: ["tabular-nums"],
		});
		const chevron = tree.root.findAll((node) => String(node.type) === "SymbolView")[0];
		expect(chevron.props).toMatchObject({ name: "chevron.left", tintColor: palette.accentInk });
	});

	it("says one other in the singular", () => {
		const tree = render(<BackButton count={1} onPress={() => {}} />);
		expect(button(tree).props.accessibilityLabel).toBe("Back, 1 other needs you");
	});

	it("is a plain Back when nobody else needs you", () => {
		const tree = render(<BackButton count={0} onPress={() => {}} />);
		expect(button(tree).props.accessibilityLabel).toBe("Back");
		expect(renderedText(tree)).toBe("");
	});

	it("goes back when pressed", () => {
		const onPress = vi.fn();
		const tree = render(<BackButton count={2} onPress={onPress} />);
		button(tree).props.onPress();
		expect(onPress).toHaveBeenCalledTimes(1);
	});
});
