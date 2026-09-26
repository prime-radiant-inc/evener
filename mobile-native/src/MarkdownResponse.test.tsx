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
