// "↓ 3 new" (spec 8.2): new content arrived below while you read above the
// end. It never moves what you read; tapping it scrolls to the end.
import { Platform, Pressable, Text } from "react-native";
import { useColors, useTextScale } from "../ui";

export function NewContentPill({ count, onPress }: { count: number; onPress: () => void }) {
	const { palette } = useColors();
	const scale = useTextScale();
	if (count <= 0) return null;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`${count} new below, scroll to the end`}
			onPress={onPress}
			style={{
				minHeight: 44,
				justifyContent: "center",
				paddingHorizontal: 16,
				borderRadius: 999,
				borderWidth: 1,
				borderColor: palette.edgeStrong,
				backgroundColor: palette.surface,
				shadowColor: "#000",
				shadowOpacity: 0.12,
				shadowRadius: 8,
				shadowOffset: { width: 0, height: 2 },
				elevation: 3,
			}}
		>
			<Text
				allowFontScaling={Platform.OS !== "ios"}
				style={{ fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.accentInk, fontVariant: ["tabular-nums"] }}
			>{`↓ ${count} new`}</Text>
		</Pressable>
	);
}
