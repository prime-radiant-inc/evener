// The stack inside a modal sheet that holds its own pages (the Hub and New
// session; ruling 1): a canvas-colored header with no hairline, an accent Back
// with no title beside it, and the canvas behind every page. The title is
// Headline (spec 16.2): semibold 17, following Dynamic Type as the header's
// buttons do, and stopping where they stop, since the bar's height is fixed.
import type { NativeStackNavigationOptions } from "@react-navigation/native-stack";
import { useColors } from "../ui";
import { useHeaderTextScale } from "./HeaderButton";

export function useSheetStackOptions(): NativeStackNavigationOptions {
	const { palette } = useColors();
	const scale = useHeaderTextScale();
	return {
		headerStyle: { backgroundColor: palette.canvas },
		headerTintColor: palette.accentInk,
		headerTitleStyle: { color: palette.inkHi, fontSize: 17 * scale, fontWeight: "600" },
		headerShadowVisible: false,
		headerBackButtonDisplayMode: "minimal",
		contentStyle: { backgroundColor: palette.canvas },
	};
}
