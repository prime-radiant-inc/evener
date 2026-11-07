export type WakeTrialLabel = "positive" | "negative" | "falseActivation";
export interface WakeTrialAttempt {
	id: string;
	modelId: string;
	phrase: string;
	settings: {
		threshold: number;
		releaseThreshold: number;
		hopSamples: number;
		holdoffMs: number;
		embeddingFrames: number;
	};
	startedAt: number;
	running: boolean;
	interrupted: boolean;
	label?: WakeTrialLabel;
	detections: number;
	lastScore?: number;
	scoredSamples: number;
	droppedSamples: number;
}
export interface WakeSnapshot {
	state: "paused" | "warming" | "listening" | "holdoff";
	phrase: string;
	modelId: string;
	score: number | null;
	lastDetectionScore: number | null;
	detections: number;
	error: string;
	trials: WakeTrialAttempt[];
}

/** Derive counters from native persisted attempts, never count bridge deliveries.
 * Keep model/settings cohorts separate so replacing the classifier cannot inherit
 * a previous model's qualification. Incomplete/interrupted attempts stay visible
 * but cannot contribute to gates.
 */
export function summarizeWakeTrials(attempts: readonly WakeTrialAttempt[], cohort?: WakeTrialAttempt) {
	const selected = cohort ?? attempts.at(-1);
	const sameSettings = (attempt: WakeTrialAttempt) =>
		selected &&
		attempt.modelId === selected.modelId &&
		attempt.settings.threshold === selected.settings.threshold &&
		attempt.settings.releaseThreshold === selected.settings.releaseThreshold &&
		attempt.settings.hopSamples === selected.settings.hopSamples &&
		attempt.settings.holdoffMs === selected.settings.holdoffMs &&
		attempt.settings.embeddingFrames === selected.settings.embeddingFrames;
	const completed = attempts.filter((attempt) => sameSettings(attempt) && attempt.label && !attempt.interrupted);
	const positive = completed.filter((attempt) => attempt.label === "positive");
	const negative = completed.filter((attempt) => attempt.label !== "positive");
	return {
		positiveAttempts: positive.length,
		positiveDetected: positive.filter((attempt) => attempt.detections > 0).length,
		extraDetections: positive.reduce((sum, attempt) => sum + Math.max(0, attempt.detections - 1), 0),
		negativeAttempts: negative.length,
		falseActivations: negative.reduce((sum, attempt) => sum + attempt.detections, 0),
		markedFalseActivations: negative.filter((attempt) => attempt.label === "falseActivation").length,
		negativeHours: negative.reduce((sum, attempt) => sum + attempt.scoredSamples, 0) / 16000 / 3600,
	};
}

const TRIAL_TARGET = 60;
const DETECTION_TARGET = 0.95;
const NEGATIVE_HOURS_TARGET = 30;

/** The qualification gates as meters a surface can draw: what is recorded
 * against what the protocol asks for, each clipped to one full bar. */
export function qualificationProgress(summary: ReturnType<typeof summarizeWakeTrials>) {
	return {
		trials: {
			done: summary.positiveAttempts,
			target: TRIAL_TARGET,
			fraction: Math.min(1, summary.positiveAttempts / TRIAL_TARGET),
		},
		detection: {
			done: summary.positiveDetected,
			of: summary.positiveAttempts,
			target: DETECTION_TARGET,
			fraction: summary.positiveAttempts ? summary.positiveDetected / summary.positiveAttempts : 0,
		},
		negatives: {
			hours: summary.negativeHours,
			target: NEGATIVE_HOURS_TARGET,
			fraction: Math.min(1, summary.negativeHours / NEGATIVE_HOURS_TARGET),
		},
	};
}
