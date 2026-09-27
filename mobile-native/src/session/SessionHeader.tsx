// The block under the Session's nav bar (spec 8.1): the connection bar, then
// the context chips. The screen floats it over the list's top edge and
// reserves its height with the list's top padding; hiding slides the chips up
// behind the bar with a transform, so the list's layout, and the reader's
// position in it, never move.
import { type SFSymbol, SymbolView } from "expo-symbols";
import { type ReactNode, useCallback, useEffect, useRef, useState } from "react";
import { AccessibilityInfo, Animated, Platform, Pressable, ScrollView, Text, View } from "react-native";
import { useColors, useTextScale } from "../ui";
import type { ChipKind, ContextChip } from "./sessionState";

const SYMBOLS: Record<ChipKind, SFSymbol> = {
	subagents: "person.2",
	tasks: "checklist",
	goal: "target",
	queue: "tray",
};

const UPDATE_NEEDED_HINT =
	"This app and the hub need compatible versions. Update the app from TestFlight, or update Evener on the hub.";

/** How far the list scrolls down, from where it last turned, before the chips
 * get out of the way. */
const HIDE_AFTER_PT = 8;
const HIDE_DURATION_MS = 200;

export function SessionHeader({
	status,
	chips,
	hidden,
	onChip,
	notes,
}: {
	status: string | null;
	chips: readonly ContextChip[];
	hidden: boolean;
	onChip: (kind: ChipKind) => void;
	notes?: ReactNode;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const [rowHeight, setRowHeight] = useState(0);
	const offset = useSlide(hidden ? -rowHeight : 0);
	const hasRow = chips.length > 0 || notes != null;
	if (status === null && !hasRow) return null;
	return (
		// Clipping keeps the slid-away row from drawing over the nav bar, and
		// box-none lets touches on the list's uncovered top reach the list.
		<View pointerEvents="box-none" style={{ overflow: "hidden" }}>
			{status !== null ? (
				<View
					style={{
						minHeight: 24,
						justifyContent: "center",
						paddingHorizontal: 16,
						backgroundColor: palette.page,
						// Above the chips row, which slides up behind it.
						zIndex: 1,
					}}
				>
					<Text
						allowFontScaling={Platform.OS !== "ios"}
						accessibilityLiveRegion="polite"
						accessibilityHint={status === "Update needed" ? UPDATE_NEEDED_HINT : undefined}
						style={{
							fontSize: 13 * scale,
							lineHeight: 18 * scale,
							color: palette.inkLow,
							textAlign: "center",
							fontVariant: ["tabular-nums"],
						}}
					>
						{status}
					</Text>
				</View>
			) : null}
			{hasRow ? (
				<Animated.View
					onLayout={(event) => setRowHeight(event.nativeEvent.layout.height)}
					style={{ transform: [{ translateY: offset }] }}
				>
					{chips.length > 0 ? <ChipsRow chips={chips} onChip={onChip} /> : null}
					{notes}
				</Animated.View>
			) : null}
		</View>
	);
}

function ChipsRow({ chips, onChip }: { chips: readonly ContextChip[]; onChip: (kind: ChipKind) => void }) {
	const { palette } = useColors();
	const [viewportWidth, setViewportWidth] = useState(0);
	const [contentWidth, setContentWidth] = useState(0);
	const overflows = viewportWidth > 0 && contentWidth > viewportWidth;
	return (
		<View style={{ backgroundColor: palette.page }}>
			<ScrollView
				horizontal
				showsHorizontalScrollIndicator={false}
				onLayout={(event) => setViewportWidth(event.nativeEvent.layout.width)}
				onContentSizeChange={(width) => setContentWidth(width)}
				contentContainerStyle={{ paddingHorizontal: 16, paddingVertical: 8, gap: 8 }}
			>
				{chips.map((chip) => (
					<Chip key={chip.kind} chip={chip} onPress={() => onChip(chip.kind)} />
				))}
			</ScrollView>
			{overflows ? (
				<View
					testID="chips-fade"
					pointerEvents="none"
					style={{
						position: "absolute",
						top: 0,
						bottom: 0,
						right: 0,
						width: 24,
						experimental_backgroundImage: `linear-gradient(to right, ${palette.page}00, ${palette.page})`,
					}}
				/>
			) : null}
		</View>
	);
}

function Chip({ chip, onPress }: { chip: ContextChip; onPress: () => void }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const ink = chip.attention ? palette.attentionInk : palette.inkHi;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={chip.accessibilityLabel}
			onPress={onPress}
			// 32pt tall, 44pt to the finger.
			hitSlop={{ top: 6, bottom: 6 }}
			style={({ pressed }) => ({
				height: 32,
				flexDirection: "row",
				alignItems: "center",
				gap: 6,
				paddingHorizontal: 12,
				borderRadius: 16,
				borderWidth: 1,
				borderColor: chip.attention ? palette.attentionEdge : palette.edge,
				backgroundColor: chip.attention ? palette.attentionBg : palette.inset,
				opacity: pressed ? 0.6 : 1,
			})}
		>
			<SymbolView
				name={SYMBOLS[chip.kind]}
				size={13 * scale}
				tintColor={chip.attention ? palette.attentionInk : palette.inkMid}
			/>
			<Text
				allowFontScaling={Platform.OS !== "ios"}
				numberOfLines={1}
				style={{ fontSize: 15 * scale, lineHeight: 20 * scale, color: ink, fontVariant: ["tabular-nums"] }}
			>
				{chip.label}
				{chip.failed ? " · " : null}
				{chip.failed ? <Text style={{ color: palette.dangerInk }}>{chip.failed}</Text> : null}
			</Text>
		</Pressable>
	);
}

/** An Animated value that follows `target`: over 200ms, or at once with
 * Reduce Motion on. */
function useSlide(target: number): Animated.Value {
	const value = useRef(new Animated.Value(target)).current;
	const reduceMotion = useReduceMotion();
	const current = useRef(target);
	useEffect(() => {
		if (current.current === target) return;
		current.current = target;
		if (reduceMotion) value.setValue(target);
		else Animated.timing(value, { toValue: target, duration: HIDE_DURATION_MS, useNativeDriver: true }).start();
	}, [target, reduceMotion, value]);
	return value;
}

function useReduceMotion(): boolean {
	const [reduceMotion, setReduceMotion] = useState(false);
	useEffect(() => {
		let live = true;
		void AccessibilityInfo.isReduceMotionEnabled().then((enabled) => {
			if (live) setReduceMotion(enabled);
		});
		const subscription = AccessibilityInfo.addEventListener("reduceMotionChanged", setReduceMotion);
		return () => {
			live = false;
			subscription.remove();
		};
	}, []);
	return reduceMotion;
}

export interface HeaderHiding {
	hidden: boolean;
	/** The last offset seen. */
	lastY: number;
	/** Where the list last turned from scrolling up to scrolling down. */
	turnY: number;
}

/** The chips hide once the list scrolls down more than 8pt from where it last
 * turned, and come back on any upward scroll or at the top. */
export function nextHeaderHiding(state: HeaderHiding, y: number): HeaderHiding {
	if (y <= 0 || y < state.lastY) return { hidden: false, lastY: y, turnY: y };
	return { hidden: state.hidden || y - state.turnY > HIDE_AFTER_PT, lastY: y, turnY: state.turnY };
}

/** Whether the header's chips are hidden, fed the list's scroll offsets from
 * its own onScroll. */
export function useHeaderHiding(): { hidden: boolean; onScroll: (y: number) => void } {
	const tracker = useRef<HeaderHiding>({ hidden: false, lastY: 0, turnY: 0 });
	const [hidden, setHidden] = useState(false);
	const onScroll = useCallback((y: number) => {
		tracker.current = nextHeaderHiding(tracker.current, y);
		setHidden(tracker.current.hidden);
	}, []);
	return { hidden, onScroll };
}
