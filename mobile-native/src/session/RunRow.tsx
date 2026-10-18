// A run of steps on the transcript (spec 8.2): one line that folds the steps
// ("▸ 12 steps · 8m · read 6 files, ran go test (2 failed)"), and, expanded,
// one line per step with its intent, its target and a status mark. A step
// with evidence opens it under its line (StepEvidence). A run held open while
// it is live (the last run of the turn in progress, at the levels that show
// tool calls) has no fold control until it finishes.
import { composeStepWords, isExitOnly } from "@evener/appwire-client";
import { SymbolView } from "expo-symbols";
import { Fragment } from "react";
import { Pressable, Text, View } from "react-native";
import { FoldChevron } from "../board/BoardRow";
import { scaledType, typeRoles } from "../design/tokens";
import { toggleDisclosure, useDisclosureOpen } from "../nativeDisclosure";
import type { RunStep, TimelineRow } from "../timeline";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { rowDisclosureIds } from "./disclosureKeys";
import { ExitMark } from "./ExitMark";
import { useStepEvidence } from "./useStepEvidence";
import { StepEvidence } from "./StepEvidence";
import { runHeadText, runPartFailedText, runSummary, runSummaryText, stepWords } from "./transcriptRows";

type Run = Extract<TimelineRow, { kind: "run" }>;

interface StepPlace {
	hubId: string;
	sessionRef: string;
	/** Evidence starts open, at the levels that show output as it arrives. */
	evidenceOpenByDefault: boolean;
}

function StepLine({ step, hubId, sessionRef, evidenceOpenByDefault }: { step: RunStep } & StepPlace) {
	const { palette } = useColors();
	const scale = useTextScale();
	// The model's own intent for the step, or else the step's words (the
	// package's toolStepWords), which name what it acted on. Either way the
	// thing it acted on is its words' target, set in Menlo (spec § Activity
	// run): on its own line under an intent, or inside the words' sentence.
	const ownIntent = step.detail.description;
	const { words } = step.detail;
	const target = words?.target;
	const machine = { ...scaledType(typeRoles.machine, scale), color: palette.inkMid };
	const evidence = useStepEvidence(step);
	const hasEvidence = evidence.length > 0 || (step.images?.length ?? 0) > 0;
	const [disclosureId = ""] = rowDisclosureIds(hubId, sessionRef, step);
	const open = useDisclosureOpen(disclosureId, evidenceOpenByDefault) && hasEvidence;
	const failed = step.state === "failed";
	// An otherwise-clean call whose process exited nonzero: not a failure
	// (hasItemFailure no longer reads the exit, Jesse 2026-10-16), but the exit
	// stays visible as a small x in attention ink, not the danger octagon.
	const exitOnly = isExitOnly({ status: step.state, error: step.detail.error, exitCode: step.detail.exitCode });
	// sessionRows leaves every running step to the tray, so a run holds only
	// completed, exit-only and failed steps: three marks, chosen in that order.
	// The exit-only small x is visible without reading as a failure.
	const mark = failed ? (
		<SymbolView name="xmark.octagon.fill" tintColor={palette.dangerInk} size={15 * scale} />
	) : exitOnly ? (
		<ExitMark size={15 * scale} />
	) : (
		<SymbolView name="checkmark.circle.fill" tintColor={palette.inkLow} size={15 * scale} />
	);
	const line = (
		<>
			<View style={{ paddingTop: 2 * scale }}>{mark}</View>
			<View style={{ flex: 1, minWidth: 0 }}>
				<Text
					allowFontScaling={allowFontScaling}
					style={{ fontSize: 14 * scale, lineHeight: 19 * scale, color: palette.inkHi }}
				>
					{ownIntent ||
						(words ? (
							<>
								{words.verb}
								{target ? (
									<>
										{" "}
										<Text style={machine}>{target}</Text>
									</>
								) : null}
								{words.after ? ` ${words.after}` : null}
								{words.detail ? ` · ${words.detail}` : null}
							</>
						) : (
							stepWords(step)
						))}
				</Text>
				{ownIntent && target ? (
					<Text allowFontScaling={allowFontScaling} numberOfLines={2} style={machine}>
						{target}
					</Text>
				) : null}
			</View>
			{hasEvidence ? (
				<View style={{ paddingTop: 2 * scale }}>
					<SymbolView name={open ? "chevron.down" : "chevron.right"} tintColor={palette.inkLow} size={13 * scale} />
				</View>
			) : null}
		</>
	);
	// The label reads what the line draws: the same bounded words, composed.
	// An exit-only call is still done - never "failed" - and the word names the
	// exit so the small x is not the only place it is said.
	const sentence = words ? composeStepWords(words) : stepWords(step);
	const stateWord = failed ? "failed" : exitOnly ? `exit ${step.detail.exitCode}` : "done";
	const label = [ownIntent ? [ownIntent, target] : [sentence], stateWord].flat().filter(Boolean).join(", ");
	const lineStyle = { flexDirection: "row", alignItems: "flex-start", gap: 8, paddingVertical: 4 } as const;
	return (
		<View>
			{hasEvidence ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={label}
					accessibilityState={{ expanded: open }}
					onPress={() => toggleDisclosure(disclosureId, evidenceOpenByDefault)}
					style={lineStyle}
				>
					{line}
				</Pressable>
			) : (
				<View accessible accessibilityLabel={label} style={lineStyle}>
					{line}
				</View>
			)}
			{open ? (
				<View style={{ paddingLeft: 23 * scale, paddingBottom: 8 }}>
					<StepEvidence step={step} evidence={evidence} hubId={hubId} />
				</View>
			) : null}
		</View>
	);
}

export function RunRow({
	run,
	live,
	expanded,
	onToggle,
	hubId,
	sessionRef,
	evidenceOpenByDefault = false,
}: {
	run: Run;
	live: boolean;
	expanded: boolean;
	onToggle: () => void;
	hubId: string;
	sessionRef: string;
	evidenceOpenByDefault?: boolean;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const summary = runSummary(run.steps);
	const open = live || expanded;
	const line = (
		<View style={{ flexDirection: "row", alignItems: "center", gap: 5 }}>
			{live ? null : <FoldChevron folded={!open} />}
			<Text
				style={{
					flex: 1,
					minWidth: 0,
					fontSize: 14 * scale,
					lineHeight: 19 * scale,
					color: palette.inkMid,
					fontVariant: ["tabular-nums"],
				}}
				allowFontScaling={allowFontScaling}
			>
				{`${runHeadText(summary)} · `}
				{summary.parts.map((part, index) => (
					<Fragment key={part.key}>
						{index > 0 ? ", " : null}
						{part.text}
						{part.failed > 0 ? <Text style={{ color: palette.dangerInk }}>{runPartFailedText(part)}</Text> : null}
					</Fragment>
				))}
			</Text>
		</View>
	);
	return (
		<View>
			{live ? (
				<View
					accessible
					accessibilityLabel={runSummaryText(summary)}
					style={{ minHeight: 44, justifyContent: "center" }}
				>
					{line}
				</View>
			) : (
				<Pressable
					testID="run-fold"
					accessibilityRole="button"
					accessibilityLabel={`${runSummaryText(summary)}, ${open ? "expanded" : "collapsed"}`}
					accessibilityState={{ expanded: open }}
					onPress={onToggle}
					style={{ minHeight: 44, justifyContent: "center" }}
				>
					{line}
				</Pressable>
			)}
			{open
				? run.steps.map((step) => (
						<StepLine
							key={step.id}
							step={step}
							hubId={hubId}
							sessionRef={sessionRef}
							evidenceOpenByDefault={evidenceOpenByDefault}
						/>
					))
				: null}
		</View>
	);
}
