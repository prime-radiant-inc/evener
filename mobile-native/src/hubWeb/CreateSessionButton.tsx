import { GlassView } from "expo-glass-effect";
import { SymbolView } from "expo-symbols";
import { Pressable, StyleSheet, View } from "react-native";
import type { Palette } from "../design/tokens";

export interface ButtonFrame {
	x: number;
	y: number;
	width: number;
	height: number;
}

export function isButtonFrame(value: unknown): value is ButtonFrame {
	if (!value || typeof value !== "object") return false;
	const frame = value as Partial<ButtonFrame>;
	return (
		Number.isFinite(frame.x) &&
		Number.isFinite(frame.y) &&
		typeof frame.width === "number" &&
		Number.isFinite(frame.width) &&
		frame.width > 0 &&
		typeof frame.height === "number" &&
		Number.isFinite(frame.height) &&
		frame.height > 0
	);
}

export function CreateSessionButton({
	frame,
	palette,
	glass,
	onPress,
}: {
	frame: ButtonFrame;
	palette: Palette;
	glass: boolean;
	onPress(): void;
}) {
	const { x, y, width, height } = frame;
	const shape = { borderRadius: Math.min(width, height) / 2 };
	const control = (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel="New session"
			accessibilityShowsLargeContentViewer
			testID="workspace-new-session"
			hitSlop={Math.max(0, (44 - Math.min(width, height)) / 2)}
			onPress={onPress}
			style={({ pressed }) => [styles.button, { opacity: pressed ? 0.6 : 1 }]}
		>
			<SymbolView name="plus" size={22} weight="semibold" tintColor={palette.onFill} />
		</Pressable>
	);
	return (
		<View style={{ position: "absolute", left: x, top: y, width, height }}>
			{glass ? (
				<GlassView
					isInteractive
					glassEffectStyle="regular"
					colorScheme={palette.scheme}
					tintColor={palette.accentFill}
					style={[shape, styles.fill]}
				>
					{control}
				</GlassView>
			) : (
				<View style={[shape, styles.fill, { backgroundColor: palette.accentFill }]}>{control}</View>
			)}
		</View>
	);
}

const styles = StyleSheet.create({
	fill: { flex: 1 },
	button: { flex: 1, alignItems: "center", justifyContent: "center" },
});
