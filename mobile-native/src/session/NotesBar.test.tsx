import type { ReactTestRenderer } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import { pressable, render, textOf } from "../renderNative.testkit";
import { NotesBar } from "./NotesBar";
import type { NotesBarPreview } from "./sessionNotes";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const palette = paletteFor("light");

const texts = (tree: ReactTestRenderer) => tree.root.findAll((node) => String(node.type) === "Text");
const symbol = (tree: ReactTestRenderer) => tree.root.find((node) => String(node.type) === "SymbolView");

describe("the notes bar (spec 8.8)", () => {
	// Under the nav bar's glass the bar draws clear on it; elsewhere on the page.
	it("draws clear on the header's glass, and on the page off it", () => {
		const fill = (onGlass?: boolean) => {
			const tree = render(
				<NotesBar preview={{ glyph: "person", text: "Your note" }} onPress={() => {}} onGlass={onGlass} />,
			);
			return tree.root.find((node) => String(node.type) === "Pressable").props.style({ pressed: false })
				.backgroundColor;
		};
		expect(fill()).toBe(palette.page);
		expect(fill(true)).toBe("transparent");
	});

	it.each([
		["person", "Your note: keep the tests", "person"],
		["sparkles", "Agent's note: reading the router", "sparkles"],
		["link", "Design doc", "link"],
	] as const)("shows the %s glyph at 13pt in inkMid, then its text on one line", (glyph, text, name) => {
		const tree = render(<NotesBar preview={{ glyph, text }} onPress={() => {}} />);
		expect(symbol(tree).props).toMatchObject({ name, size: 13, tintColor: palette.inkMid });
		const [line] = texts(tree);
		expect(textOf(line)).toBe(text);
		expect(line.props).toMatchObject({ numberOfLines: 1, ellipsizeMode: "tail" });
		expect(line.props.style).toMatchObject({ fontSize: 15, lineHeight: 20, color: palette.inkHi });
		expect(texts(tree)).toHaveLength(1);
	});

	it("says at the trailing edge, in words, that links exist", () => {
		const preview: NotesBarPreview = { glyph: "person", text: "Your note: keep the tests", links: "2 links" };
		const tree = render(<NotesBar preview={preview} onPress={() => {}} />);
		const [line, links] = texts(tree);
		expect(textOf(line)).toBe("Your note: keep the tests");
		expect(textOf(links)).toBe("2 links");
		expect(links.props.style).toMatchObject({ fontSize: 13, lineHeight: 18, color: palette.inkMid });
	});

	it("is one 32pt button that opens the sheet, read as its text and links", () => {
		const onPress = vi.fn();
		const tree = render(
			<NotesBar preview={{ glyph: "person", text: "Your note: keep the tests", links: "2 links" }} onPress={onPress} />,
		);
		const bar = pressable(tree, "Your note: keep the tests, 2 links");
		if (!bar) throw new Error("no notes bar button");
		const style = typeof bar.props.style === "function" ? bar.props.style({ pressed: false }) : bar.props.style;
		expect(style).toMatchObject({ height: 32 });
		expect(onPress).not.toHaveBeenCalled();
		bar.props.onPress();
		expect(onPress).toHaveBeenCalledOnce();
	});
});
