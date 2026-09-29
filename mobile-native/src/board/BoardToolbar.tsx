import { SymbolView } from "expo-symbols";
import type { ReactNode } from "react";
import { Pressable, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { useConnectionStatusText } from "./connectionStatus";

/** The bar under the Board, above the home indicator: the toolbar, or the
 * select bar in its place. */
export function ToolbarFrame({ children }: { children: ReactNode }) {
	const { palette } = useColors();
	const { bottom } = useSafeAreaInsets();
	return (
		<View
			style={{
				paddingBottom: bottom,
				borderTopWidth: 0.5,
				borderColor: palette.edge,
				backgroundColor: palette.page,
			}}
		>
			<View style={{ height: 50, flexDirection: "row", alignItems: "center", paddingHorizontal: 8 }}>{children}</View>
		</View>
	);
}

/** A text button in the Board's bottom bars: 17pt accent ink, or inkLow
 * and inert while disabled. */
export function BarButton({
	label,
	disabled = false,
	onPress,
}: {
	label: string;
	disabled?: boolean;
	onPress: () => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			accessibilityState={{ disabled }}
			disabled={disabled}
			onPress={disabled ? undefined : onPress}
			style={({ pressed }) => ({
				minHeight: 44,
				paddingHorizontal: 8,
				justifyContent: "center",
				opacity: pressed ? 0.6 : 1,
			})}
		>
			<Text
				allowFontScaling={allowFontScaling}
				style={{ fontSize: 17 * scale, color: disabled ? palette.inkLow : palette.accentInk }}
			>
				{label}
			</Text>
		</Pressable>
	);
}

/** The Board's bottom toolbar (spec 7.1): Select on the leading side while
 * the Board shows a session (`onSelect`), the connection status in the
 * middle, and New session on the trailing side. */
export function BoardToolbar({
	newSessionDisabled,
	onNewSession,
	onSelect,
}: {
	newSessionDisabled: boolean;
	onNewSession: () => void;
	onSelect?: () => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const status = useConnectionStatusText();
	return (
		<ToolbarFrame>
			<View style={{ flex: 1, alignItems: "flex-start" }}>
				{onSelect ? <BarButton label="Select" onPress={onSelect} /> : null}
			</View>
			{status ? (
				<Text
					allowFontScaling={allowFontScaling}
					accessibilityLiveRegion="polite"
					style={{ fontSize: 13 * scale, color: palette.inkMid }}
				>
					{status}
				</Text>
			) : null}
			<View style={{ flex: 1, alignItems: "flex-end" }}>
				<Pressable
					accessibilityRole="button"
					accessibilityLabel="New session"
					accessibilityState={{ disabled: newSessionDisabled }}
					disabled={newSessionDisabled}
					onPress={onNewSession}
					style={({ pressed }) => ({
						width: 44,
						height: 44,
						alignItems: "center",
						justifyContent: "center",
						borderRadius: 10,
						backgroundColor: pressed ? palette.pressed : "transparent",
					})}
				>
					<SymbolView
						name="square.and.pencil"
						size={22}
						tintColor={newSessionDisabled ? palette.inkLow : palette.accentInk}
					/>
				</Pressable>
			</View>
		</ToolbarFrame>
	);
}
