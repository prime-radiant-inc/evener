// A subagent in the transcript (spec 8.2): the web's shape, a 2pt left rail
// in the state's hue with no card or pill. The title and the state's time sit
// on one line, the latest activity beneath. Tapping opens its own transcript.
import { Platform, Pressable, Text, View } from "react-native";
import { useColors, useTextScale } from "../ui";
import type { SubagentLine } from "./subagentLine";

const allowFontScaling = Platform.OS !== "ios";

export function SubagentRow({ line, onOpen }: { line: SubagentLine; onOpen?: (ref: string, title: string) => void }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const rail = line.state === "running" ? palette.alive : line.state === "failed" ? palette.danger : palette.edgeStrong;
	const body = (
		<View style={{ borderLeftWidth: 2, borderLeftColor: rail, paddingLeft: 12, paddingVertical: 4, gap: 2 }}>
			<View style={{ flexDirection: "row", alignItems: "baseline", gap: 8 }}>
				<Text
					allowFontScaling={allowFontScaling}
					numberOfLines={1}
					style={{ flex: 1, fontWeight: "600", fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.inkHi }}
				>
					{line.title}
				</Text>
				<Text
					allowFontScaling={allowFontScaling}
					style={{
						fontSize: 13 * scale,
						lineHeight: 18 * scale,
						fontVariant: ["tabular-nums"],
						color: line.state === "failed" ? palette.dangerInk : palette.inkMid,
					}}
				>
					{line.stateText}
				</Text>
			</View>
			{line.activity ? (
				<Text allowFontScaling={allowFontScaling} style={{ fontSize: 14 * scale, lineHeight: 19 * scale, color: palette.inkMid }}>
					{line.activity}
				</Text>
			) : null}
		</View>
	);
	const { ref } = line;
	if (!ref || !onOpen) return <View accessible accessibilityLabel={`${line.title}, ${line.stateText}`}>{body}</View>;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`${line.title}, ${line.stateText}${line.activity ? `, ${line.activity}` : ""}`}
			accessibilityHint="Opens the subagent"
			onPress={() => onOpen(ref, line.title)}
		>
			{body}
		</Pressable>
	);
}
