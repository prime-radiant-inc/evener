// A run of steps on the transcript (spec 8.2): one line that folds the steps
// ("▸ 12 steps · 8m · read 6 files, ran go test (2 failed)"), and, expanded,
// one line per step with its intent, its target and a status mark. A live run
// (the last run of the turn in progress) never folds.
import { parseArgs, str } from "@evener/appwire-client";
import { SymbolView } from "expo-symbols";
import { Fragment } from "react";
import { Platform, Pressable, Text, View } from "react-native";
import { typeRoles } from "../design/tokens";
import type { RunStep, TimelineRow } from "../timeline";
import { useColors, useTextScale } from "../ui";
import { runHeadText, runPartFailedText, runSummary, runSummaryText } from "./transcriptRows";

type Run = Extract<TimelineRow, { kind: "run" }>;

/** What the step acted on: the command for a shell step, else the file or
 * path it named. */
function stepTarget(step: RunStep): string | undefined {
	const args = parseArgs(step.detail.arguments);
	return step.label === "shell" ? str(args, "command") : (str(args, "file_path") ?? str(args, "path"));
}

function StepLine({ step, onStep }: { step: RunStep; onStep: (step: RunStep) => void }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const intent = step.detail.description || step.label;
	const target = stepTarget(step);
	const failed = step.state === "failed";
	return (
		<Pressable
			accessibilityLabel={[intent, target, failed ? "failed" : "done"].filter(Boolean).join(", ")}
			onPress={() => onStep(step)}
			style={{ flexDirection: "row", alignItems: "flex-start", gap: 8, paddingVertical: 4 }}
		>
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
				<Text allowFontScaling={Platform.OS !== "ios"} style={{ fontSize: 14 * scale, lineHeight: 19 * scale, color: palette.inkHi }}>
					{intent}
				</Text>
				{target ? (
					<Text
						allowFontScaling={Platform.OS !== "ios"}
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
		</Pressable>
	);
}

export function RunRow({
	run,
	live,
	expanded,
	onToggle,
	onStep,
}: {
	run: Run;
	live: boolean;
	expanded: boolean;
	onToggle: () => void;
	onStep: (step: RunStep) => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const summary = runSummary(run.steps);
	const open = live || expanded;
	const line = (
		<Text
			allowFontScaling={Platform.OS !== "ios"}
			style={{ fontSize: 14 * scale, lineHeight: 19 * scale, color: palette.inkMid, fontVariant: ["tabular-nums"] }}
		>
			{live ? null : open ? "▾ " : "▸ "}
			{`${runHeadText(summary)} · `}
			{summary.parts.map((part, index) => (
				<Fragment key={part.text}>
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
				<View accessible accessibilityLabel={runSummaryText(summary)} style={{ minHeight: 44, justifyContent: "center" }}>
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
			{open ? run.steps.map((step) => <StepLine key={step.id} step={step} onStep={onStep} />) : null}
		</View>
	);
}
