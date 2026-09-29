// A sheet's header action (Done, Cancel, Save): accent ink, semibold when it
// finishes the page, a 44pt target that dims while pressed and reads in ink-low
// while it can't run, following Dynamic Type on iOS.
import { Platform, Pressable, Text, useWindowDimensions } from "react-native";
import { allowFontScaling, useColors } from "../ui";

export function HeaderButton({
	label,
	strong = false,
	disabled = false,
	busy = false,
	onPress,
}: {
	label: string;
	strong?: boolean;
	disabled?: boolean;
	/** Its action is running, such as a save in flight. */
	busy?: boolean;
	onPress(): void;
}) {
	const { palette } = useColors();
	const { fontScale } = useWindowDimensions();
	const scale = Platform.OS === "ios" ? fontScale : 1;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			accessibilityState={busy ? { disabled, busy } : { disabled }}
			disabled={disabled}
			hitSlop={8}
			onPress={onPress}
			style={({ pressed }) => ({
				minHeight: 44,
				minWidth: 44,
				justifyContent: "center",
				paddingHorizontal: 8,
				opacity: pressed && !disabled ? 0.6 : 1,
			})}
		>
			<Text
				allowFontScaling={allowFontScaling}
				style={{
					color: disabled ? palette.inkLow : palette.accentInk,
					fontSize: 17 * scale,
					lineHeight: 24 * scale,
					fontWeight: strong ? "600" : "400",
				}}
			>
				{label}
			</Text>
		</Pressable>
	);
}
