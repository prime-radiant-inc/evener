// A run of steps on the transcript (spec 8.2): one line that folds the steps
// ("▸ 12 steps · 8m · read 6 files, ran go test (2 failed)"), and, expanded,
// one line per step with its intent, its target and a status mark. A step
// with evidence opens it under its line (StepEvidence). A run held open while
// it is live (the last run of the turn in progress, at the levels that show
// tool calls) has no fold control until it finishes.
import { SymbolView } from "expo-symbols";
import { Fragment, useMemo } from "react";
import { Pressable, Text, View } from "react-native";
import { typeRoles } from "../design/tokens";
import { toggleDisclosure, useDisclosureOpen } from "../nativeDisclosure";
import type { RunStep, TimelineRow } from "../timeline";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { rowDisclosureIds } from "./disclosureKeys";
import { useStepEvidence } from "./useStepEvidence";
import { StepEvidence } from "./StepEvidence";
import { runHeadText, runPartFailedText, runSummary, runSummaryText, stepTarget } from "./transcriptRows";

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
	const intent = step.detail.description || step.label;
	// Rows re-render on every publish; keyed on the arguments text, a settled
	// step parses its arguments (up to 64 KiB) once rather than every time.
	const target = useMemo(() => stepTarget(step.label, step.detail.arguments), [step.label, step.detail.arguments]);
	const evidence = useStepEvidence(step);
	const hasEvidence = evidence.length > 0 || (step.images?.length ?? 0) > 0;
	const [disclosureId = ""] = rowDisclosureIds(hubId, sessionRef, step);
	const open = useDisclosureOpen(disclosureId, evidenceOpenByDefault) && hasEvidence;
	const failed = step.state === "failed";
	const line = (
		<>
			<View style={{ paddingTop: 2 * scale }}>
				{/* Two marks are enough: sessionRows leaves every running step to the
				tray, so a run holds only completed and failed steps. */}
				<SymbolView
					name={failed ? "xmark.octagon.fill" : "checkmark.circle.fill"}
					tintColor={failed ? palette.dangerInk : palette.inkLow}
					size={15 * scale}
				/>
			</View>
			<View style={{ flex: 1, minWidth: 0 }}>
				<Text
					allowFontScaling={allowFontScaling}
					style={{ fontSize: 14 * scale, lineHeight: 19 * scale, color: palette.inkHi }}
				>
					{intent}
				</Text>
				{target ? (
					<Text
						allowFontScaling={allowFontScaling}
						numberOfLines={2}
						style={{
							fontFamily: typeRoles.machine.fontFamily,
							fontSize: typeRoles.machine.fontSize * scale,
							lineHeight: typeRoles.machine.lineHeight * scale,
							color: palette.inkMid,
						}}
					>
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
	const label = [intent, target, failed ? "failed" : "done"].filter(Boolean).join(", ");
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
		<Text
			allowFontScaling={allowFontScaling}
			style={{ fontSize: 14 * scale, lineHeight: 19 * scale, color: palette.inkMid, fontVariant: ["tabular-nums"] }}
		>
			{live ? null : open ? "▾ " : "▸ "}
			{`${runHeadText(summary)} · `}
			{summary.parts.map((part, index) => (
				<Fragment key={part.family}>
					{index > 0 ? ", " : null}
					{part.text}
					{part.failed > 0 ? <Text style={{ color: palette.dangerInk }}>{runPartFailedText(part)}</Text> : null}
				</Fragment>
			))}
		</Text>
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
