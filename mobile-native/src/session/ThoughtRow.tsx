// A settled thought (spec 8.2, "Thinking"): "Thought for 12s ›", folded; it
// opens to the thought in the serif. The live thought is the live status line's
// line (ruling 10), so it never reaches the transcript. A redacted critical
// thought passes `label` so the projector's neutral summary ("Thought failed",
// "Thought not shown") stays the collapsed label (ruling 2026-11-01).
import { Pressable, Text } from "react-native";
import { compactDuration } from "@evener/appwire-client";
import { FoldChevron } from "../board/BoardRow";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { useReadingFace } from "../display/displayContext";

export function ThoughtRow({
	durationMs,
	text,
	label: labelOverride,
	expanded,
	onToggle,
}: {
	durationMs: number | undefined;
	text: string | undefined;
	label?: string;
	expanded: boolean;
	onToggle: () => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const face = useReadingFace();
	const label = labelOverride ?? (durationMs === undefined ? "Thought" : `Thought for ${compactDuration(durationMs)}`);
	return (
		<>
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={label}
				accessibilityState={{ expanded }}
				onPress={onToggle}
				style={{ minHeight: 44, justifyContent: "center", flexDirection: "row", alignItems: "center", gap: 4 }}
			>
				<Text
					allowFontScaling={allowFontScaling}
					style={{ fontSize: 14 * scale, lineHeight: 19 * scale, color: palette.inkMid, fontVariant: ["tabular-nums"] }}
				>
					{label}
				</Text>
				<FoldChevron folded={!expanded} />
			</Pressable>
			{expanded && text ? (
				<Text
					selectable
					allowFontScaling={allowFontScaling}
					style={{ ...face.regular, fontSize: 15 * scale, lineHeight: 21 * scale, color: palette.inkMid }}
				>
					{text}
				</Text>
			) : null}
		</>
	);
}
