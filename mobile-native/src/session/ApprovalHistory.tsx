// A sandbox approval you answered earlier (spec 8.2, "Approval (history)"):
// "Allowed: write /path" or "Denied: read /path" in ink-mid with the approval
// mark, and how long ago. The mark takes the row's ink, not the dock's amber:
// amber is for an approval still waiting on you, and while one waits the dock
// is the approval, so the transcript never shows the live one this way.
import { SymbolView } from "expo-symbols";
import { Text, View } from "react-native";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { spokenDuration, timeAgo, wrapAfterSlashes } from "./format";
import { useMinuteClock } from "./minuteClock";

export function ApprovalHistory({ text, decidedAtMs }: { text: string; decidedAtMs: number | undefined }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const now = useMinuteClock();
	const age = decidedAtMs === undefined ? null : now - decidedAtMs;
	const shown = age === null ? text : `${text} · ${timeAgo(age)}`;
	const spoken = age === null ? text : `${text}, ${timeAgo(age, spokenDuration)}`;
	return (
		<View accessible accessibilityLabel={spoken} style={{ flexDirection: "row", alignItems: "flex-start", gap: 8 }}>
			<SymbolView name="hand.raised.circle.fill" tintColor={palette.inkMid} size={20 * scale} />
			<Text
				allowFontScaling={allowFontScaling}
				style={{ flex: 1, fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.inkMid }}
			>
				{wrapAfterSlashes(shown)}
			</Text>
		</View>
	);
}
