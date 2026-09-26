import type { NavigationSessionSummary } from "@evener/appwire-client";
import { relativeAge } from "@evener/appwire-client/state/navigation";
import { SymbolView } from "expo-symbols";
import type { ReactElement } from "react";
import { Platform, Pressable, Text, useWindowDimensions, View } from "react-native";
import { useColors } from "../ui";
import { bandOf, type ClassifiedRow, lastLine, stateWord, type Usual, whyLine } from "./attention";
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
	now: number;
	onOpen: (row: NavigationSessionSummary) => void;
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
 * between rows, inset to the title (16 + 28 + 10 = 54pt). */
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
}: BoardRowProps): ReactElement {
	const { palette } = useColors();
	const { fontScale } = useWindowDimensions();
	const scale = Platform.OS === "ios" ? fontScale : 1;
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
						{last.project ? <Place glyph="folder" text={last.project} scale={scale} /> : null}
						{last.host ? <Place glyph="server.rack" text={last.host} scale={scale} /> : null}
					</View>
				) : null}
			</View>
		</Pressable>
	);
}

/** A glyph and a name on the last line, which never wraps. */
function Place({ glyph, text, scale }: { glyph: "folder" | "server.rack"; text: string; scale: number }) {
	const { palette } = useColors();
	return (
		<View style={{ flexDirection: "row", alignItems: "center", columnGap: 3, flexShrink: 1 }}>
			<SymbolView name={glyph} size={13 * scale} tintColor={palette.inkLow} />
			<Text
				allowFontScaling={Platform.OS !== "ios"}
				numberOfLines={1}
				ellipsizeMode="tail"
				style={{ flexShrink: 1, fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow }}
			>
				{text}
			</Text>
		</View>
	);
}
