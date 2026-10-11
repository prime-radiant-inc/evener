// The stack inside a modal sheet that holds its own pages (the Hub and New
// session; ruling 1): a canvas-colored header with no hairline, an accent Back
// with no title beside it, a Headline title (useHeaderTitleStyle), and the
// canvas behind every page.
import type { NativeStackNavigationOptions } from "@react-navigation/native-stack";
import { useMemo } from "react";
import { useHeaderTitleStyle } from "../headerText";
import { useColors } from "../ui";

export function useSheetStackOptions(): NativeStackNavigationOptions {
	const { palette } = useColors();
	const headerTitleStyle = useHeaderTitleStyle(palette.inkHi);
	// The navigator takes these on every render: they keep their identity
	// until the colors or the text size change.
	return useMemo(
		() => ({
			headerStyle: { backgroundColor: palette.canvas },
			headerTintColor: palette.accentInk,
			headerTitleStyle,
			headerShadowVisible: false,
			headerBackButtonDisplayMode: "minimal",
			contentStyle: { backgroundColor: palette.canvas },
		}),
		[palette, headerTitleStyle],
	);
}
