import type { NavigationSessionSummary, SessionActivity } from "@evener/appwire-client";
import { subagentTallyToShow } from "@evener/appwire-client/state/navigation";
import { relativeAge } from "@evener/appwire-client/state/navigation";
import { SymbolView } from "expo-symbols";
import { type ReactElement, useEffect, useState } from "react";
import {
	type AccessibilityActionEvent,
	type AccessibilityActionInfo,
	Animated,
	Pressable,
	Text,
	View,
} from "react-native";
import type { Palette } from "../design/tokens";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import {
	bandOf,
	type ClassifiedRow,
	lastLine,
	stateWord,
	subagentChipText,
	type Usual,
	type WhyLine,
	whyLine,
} from "./attention";
import { WASH_MS } from "./settledList";
import { StateMark } from "./StateMark";

export interface BoardRowProps {
	item: ClassifiedRow;
	/** Signal rows (Needs you, unseen Finished, Working in Live) have up to
	 * three lines; quiet rows (Idle, and rows in pinned categories, Projects
	 * and Archived) have one. */
	variant: "signal" | "quiet";
	/** Live's working rows move; every other copy of a session is still. */
	moving: boolean;
	connected: boolean;
	usual: Usual;
	hostLabel: (hostId: string) => string;
	hasDraft: boolean;
	/** S5's latest activity read for this session; absent before the Board's
	 * poll answers for it, on a hub that predates S5, or while disconnected. */
	activity?: SessionActivity;
	/** How long ago that read landed, which quiet time keeps counting from;
	 * null without one. */
	msSinceRead: number | null;
	now: number;
	onOpen: (row: NavigationSessionSummary) => void;
	/** Half opacity and busy while a change to this row is on its way. */
	dimmed?: boolean;
	/** What waits for the connection on this row (phase 6 ruling 18), in
	 * place of its why line. */
	waiting?: string | null;
	accessibilityActions?: readonly AccessibilityActionInfo[];
	onAccessibilityAction?: (event: AccessibilityActionEvent) => void;
	/** The row's long press, which RowMenu gives it: the row is the one
	 * pressable a touch reaches, so the menu's press has to live on it. */
	onLongPress?: () => void;
	delayLongPress?: number;
	/** Non-zero while the row has just entered Needs you: each new value
	 * washes it amber again (spec 7.3). */
	wash?: number;
	/** In select mode, whether the row is chosen: its mark column shows a
	 * checkbox instead of its state. Undefined outside select mode. */
	selected?: boolean;
}

/** Where a row's title starts: its 16pt padding, the 28pt mark column and
 * the 10pt gap. */
export const TITLE_INSET = 16 + 28 + 10;

/** The separator between rows, inset to the title by default. */
export function Hairline({ inset = TITLE_INSET }: { inset?: number }) {
	const { palette } = useColors();
	return <View style={{ height: 0.5, marginLeft: inset, backgroundColor: palette.edge }} />;
}

/** The type of the Board's section headers (spec 7.1): 13pt semibold,
 * inkMid, 0.4 letter-spacing. */
export const bandHeaderText = (palette: Palette, scale: number) => ({
	fontSize: 13 * scale,
	fontWeight: "600" as const,
	letterSpacing: 0.4,
	color: palette.inkMid,
});

/** A fold's chevron at a header's trailing edge: right while folded, turned
 * down while open. */
export function FoldChevron({ folded }: { folded: boolean }): ReactElement {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View style={{ transform: [{ rotate: folded ? "0deg" : "90deg" }] }}>
			<SymbolView name="chevron.right" size={13 * scale} tintColor={palette.inkLow} />
		</View>
	);
}

const UNITS: Record<string, string> = { m: "minute", h: "hour", d: "day" };

/** relativeAge's "2m" as VoiceOver should say it: "2 minutes". */
export function spokenAge(age: string): string {
	const match = /^(\d+)([mhd])$/.exec(age);
	if (!match) return age;
	const count = Number(match[1]);
	return `${count} ${UNITS[match[2]]}${count === 1 ? "" : "s"}`;
}

/** A group's header: a Live band, or a search group. */
export function BandHeader({ text }: { text: string }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Text
			testID="band-header"
			accessibilityRole="header"
			allowFontScaling={allowFontScaling}
			style={{
				paddingTop: 22,
				paddingBottom: 6,
				paddingHorizontal: 16,
				...bandHeaderText(palette, scale),
			}}
		>
			{text}
		</Text>
	);
}

/** One Board row (spec 7.2). It draws no separator: the list draws hairlines
 * between rows, inset to the title at TITLE_INSET. */
export function BoardRow({
	item,
	variant,
	moving,
	connected,
	usual,
	hostLabel,
	hasDraft,
	activity,
	msSinceRead,
	now,
	onOpen,
	dimmed = false,
	waiting = null,
	accessibilityActions,
	onAccessibilityAction,
	onLongPress,
	delayLongPress,
	wash = 0,
	selected,
}: BoardRowProps): ReactElement {
	const { palette } = useColors();
	const scale = useTextScale();
	const { row, state } = item;
	const signal = variant === "signal";
	const needsYou = signal && bandOf(state) === "needsYou";
	const why = signal ? whyLine(item, activity, msSinceRead ?? 0) : null;
	const last = signal ? lastLine(row, usual, hostLabel) : null;
	const age = relativeAge(row.updated_at, now);
	const word = stateWord(state);
	// The chip reads the navigation row's tally (the shared gate the web rail
	// uses), which the hub revises on invalidation; it is not the Board's S5
	// activity read, which is Board-only and can lag behind this count.
	const tally = subagentTallyToShow(row);
	const chipText = tally ? subagentChipText(tally) : undefined;
	// A working row with nothing more specific to say reads "Working" once.
	const reason = why && why.text !== word ? why.text : undefined;
	const label = [row.title, word, waiting ?? reason, chipText, age && spokenAge(age)].filter(Boolean).join(", ");
	const lineOne = 22 * scale;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			accessibilityState={selected === undefined ? { busy: dimmed } : { busy: dimmed, selected }}
			accessibilityActions={accessibilityActions}
			onAccessibilityAction={onAccessibilityAction}
			onPress={() => onOpen(row)}
			onLongPress={onLongPress}
			delayLongPress={delayLongPress}
			style={({ pressed }) => ({
				flexDirection: "row",
				alignItems: signal ? "flex-start" : "center",
				columnGap: 10,
				paddingHorizontal: 16,
				paddingTop: signal ? 11 : 0,
				paddingBottom: signal ? 12 : 0,
				minHeight: signal ? 64 : 48,
				backgroundColor: pressed ? palette.pressed : palette.page,
				opacity: dimmed ? 0.5 : 1,
			})}
		>
			{wash ? <Wash key={wash} /> : null}
			<View style={{ height: lineOne, justifyContent: "center" }}>
				{selected === undefined ? (
					<StateMark
						state={state}
						moving={moving}
						connected={connected}
						stuck={why?.stuck}
						perMinute={activity?.minutes}
					/>
				) : (
					// The mark column's 28pt, as StateMark's, so titles stay put.
					<View style={{ width: 28, alignItems: "center" }}>
						<SymbolView
							name={selected ? "checkmark.circle.fill" : "circle"}
							size={22}
							tintColor={selected ? palette.accent : palette.inkLow}
						/>
					</View>
				)}
			</View>
			<View style={{ flex: 1, minWidth: 0 }}>
				<View style={{ flexDirection: "row", alignItems: "flex-start", columnGap: 8 }}>
					<Text
						allowFontScaling={allowFontScaling}
						numberOfLines={needsYou ? 2 : 1}
						ellipsizeMode="tail"
						style={{
							flex: 1,
							fontSize: 17 * scale,
							lineHeight: lineOne,
							fontWeight: "600",
							color: palette.inkHi,
						}}
					>
						{row.title}
					</Text>
					{hasDraft || age || tally ? (
						<View style={{ height: lineOne, flexDirection: "row", alignItems: "center", columnGap: 6 }}>
							<SubagentChip session={row} />
							{hasDraft ? (
								<View style={{ backgroundColor: palette.accentBg, borderRadius: 4, paddingHorizontal: 4 }}>
									<Text
										allowFontScaling={allowFontScaling}
										style={{
											fontSize: 11 * scale,
											lineHeight: 13 * scale,
											fontWeight: "600",
											color: palette.accentInk,
										}}
									>
										Draft
									</Text>
								</View>
							) : null}
							{age ? (
								<Text
									allowFontScaling={allowFontScaling}
									style={{ fontSize: 13 * scale, color: palette.inkLow, fontVariant: ["tabular-nums"] }}
								>
									{age}
								</Text>
							) : null}
						</View>
					) : null}
				</View>
				{waiting ? (
					<WhyText why={{ text: waiting }} numberOfLines={1} marginTop={2} />
				) : why ? (
					<WhyText why={why} numberOfLines={needsYou ? 2 : 1} marginTop={2} />
				) : null}
				{last ? (
					<View style={{ marginTop: 4, flexDirection: "row", alignItems: "center", columnGap: 6, overflow: "hidden" }}>
						{last.task ? <Fact glyph="checklist" text={last.task} scale={scale} shrink /> : null}
						{last.project ? <Fact glyph="folder" text={last.project} scale={scale} /> : null}
						{last.host ? <Fact glyph="server.rack" text={last.host} scale={scale} /> : null}
					</View>
				) : null}
			</View>
		</Pressable>
	);
}

/** The subagent count chip on a live root row (S3). It never takes the Needs
 * you background (D2), and only the failure run reads in the danger ink: the
 * running count stays in the neutral ink, as the web rail's chip does. */
export function SubagentChip({ session }: { session: NavigationSessionSummary }): ReactElement | null {
	const { palette } = useColors();
	const scale = useTextScale();
	const tally = subagentTallyToShow(session);
	if (!tally) return null;
	const failed = tally.failed > 0;
	return (
		<View
			testID="subagent-chip"
			accessibilityLabel={subagentChipText(tally)}
			style={{
				backgroundColor: palette.inset,
				borderRadius: 4,
				paddingHorizontal: 4,
			}}
		>
			<Text
				allowFontScaling={allowFontScaling}
				style={{
					fontSize: 11 * scale,
					lineHeight: 13 * scale,
					fontWeight: "600",
					color: palette.inkMid,
				}}
			>
				{tally.running > 0 ? <Text style={{ color: palette.inkMid }}>{`${tally.running} running`}</Text> : null}
				{tally.running > 0 && failed ? " · " : null}
				{failed ? <Text style={{ color: palette.dangerInk }}>{`${tally.failed} failed`}</Text> : null}
			</Text>
		</View>
	);
}

/** A session row's subagent chip, for the lists that render their own rows
 * (Projects, Project and Pin sections) rather than a BoardRow. Null when the
 * row has no chip, so a list can tell whether it has one. */
export const sessionSubagentChip = (session: NavigationSessionSummary): ReactElement | null =>
	subagentTallyToShow(session) ? <SubagentChip session={session} /> : null;

/** The amber wash behind a row that just entered Needs you (spec 7.3): full
 * at once, then fading out over WASH_MS. Reduce Motion keeps it (ruling 23):
 * it is a fade, and with rows jumping it is the only cue to where one landed.
 * Remounted by its key for each new wash. */
function Wash() {
	const { palette } = useColors();
	const [opacity] = useState(() => new Animated.Value(1));
	useEffect(() => {
		Animated.timing(opacity, { toValue: 0, duration: WASH_MS, useNativeDriver: true }).start();
	}, [opacity]);
	return (
		<Animated.View
			pointerEvents="none"
			style={{
				position: "absolute",
				top: 0,
				right: 0,
				bottom: 0,
				left: 0,
				backgroundColor: palette.attentionBg,
				opacity,
			}}
		/>
	);
}

/** A row's why line (spec 7.2): the state's word in its hue, when it has
 * one, then the reason. */
export function WhyText({
	why,
	numberOfLines,
	marginTop,
}: {
	why: WhyLine;
	numberOfLines?: number;
	marginTop?: number;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Text
			allowFontScaling={allowFontScaling}
			numberOfLines={numberOfLines}
			ellipsizeMode="tail"
			style={{
				marginTop,
				fontSize: 15 * scale,
				lineHeight: 20 * scale,
				color: why.word ? palette.inkHi : why.stuck ? palette.attentionInk : palette.inkMid,
			}}
		>
			{why.word ? (
				<>
					<Text
						style={{
							fontWeight: "600",
							color: why.hue === "danger" ? palette.dangerInk : palette.attentionInk,
						}}
					>
						{why.word}
					</Text>
					{" · "}
				</>
			) : null}
			{why.text}
		</Text>
	);
}

/** A glyph and a fact on the last line, which never wraps. A `shrink` fact
 * gives up width when the line runs out of room, tail-truncating its text;
 * the others keep theirs. Only the task line shrinks: its title comes after
 * the "Task 4 of 7 · " prefix, so the title truncates first (spec 7.2). */
export function Fact({
	glyph,
	text,
	scale,
	shrink = false,
}: {
	glyph: "checklist" | "folder" | "server.rack";
	text: string;
	scale: number;
	shrink?: boolean;
}) {
	const { palette } = useColors();
	const flexShrink = shrink ? 1 : 0;
	return (
		<View style={{ flexDirection: "row", alignItems: "center", columnGap: 3, flexShrink }}>
			<SymbolView name={glyph} size={13 * scale} tintColor={palette.inkLow} />
			<Text
				allowFontScaling={allowFontScaling}
				numberOfLines={1}
				ellipsizeMode="tail"
				style={{ flexShrink, fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow }}
			>
				{text}
			</Text>
		</View>
	);
}
