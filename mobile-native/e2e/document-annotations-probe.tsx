// Qualification-only entry. Production index.ts and navigation never import it.
import { registerRootComponent } from "expo";
import * as Clipboard from "expo-clipboard";
import { useRef, useState } from "react";
import { FlatList, Pressable, SafeAreaView, ScrollView, Text, View, useWindowDimensions } from "react-native";
import { EnrichedMarkdownText } from "react-native-enriched-markdown";
import { useCodeText } from "../src/reader/ReaderBlock";
import { allowFontScaling, useColors } from "../src/ui";

export const RICH_SOURCE =
	"# Note\r\n\r\nfirst **same** [link](https://example.test) &amp; 😀\r\n\r\n" +
	"second same\r\n\r\n- item **same**\r\n\r\n" +
	"| Cell | Other |\r\n| --- | --- |\r\n| same | tail |\r\n\r\n```ts\r\nsame()\r\n```\r\n";

// Independent literal input ranges, not results of a source map or row scanner.
const ROWS = [
	{ id: "heading", start: 0, end: 10 },
	{ id: "first-passage", start: 10, end: 66 },
	{ id: "second-passage", start: 66, end: 81 },
	{ id: "list", start: 81, end: 100 },
	{ id: "table", start: 100, end: 152 },
	{ id: "code", start: 152, end: 172 },
];

// Original documents and endpoints are authored independently of the native model.
// The extra LF before the closing fence is Marked's discarded code separator.
export const CODE_FIXTURES = [
	{ text: "", source: "```text\n\n```\n", blockEnd: 13, textEnd: 8 },
	{ text: "same()", source: "```text\nsame()\n```\n", blockEnd: 19, textEnd: 14 },
	{ text: "same()\n", source: "```text\nsame()\n\n```\n", blockEnd: 20, textEnd: 15 },
	{ text: "same()\n\n", source: "```text\nsame()\n\n\n```\n", blockEnd: 21, textEnd: 16 },
	{ text: "\nsame()\n\nnext()\n", source: "```text\n\nsame()\n\nnext()\n\n```\n", blockEnd: 29, textEnd: 24 },
	{ text: "\tfirst\tsecond\n", source: "```text\n\tfirst\tsecond\n\n```\n", blockEnd: 27, textEnd: 22 },
	{ text: "😀 e\u0301 שלום same()\n", source: "```text\n😀 e\u0301 שלום same()\n\n```\n", blockEnd: 31, textEnd: 26 },
	{
		text: "0123456789012345678901234567890123456789012345678901234567890123456789\n",
		source: "```text\n0123456789012345678901234567890123456789012345678901234567890123456789\n\n```\n",
		blockEnd: 84,
		textEnd: 79,
	},
	{ text: "same()\r\n\r\n", source: "```text\nsame()\r\n\r\n\n```\n", blockEnd: 23, textEnd: 18 },
] as const;

function ProbeButton({ id, title, onPress }: { id: string; title: string; onPress(): void }) {
	return (
		<Pressable testID={id} accessibilityRole="button" accessibilityLabel={title} onPress={onPress} style={{ padding: 8 }}>
			<Text>{title}</Text>
		</Pressable>
	);
}

function ReaderCode({ text, id }: { text: string; id: string }) {
	const { palette } = useColors();
	const code = useCodeText();
	return (
		<View style={{ backgroundColor: palette.inset, borderRadius: 12 }}>
			<ScrollView horizontal contentContainerStyle={{ paddingVertical: 12, paddingHorizontal: 14 }}>
				<Text testID={id} allowFontScaling={allowFontScaling} selectable style={code}>
					{text}
				</Text>
			</ScrollView>
		</View>
	);
}

function CodeComparison() {
	const [index, setIndex] = useState(0);
	const [clipboard, setClipboard] = useState("Clipboard unread");
	const readId = useRef(0);
	const dimensions = useWindowDimensions();
	const fixture = CODE_FIXTURES[index];
	return (
		<View style={{ flex: 1, padding: 24, gap: 16 }}>
			<Text testID="native-probe-code-case">Code case {index}</Text>
			<Text testID="native-probe-display-scale">{JSON.stringify(dimensions)}</Text>
			<Text>Unchanged Reader RN Text</Text>
			<ReaderCode text={fixture.text} id="native-probe-code-reference" />
			<Text>Comparison slot, RN Text before amendment implementation</Text>
			<ReaderCode text={fixture.text} id="native-probe-code-candidate" />
			<Text testID="native-probe-clipboard" accessibilityValue={{ text: clipboard }}>{clipboard}</Text>
			<ProbeButton id="native-probe-read-clipboard" title="Read clipboard" onPress={() => {
				const read = ++readId.current;
				void Clipboard.getStringAsync().then((text) => setClipboard(JSON.stringify({ read, text })));
			}} />
			<ProbeButton id="native-probe-block-copy" title="Whole block Copy" onPress={() => {
				void Clipboard.setStringAsync(fixture.text);
			}} />
			<ProbeButton id="native-probe-next-case" title="Next code case" onPress={() => setIndex((index + 1) % CODE_FIXTURES.length)} />
		</View>
	);
}

function DocumentAnnotationsProbe() {
	const [menuEvent, setMenuEvent] = useState("No selection-menu event");
	const [comparison, setComparison] = useState(false);
	const list = useRef<FlatList>(null);
	return (
		<SafeAreaView style={{ flex: 1, backgroundColor: "white" }}>
			<Text testID="native-probe-ready">Document annotations qualification</Text>
			<Text testID="native-probe-menu-event" accessibilityValue={{ text: menuEvent }}>
				{menuEvent}
			</Text>
			{comparison ? <CodeComparison /> : <FlatList
				ref={list}
				data={ROWS}
				keyExtractor={(row) => row.id}
				contentContainerStyle={{ padding: 24, gap: 24 }}
				renderItem={({ item }) => (
					<View testID={`native-probe-${item.id}`} collapsable={false}>
						{item.id === "code" ? <ReaderCode text="same()" id="native-probe-reader-code-text" /> : <EnrichedMarkdownText
							markdown={RICH_SOURCE.slice(item.start, item.end)}
							flavor="github"
							selectable
							allowFontScaling
							enableTaskListItemToggle={false}
							streamingAnimation={false}
							markdownStyle={{ paragraph: { fontSize: 20, lineHeight: 28, color: "#000000" } }}
							contextMenuItems={[
								{
									text: "Observe selection",
									onPress: ({ text, selection }) => {
										// Existing event coordinates are rendered, row-local offsets.
										// Never label them original source or gesture completion.
										setMenuEvent(JSON.stringify({ row: item.id, text, renderedRange: selection }));
									},
								},
							]}
						/>}
					</View>
				)}
			/>}
			<View style={{ flexDirection: "row" }}>
				<ProbeButton id="native-probe-show-code" title="Show table and code" onPress={() => list.current?.scrollToEnd({ animated: false })} />
				<ProbeButton id="native-probe-code-comparison" title={comparison ? "Rich document" : "Code comparison"} onPress={() => setComparison(!comparison)} />
			</View>
		</SafeAreaView>
	);
}

registerRootComponent(DocumentAnnotationsProbe);
