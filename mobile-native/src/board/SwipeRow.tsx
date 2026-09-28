// A row that swipes (spec 7.3): a full swipe right does its leading action
// (Archive on the Board), and a swipe left reveals its trailing actions
// (Stop, Pin, More). It takes any children and actions, so phase 3's queued
// messages reuse it. A swipe that begins in the screen's left 24 points never
// acts, by two guards. hitSlop takes the band out of the row's pan gesture
// (react-native-gesture-handler 2.32 applies it to the Pan, not the view), so
// a drag there never moves the row and the system's back gesture keeps it; a
// tap there still reaches the row. And anything a swipe from the band opens
// is closed without acting. hitSlop measures the band from the row's own left
// edge, so a row that doesn't start at the screen's edge still has the
// start-x guard, which reads window points.
import { type SFSymbol, SymbolView } from "expo-symbols";
import { type ReactNode, useRef } from "react";
import {
	type AccessibilityActionEvent,
	Pressable,
	Text,
	useWindowDimensions,
	View,
} from "react-native";
import ReanimatedSwipeable, {
	SwipeDirection,
	type SwipeableMethods,
} from "react-native-gesture-handler/ReanimatedSwipeable";
import { useColors } from "../ui";
import { EDGE_ZONE_PT, startsInEdgeZone } from "./swipeEdge";

export interface SwipeAction {
	key: string;
	label: string;
	symbol: SFSymbol;
	fill: "inkHi" | "inkMid" | "inkLow";
	run(): void;
}

export interface SwipeRowProps {
	/** Done by a full swipe right: past half the row. */
	leading?: SwipeAction;
	/** Revealed by a swipe left, each a button. */
	trailing?: readonly SwipeAction[];
	/** True from a swipe's first drag until the row is closed again. */
	onActiveChange?(active: boolean): void;
	children: ReactNode;
}

const ACTION_WIDTH = 76;
/** ReanimatedSwipeable names the swipe, not the panel: a swipe to the right
 * opens the left (leading) panel (its dispatchEndEvents reports RIGHT for a
 * positive translation). */
const LEADING_OPENED = SwipeDirection.RIGHT;

/** The row's actions for VoiceOver, spread on the row's accessible element. */
export function swipeAccessibility(leading: SwipeAction | undefined, trailing: readonly SwipeAction[]) {
	const actions = [...(leading ? [leading] : []), ...trailing];
	return {
		accessibilityActions: actions.map((action) => ({ name: action.key, label: action.label })),
		onAccessibilityAction: (event: AccessibilityActionEvent) =>
			actions.find((action) => action.key === event.nativeEvent.actionName)?.run(),
	};
}

export function SwipeRow({ leading, trailing = [], onActiveChange, children }: SwipeRowProps) {
	const { palette } = useColors();
	const { width } = useWindowDimensions();
	const swipeable = useRef<SwipeableMethods>(null);
	// Where the touch that may become a swipe began, in window points.
	const startX = useRef(Number.POSITIVE_INFINITY);
	const close = () => swipeable.current?.close();
	const button = (action: SwipeAction) => (
		<Pressable
			key={action.key}
			accessibilityRole="button"
			accessibilityLabel={action.label}
			onPress={() => {
				close();
				action.run();
			}}
			style={{
				width: ACTION_WIDTH,
				alignItems: "center",
				justifyContent: "center",
				gap: 4,
				backgroundColor: palette[action.fill],
			}}
		>
			<SymbolView name={action.symbol} tintColor={palette.page} size={20} />
			<Text style={{ color: palette.page, fontSize: 13, fontWeight: "600" }}>{action.label}</Text>
		</Pressable>
	);
	const panel = (actions: readonly SwipeAction[]) => () => (
		<View style={{ flexDirection: "row" }}>{actions.map((action) => button(action))}</View>
	);
	return (
		<ReanimatedSwipeable
			ref={swipeable}
			// Takes the left edge band out of the row's pan, so the system's back
			// gesture keeps drags that begin there (spec 7.3).
			hitSlop={{ left: -EDGE_ZONE_PT }}
			leftThreshold={width / 2}
			renderLeftActions={leading ? panel([leading]) : undefined}
			renderRightActions={trailing.length > 0 ? panel(trailing) : undefined}
			onSwipeableOpenStartDrag={() => onActiveChange?.(true)}
			onSwipeableWillOpen={() => {
				if (startsInEdgeZone(startX.current)) close();
			}}
			onSwipeableOpen={(direction) => {
				if (startsInEdgeZone(startX.current)) {
					close();
					return;
				}
				if (direction === LEADING_OPENED && leading) {
					close();
					leading.run();
				}
			}}
			onSwipeableClose={() => onActiveChange?.(false)}
		>
			<View
				testID="swipe-row-content"
				onTouchStart={(event) => {
					startX.current = event.nativeEvent.pageX;
				}}
			>
				{children}
			</View>
		</ReanimatedSwipeable>
	);
}
