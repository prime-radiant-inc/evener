// The Next capsule (spec 8.3): it floats above the bottom of the Session
// while another session needs you. A tap opens that session; touch and hold
// lists every session that needs you.
import type { NavigationSessionSummary } from "@evener/appwire-client";
import { SymbolView } from "expo-symbols";
import { Platform, Pressable, Text, useWindowDimensions } from "react-native";
import { useColors, useTextScale } from "../ui";

export function NextCapsule({
	target,
	onOpen,
	onHold,
}: {
	target: NavigationSessionSummary;
	onOpen: () => void;
	onHold: () => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const { width } = useWindowDimensions();
	const text = { fontSize: 15 * scale, lineHeight: 20 * scale };
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`Next, ${target.title}`}
			onPress={onOpen}
			onLongPress={onHold}
			style={({ pressed }) => ({
				minHeight: 44,
				flexDirection: "row",
				alignItems: "center",
				gap: 6,
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
				opacity: pressed ? 0.6 : 1,
			})}
		>
			<Text allowFontScaling={Platform.OS !== "ios"} style={{ ...text, color: palette.accentInk, fontWeight: "600" }}>
				Next
			</Text>
			<Text
				allowFontScaling={Platform.OS !== "ios"}
				numberOfLines={1}
				ellipsizeMode="tail"
				style={{ ...text, color: palette.inkHi, maxWidth: width * 0.6 }}
			>
				{target.title}
			</Text>
			<SymbolView name="chevron.right" tintColor={palette.inkLow} size={12 * scale} />
		</Pressable>
	);
}
