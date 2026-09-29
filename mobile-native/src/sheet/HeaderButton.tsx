// A sheet's header action (Done, Cancel, Save): accent ink, semibold when it
// finishes the page, a 44pt target that dims while pressed and reads in ink-low
// while it can't run, following Dynamic Type on iOS up to xxxLarge. Like iOS's
// own bar buttons it stops growing at the accessibility sizes and stays on one
// line, so a word never breaks across the header.
import { Platform, Pressable, Text, useWindowDimensions } from "react-native";
import { allowFontScaling, useColors } from "../ui";

/** Body at xxxLarge, the largest size before the accessibility sizes: 23pt
 * over the default 17 (Apple's Dynamic Type sizes). */
const XXXL_SCALE = 23 / 17;

/** How much a sheet header's text grows: with Dynamic Type on iOS, up to
 * xxxLarge. The header's title and its buttons share it. */
export function useHeaderTextScale(): number {
	const { fontScale } = useWindowDimensions();
	return Platform.OS === "ios" ? Math.min(fontScale, XXXL_SCALE) : 1;
}

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
	const scale = useHeaderTextScale();
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
				numberOfLines={1}
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
