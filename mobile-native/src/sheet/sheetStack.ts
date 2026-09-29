// The stack inside a modal sheet that holds its own pages (the Hub and New
// session; ruling 1): a canvas-colored header with no hairline, an accent Back
// with no title beside it, a Headline title (useHeaderTitleStyle), and the
// canvas behind every page.
import type { NativeStackNavigationOptions } from "@react-navigation/native-stack";
import { useHeaderTitleStyle } from "../headerText";
import { useColors } from "../ui";

export function useSheetStackOptions(): NativeStackNavigationOptions {
	const { palette } = useColors();
	const headerTitleStyle = useHeaderTitleStyle(palette.inkHi);
	return {
		headerStyle: { backgroundColor: palette.canvas },
		headerTintColor: palette.accentInk,
		headerTitleStyle,
		headerShadowVisible: false,
		headerBackButtonDisplayMode: "minimal",
		contentStyle: { backgroundColor: palette.canvas },
	};
}
