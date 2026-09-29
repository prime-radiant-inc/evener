// A sheet's header action (Done, Cancel, Save): accent ink, semibold when it
// finishes the page, a 44pt target that dims while pressed and reads in ink-low
// while it can't run, following Dynamic Type on iOS up to xxxLarge. Our design
// stops it growing at the accessibility sizes and keeps it to one line, so a
// word never breaks across the header; a long press shows the whole label in
// the Large Content Viewer, as Apple asks of text that stops growing.
import { Pressable, Text } from "react-native";
import { allowFontScaling, useColors, useTextScale } from "../ui";

/** Body at xxxLarge, the largest size before the accessibility sizes: 23pt
 * over the default 17 (Apple's Dynamic Type sizes). */
const XXXL_SCALE = 23 / 17;

/** How much a sheet header's text grows, its buttons and its title alike:
 * with Dynamic Type, up to xxxLarge. */
export function useHeaderTextScale(): number {
	return Math.min(useTextScale(), XXXL_SCALE);
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
			accessibilityShowsLargeContentViewer
			accessibilityLargeContentTitle={label}
			accessibilityState={busy ? { disabled, busy } : { disabled }}
			disabled={disabled}
			hitSlop={8}
			onPress={onPress}
			style={({ pressed }) => ({
				minHeight: 44,
				minWidth: 44,
				// A long label truncates inside its slot.
				maxWidth: "100%",
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
					flexShrink: 1,
				}}
			>
				{label}
			</Text>
		</Pressable>
	);
}
