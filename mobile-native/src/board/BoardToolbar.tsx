import type { ConnectionState } from "@evener/appwire-client";
import { SymbolView } from "expo-symbols";
import { Platform, Pressable, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { useColors, useTextScale } from "../ui";
import { useConnectionStatusText } from "./connectionStatus";

/** The Board's bottom toolbar (spec 7.1): the connection status in the
 * middle, and New session on the trailing side. Select arrives in PR 4. */
export function BoardToolbar({
	state,
	fatal,
	newSessionDisabled,
	onNewSession,
}: {
	state: ConnectionState;
	fatal: boolean;
	newSessionDisabled: boolean;
	onNewSession: () => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const { bottom } = useSafeAreaInsets();
	const status = useConnectionStatusText(state, fatal);
	return (
		<View
			style={{
				paddingBottom: bottom,
				borderTopWidth: 0.5,
				borderColor: palette.edge,
				backgroundColor: palette.page,
			}}
		>
			<View style={{ height: 50, flexDirection: "row", alignItems: "center", paddingHorizontal: 8 }}>
				<View style={{ flex: 1 }} />
				{status ? (
					<Text
						allowFontScaling={Platform.OS !== "ios"}
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
			</View>
		</View>
	);
}
