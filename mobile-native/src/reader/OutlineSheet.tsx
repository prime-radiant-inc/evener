// The outline (spec 10.2, ruling 26): the open document's headings, indented
// by depth. Tapping one closes the sheet and scrolls the Reader to it.
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { FlatList, Pressable, Text } from "react-native";
import type { Routes } from "../screens";
import { Sheet } from "../sheet/Sheet";
import { useSheet } from "../sheet/useSheet";
import { sheetKey, useSheetHost } from "../sheet/sheetHosts";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import type { OutlineEntry } from "./documentBlocks";
import { readerHosts } from "./readerHosts";

/** Points of indent per heading level below the first. */
const INDENT = 16;

export function OutlineSheet({ route }: NativeStackScreenProps<Routes, "OutlineSheet">) {
	const { hubId, sessionRef, path } = route.params;
	const sheet = useSheet();
	const host = useSheetHost(readerHosts, sheetKey(hubId, sessionRef, path), sheet);
	const { palette } = useColors();
	const scale = useTextScale();
	const jump = (entry: OutlineEntry) => {
		sheet.finish();
		host?.jumpTo(entry.index);
	};
	return (
		<Sheet title="Outline" done={{ onPress: () => sheet.finish() }}>
			<FlatList
				data={host?.outline ?? []}
				keyExtractor={(entry: OutlineEntry) => String(entry.index)}
				renderItem={({ item: entry }: { item: OutlineEntry }) => (
					<Pressable
						accessibilityRole="button"
						accessibilityLabel={entry.title}
						accessibilityHint="Jumps to this heading"
						onPress={() => jump(entry)}
						style={{
							minHeight: 44,
							justifyContent: "center",
							paddingVertical: 10,
							paddingRight: 16,
							paddingLeft: 16 + (Math.min(entry.depth, 6) - 1) * INDENT,
						}}
					>
						<Text
							allowFontScaling={allowFontScaling}
							numberOfLines={2}
							style={{
								color: palette.inkHi,
								fontSize: 17 * scale,
								lineHeight: 22 * scale,
								fontWeight: entry.depth === 1 ? "600" : "400",
							}}
						>
							{entry.title}
						</Text>
					</Pressable>
				)}
			/>
		</Sheet>
	);
}
