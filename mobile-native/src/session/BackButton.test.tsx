import { describe, expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import { render, renderedText, textOf } from "../renderNative.testkit";
import { BackButton } from "./BackButton";

const viewport = vi.hoisted(() => ({ fontScale: 1 }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	useWindowDimensions: () => ({ width: 390, height: 844, scale: 3, fontScale: viewport.fontScale }),
}));

const palette = paletteFor("light");

function button(tree: ReturnType<typeof render>) {
	return tree.root.findAll((node) => String(node.type) === "Pressable")[0];
}

describe("Back with who else needs you (spec 13.2)", () => {
	it("caps text to the fixed navigation bar and supplies the large content viewer", () => {
		viewport.fontScale = 3;
		try {
			const tree = render(<BackButton count={123} onPress={() => {}} />);
			const count = tree.root.findByType("Text" as never);
			expect(count.props.style.fontSize).toBeLessThanOrEqual(23);
			expect(button(tree).props.accessibilityShowsLargeContentViewer).toBe(true);
		} finally {
			viewport.fontScale = 1;
		}
	});
	it("carries the count of the others that need you, in amber", () => {
		const tree = render(<BackButton count={4} onPress={() => {}} />);
		expect(button(tree).props.accessibilityLabel).toContain("4");
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
		const label = (count: number) =>
			button(render(<BackButton count={count} onPress={() => {}} />)).props.accessibilityLabel as string;
		expect(label(1)).toContain("1");
		// The words around the count agree with it.
		expect(label(1).replace("1", "#")).not.toBe(label(4).replace("4", "#"));
		expect(label(4).replace("4", "#")).toBe(label(7).replace("7", "#"));
	});

	it("says what a screen names instead, when it gives a label (the Reader's held count)", () => {
		const tree = render(<BackButton count={2} label="Back, 2 new while you read" onPress={() => {}} />);
		expect(button(tree).props.accessibilityLabel).toBe("Back, 2 new while you read");
		expect(textOf(tree.root.findAll((node) => String(node.type) === "Text")[0])).toBe("2");
	});

	it("is a plain Back when nobody else needs you", () => {
		const tree = render(<BackButton count={0} onPress={() => {}} />);
		const plain: string = button(tree).props.accessibilityLabel;
		expect(plain).not.toMatch(/\d/);
		// The counted labels are this one with the count after it.
		expect(button(render(<BackButton count={4} onPress={() => {}} />)).props.accessibilityLabel).toMatch(
			new RegExp(`^${plain}, `),
		);
		expect(renderedText(tree)).toBe("");
	});

	it("goes back when pressed", () => {
		const onPress = vi.fn();
		const tree = render(<BackButton count={2} onPress={onPress} />);
		button(tree).props.onPress();
		expect(onPress).toHaveBeenCalledTimes(1);
	});
});
