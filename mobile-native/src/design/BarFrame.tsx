// The bar under a screen's content, above the home indicator: the Board's
// toolbar (spec 7.1) and the session's bottom bar (spec 8.1). Full width, a
// hairline on top, and the page fill painted down to the screen's edge, with
// room for the home indicator while the keyboard is down. With the keyboard
// up the bar rides on the keyboard, which covers the indicator, so that room
// would only float the bar's content above it.
import type { ReactNode } from "react";
import { type StyleProp, View, type ViewStyle } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { useColors } from "../ui";
import { useKeyboardShown } from "../useKeyboardShown";

export function BarFrame({
	children,
	style,
	testID,
}: {
	children: ReactNode;
	/** A screen's own layout for the bar (its cap, its top padding). */
	style?: StyleProp<ViewStyle>;
	testID?: string;
}) {
	const { palette } = useColors();
	const { bottom } = useSafeAreaInsets();
	const keyboardShown = useKeyboardShown();
	return (
		<View
			testID={testID}
			style={[
				{
					paddingBottom: keyboardShown ? 0 : bottom,
					borderTopWidth: 0.5,
					borderColor: palette.edge,
					backgroundColor: palette.page,
				},
				style,
			]}
		>
			{children}
		</View>
	);
}
