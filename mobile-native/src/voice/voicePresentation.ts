import type { VoiceSnapshot } from "./voiceClient";

/** What the voice surface is doing, in the order a glance needs it: a turn in
 * flight outranks a paused arm, which outranks the ordinary listening state. */
export type VoicePhase = "disarmed" | "trial" | "recovering" | "error" | "speaking" | "talking" | "heard" | "listening";

export interface VoicePresentation {
	phase: VoicePhase;
	/** The surface's one primary control. Everything else is secondary. */
	primary: "arm" | "talk" | "stop";
	/** Whether Talk can start a turn now: armed, capturing, and owned by this device. */
	talkEnabled: boolean;
}

/** Derive the surface's state from the voice snapshot. `justHeard` lets the
 * caller flash the heard-you state for a moment after a detection; the snapshot
 * itself carries no detection time, and the detection count never resets. */
export function voicePresentation(state: VoiceSnapshot, justHeard = false): VoicePresentation {
	if (state.state === "disarmed") return { phase: "disarmed", primary: "arm", talkEnabled: false };
	const trialRunning = state.wake?.trials.at(-1)?.running === true;
	const capturing = state.capturing && !trialRunning && state.state === "quiet";
	const talkEnabled = capturing && state.state !== "recovering" && state.error === "";
	if (trialRunning) return { phase: "trial", primary: "talk", talkEnabled: false };
	if (state.state === "recovering") return { phase: "recovering", primary: "talk", talkEnabled: false };
	if (state.error) return { phase: "error", primary: "talk", talkEnabled: false };
	if (state.state === "speaking") return { phase: "speaking", primary: "stop", talkEnabled: false };
	if (state.state === "conversing") return { phase: "talking", primary: "stop", talkEnabled: false };
	if (justHeard && capturing) return { phase: "heard", primary: "talk", talkEnabled };
	return { phase: "listening", primary: "talk", talkEnabled };
}
