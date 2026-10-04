// Qualification-only entry. Production index.ts and navigation never import it.
import { registerRootComponent } from "expo";
import { useState } from "react";
import { FlatList, SafeAreaView, Text, View } from "react-native";
import { EnrichedMarkdownText } from "react-native-enriched-markdown";

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

function DocumentAnnotationsProbe() {
	const [menuEvent, setMenuEvent] = useState("No selection-menu event");
	return (
		<SafeAreaView style={{ flex: 1, backgroundColor: "white" }}>
			<Text testID="native-probe-ready">Document annotations qualification</Text>
			<Text testID="native-probe-menu-event" accessibilityValue={{ text: menuEvent }}>
				{menuEvent}
			</Text>
			<FlatList
				data={ROWS}
				keyExtractor={(row) => row.id}
				contentContainerStyle={{ padding: 24, gap: 24 }}
				renderItem={({ item }) => (
					<View testID={`native-probe-${item.id}`} collapsable={false}>
						<EnrichedMarkdownText
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
						/>
					</View>
				)}
			/>
		</SafeAreaView>
	);
}

registerRootComponent(DocumentAnnotationsProbe);
