import type { ReactNode } from "react";
import type { TextProps } from "react-native";
import {
	Platform,
	Pressable,
	StyleSheet,
	Text,
	useColorScheme,
	useWindowDimensions,
} from "react-native";

export function useColors() {
	return useColorScheme() === "dark"
		? {
				background: "#121417",
				surface: "#23272d",
				text: "#f1f2f3",
				secondary: "#a6adb5",
				border: "#363b42",
				accent: "#9cb4ff",
				error: "#ffaaa5",
				onAccent: "#18244b",
			}
		: {
				background: "#fafaf8",
				surface: "#eeefeb",
				text: "#202326",
				secondary: "#62676d",
				border: "#d8dcd9",
				accent: "#315ad7",
				error: "#b52b25",
				onAccent: "#ffffff",
			};
}

export function Action({
	children,
	onPress,
	disabled = false,
	label,
	expanded,
	tone = "accent",
}: {
	children: string;
	onPress: () => void;
	disabled?: boolean;
	label?: string;
	expanded?: boolean;
	tone?: "accent" | "quiet" | "primary";
}) {
	const colors = useColors();
	const { fontScale } = useWindowDimensions();
	const textScale = Platform.OS === "ios" ? fontScale : 1;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label ?? children}
			accessibilityState={{ disabled, expanded }}
			disabled={disabled}
			onPress={onPress}
			style={({ pressed }) => [
				styles.action,
				tone === "primary" && {
					backgroundColor: colors.accent,
					borderRadius: 24,
					paddingHorizontal: 18,
				},
				{ opacity: disabled ? 0.4 : pressed ? 0.65 : 1 },
			]}
		>
			<Text
				allowFontScaling={Platform.OS !== "ios"}
				style={{
					color:
						tone === "primary"
							? colors.onAccent
							: tone === "quiet"
								? colors.secondary
								: colors.accent,
					// Give native measurement and drawing the same current size when
					// Dynamic Type changes while this control remains mounted.
					fontSize: 16 * textScale,
					fontWeight: tone === "quiet" ? "400" : "600",
					flexShrink: 1,
				}}
			>
				{children}
			</Text>
		</Pressable>
	);
}

export function Choice({
	label,
	selected,
	disabled,
	onPress,
}: {
	label: string;
	selected: boolean;
	disabled: boolean;
	onPress(): void;
}) {
	const colors = useColors();
	const { fontScale } = useWindowDimensions();
	const textScale = Platform.OS === "ios" ? fontScale : 1;
	return (
		<Pressable
			accessibilityRole="radio"
			accessibilityLabel={label}
			accessibilityState={{ checked: selected, disabled }}
			disabled={disabled}
			onPress={onPress}
			style={[styles.action, { opacity: disabled ? 0.4 : 1 }]}
		>
			<Text
				allowFontScaling={Platform.OS !== "ios"}
				style={{
					color: selected ? colors.accent : colors.text,
					fontSize: 16 * textScale,
					flexShrink: 1,
				}}
			>
				{selected ? "● " : "○ "}
				{label}
			</Text>
		</Pressable>
	);
}

export function Copy({
	children,
	muted = false,
	label,
	numberOfLines,
	ellipsizeMode,
}: {
	children: ReactNode;
	muted?: boolean;
	label?: string;
	numberOfLines?: number;
	ellipsizeMode?: TextProps["ellipsizeMode"];
}) {
	const colors = useColors();
	const { fontScale } = useWindowDimensions();
	const textScale = Platform.OS === "ios" ? fontScale : 1;
	return (
		<Text
			selectable
			allowFontScaling={Platform.OS !== "ios"}
			accessibilityLabel={label}
			numberOfLines={numberOfLines}
			ellipsizeMode={ellipsizeMode}
			style={{
				color: muted ? colors.secondary : colors.text,
				fontSize: (muted ? 13 : Platform.OS === "ios" ? 17 : 16) * textScale,
				lineHeight: (muted ? 19 : 25) * textScale,
			}}
		>
			{children}
		</Text>
	);
}

export function ErrorMessage({ message }: { message: string | null }) {
	const colors = useColors();
	const { fontScale } = useWindowDimensions();
	const textScale = Platform.OS === "ios" ? fontScale : 1;
	return message ? (
		<Text
			accessibilityRole="alert"
			allowFontScaling={Platform.OS !== "ios"}
			style={{ color: colors.error, padding: 12, fontSize: 16 * textScale }}
		>
			{message}
		</Text>
	) : null;
}

export const styles = StyleSheet.create({
	fill: { flex: 1 },
	padded: { padding: 16, gap: 12 },
	row: {
		flexDirection: "row",
		alignItems: "center",
		justifyContent: "space-between",
		gap: 12,
	},
	card: { borderRadius: 12, borderWidth: 1, padding: 16, gap: 8 },
	title: { fontSize: 22, fontWeight: "600" },
	input: {
		borderWidth: 1,
		borderRadius: 9,
		padding: 12,
		fontSize: 17,
		minHeight: 48,
	},
	action: {
		minWidth: Platform.OS === "android" ? 48 : 44,
		minHeight: Platform.OS === "android" ? 48 : 44,
		justifyContent: "center",
		paddingHorizontal: 8,
		paddingVertical: 8,
	},
});
