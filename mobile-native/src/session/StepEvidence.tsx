// A step's evidence, open under its line in a run (spec 8.2): command output
// in a Menlo inset (the first 40 lines, then the full log), an edit as a diff
// with the web's add and delete washes, the file a write wrote, an error, and
// the images the step produced.
import { formatByteCount, lineCount } from "@evener/appwire-client";
import { type ReactNode, useMemo, useState } from "react";
import { Pressable, ScrollView, Text, View } from "react-native";
import { type AnsiLine, parseAnsiLines } from "../../../cmd/evener-hub/frontend/src/widgets/codeblock/ansi";
import { AnsiOutputLine } from "../AnsiOutputLine";
import { MarkdownResponse } from "../MarkdownResponse";
import { typeRoles } from "../design/tokens";
import { TranscriptImages } from "../TranscriptImages";
import type { RunStep } from "../timeline";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { EVIDENCE_PREVIEW_LINES, type Evidence } from "./evidence";
import { LogViewer } from "./LogViewer";
import { stepWords } from "./transcriptRows";

type Palette = ReturnType<typeof useColors>["palette"];

function machineText(scale: number) {
	return {
		fontFamily: typeRoles.machine.fontFamily,
		fontSize: typeRoles.machine.fontSize * scale,
		lineHeight: typeRoles.machine.lineHeight * scale,
	};
}

// Output and diff lines repeat ("ok", "+}"), so each is keyed by where it
// starts in the text, which is unique and never changes for a given text.
function keyedByOffset<T>(lines: readonly T[], length: (line: T) => number): { key: number; line: T }[] {
	let offset = 0;
	return lines.map((line) => {
		const key = offset;
		offset += length(line) + 1;
		return { key, line };
	});
}

const ansiLineLength = (line: AnsiLine) => line.reduce((sum, run) => sum + run.text.length, 0);

function Inset({ children }: { children: ReactNode }) {
	const { palette } = useColors();
	return (
		<View style={{ backgroundColor: palette.inset, borderRadius: 12, borderCurve: "continuous", overflow: "hidden" }}>
			<ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={{ padding: 12 }}>
				<View>{children}</View>
			</ScrollView>
		</View>
	);
}

function Output({ text, lines, title }: { text: string; lines: number; title: string }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const [viewing, setViewing] = useState(false);
	// Only the preview's own lines are parsed: the output can run to 64 KiB.
	const preview = useMemo(
		() => keyedByOffset(parseAnsiLines(text.split("\n", EVIDENCE_PREVIEW_LINES).join("\n")), ansiLineLength),
		[text],
	);
	const showAll = `Show all ${lines} lines`;
	return (
		<>
			<Inset>
				{preview.map(({ key, line }) => (
					<AnsiOutputLine key={key} line={line} />
				))}
			</Inset>
			{lines > EVIDENCE_PREVIEW_LINES ? (
				<Pressable accessibilityRole="button" accessibilityLabel={showAll} onPress={() => setViewing(true)}>
					<Text
						allowFontScaling={allowFontScaling}
						style={{ fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.accentInk }}
					>
						{showAll}
					</Text>
				</Pressable>
			) : null}
			{viewing ? <LogViewer title={title} text={text} onClose={() => setViewing(false)} /> : null}
		</>
	);
}

function diffLineStyle(line: string, palette: Palette) {
	if (line.startsWith("+++") || line.startsWith("---")) return { color: palette.inkLow };
	if (line.startsWith("+")) return { color: palette.inkHi, backgroundColor: palette.diffAdd };
	if (line.startsWith("-")) return { color: palette.inkHi, backgroundColor: palette.diffDel };
	return { color: palette.inkHi };
}

function Diff({ text, added, removed }: { text: string; added: number; removed: number }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const lines = useMemo(() => keyedByOffset(text.split("\n"), (line) => line.length), [text]);
	const count = { fontSize: 13 * scale, lineHeight: 18 * scale, fontVariant: ["tabular-nums" as const] };
	return (
		<>
			<View style={{ flexDirection: "row", gap: 8 }}>
				<Text allowFontScaling={allowFontScaling} style={{ ...count, color: palette.aliveInk }}>{`+${added}`}</Text>
				{/* A real minus sign (U+2212), as a count reads. */}
				<Text allowFontScaling={allowFontScaling} style={{ ...count, color: palette.dangerInk }}>{`−${removed}`}</Text>
			</View>
			<Inset>
				{lines.map(({ key, line }) => (
					<Text
						key={key}
						allowFontScaling={allowFontScaling}
						style={{ ...machineText(scale), ...diffLineStyle(line, palette) }}
					>
						{line}
					</Text>
				))}
			</Inset>
		</>
	);
}

export function EvidenceView({ evidence, title }: { evidence: Evidence; title: string }) {
	const { palette } = useColors();
	const scale = useTextScale();
	switch (evidence.kind) {
		case "output":
			return <Output text={evidence.text} lines={evidence.lines} title={title} />;
		case "diff":
			return <Diff text={evidence.text} added={evidence.added} removed={evidence.removed} />;
		case "wrote":
			return (
				<Text
					allowFontScaling={allowFontScaling}
					style={{ fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.inkHi }}
				>
					{"Wrote "}
					<Text style={{ ...machineText(scale), color: palette.inkMid }}>{evidence.path}</Text>
				</Text>
			);
		case "exit":
			return (
				<Text
					allowFontScaling={allowFontScaling}
					style={{ fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.dangerInk }}
				>
					{`Exited ${evidence.code}`}
				</Text>
			);
		case "note":
			return (
				<Text
					allowFontScaling={allowFontScaling}
					style={{ fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow }}
				>
					{evidence.text}
				</Text>
			);
		case "page":
			return (
				<View style={{ gap: 4 }}>
					{evidence.url || evidence.bytes !== undefined ? (
						<View style={{ flexDirection: "row", gap: 8, alignItems: "baseline" }}>
							{evidence.url ? (
								<Text
									allowFontScaling={allowFontScaling}
									numberOfLines={1}
									style={{ ...machineText(scale), color: palette.inkMid, flexShrink: 1 }}
								>
									{evidence.url}
								</Text>
							) : null}
							{evidence.bytes !== undefined ? (
								<Text
									allowFontScaling={allowFontScaling}
									style={{ fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow }}
								>
									{formatByteCount(evidence.bytes)}
								</Text>
							) : null}
						</View>
					) : null}
					<Text
						allowFontScaling={allowFontScaling}
						selectable
						style={{ fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.inkHi }}
					>
						{evidence.text}
					</Text>
				</View>
			);
		case "markdown":
			return (
				<View style={{ gap: 4 }}>
					<Text
						allowFontScaling={allowFontScaling}
						style={{ fontSize: 15 * scale, lineHeight: 20 * scale, fontWeight: "600", color: palette.inkHi }}
					>
						{evidence.title}
					</Text>
					<MarkdownResponse markdown={evidence.markdown} />
				</View>
			);
		case "json":
			return (
				<View style={{ gap: 4 }}>
					<Text
						allowFontScaling={allowFontScaling}
						style={{ fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow }}
					>
						{evidence.label}
					</Text>
					<Output text={evidence.text} lines={lineCount(evidence.text)} title={`${title}: ${evidence.label}`} />
				</View>
			);
		case "error":
			return (
				<View style={{ gap: 2 }}>
					<Text
						allowFontScaling={allowFontScaling}
						style={{ fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.dangerInk }}
					>
						{evidence.text}
					</Text>
					{evidence.exitCode === undefined ? null : (
						<Text
							allowFontScaling={allowFontScaling}
							style={{ fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow }}
						>
							{`Exit ${evidence.exitCode}`}
						</Text>
					)}
				</View>
			);
	}
}

/** Draws a step's evidence. Its row works the evidence out (useStepEvidence),
 * since the row also needs it to decide whether the step opens at all. */
export function StepEvidence({
	step,
	evidence,
	hubId,
}: {
	step: RunStep;
	evidence: readonly Evidence[];
	hubId: string;
}) {
	const title = step.detail.description || stepWords(step);
	return (
		<View style={{ gap: 8 }}>
			{evidence.map((item, index) => (
				// Two pieces can share a kind (a tool's arguments and result),
				// and a step's evidence is a fixed list, so its place is its key.
				// biome-ignore lint/suspicious/noArrayIndexKey: fixed list, see above
				<EvidenceView key={`${item.kind}:${index}`} evidence={item} title={title} />
			))}
			{step.images?.length ? <TranscriptImages images={step.images} hubId={hubId} /> : null}
		</View>
	);
}
