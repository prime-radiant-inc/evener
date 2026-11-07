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
export function voicePresentation(state: VoiceSnapshot, justHeard = false, waitingForReply = false): VoicePresentation {
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
	// Native can sit in `conversing` with no live turn (Stop during a reply,
	// rejected speech, a late provider error), and Talk is the only way out
	// because wake listening needs quiet. `cloudMicrophone` marks a live turn.
	if (state.state === "conversing") {
		// While the manager composes a reply, no turn is streaming either, but
		// Talk would start speech that later barges in on that reply.
		return {
			phase: "talking",
			primary: "talk",
			talkEnabled: state.capturing && !state.cloudMicrophone,
		};
	}
	// Routine turn aborts (input_idle, input_start_timeout, recoverable provider
	// errors) leave `error` set while the phone stays armed and listening, so
	// only a stopped capture makes the whole surface read as stopped.
	if (state.error && !capturing) return { phase: "error", primary: "talk", talkEnabled: false };
	return { phase: "listening", primary: "talk", talkEnabled };
}
