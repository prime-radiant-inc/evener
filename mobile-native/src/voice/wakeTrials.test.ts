import { expect, it } from "vitest";
import recorded from "../../modules/evener-voice/runtime-tests/fixtures/wake-trials.json";
import { qualificationProgress, summarizeWakeTrials, type WakeTrialAttempt } from "./wakeTrials";

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

const empty = {
	positiveAttempts: 0,
	positiveDetected: 0,
	extraDetections: 0,
	negativeAttempts: 0,
	falseActivations: 0,
	markedFalseActivations: 0,
	negativeHours: 0,
};
it("starts every qualification meter empty", () => {
	const progress = qualificationProgress(empty);
	expect(progress.trials).toMatchObject({ done: 0, target: 60, fraction: 0 });
	expect(progress.detection).toMatchObject({ done: 0, of: 0, target: 0.95, fraction: 0 });
	expect(progress.negatives).toMatchObject({ hours: 0, target: 30, fraction: 0 });
});
it("fills the meters in proportion to the recorded evidence", () => {
	const progress = qualificationProgress({ ...empty, positiveAttempts: 30, positiveDetected: 30, negativeHours: 7.5 });
	expect(progress.trials.fraction).toBeCloseTo(0.5);
	expect(progress.detection.fraction).toBeCloseTo(1);
	expect(progress.negatives.fraction).toBeCloseTo(0.25);
});
it("counts a miss against detection and keeps every meter at full once passed", () => {
	const partial = qualificationProgress({ ...empty, positiveAttempts: 20, positiveDetected: 15 });
	expect(partial.detection.fraction).toBeCloseTo(0.75);
	const passed = qualificationProgress({ ...empty, positiveAttempts: 80, positiveDetected: 80, negativeHours: 40 });
	expect(passed.trials.fraction).toBe(1);
	expect(passed.negatives.fraction).toBe(1);
});
