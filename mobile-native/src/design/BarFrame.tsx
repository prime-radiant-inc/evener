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
import { type LayoutChangeEvent, type StyleProp, View, type ViewStyle } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { useReduceTransparency } from "../accessibilitySettings";
import { useColors } from "../ui";
import { useKeyboardShown } from "../useKeyboardShown";

/** Whether the device has the Liquid Glass API. expo-glass-effect reads its
 * native module once per process and checks the API itself (iOS 26 betas
 * lacked it); reading the module throws only for a binary built without it,
 * which gets the opaque fill. */
function systemGlassAvailable(): boolean {
	try {
		return isGlassEffectAPIAvailable();
	} catch {
		return false;
	}
}

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
	const reduceTransparency = useReduceTransparency();
	const glass = systemGlassAvailable() && !reduceTransparency;
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
		// The glass follows the window's appearance, which the app's own light or
		// dark choice sets (Appearance.setColorScheme), as its colors do.
		<GlassView testID={testID} glassEffectStyle="regular" style={frame} onLayout={onLayout}>
			{children}
		</GlassView>
	) : (
		<View testID={testID} style={frame} onLayout={onLayout}>
			{children}
		</View>
	);
}
