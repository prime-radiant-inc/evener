// What takes the composer's place when a session can't take a message yet
// (ruling 20): one that runs an older Evener and needs a restart, or one that
// is paused. Plain text on the page with its one button beneath; no box.
import { Pressable, Text, View } from "react-native";
import { allowFontScaling, useColors, useTextScale } from "../ui";

const COPY = {
	restartNeeded: {
		text: "This session runs an older Evener. Restart it to pick up the hub's update.",
		button: "Restart session",
		busy: "Restarting…",
	},
	paused: { text: "This session is paused.", button: "Resume", busy: "Resume" },
} as const;

export function SessionNotice({
	kind,
	busy,
	disabled = false,
	onPress,
}: {
	kind: "restartNeeded" | "paused";
	busy: boolean;
	/** Nothing could act on the press, such as while the hub is away. */
	disabled?: boolean;
	onPress: () => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const copy = COPY[kind];
	const label = busy ? copy.busy : copy.button;
	const off = busy || disabled;
	return (
		<View style={{ paddingHorizontal: 16, paddingVertical: 12, gap: 12 }}>
			<Text
				allowFontScaling={allowFontScaling}
				style={{ color: palette.inkHi, fontSize: 15 * scale, lineHeight: 20 * scale }}
			>
				{copy.text}
			</Text>
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={label}
				accessibilityState={{ disabled: off, busy }}
				disabled={off}
				onPress={onPress}
				style={({ pressed }) => ({
					alignSelf: "flex-start",
					minHeight: 44,
					justifyContent: "center",
					paddingHorizontal: 16,
					borderRadius: 22,
					backgroundColor: palette.accentFill,
					opacity: off ? 0.5 : pressed ? 0.7 : 1,
				})}
			>
				<Text
					allowFontScaling={allowFontScaling}
					style={{ color: palette.onFill, fontSize: 17 * scale, lineHeight: 24 * scale, fontWeight: "600" }}
				>
					{label}
				</Text>
			</Pressable>
		</View>
	);
}
