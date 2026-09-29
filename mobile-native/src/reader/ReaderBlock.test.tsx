import { expect, it, vi } from "vitest";
import { act } from "react-test-renderer";
import { DisplayProvider } from "../display/displayContext";
import { DisplayPreferences, type ReadingFont } from "../display/displayPreferences";
import { render } from "../renderNative.testkit";
import { documentBlocks } from "./documentBlocks";
import { ReaderBlock } from "./ReaderBlock";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("expo-clipboard", () => ({ setStringAsync: async () => {} }));
vi.mock("react-native-enriched-markdown", () => ({ EnrichedMarkdownText: "EnrichedMarkdownText" }));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("react-native-webview", () => ({ WebView: "WebView" }));

function preferences(font: ReadingFont) {
	const values = new Map<string, string>();
	const prefs = new DisplayPreferences({
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => {
			values.set(key, value);
		},
	});
	prefs.set({ readingFont: font });
	return prefs;
}

function styleIn(font: ReadingFont) {
	const [block] = documentBlocks("A plan's opening paragraph.");
	if (!block) throw new Error("no block");
	const tree = render(
		<DisplayProvider value={preferences(font)}>
			<ReaderBlock block={block} changed={false} />
		</DisplayProvider>,
	);
	return tree.root.findByType("EnrichedMarkdownText" as never).props.markdownStyle;
}

it("reads a document in the serif by default, and in the system face under Sans (spec 12)", () => {
	const serif = styleIn("serif");
	expect(serif.paragraph).toMatchObject({ fontFamily: "SourceSerif4-Regular", fontSize: 18, lineHeight: 28 });
	expect(serif.h1.fontFamily).toBe("SourceSerif4-SemiBold");
	const sans = styleIn("sans");
	expect(sans.paragraph.fontFamily).toBeUndefined();
	expect(sans.paragraph).toMatchObject({ fontSize: 18, lineHeight: 28 });
	expect(sans.h1.fontFamily).toBeUndefined();
	expect(sans.h1).toMatchObject({ fontSize: 24, fontWeight: "600" });
	expect(sans.codeBlock).toMatchObject({ fontFamily: "Menlo" });
});

it("ends a selection when you tap a rule, which has no menu of its own", () => {
	const [rule] = documentBlocks("A plan.\n\n---\n\n").filter((block) => block.kind === "rule");
	if (!rule) throw new Error("no rule block");
	const onTap = vi.fn();
	const tree = render(
		<DisplayProvider value={preferences("serif")}>
			<ReaderBlock block={rule} changed={false} onTap={onTap} />
		</DisplayProvider>,
	);
	act(() => tree.root.findByType("Pressable" as never).props.onPress());
	expect(onTap).toHaveBeenCalledTimes(1);
});
