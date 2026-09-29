// Where a running subagent's composer would be (spec 9, ruling 30): the hub
// takes no message for a running subagent, so its screen offers the one
// thing you can do about it, stop it, and a way back to its coordinator.
// The two stack, each the bar's full width: side by side, "Ask coordinator
// to stop it" needs about 180pt of the 143pt half the bar leaves it even at
// the default text size (device audit N9), and every label keeps to one line.
import { SymbolView } from "expo-symbols";
import { Pressable, Text, View } from "react-native";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import type { StopOfferKind } from "./stopOffer";

export function SubagentBar({
	offer,
	onStop,
	onOpenCoordinator,
}: {
	offer: StopOfferKind;
	/** A direct stop (S6) or a request of the coordinator, as `offer` says. */
	onStop(): void;
	onOpenCoordinator(): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const label = { fontSize: 15 * scale, lineHeight: 20 * scale };
	return (
		<View testID="subagent-bar" style={{ flexDirection: "column", gap: 8, padding: 12 }}>
			{offer === "stop" || offer === "ask" ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={offer === "stop" ? "Stop subagent" : "Ask coordinator to stop it"}
					accessibilityState={{ disabled: false }}
					onPress={onStop}
					style={({ pressed }) => ({
						minHeight: 44,
						flexDirection: "row",
						alignItems: "center",
						justifyContent: "center",
						gap: 6,
						paddingHorizontal: 12,
						borderRadius: 12,
						borderWidth: 0.5,
						borderColor: palette.edgeStrong,
						opacity: pressed ? 0.6 : 1,
					})}
				>
					<SymbolView name="stop.fill" size={12} tintColor={palette.inkHi} />
					<Text
						allowFontScaling={allowFontScaling}
						numberOfLines={1}
						style={{ ...label, flexShrink: 1, textAlign: "center", color: palette.inkHi }}
					>
						{offer === "stop" ? "Stop subagent" : "Ask coordinator to stop it"}
					</Text>
				</Pressable>
			) : offer === "requested" ? (
				<View style={{ minHeight: 44, alignItems: "center", justifyContent: "center" }}>
					<Text allowFontScaling={allowFontScaling} style={{ ...label, color: palette.inkMid }}>
						Stop requested
					</Text>
				</View>
			) : null}
			<Pressable
				accessibilityRole="button"
				accessibilityLabel="Open coordinator"
				accessibilityState={{ disabled: false }}
				onPress={onOpenCoordinator}
				style={({ pressed }) => ({
					minHeight: 44,
					alignItems: "center",
					justifyContent: "center",
					paddingHorizontal: 12,
					borderRadius: 12,
					backgroundColor: palette.accentFill,
					opacity: pressed ? 0.6 : 1,
				})}
			>
				<Text
					allowFontScaling={allowFontScaling}
					numberOfLines={1}
					style={{ ...label, fontWeight: "600", color: palette.onFill }}
				>
					Open coordinator
				</Text>
			</Pressable>
		</View>
	);
}
