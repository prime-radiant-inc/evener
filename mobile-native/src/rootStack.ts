// The app's own stack (the Board, sessions and the pages pushed over them): a
// page-colored header with no hairline, an accent Back with no title beside
// it, a Headline title (useHeaderTitleStyle), and the page color behind every
// screen.
import type { NativeStackNavigationOptions } from "@react-navigation/native-stack";
import { useMemo } from "react";
import { useHeaderTitleStyle } from "./headerText";
import { useColors } from "./ui";

export function useRootStackOptions(): NativeStackNavigationOptions {
	const { palette } = useColors();
	const headerTitleStyle = useHeaderTitleStyle(palette.inkHi);
	// The navigator takes these on every render: they keep their identity
	// until the colors or the text size change.
	return useMemo(
		() => ({
			headerStyle: { backgroundColor: palette.page },
			headerTintColor: palette.accentInk,
			headerTitleStyle,
			headerShadowVisible: false,
			headerBackButtonDisplayMode: "minimal",
			contentStyle: { backgroundColor: palette.page },
		}),
		[palette, headerTitleStyle],
	);
}
