import { expect, it, vi } from "vitest";
import { MarkdownResponse } from "./MarkdownResponse";
import { render } from "./renderNative.testkit";

const mode = vi.hoisted(() => ({ scheme: "light" as "light" | "dark" }));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	AccessibilityInfo: { announceForAccessibility: () => {} },
	Linking: { openURL: async () => {} },
	useColorScheme: () => mode.scheme,
}));
vi.mock("expo-clipboard", () => ({ setStringAsync: async () => {} }));
vi.mock("react-native-enriched-markdown", () => ({ EnrichedMarkdownText: "EnrichedMarkdownText" }));

function markdownStyle(markdown: string) {
	const tree = render(<MarkdownResponse markdown={markdown} />);
	return tree.root.findByType("EnrichedMarkdownText" as never).props.markdownStyle;
}

it("picks light code colors in light mode", () => {
	mode.scheme = "light";
	expect(markdownStyle("`x`").codeBlock.syntaxColors).toMatchObject({ string: "#2e6443", number: "#785119" });
});

it("picks dark code colors in dark mode", () => {
	mode.scheme = "dark";
	expect(markdownStyle("`x`").codeBlock.syntaxColors).toMatchObject({ string: "#b8d8a3", number: "#ecc48d" });
});

it("fills a checked task box with accent-fill so its white checkmark stays legible", () => {
	mode.scheme = "dark";
	expect(markdownStyle("- [x] done").taskList.checkedColor).toBe("#0070E0");
});

it("sets agent prose in Source Serif 4 at 17/26 and headings in the system font", () => {
	mode.scheme = "light";
	const s = markdownStyle("Hello");
	expect(s.paragraph).toMatchObject({ fontFamily: "SourceSerif4-Regular", fontSize: 17, lineHeight: 26, color: "#252521" });
	expect(s.list).toMatchObject({ fontFamily: "SourceSerif4-Regular" });
	expect(s.list).toMatchObject({ markerFontWeight: "normal" });
	expect(s.blockquote).toMatchObject({ fontFamily: "SourceSerif4-Regular" });
	expect(s.h1).toMatchObject({ fontSize: 20, fontWeight: "600" });
	expect(s.h1.fontFamily).toBeUndefined();
	expect(s.h2).toMatchObject({ fontSize: 17, fontWeight: "600" });
	expect(s.h3).toMatchObject({ fontSize: 15, fontWeight: "600" });
	expect(s.codeBlock).toMatchObject({ fontFamily: "Menlo" });
	expect(s.code).toMatchObject({ fontFamily: "Menlo" });
});

it("uses the dimmer prose ink in dark mode", () => {
	mode.scheme = "dark";
	expect(markdownStyle("Hello").paragraph).toMatchObject({ color: "#E0DED6" });
});

it("keeps tables in the system font and ink-hi, unlike the serif prose", () => {
	mode.scheme = "light";
	const light = markdownStyle("| a |\n|---|\n| 1 |").table;
	expect(light.fontFamily).toBeUndefined();
	expect(light.color).toBe("#252521");

	mode.scheme = "dark";
	expect(markdownStyle("| a |\n|---|\n| 1 |").table.color).toBe("#F2F1EB");
});
