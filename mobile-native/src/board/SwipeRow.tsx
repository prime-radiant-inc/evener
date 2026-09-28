// A row that swipes (spec 7.3): a full swipe right does its leading action
// (Archive on the Board), and a swipe left reveals its trailing actions
// (Stop, Pin, More). A row with one destructive action instead does it on a
// full swipe left (spec 8.5, 8.8: cancel a queued message, remove a link).
// A full swipe acts only when the finger has dragged the row past half its
// width at release, as on iOS. Velocity doesn't count: ReanimatedSwipeable
// opens a row whose drag plus a share of its velocity passes the threshold,
// so a short fast flick opens it, and the row closes without acting. The
// drag is read from a pan of the row's own that recognizes alongside the
// swipeable's, since the swipeable never reports where the finger let go.
// The gate measures the finger's travel from where it touched down, so a
// swipe that begins on a row already open errs toward not acting.
// It takes any children and actions, so the Session's ghosts and links
// reuse it. A swipe that begins in the screen's left 24 points never
// acts, by two guards. hitSlop takes the band out of the row's pan gesture
// (react-native-gesture-handler 2.32 applies it to the Pan, not the view), so
// a drag there never moves the row and the system's back gesture keeps it; a
// tap there still reaches the row. And anything a swipe from the band opens
// is closed without acting. hitSlop measures the band from the row's own left
// edge, so a row that doesn't start at the screen's edge still has the
// start-x guard, which reads window points.
import { type SFSymbol, SymbolView } from "expo-symbols";
import { type ReactNode, useMemo, useRef } from "react";
import {
	type AccessibilityActionEvent,
	Pressable,
	Text,
	useWindowDimensions,
	View,
} from "react-native";
import { Gesture, GestureDetector } from "react-native-gesture-handler";
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

/** A destructive action reads as its label alone, in danger ink on the
 * danger wash. */
export type DestructiveSwipeAction = Pick<SwipeAction, "key" | "label" | "run">;

export type SwipeRowProps = {
	/** Done by a full swipe right: dragged past half the row at release.
	 * A fast flick short of half never does it. */
	leading?: SwipeAction;
	/** True from a swipe's first drag until the row is closed again. */
	onActiveChange?(active: boolean): void;
	/** The color the row sits on, painted under content that has no fill
	 * of its own, since the action panels are drawn behind the row as soon
	 * as it moves. */
	backdrop?: string;
	/** False holds the row still without unmounting its content. */
	enabled?: boolean;
	children: ReactNode;
} & (
	| {
			/** Revealed by a swipe left, each a button. */
			trailing?: readonly SwipeAction[];
			destructive?: never;
	  }
	| {
			trailing?: never;
			/** Revealed as the row drags left, and done by a full swipe left:
			 * dragged past half the row at release. A fast flick short of half
			 * never does it. */
			destructive: DestructiveSwipeAction;
	  }
);

const ACTION_WIDTH = 76;
/** How far a drag goes sideways before the row's pan, and the tracker that
 * reads its release, start: the same for both, so the tracker never runs
 * without the row. */
const SWIPE_ACTIVATION_PT = 10;
/** Takes the screen's left edge band out of a pan's hit frame, so the
 * system's back gesture keeps drags that begin there (spec 7.3). */
const EDGE_BAND_SLOP = { left: -EDGE_ZONE_PT };
/** ReanimatedSwipeable names the swipe, not the panel: a swipe to the right
 * opens the left (leading) panel (its dispatchEndEvents reports RIGHT for a
 * positive translation). */
const LEADING_OPENED = SwipeDirection.RIGHT;
const TRAILING_OPENED = SwipeDirection.LEFT;
/** The layout and label type shared by a button and the destructive panel;
 * only their colors differ. */
const ACTION_CELL = {
	width: ACTION_WIDTH,
	alignItems: "center",
	justifyContent: "center",
} as const;
const ACTION_LABEL = { fontSize: 13, fontWeight: "600" } as const;

/** The row's actions for VoiceOver, spread on the row's accessible element. */
export function swipeAccessibility(
	leading: DestructiveSwipeAction | undefined,
	trailing: readonly DestructiveSwipeAction[],
) {
	const actions = [...(leading ? [leading] : []), ...trailing];
	return {
		accessibilityActions: actions.map((action) => ({ name: action.key, label: action.label })),
		onAccessibilityAction: (event: AccessibilityActionEvent) =>
			actions.find((action) => action.key === event.nativeEvent.actionName)?.run(),
	};
}

export function SwipeRow({
	leading,
	trailing = [],
	destructive,
	onActiveChange,
	backdrop,
	enabled = true,
	children,
}: SwipeRowProps) {
	const { palette } = useColors();
	const { width } = useWindowDimensions();
	const swipeable = useRef<SwipeableMethods>(null);
	// Where the touch that may become a swipe began, in window points.
	const startX = useRef(Number.POSITIVE_INFINITY);
	// How far the finger has dragged the row, in points; positive to the
	// right.
	const dragX = useRef(0);
	// Whether the swipe the row is opening for was a full one, decided as the
	// finger let go, so a touch that lands while the row springs open can't
	// change it.
	const fullSwipe = useRef(false);
	const releaseTracker = useMemo(
		() =>
			Gesture.Pan()
				.enabled(enabled)
				// Plain JS callbacks: it only records a number.
				.runOnJS(true)
				// It activates only as the swipeable's own pan does: the same
				// sideways offset, outside the same edge band.
				.activeOffsetX([-SWIPE_ACTIVATION_PT, SWIPE_ACTIVATION_PT])
				.hitSlop(EDGE_BAND_SLOP)
				.onBegin(() => {
					dragX.current = 0;
				})
				// Kept current through the drag as well as at the end, since the
				// swipeable's will-open and this pan's end reach JS separately.
				.onUpdate((event) => {
					dragX.current = event.translationX;
				})
				.onEnd((event) => {
					dragX.current = event.translationX;
				}),
		[enabled],
	);
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
			style={{ ...ACTION_CELL, gap: 4, backgroundColor: palette[action.fill] }}
		>
			<SymbolView name={action.symbol} tintColor={palette.page} size={20} />
			<Text style={{ ...ACTION_LABEL, color: palette.page }}>{action.label}</Text>
		</Pressable>
	);
	const panel = (actions: readonly SwipeAction[]) => () => (
		<View style={{ flexDirection: "row" }}>{actions.map((action) => button(action))}</View>
	);
	// Never a button: the row never rests open on it, since a drag short of
	// the full swipe springs back.
	const destructivePanel = (action: DestructiveSwipeAction) => () => (
		<View style={{ ...ACTION_CELL, backgroundColor: palette.dangerBg }}>
			<Text style={{ ...ACTION_LABEL, color: palette.dangerInk }}>{action.label}</Text>
		</View>
	);
	return (
		<GestureDetector gesture={releaseTracker}>
			{/* The detector attaches to a native view of its own, apart from the
			 * one the swipeable's detector holds. */}
			<View collapsable={false}>
				<ReanimatedSwipeable
					ref={swipeable}
					enabled={enabled}
					simultaneousWithExternalGesture={releaseTracker}
					hitSlop={EDGE_BAND_SLOP}
					dragOffsetFromLeftEdge={SWIPE_ACTIVATION_PT}
					dragOffsetFromRightEdge={SWIPE_ACTIVATION_PT}
					leftThreshold={width / 2}
					rightThreshold={destructive ? width / 2 : undefined}
					renderLeftActions={leading ? panel([leading]) : undefined}
					renderRightActions={
						destructive ? destructivePanel(destructive) : trailing.length > 0 ? panel(trailing) : undefined
					}
					onSwipeableOpenStartDrag={() => onActiveChange?.(true)}
					// The swipeable says it will open as the finger lets go, whether
					// the drag or its velocity carried the row past the threshold.
					onSwipeableWillOpen={(direction) => {
						if (startsInEdgeZone(startX.current)) close();
						const dragged = direction === LEADING_OPENED ? dragX.current : -dragX.current;
						fullSwipe.current = dragged > width / 2;
					}}
					// The swipeable says it has opened when the open animation ends:
					// a spring of about half a second, or the next frame under Reduce
					// Motion. A new touch can land before then, so this acts on the
					// decision will-open latched, and clears it.
					onSwipeableOpen={(direction) => {
						const wasFullSwipe = fullSwipe.current;
						fullSwipe.current = false;
						if (startsInEdgeZone(startX.current)) {
							close();
							return;
						}
						const action = direction === LEADING_OPENED ? leading : direction === TRAILING_OPENED ? destructive : undefined;
						if (!action) return;
						close();
						// A flick that opened the panel short of half closes it
						// without acting.
						if (wasFullSwipe) action.run();
					}}
					onSwipeableClose={() => onActiveChange?.(false)}
				>
					<View
						testID="swipe-row-content"
						style={backdrop ? { backgroundColor: backdrop } : undefined}
						onTouchStart={(event) => {
							startX.current = event.nativeEvent.pageX;
						}}
					>
						{children}
					</View>
				</ReanimatedSwipeable>
			</View>
		</GestureDetector>
	);
}
