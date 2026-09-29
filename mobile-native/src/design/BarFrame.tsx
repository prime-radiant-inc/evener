// The bar under a screen's content, above the home indicator: the Board's
// toolbar (spec 7.1) and the session's bottom bar (spec 8.1). Full width, a
// hairline on top, and a fill painted down to the screen's edge, with room
// for the home indicator while the keyboard is down. With the keyboard up the
// bar rides on the keyboard, which covers the indicator, so that room would
// only float the bar's content above it.
//
// The fill is the system's Liquid Glass (spec 16.3) where the device has it
// (iOS 26 and later), and the opaque page color elsewhere and while Reduce
// Transparency is on.
import { GlassView, isGlassEffectAPIAvailable } from "expo-glass-effect";
import type { ReactNode } from "react";
import { type StyleProp, useColorScheme, View, type ViewStyle } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { useReduceTransparency } from "../reduceMotion";
import { useColors } from "../ui";
import { useKeyboardShown } from "../useKeyboardShown";

export function BarFrame({
	children,
	style,
	testID,
}: {
	children: ReactNode;
	/** A screen's own layout for the bar (its cap, its top padding). The
	 * home indicator's room, the hairline and the fill are the frame's. */
	style?: StyleProp<ViewStyle>;
	testID?: string;
}) {
	const { palette } = useColors();
	const scheme = useColorScheme() === "dark" ? "dark" : "light";
	const { bottom } = useSafeAreaInsets();
	const keyboardShown = useKeyboardShown();
	const reduceTransparency = useReduceTransparency();
	const glass = isGlassEffectAPIAvailable() && !reduceTransparency;
	// The screen's layout goes first, so it can't undo what the frame owns:
	// the home indicator's room, the hairline and the fill.
	const frame: StyleProp<ViewStyle> = [
		style,
		{
			paddingBottom: keyboardShown ? 0 : bottom,
			borderTopWidth: 0.5,
			borderColor: palette.edge,
			...(glass ? {} : { backgroundColor: palette.page }),
		},
	];
	return glass ? (
		<GlassView testID={testID} glassEffectStyle="regular" colorScheme={scheme} style={frame}>
			{children}
		</GlassView>
	) : (
		<View testID={testID} style={frame}>
			{children}
		</View>
	);
}
