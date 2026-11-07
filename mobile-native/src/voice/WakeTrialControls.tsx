import { useEffect, useState } from "react";
import { View } from "react-native";
import { Button } from "../sheet/Grouped";
import { Copy, ErrorMessage, useColors } from "../ui";
import type { VoiceClient, VoiceSnapshot } from "./voiceClient";
import { qualificationProgress, summarizeWakeTrials, type WakeTrialLabel } from "./wakeTrials";

/** The qualification gates as meters: recorded evidence against the protocol's
 * targets, so a glance says how far the phone is from qualifying the model. */
export function WakeTrialControls({ client, state }: { client: VoiceClient; state: VoiceSnapshot }) {
	const [expanded, setExpanded] = useState(false);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const wake = state.wake;
	const running = wake?.trials.at(-1)?.running === true;
	useEffect(() => {
		if (running) setExpanded(true);
	}, [running]);
	if (!wake) return null;
	const attempt = wake.trials.at(-1);
	const summary = summarizeWakeTrials(wake.trials);
	const progress = qualificationProgress(summary);
	const command = async (label?: WakeTrialLabel) => {
		setBusy(true);
		setError(null);
		try {
			if (label) await client.finishWakeTrial(label);
			else await client.startWakeTrial();
		} catch {
			setError("Trial could not change. Turn voice on and wait for listening before starting.");
		} finally {
			setBusy(false);
		}
	};
	return (
		<View testID="wake-trial-controls" style={{ gap: 10 }}>
			<Button
				text
				quiet
				label={expanded ? "Hide wake tests" : "Test wake word"}
				expanded={expanded}
				onPress={() => setExpanded(!expanded)}
			/>
			{expanded ? (
				<>
					<Meter
						label="Intended wakes"
						value={`${progress.trials.done} of ${progress.trials.target}`}
						fraction={progress.trials.fraction}
						met={progress.trials.fraction >= 1}
					/>
					<Meter
						label="Detected"
						value={
							progress.detection.of
								? `${Math.round(progress.detection.fraction * 100)}% of ${progress.detection.of} · goal ${Math.round(
										progress.detection.target * 100,
									)}%`
								: `goal ${Math.round(progress.detection.target * 100)}%`
						}
						fraction={progress.detection.fraction}
						tick={progress.detection.of ? progress.detection.target : undefined}
						met={progress.trials.fraction >= 1 && progress.detection.fraction >= progress.detection.target}
					/>
					<Meter
						label="Background audio"
						value={`${progress.negatives.hours.toFixed(2)} of ${progress.negatives.target} h`}
						fraction={progress.negatives.fraction}
						met={progress.negatives.fraction >= 1 && summary.falseActivations === 0}
					/>
					<Copy muted>
						False alarms: {summary.falseActivations} across {summary.negativeAttempts} background attempts
					</Copy>
					<Copy muted>
						Wake: {wake.phrase || "turn voice on to load the model"} ·{" "}
						{wake.score != null
							? `Last score: ${wake.score.toFixed(4)}`
							: attempt?.lastScore != null
								? `Last score: ${attempt.lastScore.toFixed(4)}`
								: "no score yet"}
					</Copy>
					{attempt ? (
						<Copy muted>
							Attempt {wake.trials.length} · {attempt.detections} detections ·{" "}
							{attempt.label ?? (attempt.running ? "running" : "unmarked")}
							{attempt.interrupted ? " · interrupted" : ""} · {(attempt.scoredSamples / 16000 / 3600).toFixed(3)} h
							scored
						</Copy>
					) : null}
					<View style={{ flexDirection: "row", flexWrap: "wrap", alignItems: "center", gap: 4 }}>
						<Button
							primary
							compact
							label="Start test"
							disabled={busy || !state.capturing || state.state !== "quiet" || !!(attempt && !attempt.label)}
							onPress={() => void command()}
						/>
						{(
							[
								["positive", "Intended"],
								["negative", "Background"],
								["falseActivation", "False alarm"],
							] as const
						).map(([label, title]) => (
							<Button
								key={label}
								text
								quiet
								label={title}
								disabled={busy || !attempt || !!attempt.label}
								onPress={() => void command(label)}
							/>
						))}
					</View>
					<Copy muted>
						Say “{wake.phrase || "the wake word"}”, then mark Intended, even if it missed. Background is for other
						audio. Tests stay on this iPhone and never start a conversation; results apply to this iPhone and this wake
						word.
					</Copy>
				</>
			) : null}
			<ErrorMessage message={error || (wake.error ? `Wake paused (${wake.error}).` : "")} />
		</View>
	);
}

function Meter({
	label,
	value,
	fraction,
	met = false,
	tick,
}: {
	label: string;
	value: string;
	fraction: number;
	/** The protocol target is met: the bar reads as working, not just filled. */
	met?: boolean;
	/** A goal the meter must reach, drawn as a mark on the track. */
	tick?: number;
}) {
	const { palette } = useColors();
	const fill = met ? palette.alive : palette.accentFill;
	return (
		<View style={{ gap: 4 }}>
			<View style={{ flexDirection: "row", alignItems: "baseline", justifyContent: "space-between", gap: 8 }}>
				<Copy muted>{label}</Copy>
				<Copy muted>{value}</Copy>
			</View>
			<View
				accessibilityRole="progressbar"
				accessibilityLabel={label}
				accessibilityValue={{ min: 0, max: 100, now: Math.round(fraction * 100) }}
				style={{ height: 3, borderRadius: 2, backgroundColor: palette.edge, overflow: "hidden" }}
			>
				<View style={{ height: 3, width: `${Math.round(fraction * 100)}%`, backgroundColor: fill }} />
				{tick != null && tick < 1 ? (
					<View
						style={{
							position: "absolute",
							top: 0,
							bottom: 0,
							left: `${Math.round(tick * 100)}%`,
							width: 1,
							backgroundColor: palette.edgeStrong,
						}}
					/>
				) : null}
			</View>
		</View>
	);
}
