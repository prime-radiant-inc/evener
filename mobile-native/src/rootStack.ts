// The app's own stack (the Board, sessions and the pages pushed over them): a
// page-colored header with no hairline, an accent Back with no title beside
// it, a Headline title (useHeaderTitleStyle), and the page color behind every
// screen.
import type { NativeStackNavigationOptions } from "@react-navigation/native-stack";
import { useHeaderTitleStyle } from "./headerText";
import { useColors } from "./ui";

export function useRootStackOptions(): NativeStackNavigationOptions {
	const colors = useColors();
	const headerTitleStyle = useHeaderTitleStyle(colors.text);
	return {
		headerStyle: { backgroundColor: colors.background },
		headerTintColor: colors.accent,
		headerTitleStyle,
		headerShadowVisible: false,
		headerBackButtonDisplayMode: "minimal",
		contentStyle: { backgroundColor: colors.background },
	};
}
