// Hub and New session keep UIKit's navigation layout and system bar buttons.
import type { NativeStackNavigationOptions } from "@react-navigation/native-stack";
import { useMemo } from "react";
import { useSystemGlass } from "../design/systemGlass";
import { useHeaderTitleStyle } from "../headerText";
import { useColors } from "../ui";

export function useSheetStackOptions(): NativeStackNavigationOptions {
	const { palette } = useColors();
	const glass = useSystemGlass();
	const headerTitleStyle = useHeaderTitleStyle(palette.inkHi);
	// The navigator takes these on every render: they keep their identity
	// until the colors or the text size change.
	return useMemo(
		() => ({
			headerStyle: { backgroundColor: glass ? "transparent" : palette.canvas },
			headerTintColor: palette.accentInk,
			headerTitleStyle,
			headerShadowVisible: false,
			headerBackButtonDisplayMode: "minimal",
			contentStyle: { backgroundColor: palette.canvas },
		}),
		[palette, headerTitleStyle, glass],
	);
}
