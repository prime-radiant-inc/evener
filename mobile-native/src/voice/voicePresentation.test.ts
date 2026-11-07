import { expect, it } from "vitest";
import type { VoiceSnapshot } from "./voiceClient";
import { voicePresentation } from "./voicePresentation";

const quiet: VoiceSnapshot = {
	state: "quiet",
	capturing: true,
	cloudMicrophone: true,
	route: "iPhone speaker",
	speakerConsentRequired: false,
	error: "",
	interpreted: "",
	custody: "",
	canonical: "",
	generated: "",
	bindingId: "binding",
	provider: "openai",
};

it("offers arm as the one action while disarmed", () => {
	const presented = voicePresentation({ ...quiet, state: "disarmed", capturing: false, bindingId: "" });
	expect(presented.phase).toBe("disarmed");
	expect(presented.primary).toBe("arm");
	expect(presented.talkEnabled).toBe(false);
});

it("offers talk once armed and capturing, and keeps it off while paused", () => {
	expect(voicePresentation(quiet)).toMatchObject({ phase: "listening", primary: "talk", talkEnabled: true });
	expect(voicePresentation({ ...quiet, capturing: false })).toMatchObject({ phase: "listening", talkEnabled: false });
});

it("shows the heard-you state only when told a detection just happened", () => {
	expect(voicePresentation(quiet, true).phase).toBe("heard");
	expect(voicePresentation({ ...quiet, capturing: false }, true).phase).toBe("listening");
});

it("swaps the primary action to stop while a turn runs or the manager speaks", () => {
	expect(voicePresentation({ ...quiet, state: "conversing" })).toMatchObject({ phase: "talking", primary: "none" });
	expect(voicePresentation({ ...quiet, state: "speaking" })).toMatchObject({ phase: "speaking", primary: "stop" });
	expect(voicePresentation({ ...quiet, state: "conversing" }).talkEnabled).toBe(false);
});

it("reports recovery and errors above the quiet states", () => {
	expect(voicePresentation({ ...quiet, state: "recovering" })).toMatchObject({
		phase: "recovering",
		primary: "talk",
		talkEnabled: false,
	});
	expect(voicePresentation({ ...quiet, capturing: false, error: "capture_paused" })).toMatchObject({
		phase: "error",
		talkEnabled: false,
	});
});

it("keeps a stale routine abort from hiding the live state or gating Talk", () => {
	expect(voicePresentation({ ...quiet, state: "conversing", error: "input_idle" })).toMatchObject({
		phase: "talking",
		primary: "none",
	});
	expect(voicePresentation({ ...quiet, error: "input_idle" })).toMatchObject({
		phase: "listening",
		talkEnabled: true,
	});
});

it("shows the heard flash even when the detection starts a turn", () => {
	expect(voicePresentation({ ...quiet, state: "conversing" }, true).phase).toBe("heard");
});

it("keeps talk disabled while a wake trial owns the microphone", () => {
	const trial = {
		id: "attempt",
		modelId: "hey-evener",
		phrase: "Hey Evener.",
		settings: { threshold: 0.99, releaseThreshold: 0.5, hopSamples: 1280, holdoffMs: 2500, embeddingFrames: 16 },
		startedAt: 0,
		running: true,
		interrupted: false,
		detections: 0,
		scoredSamples: 0,
		droppedSamples: 0,
	};
	const presented = voicePresentation({ ...quiet, wake: { ...wakeBase, trials: [trial] } });
	expect(presented).toMatchObject({ phase: "trial", primary: "talk", talkEnabled: false });
});

const wakeBase = {
	state: "listening" as const,
	phrase: "Hey Evener.",
	modelId: "hey-evener",
	score: null,
	lastDetectionScore: 0.9999,
	detections: 1,
	error: "",
	trials: [],
};
