// A sandbox approval you answered earlier (spec 8.2, "Approval (history)"):
// "Allowed: write /path" or "Denied: read /path", and how long ago, drawn like
// question history with the amber left rule. While an approval is still open
// the dock is the approval, so the transcript never shows the live one this
// way.
import { Text, View } from "react-native";
import { useReadingFace } from "../display/displayContext";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { compactDuration, spokenDuration } from "./format";
import { useMinuteClock } from "./minuteClock";

export function ApprovalHistory({ text, decidedAt }: { text: string; decidedAt: string | undefined }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const face = useReadingFace();
	const now = useMinuteClock();
	const decided = Date.parse(decidedAt ?? "");
	const age = Number.isNaN(decided) ? null : Math.max(0, now - decided);
	const label = age === null ? text : `${text}, ${spokenDuration(age)} ago`;
	return (
		<View
			accessible
			accessibilityLabel={label}
			style={{ borderLeftWidth: 2, borderLeftColor: palette.attention, paddingLeft: 12, gap: 6 }}
		>
			<Text
				allowFontScaling={allowFontScaling}
				style={{ ...face.regular, fontSize: 15 * scale, lineHeight: 21 * scale, color: palette.prose }}
			>
				{text}
			</Text>
			{age === null ? null : (
				<Text
					allowFontScaling={allowFontScaling}
					style={{ fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkMid }}
				>
					{`${compactDuration(age)} ago`}
				</Text>
			)}
		</View>
	);
}
