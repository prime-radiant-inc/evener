// "↓ 3 new" (spec 8.2): new content arrived below while you read above the
// end. It never moves what you read; tapping it scrolls to the end.
import { Platform, Pressable, Text } from "react-native";
import { useColors, useTextScale } from "../ui";
import { floatingCapsule } from "./FloatingStack";

export function NewContentPill({ count, onPress }: { count: number; onPress: () => void }) {
	const { palette } = useColors();
	const scale = useTextScale();
	if (count <= 0) return null;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`${count} new below, scroll to the end`}
			onPress={onPress}
			style={{ ...floatingCapsule(palette), justifyContent: "center" }}
		>
			<Text
				allowFontScaling={Platform.OS !== "ios"}
				style={{ fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.accentInk, fontVariant: ["tabular-nums"] }}
			>{`↓ ${count} new`}</Text>
		</Pressable>
	);
}
