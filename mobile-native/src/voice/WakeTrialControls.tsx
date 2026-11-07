import { useState } from "react";
import { View } from "react-native";
import { HeaderButton } from "../sheet/HeaderButton";
import { Copy, ErrorMessage } from "../ui";
import type { VoiceClient, VoiceSnapshot } from "./voiceClient";
import { summarizeWakeTrials, type WakeTrialLabel } from "./wakeTrials";

export function WakeTrialControls({ client, state }: { client: VoiceClient; state: VoiceSnapshot }) {
	const [expanded, setExpanded] = useState(false);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const wake = state.wake;
	if (!wake) return null;
	const attempt = wake.trials.at(-1);
	const summary = summarizeWakeTrials(wake.trials);
	const command = async (label?: WakeTrialLabel) => {
		setBusy(true);
		setError(null);
		try {
			if (label) await client.finishWakeTrial(label);
			else await client.startWakeTrial();
		} catch {
			setError("Trial could not change. Arm voice and wait for quiet local listening before starting.");
		} finally {
			setBusy(false);
		}
	};
	return (
		<View testID="wake-trial-controls">
			<HeaderButton
				label={expanded ? "Hide wake trials" : "Debug · Wake trials"}
				onPress={() => setExpanded(!expanded)}
			/>
			{expanded ? (
				<>
					<Copy>Wake preview: {wake.phrase || "Arm to load the model"}</Copy>
					<Copy muted>Model: {wake.modelId || "Not loaded"}</Copy>
					<Copy muted>
						Trials stay local, never open a conversation, and save counts on this phone. Wait for Listening before
						speaking. Mark Positive for an intended wake, even if it was missed. Mark Negative for background audio, or
						False activation for an unintended wake. Finish before starting the next attempt.
					</Copy>
					<Copy>
						{wake.state === "listening" ? "Listening" : wake.state} · Last score:{" "}
						{(wake.score ?? attempt?.lastScore)?.toFixed(4) ?? "—"}
					</Copy>
					{attempt ? (
						<Copy>
							Attempt {wake.trials.length}: {attempt.detections} detections ·{" "}
							{attempt.label ?? (attempt.running ? "running" : "unmarked")}
							{attempt.interrupted ? " · interrupted, excluded from totals" : ""} · Scored audio:{" "}
							{(attempt.scoredSamples / 16000 / 3600).toFixed(3)} h · Dropped: {attempt.droppedSamples} samples
						</Copy>
					) : null}
					<View style={{ flexDirection: "row", flexWrap: "wrap" }}>
						<HeaderButton
							label="Start wake trial"
							disabled={busy || !state.capturing || state.state !== "quiet" || !!(attempt && !attempt.label)}
							onPress={() => void command()}
						/>
						{(
							[
								["positive", "Mark positive"],
								["negative", "Mark negative"],
								["falseActivation", "Mark false activation"],
							] as const
						).map(([label, title]) => (
							<HeaderButton
								key={label}
								label={title}
								disabled={busy || !attempt || !!attempt.label}
								onPress={() => void command(label)}
							/>
						))}
					</View>
					<Copy>
						Current trial model/settings: {summary.positiveDetected}/{summary.positiveAttempts} intended wakes detected
						· Extra detections: {summary.extraDetections} · Negative audio: {summary.negativeHours.toFixed(3)} h · False
						activations: {summary.falseActivations} ({summary.markedFalseActivations} marked attempts)
					</Copy>
					<Copy muted>
						Targets: ≥60 intended trials, ≥95% detection, 30 h negative audio. Warmup and capture gaps do not count as
						scored time. These counters do not certify acoustic accuracy.
					</Copy>
					<ErrorMessage message={error || (wake.error ? `Wake paused (${wake.error}).` : null)} />
				</>
			) : null}
		</View>
	);
}
