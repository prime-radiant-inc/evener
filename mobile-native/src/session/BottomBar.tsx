// The session's bottom bar (spec 8.1; the prototype's .bottom): the full
// width under a hairline, holding the tray or a dock and the composer, and
// painting down through the home indicator's area to the screen's edge.
import type { ReactNode } from "react";
import { View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { useColors } from "../ui";
import { useKeyboardShown } from "../useKeyboardShown";

export function BottomBar({ children }: { children: ReactNode }) {
	const { palette } = useColors();
	const { bottom } = useSafeAreaInsets();
	// With the keyboard up the bar rides on the keyboard, which covers the
	// home indicator, so the indicator's room would only float the composer.
	const keyboardShown = useKeyboardShown();
	return (
		<View
			testID="session-bottom-bar"
			style={{
				flexShrink: 1,
				// The transcript keeps a fifth of the screen however much the bar holds.
				maxHeight: "80%",
				paddingTop: 8,
				paddingBottom: keyboardShown ? 0 : bottom,
				borderTopWidth: 0.5,
				borderColor: palette.edge,
				backgroundColor: palette.page,
			}}
		>
			{children}
		</View>
	);
}
