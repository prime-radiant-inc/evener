// The Session's nav bar title (spec 8.1): the session's name over its state
// line, one button that opens the session's info. A horizontal pan on it
// moves to the next or previous session in Live order (spec 6).
import { SymbolView } from "expo-symbols";
import { useEffect, useMemo, useRef } from "react";
import { Platform, Pressable, Text, View } from "react-native";
import { Gesture, GestureDetector } from "react-native-gesture-handler";
import { markFor } from "../board/StateMark";
import { useColors, useTextScale } from "../ui";
import type { SessionStateLine } from "./sessionState";
import { titleSwipeDirection } from "./titleSwipe";

export function SessionTitle({
	title,
	line,
	onPress,
	onSwipe,
}: {
	title: string;
	line: SessionStateLine;
	onPress: () => void;
	/** 1 for the next session in Live order, -1 for the previous one. */
	onSwipe: (direction: 1 | -1) => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const mark = markFor(line.state, false);
	// The gesture is built once and reaches the latest onSwipe through the
	// ref, so a re-render never hands the detector a new gesture mid-pan.
	const onSwipeRef = useRef(onSwipe);
	useEffect(() => {
		onSwipeRef.current = onSwipe;
	});
	const pan = useMemo(
		() =>
			Gesture.Pan()
				// Plain JS callbacks: the move is a navigation, never a worklet.
				.runOnJS(true)
				// It starts only once a drag has gone 10pt sideways, so a tap
				// stays the title's, and a drag that goes 10pt up or down first
				// is never a swipe. The title sits mid-bar, clear of the screen
				// edge the swipe back starts from.
				.activeOffsetX([-10, 10])
				.failOffsetY([-10, 10])
				// success is false for a pan that failed or that the system
				// cancelled, which moves nowhere.
				.onEnd((event, success) => {
					if (!success) return;
					const direction = titleSwipeDirection(event.translationX, event.velocityX);
					if (direction !== null) onSwipeRef.current(direction);
				}),
		[],
	);
	return (
		<GestureDetector gesture={pan}>
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={`${title}, ${line.text}, session info`}
				onPress={onPress}
				style={({ pressed }) => ({
					minHeight: 44,
					alignItems: "center",
					justifyContent: "center",
					opacity: pressed ? 0.6 : 1,
				})}
			>
				<Text
					allowFontScaling={Platform.OS !== "ios"}
					numberOfLines={1}
					ellipsizeMode="tail"
					style={{ color: palette.inkHi, fontSize: 15 * scale, fontWeight: "600" }}
				>
					{title}
				</Text>
				<View style={{ flexDirection: "row", alignItems: "center", gap: 4 }}>
					{/* Never "meter": the title's mark is still (spec 8.1); the check
					 * only narrows the type. */}
					{mark && mark !== "meter" ? (
						<SymbolView name={mark.name} tintColor={palette[mark.tint]} size={12 * scale} />
					) : null}
					<Text
						allowFontScaling={Platform.OS !== "ios"}
						numberOfLines={1}
						style={{
							color: palette.inkMid,
							fontSize: 13 * scale,
							lineHeight: 18 * scale,
							fontVariant: ["tabular-nums"],
						}}
					>
						{line.text}
					</Text>
					<SymbolView name="chevron.right" tintColor={palette.inkLow} size={9 * scale} />
				</View>
			</Pressable>
		</GestureDetector>
	);
}
