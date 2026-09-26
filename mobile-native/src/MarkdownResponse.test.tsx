import { expect, it, vi } from "vitest";

const mode = vi.hoisted(() => ({ scheme: "light" as "light" | "dark" }));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	AccessibilityInfo: { announceForAccessibility: () => {} },
	Linking: { openURL: async () => {} },
	useColorScheme: () => mode.scheme,
}));
vi.mock("expo-clipboard", () => ({ setStringAsync: async () => {} }));
vi.mock("react-native-enriched-markdown", () => ({ EnrichedMarkdownText: "EnrichedMarkdownText" }));

const { MarkdownResponse } = await import("./MarkdownResponse");
const { render } = await import("./renderNative.testkit");

function codeColors() {
	const tree = render(<MarkdownResponse markdown="`x`" />);
	return tree.root.findByType("EnrichedMarkdownText" as never).props.markdownStyle.codeBlock.syntaxColors;
}

it("picks light code colors in light mode", () => {
	mode.scheme = "light";
	expect(codeColors()).toMatchObject({ string: "#2e6443", number: "#785119" });
});

it("picks dark code colors in dark mode", () => {
	mode.scheme = "dark";
	expect(codeColors()).toMatchObject({ string: "#b8d8a3", number: "#ecc48d" });
});

it("fills a checked task box with accent-fill so its white checkmark stays legible", () => {
	mode.scheme = "dark";
	const tree = render(<MarkdownResponse markdown={"- [x] done"} />);
	const { checkedColor } = tree.root.findByType("EnrichedMarkdownText" as never).props.markdownStyle.taskList;
	expect(checkedColor).toBe("#0070E0");
});
