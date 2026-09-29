// The Board's Continue reading row (spec 7.1, ruling 20): the document you
// left partway through in the last two hours, one tap from where you were.
// A flat row like the notices, with no box.
import { SymbolView } from "expo-symbols";
import { Pressable, Text, View } from "react-native";
import type { ContinueReading } from "../reader/documentMemory";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { useReadingFace } from "../display/displayContext";

export function ContinueReadingRow({
	trail,
	onOpen,
}: {
	trail: ContinueReading;
	onOpen(trail: ContinueReading): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const face = useReadingFace();
	const percent = Math.round(trail.progress * 100);
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`Continue reading, ${percent} percent, ${trail.title}`}
			onPress={() => onOpen(trail)}
			style={({ pressed }) => ({
				minHeight: 44,
				flexDirection: "row",
				alignItems: "center",
				columnGap: 10,
				padding: 16,
				backgroundColor: pressed ? palette.pressed : palette.page,
			})}
		>
			<View style={{ width: 28, alignItems: "center" }}>
				<SymbolView name="doc.text" size={18} tintColor={palette.accentInk} />
			</View>
			<View style={{ flex: 1, minWidth: 0, gap: 2 }}>
				<Text
					allowFontScaling={allowFontScaling}
					style={{ fontSize: 12 * scale, lineHeight: 16 * scale, fontWeight: "600", color: palette.inkMid }}
				>
					{`Continue reading · ${percent}%`}
				</Text>
				<Text
					allowFontScaling={allowFontScaling}
					numberOfLines={1}
					ellipsizeMode="tail"
					style={{
						...face("semibold"),
						fontSize: 15 * scale,
						lineHeight: 19 * scale,
						color: palette.inkHi,
					}}
				>
					{trail.title}
				</Text>
			</View>
			<SymbolView name="chevron.right" size={13 * scale} tintColor={palette.inkLow} />
		</Pressable>
	);
}
