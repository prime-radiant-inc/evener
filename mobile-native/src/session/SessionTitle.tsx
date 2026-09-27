// The Session's nav bar title (spec 8.1): the session's name over its state
// line, one button that opens the session's info.
import { SymbolView } from "expo-symbols";
import { Platform, Pressable, Text, View } from "react-native";
import { markFor } from "../board/StateMark";
import { useColors, useTextScale } from "../ui";
import type { SessionStateLine } from "./sessionState";

export function SessionTitle({
	title,
	line,
	onPress,
}: {
	title: string;
	line: SessionStateLine;
	onPress: () => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const mark = markFor(line.state, false);
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`${title}, ${line.text}, session info`}
			onPress={onPress}
			style={({ pressed }) => ({
				minHeight: 44,
				alignItems: "center",
				justifyContent: "center",
				opacity: pressed ? 0.6 : 1,
			})}
		>
			<Text
				allowFontScaling={Platform.OS !== "ios"}
				numberOfLines={1}
				ellipsizeMode="tail"
				style={{ color: palette.inkHi, fontSize: 15 * scale, fontWeight: "600" }}
			>
				{title}
			</Text>
			<View style={{ flexDirection: "row", alignItems: "center", gap: 4 }}>
				{mark && mark !== "meter" ? (
					<SymbolView name={mark.name} tintColor={palette[mark.tint]} size={12 * scale} />
				) : null}
				<Text
					allowFontScaling={Platform.OS !== "ios"}
					numberOfLines={1}
					style={{
						color: palette.inkMid,
						fontSize: 13 * scale,
						lineHeight: 18 * scale,
						fontVariant: ["tabular-nums"],
					}}
				>
					{line.text}
				</Text>
				<SymbolView name="chevron.right" tintColor={palette.inkLow} size={9 * scale} />
			</View>
		</Pressable>
	);
}
