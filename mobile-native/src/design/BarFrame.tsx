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
import { GlassView } from "expo-glass-effect";
import type { ReactNode } from "react";
import { type LayoutChangeEvent, type StyleProp, View, type ViewStyle } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { useColors } from "../ui";
import { systemGlassAvailable, useSystemGlass } from "./systemGlass";
import { useKeyboardShown } from "../useKeyboardShown";

export function BarFrame({
	children,
	style,
	testID,
	onLayout,
}: {
	children: ReactNode;
	/** A screen's own layout for the bar (its cap, its top padding). The
	 * home indicator's room, the hairline and the fill are the frame's. */
	style?: StyleProp<ViewStyle>;
	testID?: string;
	/** How tall the bar stands, for a screen whose content runs under it. */
	onLayout?: (event: LayoutChangeEvent) => void;
}) {
	const { palette } = useColors();
	const { bottom } = useSafeAreaInsets();
	const keyboardShown = useKeyboardShown();
	const hasGlass = systemGlassAvailable();
	const glass = useSystemGlass();
	// The screen's layout goes first, so it can't undo what the frame owns:
	// the home indicator's room, the hairline and the fill.
	const frame: StyleProp<ViewStyle> = [
		style,
		{
			paddingBottom: keyboardShown ? 0 : bottom,
			borderTopWidth: 0.5,
			borderColor: palette.edge,
			backgroundColor: glass ? "transparent" : palette.page,
		},
	];
	// Where the device has the glass, the bar is always a GlassView, its
	// effect "none" (over the opaque fill) until Reduce Transparency is known
	// to be off: a GlassView swapped for a View would remount the bar's
	// content, and the composer would lose its focus.
	return hasGlass ? (
		<GlassView
			testID={testID}
			glassEffectStyle={glass ? "regular" : "none"}
			colorScheme="auto"
			style={frame}
			onLayout={onLayout}
		>
			{children}
		</GlassView>
	) : (
		<View testID={testID} style={frame} onLayout={onLayout}>
			{children}
		</View>
	);
}
