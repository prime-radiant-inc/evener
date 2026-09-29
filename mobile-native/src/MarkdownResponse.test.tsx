import { expect, it, vi } from "vitest";
import * as Clipboard from "expo-clipboard";
import { DisplayProvider } from "./display/displayContext";
import { DisplayPreferences } from "./display/displayPreferences";
import { MarkdownResponse } from "./MarkdownResponse";
import { MermaidDiagram } from "./MermaidDiagram";
import { render } from "./renderNative.testkit";

const mode = vi.hoisted(() => ({ scheme: "light" as "light" | "dark" }));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	AccessibilityInfo: { announceForAccessibility: () => {} },
	Linking: { openURL: async () => {} },
	useColorScheme: () => mode.scheme,
}));
vi.mock("expo-clipboard", () => ({ setStringAsync: vi.fn(async () => {}) }));
vi.mock("react-native-enriched-markdown", () => ({ EnrichedMarkdownText: "EnrichedMarkdownText" }));
vi.mock("react-native-webview", () => ({ WebView: "WebView" }));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));

function markdownStyle(markdown: string) {
	const tree = render(<MarkdownResponse markdown={markdown} />);
	return tree.root.findByType("EnrichedMarkdownText" as never).props.markdownStyle;
}

// MermaidDiagram is a React.memo component, so the test renderer reports its
// inner render function as the node type - a string lookup cannot find it.
// Match that function by identity, so a rename cannot quietly empty the finder.
const MERMAID_INNER = (MermaidDiagram as unknown as { type: unknown }).type;
function diagrams(tree: ReturnType<typeof render>) {
	return tree.root.findAll((node) => node.type === MERMAID_INNER);
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
	expect(s.paragraph).toMatchObject({
		fontFamily: "SourceSerif4-Regular",
		fontSize: 17,
		lineHeight: 26,
		color: "#252521",
	});
	expect(s.list).toMatchObject({ fontFamily: "SourceSerif4-Regular", markerFontWeight: "normal" });
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

it("stays selectable with its Copy response item by default, for the task and subagent sheets", () => {
	const tree = render(<MarkdownResponse markdown="Hello" />);
	const text = tree.root.findByType("EnrichedMarkdownText" as never);
	expect(text.props.selectable).toBe(true);
	expect(text.props.contextMenuItems.map((item: { text: string }) => item.text)).toEqual(["Copy response"]);
});

it("drops native selection and its menu when the caller owns touch and hold", () => {
	const tree = render(<MarkdownResponse markdown="Hello" selectable={false} />);
	const text = tree.root.findByType("EnrichedMarkdownText" as never);
	expect(text.props.selectable).toBe(false);
	expect(text.props.contextMenuItems).toBeUndefined();
});

it("drops the serif for agent prose when the phone reads in Sans (spec 12)", () => {
	mode.scheme = "light";
	const values = new Map<string, string>();
	const prefs = new DisplayPreferences({
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => {
			values.set(key, value);
		},
	});
	prefs.set({ readingFont: "sans" });
	const tree = render(
		<DisplayProvider value={prefs}>
			<MarkdownResponse markdown="Hello" />
		</DisplayProvider>,
	);
	const style = tree.root.findByType("EnrichedMarkdownText" as never).props.markdownStyle;
	expect(style.paragraph.fontFamily).toBeUndefined();
	expect(style.paragraph).toMatchObject({ fontSize: 17, lineHeight: 26 });
	expect(style.codeBlock).toMatchObject({ fontFamily: "Menlo" });
});

it("renders a mermaid fence as a diagram between prose segments", () => {
	const tree = render(<MarkdownResponse markdown={"before\n\n```mermaid\ngraph TD; A-->B\n```\n\nafter"} />);
	const prose = tree.root.findAllByType("EnrichedMarkdownText" as never);
	expect(prose.map((node) => node.props.markdown)).toEqual(["before\n\n", "\n\nafter"]);
	expect(diagrams(tree)).toHaveLength(1);
});

it("keeps the whole message in every segment's Copy response item", () => {
	const markdown = "before\n\n```mermaid\ngraph TD; A-->B\n```\n\nafter";
	const tree = render(<MarkdownResponse markdown={markdown} />);
	for (const prose of tree.root.findAllByType("EnrichedMarkdownText" as never)) {
		const copy = prose.props.contextMenuItems.find((item: { text: string }) => item.text === "Copy response");
		copy.onPress();
	}
	expect(Clipboard.setStringAsync).toHaveBeenCalledWith(markdown);
});

it("hands the caller's accessibility actions to every prose segment and the diagram", () => {
	const actions = [{ name: "select", label: "Select text" }];
	const onAccessibilityAction = () => {};
	const tree = render(
		<MarkdownResponse
			markdown={"before\n\n```mermaid\ngraph TD; A-->B\n```\n\nafter"}
			accessibilityActions={actions}
			onAccessibilityAction={onAccessibilityAction}
		/>,
	);
	for (const prose of tree.root.findAllByType("EnrichedMarkdownText" as never)) {
		expect(prose.props.accessibilityActions).toBe(actions);
		expect(prose.props.onAccessibilityAction).toBe(onAccessibilityAction);
	}
	const diagram = diagrams(tree)[0];
	expect(diagram.props.accessibilityActions).toBe(actions);
	expect(diagram.props.onAccessibilityAction).toBe(onAccessibilityAction);
});

it("hands the caller's accessibility actions to the diagram for a diagram-only message", () => {
	const actions = [{ name: "select", label: "Select text" }];
	const tree = render(
		<MarkdownResponse
			markdown={"```mermaid\ngraph TD; A-->B\n```"}
			accessibilityActions={actions}
			onAccessibilityAction={() => {}}
		/>,
	);
	expect(diagrams(tree)[0]?.props.accessibilityActions).toBe(actions);
});
