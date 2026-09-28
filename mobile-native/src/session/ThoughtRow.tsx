// A settled thought (spec 8.2, "Thinking"): "Thought for 12s ›", folded; it
// opens to the thought in the serif. The live thought is the status tray's
// line (ruling 10), so it never reaches the transcript.
import { Pressable, Text } from "react-native";
import { fonts } from "../design/tokens";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { compactDuration } from "./format";

export function ThoughtRow({
	durationMs,
	text,
	expanded,
	onToggle,
}: {
	durationMs: number | undefined;
	text: string | undefined;
	expanded: boolean;
	onToggle: () => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const label = durationMs === undefined ? "Thought" : `Thought for ${compactDuration(durationMs)}`;
	return (
		<>
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={label}
				accessibilityState={{ expanded }}
				onPress={onToggle}
				style={{ minHeight: 44, justifyContent: "center" }}
			>
				<Text
					allowFontScaling={allowFontScaling}
					style={{ fontSize: 14 * scale, lineHeight: 19 * scale, color: palette.inkMid, fontVariant: ["tabular-nums"] }}
				>{`${label} ${expanded ? "⌄" : "›"}`}</Text>
			</Pressable>
			{expanded && text ? (
				<Text
					selectable
					allowFontScaling={allowFontScaling}
					style={{ fontFamily: fonts.serif, fontSize: 15 * scale, lineHeight: 21 * scale, color: palette.inkMid }}
				>
					{text}
				</Text>
			) : null}
		</>
	);
}
