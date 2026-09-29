// The Next capsule (spec 8.3): it floats above the bottom of the Session
// while another session needs you. A tap opens that session; touch and hold
// lists every session that needs you.
import type { NavigationSessionSummary } from "@evener/appwire-client";
import { SymbolView } from "expo-symbols";
import { Pressable, Text, useWindowDimensions } from "react-native";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { floatingCapsule } from "./FloatingStack";

/** How wide the capsule may grow on a window this wide: small, at the
 * trailing edge (spec 8.3), so the leading side of the transcript stays free
 * to drag: 60% of a phone's width, and never past 320pt on a wide window. */
export function nextCapsuleMaxWidth(windowWidth: number): number {
	return Math.min(Math.round(windowWidth * 0.6), 320);
}

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
				...floatingCapsule(palette),
				maxWidth: nextCapsuleMaxWidth(width),
				flexDirection: "row",
				alignItems: "center",
				gap: 6,
				opacity: pressed ? 0.6 : 1,
			})}
		>
			<Text allowFontScaling={allowFontScaling} style={{ ...text, color: palette.accentInk, fontWeight: "600" }}>
				Next
			</Text>
			<Text
				allowFontScaling={allowFontScaling}
				numberOfLines={1}
				ellipsizeMode="tail"
				style={{ ...text, color: palette.inkHi, flexShrink: 1 }}
			>
				{target.title}
			</Text>
			<SymbolView name="chevron.right" tintColor={palette.inkLow} size={12 * scale} />
		</Pressable>
	);
}
