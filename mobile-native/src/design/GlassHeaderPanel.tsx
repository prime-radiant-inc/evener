// The panel under a screen's nav bar (spec 16.3). Where the bar is the
// system's glass (`glassTop`, the bar's height), one glass runs from the
// screen's top under the bar and the rows beneath it, the iOS pattern for a
// bar with a search field or segmented control; the panel leaves the bar its
// room, and its rows draw clear on the glass (headerRowFill). Off the glass
// it is a plain container for its rows. The screen places it (`style`) and
// reads its height (`onLayout`) to keep its content clear of it.
import { GlassView } from "expo-glass-effect";
import type { ReactNode } from "react";
import { Animated, type LayoutChangeEvent, type StyleProp, View, type ViewStyle } from "react-native";

export function GlassHeaderPanel({
	glassTop,
	slideOffset,
	onLayout,
	style,
	testID,
	children,
}: {
	glassTop?: number;
	/** How far the rows have slid up out of view: the glass moves with them,
	 * so its lower edge rises with theirs toward the bar's. */
	slideOffset?: Animated.Value;
	onLayout?: (event: LayoutChangeEvent) => void;
	style?: StyleProp<ViewStyle>;
	testID?: string;
	children?: ReactNode;
}) {
	const onGlass = glassTop !== undefined;
	return (
		// box-none lets touches on what the panel doesn't cover reach the screen.
		<View testID={testID} pointerEvents="box-none" style={style} onLayout={onLayout}>
			{onGlass ? (
				<Animated.View
					pointerEvents="none"
					style={{
						position: "absolute",
						top: 0,
						left: 0,
						right: 0,
						bottom: 0,
						transform: slideOffset ? [{ translateY: slideOffset }] : undefined,
					}}
				>
					<GlassView glassEffectStyle="regular" colorScheme="auto" style={{ flex: 1 }} />
				</Animated.View>
			) : null}
			{onGlass ? <View testID="nav-bar-room" pointerEvents="none" style={{ height: glassTop }} /> : null}
			{children}
		</View>
	);
}
