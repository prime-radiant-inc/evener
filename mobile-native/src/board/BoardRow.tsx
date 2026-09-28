import type { NavigationSessionSummary } from "@evener/appwire-client";
import { relativeAge } from "@evener/appwire-client/state/navigation";
import { SymbolView } from "expo-symbols";
import type { ReactElement } from "react";
import { type AccessibilityActionEvent, type AccessibilityActionInfo, Platform, Pressable, Text, View } from "react-native";
import type { Palette } from "../design/tokens";
import { useColors, useTextScale } from "../ui";
import { bandOf, type ClassifiedRow, lastLine, stateWord, type Usual, whyLine } from "./attention";
import type { RowSwipes } from "./rowSwipes";
import { StateMark } from "./StateMark";
import { SwipeRow, swipeAccessibility } from "./SwipeRow";

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
	now: number;
	onOpen: (row: NavigationSessionSummary) => void;
	/** Half opacity and busy while a change to this row is on its way. */
	dimmed?: boolean;
	accessibilityActions?: readonly AccessibilityActionInfo[];
	onAccessibilityAction?: (event: AccessibilityActionEvent) => void;
}

/** Where a row's title starts: its 16pt padding, the 28pt mark column and
 * the 10pt gap. */
export const TITLE_INSET = 16 + 28 + 10;

/** What every row in one of the Board's lists shares. */
export type RowContext = Pick<BoardRowProps, "connected" | "usual" | "hostLabel" | "now" | "onOpen"> & {
	draftRefs: ReadonlySet<string>;
	/** A row's swipes, by whether it sits in an archived tier. */
	swipes: (item: ClassifiedRow, archived: boolean) => RowSwipes;
};

function Hairline({ inset = 0 }: { inset?: number }) {
	const { palette } = useColors();
	return <View style={{ height: 0.5, marginLeft: inset, backgroundColor: palette.edge }} />;
}

/** A list of Board rows, each one swipeable, separated by hairlines inset
 * to the title. `archived` rows sit in an archived tier. */
export function BoardRows({
	items,
	variant,
	moving,
	archived = false,
	context,
}: {
	items: readonly ClassifiedRow[];
	variant: BoardRowProps["variant"];
	moving: boolean;
	archived?: boolean;
	context: RowContext;
}): ReactElement {
	const { draftRefs, swipes, ...shared } = context;
	return (
		<>
			{items.map((item, index) => {
				const { leading, trailing, dimmed } = swipes(item, archived);
				return (
					<View key={item.row.ref}>
						{index > 0 ? <Hairline inset={TITLE_INSET} /> : null}
						<SwipeRow leading={leading} trailing={trailing}>
							<BoardRow
								item={item}
								variant={variant}
								moving={moving}
								hasDraft={draftRefs.has(item.row.ref)}
								dimmed={dimmed}
								{...swipeAccessibility(leading, trailing)}
								{...shared}
							/>
						</SwipeRow>
					</View>
				);
			})}
		</>
	);
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
function spokenAge(age: string): string {
	const match = /^(\d+)([mhd])$/.exec(age);
	if (!match) return age;
	const count = Number(match[1]);
	return `${count} ${UNITS[match[2]]}${count === 1 ? "" : "s"}`;
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
	now,
	onOpen,
	dimmed = false,
	accessibilityActions,
	onAccessibilityAction,
}: BoardRowProps): ReactElement {
	const { palette } = useColors();
	const scale = useTextScale();
	const { row, state } = item;
	const signal = variant === "signal";
	const needsYou = signal && bandOf(state) === "needsYou";
	const why = signal ? whyLine(item) : null;
	const last = signal ? lastLine(row, usual, hostLabel) : null;
	const age = relativeAge(row.updated_at, now);
	const word = stateWord(state);
	// A working row with nothing more specific to say reads "Working" once.
	const reason = why && why.text !== word ? why.text : undefined;
	const label = [row.title, word, reason, age && spokenAge(age)].filter(Boolean).join(", ");
	const lineOne = 22 * scale;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			accessibilityState={{ busy: dimmed }}
			accessibilityActions={accessibilityActions}
			onAccessibilityAction={onAccessibilityAction}
			onPress={() => onOpen(row)}
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
			<View style={{ height: lineOne, justifyContent: "center" }}>
				<StateMark state={state} moving={moving} connected={connected} />
			</View>
			<View style={{ flex: 1, minWidth: 0 }}>
				<View style={{ flexDirection: "row", alignItems: "flex-start", columnGap: 8 }}>
					<Text
						allowFontScaling={Platform.OS !== "ios"}
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
					{hasDraft || age ? (
						<View style={{ height: lineOne, flexDirection: "row", alignItems: "center", columnGap: 6 }}>
							{hasDraft ? (
								<View style={{ backgroundColor: palette.accentBg, borderRadius: 4, paddingHorizontal: 4 }}>
									<Text
										allowFontScaling={Platform.OS !== "ios"}
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
									allowFontScaling={Platform.OS !== "ios"}
									style={{ fontSize: 13 * scale, color: palette.inkLow, fontVariant: ["tabular-nums"] }}
								>
									{age}
								</Text>
							) : null}
						</View>
					) : null}
				</View>
				{why ? (
					<Text
						allowFontScaling={Platform.OS !== "ios"}
						numberOfLines={needsYou ? 2 : 1}
						ellipsizeMode="tail"
						style={{
							marginTop: 2,
							fontSize: 15 * scale,
							lineHeight: 20 * scale,
							color: why.word ? palette.inkHi : palette.inkMid,
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

/** A glyph and a fact on the last line, which never wraps. A `shrink` fact
 * gives up width when the line runs out of room, tail-truncating its text;
 * the others keep theirs. Only the task line shrinks: its title comes after
 * the "Task 4 of 7 · " prefix, so the title truncates first (spec 7.2). */
function Fact({
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
				allowFontScaling={Platform.OS !== "ios"}
				numberOfLines={1}
				ellipsizeMode="tail"
				style={{ flexShrink, fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow }}
			>
				{text}
			</Text>
		</View>
	);
}
