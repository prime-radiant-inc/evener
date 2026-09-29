// The in-app alert banner (spec 13.3, 16.3, 16.6): a card just below the nav
// bar that says which session needs you and why, or how many do. A finger on
// it keeps it, a tap opens what it names, and an upward swipe sends it away.
// It drops in when it's new and stays still while it updates in place (a
// session joining it), since nothing moves unless its data moved.
import { SymbolView } from "expo-symbols";
import { useEffect, useMemo, useRef } from "react";
import { AccessibilityInfo, Animated, PanResponder, Pressable, Text, View } from "react-native";
import type { BoardState, WhyLine } from "../board/attention";
import { StateMark } from "../board/StateMark";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { type Alert, type Banner, quiet } from "./alertCenter";
import { swipeDismisses } from "./bannerGesture";

const COALESCED_LINE = "Tap to see them on the Board.";
const STARTED_LINE = "Session started. Tap to open it.";
const START_FAILED_HINT = "Tap to open New session.";
const MAY_HAVE_STARTED_LINE = "It may have started.";
const DRAFT_KEPT_LINE = "Your draft is kept.";

/** Each session alert's mark: its Board state, a session you started as
 * working, and a start that failed as failed. */
const MARK_STATE: Record<Exclude<Alert["kind"], "notice">, BoardState> = {
	failed: "failed",
	question: "question",
	approval: "approval",
	warning: "warning",
	restartNeeded: "restartNeeded",
	finished: "finished",
	started: "working",
	startFailed: "failed",
};

/** What the banner's two lines say: the title, and the why line for one
 * session, the prototype's hint for several, or nothing for a notice or a
 * finished result (until S11 and S1 give them one). */
function words(banner: Banner): { title: string; why: WhyLine | null; hint: string | null } {
	const [only] = banner.alerts;
	// Two or more alerts are always sessions that need you: the center
	// combines only those (AlertCenter.show's needsYou check, and release()
	// joins held sessions only), never a notice or a finished result.
	if (banner.alerts.length > 1 || only === undefined)
		return { title: `${banner.alerts.length} sessions need you`, why: null, hint: COALESCED_LINE };
	if (only.kind === "started") return { title: only.title, why: null, hint: STARTED_LINE };
	// The form in New session keeps the store's full reason; the banner says
	// only what happened, on which hub, in one short line.
	if (only.kind === "startFailed")
		return {
			title: only.uncertain ? "Couldn't confirm the new session started" : "Couldn't start the new session",
			why: null,
			hint: `On ${only.hubName}. ${only.uncertain ? MAY_HAVE_STARTED_LINE : DRAFT_KEPT_LINE} ${START_FAILED_HINT}`,
		};
	return { title: only.title, why: only.kind === "notice" ? null : only.why, hint: null };
}

/** The one sentence VoiceOver reads for the banner. */
export function bannerLabel(banner: Banner): string {
	const { title, why, hint } = words(banner);
	return [title, why?.word, why?.text, hint].filter((part) => !!part).join(", ");
}

function Mark({ banner }: { banner: Banner }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const [only] = banner.alerts;
	if (banner.alerts.length > 1 || only === undefined)
		return (
			<View
				style={{
					width: 22,
					height: 22,
					borderRadius: 11,
					backgroundColor: palette.attentionBg,
					alignItems: "center",
					justifyContent: "center",
				}}
			>
				<Text
					allowFontScaling={false}
					style={{ color: palette.attentionInk, fontSize: 13, fontWeight: "600", fontVariant: ["tabular-nums"] }}
				>
					{String(banner.alerts.length)}
				</Text>
			</View>
		);
	if (only.kind === "notice")
		return <SymbolView name="exclamationmark.triangle.fill" size={17 * scale} tintColor={palette.attention} />;
	return <StateMark state={MARK_STATE[only.kind]} />;
}

export function AlertBanner({
	banner,
	onTap,
	onDismiss,
	onTouch,
}: {
	banner: Banner;
	onTap: () => void;
	onDismiss: () => void;
	onTouch: (down: boolean) => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const { title, why, hint } = words(banner);
	const label = bannerLabel(banner);
	// News rather than a request: a finished result or a session you started.
	const news = banner.alerts.every(quiet);

	// The drop: this component mounts once per banner (the host keys it by
	// id), so it drops in on mount and never again. Reduce Motion is read at
	// that moment, since the banner is gone long before the setting changes.
	const drop = useRef(new Animated.Value(-24)).current;
	const opacity = useRef(new Animated.Value(0)).current;
	useEffect(() => {
		let live = true;
		// A read that fails shows the banner without motion rather than not at all.
		void AccessibilityInfo.isReduceMotionEnabled()
			.catch(() => true)
			.then((reduce) => {
				if (!live) return;
				const fade = Animated.timing(opacity, { toValue: 1, duration: 200, useNativeDriver: true });
				if (reduce) {
					drop.setValue(0);
					fade.start();
					return;
				}
				// Settles in about 300ms (spec 16.6).
				Animated.parallel([
					Animated.spring(drop, { toValue: 0, stiffness: 300, damping: 26, mass: 1, useNativeDriver: true }),
					fade,
				]).start();
			});
		return () => {
			live = false;
		};
	}, [drop, opacity]);

	// The swipe: an upward drag follows the finger, never a downward one, and
	// the release decides between going and springing back. The responder
	// claims only upward moves, so the Pressable keeps its taps.
	const drag = useRef(new Animated.Value(0)).current;
	const dismiss = useRef(onDismiss);
	dismiss.current = onDismiss;
	// The drag takes the touch from the Pressable, whose press-out would let
	// the banner expire under the finger, so the drag keeps it itself.
	const touch = useRef(onTouch);
	touch.current = onTouch;
	const pan = useMemo(() => {
		const springBack = () => Animated.spring(drag, { toValue: 0, useNativeDriver: true }).start();
		return PanResponder.create({
			onMoveShouldSetPanResponder: (_event, gesture) => gesture.dy < -4 && Math.abs(gesture.dy) > Math.abs(gesture.dx),
			onPanResponderGrant: () => touch.current(true),
			onPanResponderMove: (_event, gesture) => drag.setValue(Math.min(0, gesture.dy)),
			onPanResponderRelease: (_event, gesture) => {
				touch.current(false);
				if (swipeDismisses(gesture.dy, gesture.vy)) dismiss.current();
				else springBack();
			},
			onPanResponderTerminate: () => {
				touch.current(false);
				springBack();
			},
		});
	}, [drag]);

	// VoiceOver hears a banner once when it drops in, and again only when what
	// it says changes.
	useEffect(() => {
		AccessibilityInfo.announceForAccessibility(label);
	}, [label]);

	return (
		<Animated.View {...pan.panHandlers} style={{ marginHorizontal: 8, transform: [{ translateY: drag }] }}>
			<Animated.View style={{ opacity, transform: [{ translateY: drop }] }}>
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={label}
					accessibilityActions={[{ name: "activate" }, { name: "escape", label: "Dismiss" }]}
					onAccessibilityAction={(event) => {
						if (event.nativeEvent.actionName === "escape") onDismiss();
						else onTap();
					}}
					onPress={onTap}
					onPressIn={() => onTouch(true)}
					onPressOut={() => onTouch(false)}
					style={{
						flexDirection: "row",
						columnGap: 10,
						paddingVertical: 11,
						paddingHorizontal: 14,
						borderRadius: 18,
						borderCurve: "continuous",
						borderWidth: 1,
						borderColor: news ? palette.accentEdge : palette.attentionEdge,
						backgroundColor: palette.surface,
						// A floating element's shadow (spec 16.3), as the toast's.
						shadowColor: "#000000",
						shadowOpacity: 0.12,
						shadowRadius: 12,
						shadowOffset: { width: 0, height: 4 },
					}}
				>
					<View style={{ width: 24, alignItems: "center", paddingTop: 1 }}>
						<Mark banner={banner} />
					</View>
					<View style={{ flex: 1 }}>
						<View style={{ flexDirection: "row", alignItems: "baseline", columnGap: 8 }}>
							<Text
								allowFontScaling={allowFontScaling}
								numberOfLines={1}
								style={{
									flex: 1,
									color: palette.inkHi,
									fontSize: 15 * scale,
									lineHeight: 20 * scale,
									fontWeight: "600",
								}}
							>
								{title}
							</Text>
							<Text allowFontScaling={allowFontScaling} style={{ color: palette.inkLow, fontSize: 12 * scale }}>
								now
							</Text>
						</View>
						{why || hint ? (
							<Text
								allowFontScaling={allowFontScaling}
								style={{ color: palette.inkMid, fontSize: 14 * scale, lineHeight: 19 * scale }}
							>
								{why?.word ? (
									<Text
										style={{
											color: why.hue === "danger" ? palette.dangerInk : palette.attentionInk,
											fontWeight: "600",
										}}
									>
										{why.word}
									</Text>
								) : null}
								{why?.word ? " · " : null}
								{why?.text ?? hint}
							</Text>
						) : null}
					</View>
				</Pressable>
			</Animated.View>
		</Animated.View>
	);
}
