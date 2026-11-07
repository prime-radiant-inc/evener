import type { VoiceSnapshot } from "./voiceClient";

/** What the voice surface is doing, in the order a glance needs it: a turn in
 * flight outranks a paused arm, which outranks the ordinary listening state. */
export type VoicePhase = "disarmed" | "trial" | "recovering" | "error" | "speaking" | "talking" | "heard" | "listening";

export interface VoicePresentation {
	phase: VoicePhase;
	/** The surface's one primary control. Everything else is secondary. `none`
	 * while the user's own turn runs: stopping the reply would not end it. */
	primary: "arm" | "talk" | "stop" | "none";
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
	const talkEnabled = capturing && state.state !== "recovering";
	if (trialRunning) return { phase: "trial", primary: "talk", talkEnabled: false };
	if (state.state === "recovering") return { phase: "recovering", primary: "talk", talkEnabled: false };
	// A wake detection publishes the conversing state in the same snapshot, so
	// the heard flash has to outrank it or it would never be seen.
	if (justHeard && state.capturing && !trialRunning) return { phase: "heard", primary: "talk", talkEnabled };
	if (state.state === "speaking") return { phase: "speaking", primary: "stop", talkEnabled: false };
	if (state.state === "conversing") return { phase: "talking", primary: "none", talkEnabled: false };
	// Routine turn aborts (input_idle, input_start_timeout, recoverable provider
	// errors) leave `error` set while the phone stays armed and listening, so
	// only a stopped capture makes the whole surface read as stopped.
	if (state.error && !capturing) return { phase: "error", primary: "talk", talkEnabled: false };
	return { phase: "listening", primary: "talk", talkEnabled };
}
