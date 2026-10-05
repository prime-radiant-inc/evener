// A sandbox approval you answered earlier (spec 8.2, "Approval (history)"):
// "Allowed: write /path" or "Denied: read /path" in ink-mid with the approval
// mark, and how long ago. The mark takes the row's ink, not the dock's amber:
// amber is for an approval still waiting on you, and while one waits the dock
// is the approval, so the transcript never shows the live one this way.
import { SymbolView } from "expo-symbols";
import { Text, View } from "react-native";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { compactDuration, spokenDuration, wrapAfterSlashes } from "./format";
import { useMinuteClock } from "./minuteClock";

// The minute clock can lag a decision by up to a minute, and a skewed clock
// can put it ahead; either way it was just now.
function ago(ms: number, words: (ms: number) => string): string {
	return ms < 60_000 ? "just now" : `${words(ms)} ago`;
}

export function ApprovalHistory({ text, decidedAt }: { text: string; decidedAt: string | undefined }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const now = useMinuteClock();
	const decided = Date.parse(decidedAt ?? "");
	const age = Number.isNaN(decided) ? null : now - decided;
	const shown = age === null ? text : `${text} · ${ago(age, compactDuration)}`;
	const spoken = age === null ? text : `${text}, ${ago(age, spokenDuration)}`;
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
