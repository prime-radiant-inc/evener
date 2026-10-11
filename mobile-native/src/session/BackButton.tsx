// The Session's Back (spec 13.2): the chevron, then the amber count of the
// other sessions that need you, so you know before leaving whether anything
// is waiting.
import { SymbolView } from "expo-symbols";
import { Pressable, Text } from "react-native";
import { allowFontScaling, useColors, useTextScale } from "../ui";

function backLabel(count: number): string {
	if (count <= 0) return "Back";
	return `Back, ${count} ${count === 1 ? "other needs" : "others need"} you`;
}

/** `label`, when a screen gives one, replaces what VoiceOver reads: the
 * Reader's "Back, 2 new while you read". */
export function BackButton({ count, label, onPress }: { count: number; label?: string; onPress: () => void }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label ?? backLabel(count)}
			onPress={onPress}
			hitSlop={8}
			style={({ pressed }) => ({
				minWidth: 44,
				minHeight: 44,
				flexDirection: "row",
				alignItems: "center",
				gap: 4,
				opacity: pressed ? 0.6 : 1,
			})}
		>
			<SymbolView name="chevron.left" tintColor={palette.accentInk} size={20 * scale} weight="semibold" />
			{count > 0 ? (
				<Text
					allowFontScaling={allowFontScaling}
					style={{
						color: palette.attentionInk,
						fontSize: 17 * scale,
						lineHeight: 24 * scale,
						fontWeight: "600",
						fontVariant: ["tabular-nums"],
					}}
				>
					{String(count)}
				</Text>
			) : null}
		</Pressable>
	);
}
