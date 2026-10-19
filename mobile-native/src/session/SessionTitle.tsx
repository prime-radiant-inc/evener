// The Session's nav bar title (spec 8.1): the session's name over its state
// line, one button that opens the session's info. A horizontal pan on it
// moves to the next or previous session in Live order (spec 6).
import { SymbolView } from "expo-symbols";
import { useEffect, useMemo, useRef } from "react";
import { Pressable, Text, useWindowDimensions, View } from "react-native";
import { Gesture, GestureDetector } from "react-native-gesture-handler";
import { markFor } from "../board/StateMark";
import { PulseMeter } from "../board/PulseMeter";
import { meterTone } from "../board/pulse";
import { useHeaderTextScale } from "../headerText";
import { allowFontScaling, useColors } from "../ui";
import type { SessionStateLine } from "./sessionState";
import { titleSwipeDirection } from "./titleSwipe";

/** How far in from each screen edge the nav bar's buttons reach: Back with
 * its count pill on the left, ⋯ on the right, each with its margin and the
 * gap before the title (measured on iOS 26's glass bar, iPhone 17 Pro). The
 * native header sizes a custom title view by its content, so the title keeps
 * inside the span between them itself (#2956). */
export const NAV_BAR_BUTTON_SPAN = 80;

/** The Back pill's chevron and one of its count's digits in BackButton.tsx (a
 * 20pt chevron and 17pt tabular figures, about 10pt a digit): the parts of the
 * span that grow with Dynamic Type. */
const BACK_CHEVRON = 20;
const BACK_COUNT_DIGIT = 10;

/** The room the nav bar's buttons need on each side of the title: the span
 * measured for a one-digit Back count at the default type, plus what the Back
 * pill grows by. Dynamic Type enlarges its chevron and count (AX1-AX5), and
 * each further digit widens the count, so a 3-digit count or an accessibility
 * text size can no longer run under the title (#3063). The ⋯ button on the
 * other side does not scale. */
export function navBarButtonSpan(count: number, scale: number): number {
	const digits = Math.max(1, String(count).length);
	return (
		NAV_BAR_BUTTON_SPAN + (scale - 1) * (BACK_CHEVRON + BACK_COUNT_DIGIT) + (digits - 1) * BACK_COUNT_DIGIT * scale
	);
}

/** The state line's leading glyph (spec 8.1, 16.4): the live meter while the
 * agent works, every other state's still mark, or nothing. `markFor(state,
 * false)` narrows the meter out - the title's mark never animates. */
function LeadingMark({
	line,
	mark,
	perMinute,
	connected,
	scale,
}: {
	line: SessionStateLine;
	mark: ReturnType<typeof markFor>;
	perMinute?: readonly number[];
	connected: boolean;
	scale: number;
}) {
	const { palette } = useColors();
	if (line.live) return <PulseMeter perMinute={perMinute} tone={meterTone(connected, line.live.attention)} />;
	if (!mark || mark === "meter") return null;
	return <SymbolView name={mark.name} tintColor={palette[mark.tint]} size={12 * scale} />;
}

/** The state line's words in 13pt tabular figures, amber while the agent may
 * be stuck. A long one truncates inside the title's width. */
function StateLine({ line, scale }: { line: SessionStateLine; scale: number }) {
	const { palette } = useColors();
	return (
		<Text
			allowFontScaling={allowFontScaling}
			numberOfLines={1}
			ellipsizeMode="tail"
			style={{
				color: line.live?.attention ? palette.attentionInk : palette.inkMid,
				fontSize: 13 * scale,
				lineHeight: 18 * scale,
				fontVariant: ["tabular-nums"],
				flexShrink: 1,
			}}
		>
			{line.text}
		</Text>
	);
}

export function SessionTitle({
	title,
	line,
	onPress,
	onSwipe,
	neighbors,
	backCount = 0,
	perMinute,
	onJumpToLive,
	connected = true,
}: {
	title: string;
	line: SessionStateLine;
	onPress: () => void;
	/** 1 for the next session in Live order, -1 for the previous one. */
	onSwipe: (direction: 1 | -1) => void;
	/** Whether Live order has a session on either side, for VoiceOver's
	 * stand-ins for the swipe. */
	neighbors: { previous: boolean; next: boolean };
	/** How many other sessions need you: the Back pill's count, whose digits
	 * widen the span the title must clear (#3063). */
	backCount?: number;
	/** The live meter's per-minute counts, while the state line carries a live
	 * line (spec 16.4). */
	perMinute?: readonly number[];
	/** A press on the live state line jumps to the live end. Absent where there
	 * is no live line to jump from. */
	onJumpToLive?(): void;
	/** The hub is reachable: a disconnected session's meter goes gray. */
	connected?: boolean;
}) {
	const { palette } = useColors();
	const scale = useHeaderTextScale();
	const { width } = useWindowDimensions();
	const span = navBarButtonSpan(backCount, scale);
	const mark = markFor(line.state, false);
	const liveAction = line.live && onJumpToLive;
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
			<View style={{ maxWidth: Math.max(0, width - 2 * span), flexDirection: "row", alignItems: "center", gap: 4 }}>
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={`${title}, ${line.text}, session info`}
					accessibilityShowsLargeContentViewer
					accessibilityLargeContentTitle={`${title}, ${line.text}`}
					onPress={onPress}
					accessibilityActions={[
						...(neighbors.previous ? [{ name: "previous", label: "Previous session" }] : []),
						...(neighbors.next ? [{ name: "next", label: "Next session" }] : []),
					]}
					onAccessibilityAction={(event) => {
						if (event.nativeEvent.actionName === "previous") onSwipe(-1);
						if (event.nativeEvent.actionName === "next") onSwipe(1);
					}}
					style={({ pressed }) => ({
						minHeight: 44,
						minWidth: 0,
						flexShrink: 1,
						alignItems: "center",
						justifyContent: "center",
						opacity: pressed ? 0.6 : 1,
					})}
				>
					<Text
						allowFontScaling={allowFontScaling}
						numberOfLines={1}
						ellipsizeMode="tail"
						style={{ color: palette.inkHi, fontSize: 15 * scale, fontWeight: "600" }}
					>
						{title}
					</Text>
					<View style={{ flexDirection: "row", alignItems: "center", gap: 4 }}>
						{!liveAction ? (
							<LeadingMark line={line} mark={mark} perMinute={perMinute} connected={connected} scale={scale} />
						) : null}
						<StateLine line={line} scale={scale} />
						<SymbolView name="chevron.right" tintColor={palette.inkLow} size={9 * scale} />
					</View>
				</Pressable>
				{liveAction ? (
					<Pressable
						accessibilityRole="button"
						accessibilityLabel={line.text}
						accessibilityHint="Shows the latest"
						accessibilityShowsLargeContentViewer
						accessibilityLargeContentTitle={line.text}
						onPress={onJumpToLive}
						style={{ minWidth: 44, minHeight: 44, alignItems: "center", justifyContent: "center" }}
					>
						<LeadingMark line={line} mark={mark} perMinute={perMinute} connected={connected} scale={scale} />
					</Pressable>
				) : null}
			</View>
		</GestureDetector>
	);
}
