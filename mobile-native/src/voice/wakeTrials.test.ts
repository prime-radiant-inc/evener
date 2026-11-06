import { expect, it } from "vitest";
import recorded from "../../modules/evener-voice/runtime-tests/fixtures/wake-trials.json";
import { summarizeWakeTrials, type WakeTrialAttempt } from "./wakeTrials";

const attempts = recorded as WakeTrialAttempt[];
it("counts real WAV trials once per attempt and excludes interrupted audio", () => {
	const summary = summarizeWakeTrials(attempts);
	expect(summary).toMatchObject({
		positiveAttempts: 1,
		positiveDetected: 1,
		extraDetections: 0,
		negativeAttempts: 1,
		falseActivations: 0,
	});
	expect(summary.negativeHours).toBeCloseTo(6.4 / 3600);
	// Re-reading a full native snapshot must not add a second detection.
	expect(summarizeWakeTrials(JSON.parse(JSON.stringify(attempts)))).toEqual(summary);
});
it("keeps manual positive/negative/false labels distinct from measured detections", () => {
	const positive = attempts[0];
	const negative = attempts[2];
	const summary = summarizeWakeTrials([
		{ ...positive, label: "falseActivation" },
		{ ...negative, label: "positive" },
	]);
	expect(summary).toMatchObject({
		positiveAttempts: 1,
		positiveDetected: 0,
		falseActivations: 1,
		markedFalseActivations: 1,
	});
});
it("does not transfer qualification across classifier or threshold changes", () => {
	const changed = { ...attempts[0], modelId: "hey-evener-custom" };
	expect(summarizeWakeTrials([...attempts, changed]).positiveAttempts).toBe(1);
	const threshold = { ...attempts[0], settings: { ...attempts[0].settings, threshold: 0.8 } };
	expect(summarizeWakeTrials([...attempts, threshold]).negativeAttempts).toBe(0);
	expect(summarizeWakeTrials([{ ...attempts[0], label: undefined, running: true }]).positiveAttempts).toBe(0);
});
