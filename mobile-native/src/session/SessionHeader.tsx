// The block under the Session's nav bar (spec 8.1): the connection bar, then
// the context chips. The screen floats it over the list's top edge and
// reserves its height with the list's top padding; hiding slides the chips up
// behind the bar with a transform, so the list's layout, and the reader's
// position in it, never move.
import { type SFSymbol, SymbolView } from "expo-symbols";
import { GlassView } from "expo-glass-effect";
import { type ReactNode, useCallback, useEffect, useRef, useState } from "react";
import { Animated, Pressable, ScrollView, Text, View } from "react-native";
import { UPDATE_NEEDED } from "../board/connectionStatus";
import { INCOMPATIBLE_VERSIONS } from "../connectionRecovery";
import { useReduceMotion } from "../accessibilitySettings";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { FreshDot } from "../reader/FreshDot";
import { headerRowFill } from "./headerGlass";
import type { ChipKind, ContextChip } from "./sessionState";

const SYMBOLS: Record<ChipKind, SFSymbol> = {
	subagents: "person.2",
	files: "doc.text",
	tasks: "checklist",
	goal: "target",
	queue: "tray",
};

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
	find,
	glassTop,
}: {
	status: string | null;
	chips: readonly ContextChip[];
	hidden: boolean;
	onChip: (kind: ChipKind) => void;
	notes?: ReactNode;
	/** The find bar, in the chips' place while find is open (spec 8.7). It
	 * stays put while the list scrolls from match to match. */
	find?: ReactNode;
	/** Where the nav bar is the system's glass, its height: the header then
	 * starts at the screen's top, leaves the bar that room, and spans one
	 * glass under the bar and its rows, the iOS pattern for a bar with a
	 * search field or segmented control (spec 16.3). */
	glassTop?: number;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const [rowHeight, setRowHeight] = useState(0);
	// The find bar never slides away: it stays put while the list scrolls from
	// match to match, whatever hides the chips.
	const slidAway = hidden && find == null;
	const offset = useSlide(slidAway ? -rowHeight : 0);
	const hasRow = chips.length > 0 || notes != null || find != null;
	const onGlass = glassTop !== undefined;
	if (!onGlass && status === null && !hasRow) return null;
	return (
		// box-none lets touches on the list's uncovered top reach the list.
		<View pointerEvents="box-none">
			{onGlass ? (
				// The glass moves with the rows, so as they slide away its lower
				// edge rises with them to the bar's.
				<Animated.View
					pointerEvents="none"
					style={{
						position: "absolute",
						top: 0,
						left: 0,
						right: 0,
						bottom: 0,
						transform: [{ translateY: offset }],
					}}
				>
					<GlassView glassEffectStyle="regular" colorScheme="auto" style={{ flex: 1 }} />
				</Animated.View>
			) : null}
			{onGlass ? <View testID="nav-bar-room" pointerEvents="none" style={{ height: glassTop }} /> : null}
			{status !== null ? (
				<View
					style={{
						minHeight: 24,
						justifyContent: "center",
						paddingHorizontal: 16,
						backgroundColor: headerRowFill(onGlass, palette),
					}}
				>
					<Text
						allowFontScaling={allowFontScaling}
						// Android reads this live region; iOS has none, and this
						// connection status stays unannounced there on purpose: it is
						// ambient and changes on every reconnect and offline-age tick,
						// so speaking each one would talk over the reader (#2903).
						accessibilityLiveRegion="polite"
						accessibilityHint={status === UPDATE_NEEDED ? INCOMPATIBLE_VERSIONS : undefined}
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
				// Clipping at the row's own top keeps it from drawing over the
				// status line or the bar as it slides away.
				<View pointerEvents="box-none" style={{ overflow: "hidden" }}>
					<Animated.View
						onLayout={(event) => setRowHeight(event.nativeEvent.layout.height)}
						style={{ transform: [{ translateY: offset }] }}
						// Slid away behind the nav bar, the row is out of VoiceOver's reach.
						accessibilityElementsHidden={slidAway}
						importantForAccessibility={slidAway ? "no-hide-descendants" : "auto"}
					>
						{find ?? (chips.length > 0 ? <ChipsRow chips={chips} onChip={onChip} onGlass={onGlass} /> : null)}
						{notes}
					</Animated.View>
				</View>
			) : null}
		</View>
	);
}

function ChipsRow({
	chips,
	onChip,
	onGlass,
}: {
	chips: readonly ContextChip[];
	onChip: (kind: ChipKind) => void;
	onGlass: boolean;
}) {
	const { palette } = useColors();
	const [viewportWidth, setViewportWidth] = useState(0);
	const [contentWidth, setContentWidth] = useState(0);
	const overflows = viewportWidth > 0 && contentWidth > viewportWidth;
	return (
		<View testID="chips-row" style={{ backgroundColor: headerRowFill(onGlass, palette) }}>
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
				allowFontScaling={allowFontScaling}
				numberOfLines={1}
				style={{ fontSize: 15 * scale, lineHeight: 20 * scale, color: ink, fontVariant: ["tabular-nums"] }}
			>
				{chip.label}
				{chip.failed ? " · " : null}
				{chip.failed ? <Text style={{ color: palette.dangerInk }}>{chip.failed}</Text> : null}
			</Text>
			{chip.dot ? <FreshDot /> : null}
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

export interface HeaderHiding {
	hidden: boolean;
	/** The last offset seen. */
	lastY: number;
	/** Where the list last turned from scrolling up to scrolling down. */
	turnY: number;
}

/** The chips hide once the person drags the list down more than 8pt from
 * where it last turned, and come back on any upward scroll or at the top.
 * `dragging` is false when the app moved the list itself (a reading-position
 * restore, following the latest message): that never hides the chips, and
 * the next drag counts from where the list landed. */
export function nextHeaderHiding(state: HeaderHiding, y: number, dragging: boolean): HeaderHiding {
	if (y <= 0) return { hidden: false, lastY: y, turnY: y };
	if (!dragging) return { hidden: state.hidden, lastY: y, turnY: y };
	if (y < state.lastY) return { hidden: false, lastY: y, turnY: y };
	return { hidden: state.hidden || y - state.turnY > HIDE_AFTER_PT, lastY: y, turnY: state.turnY };
}

/** Whether the header's chips are hidden, fed the list's scroll offsets from
 * its own onScroll, with whether a drag (or its momentum) moved it. */
export function useHeaderHiding(): { hidden: boolean; onScroll: (y: number, dragging: boolean) => void } {
	const tracker = useRef<HeaderHiding>({ hidden: false, lastY: 0, turnY: 0 });
	const [hidden, setHidden] = useState(false);
	const onScroll = useCallback((y: number, dragging: boolean) => {
		tracker.current = nextHeaderHiding(tracker.current, y, dragging);
		setHidden(tracker.current.hidden);
	}, []);
	return { hidden, onScroll };
}
