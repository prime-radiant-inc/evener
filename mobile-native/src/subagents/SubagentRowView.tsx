// One row of the Subagents list (spec 9): its state mark, title and time in
// that state, the why line, and a last line of who started it, its model, its
// branch and its tokens.
import { SymbolView } from "expo-symbols";
import { Fragment, memo, type ReactNode } from "react";
import { Pressable, Text, View } from "react-native";
import { fonts } from "../design/tokens";
import { compactDuration, spokenDuration } from "../session/format";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import {
	type SubagentLastLine,
	type SubagentRow,
	subagentLastLine,
	subagentStateWord,
	subagentWhy,
	timeInState,
} from "./subagentModel";

export interface SubagentRowViewProps {
	row: SubagentRow;
	now: number;
	coordinatorModel: string | null;
	modelName(model: string): string;
	/** Replaces the why line: PR 3 passes the stop request's words. */
	note?: string;
	onOpen(row: SubagentRow): void;
}

export const SubagentRowView = memo(function SubagentRowView({
	row,
	now,
	coordinatorModel,
	modelName,
	note,
	onOpen,
}: SubagentRowViewProps) {
	const { palette } = useColors();
	const scale = useTextScale();
	const time = timeInState(row, now);
	const why = subagentWhy(row, now);
	const last = subagentLastLine(row, coordinatorModel, modelName);
	// Preserve the outcome and cause; a note replaces the why line.
	const failure = note === undefined && why.word === "Failed";
	const whyLine = note ?? why.text;
	const label = [row.title, subagentStateWord(row.state), whyLine, time === null ? null : spokenDuration(time)]
		.filter((part) => part)
		.join(", ");
	const small = { fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow };
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			onPress={() => onOpen(row)}
			style={({ pressed }) => ({
				flexDirection: "row",
				alignItems: "flex-start",
				columnGap: 10,
				paddingHorizontal: 16,
				paddingTop: 11,
				paddingBottom: 12,
				minHeight: 44,
				backgroundColor: pressed ? palette.pressed : palette.page,
			})}
		>
			<View style={{ width: 28, height: 22 * scale, alignItems: "center", justifyContent: "center" }}>
				{row.state === "running" ? (
					<SymbolView name="circle.fill" size={8} tintColor={palette.alive} />
				) : row.state === "failed" ? (
					<SymbolView name="xmark.octagon.fill" size={20} tintColor={palette.inkLow} />
				) : (
					<SymbolView name="checkmark" size={14} tintColor={palette.inkLow} />
				)}
			</View>
			<View style={{ flex: 1, minWidth: 0, gap: 2 }}>
				<View style={{ flexDirection: "row", alignItems: "baseline", columnGap: 8 }}>
					<Text
						allowFontScaling={allowFontScaling}
						numberOfLines={1}
						style={{ flex: 1, fontSize: 17 * scale, lineHeight: 22 * scale, fontWeight: "600", color: palette.inkHi }}
					>
						{row.title}
					</Text>
					{time === null ? null : (
						<Text allowFontScaling={allowFontScaling} style={{ ...small, fontVariant: ["tabular-nums"] }}>
							{compactDuration(time)}
						</Text>
					)}
				</View>
				{failure || whyLine ? (
					<Text
						allowFontScaling={allowFontScaling}
						numberOfLines={failure ? 2 : 1}
						style={{ fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.inkMid }}
					>
						{failure ? (
							<>
								<Text style={{ color: palette.inkMid }}>Failed</Text>
								{whyLine ? `: ${whyLine}` : ""}
							</>
						) : (
							whyLine
						)}
					</Text>
				) : null}
				{last ? <LastLine last={last} /> : null}
			</View>
		</Pressable>
	);
});

/** "from <parent> · <model> · ⎇ <branch> · 1.2M tokens", 13/18 ink-low, one line. */
function LastLine({ last }: { last: SubagentLastLine }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const small = { fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow };
	const parts: { key: string; node: ReactNode }[] = [];
	if (last.parent)
		parts.push({
			key: "parent",
			node: (
				<Text
					allowFontScaling={allowFontScaling}
					numberOfLines={1}
					style={{ ...small, flexShrink: 1 }}
				>{`from ${last.parent}`}</Text>
			),
		});
	if (last.model)
		parts.push({
			key: "model",
			node: (
				<Text allowFontScaling={allowFontScaling} numberOfLines={1} style={{ ...small, flexShrink: 1 }}>
					{last.model}
				</Text>
			),
		});
	if (last.branch)
		parts.push({
			key: "branch",
			node: (
				<>
					<SymbolView name="arrow.triangle.branch" size={12} tintColor={palette.inkLow} />
					<Text
						allowFontScaling={allowFontScaling}
						numberOfLines={1}
						style={{ ...small, flexShrink: 1, fontFamily: fonts.mono, fontSize: 12 * scale }}
					>
						{last.branch}
					</Text>
				</>
			),
		});
	if (last.tokens)
		parts.push({
			key: "tokens",
			node: (
				<Text allowFontScaling={allowFontScaling} style={{ ...small, fontVariant: ["tabular-nums"] }}>
					{last.tokens}
				</Text>
			),
		});
	return (
		<View style={{ flexDirection: "row", alignItems: "center", columnGap: 4, overflow: "hidden" }}>
			{parts.map((part, index) => (
				<Fragment key={part.key}>
					{index > 0 ? (
						<Text allowFontScaling={allowFontScaling} style={small}>
							·
						</Text>
					) : null}
					{part.node}
				</Fragment>
			))}
		</View>
	);
}
