// The stack inside a modal sheet that holds its own pages (the Hub and New
// session; ruling 1): a canvas-colored header with no hairline, an accent Back
// with no title beside it, and the canvas behind every page.
import type { NativeStackNavigationOptions } from "@react-navigation/native-stack";
import type { Palette } from "../design/tokens";

export function sheetStackOptions(palette: Palette): NativeStackNavigationOptions {
	return {
		headerStyle: { backgroundColor: palette.canvas },
		headerTintColor: palette.accentInk,
		headerTitleStyle: { color: palette.inkHi },
		headerShadowVisible: false,
		headerBackButtonDisplayMode: "minimal",
		contentStyle: { backgroundColor: palette.canvas },
	};
}
