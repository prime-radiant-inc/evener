import { GlassView } from "expo-glass-effect";
import type { ReactNode } from "react";
import { StyleSheet, View } from "react-native";
import type { Palette } from "./tokens";

export function GlassControlGroup({
	glass,
	palette,
	children,
}: {
	glass: boolean;
	palette: Palette;
	children: ReactNode;
}) {
	const shape = { flexDirection: "row", alignItems: "center", borderRadius: 24 } as const;
	return glass ? (
		<GlassView glassEffectStyle="regular" colorScheme={palette.scheme} isInteractive style={shape}>
			{children}
		</GlassView>
	) : (
		<View
			style={[
				shape,
				{ backgroundColor: palette.inset, borderColor: palette.edgeStrong, borderWidth: StyleSheet.hairlineWidth },
			]}
		>
			{children}
		</View>
	);
}
