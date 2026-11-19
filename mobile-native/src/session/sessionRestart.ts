// Restarting a session that runs an older Evener (ruling 20): stop it, then
// resume it. The stop's own refresh can replace the session's controls
// before the resume runs, and a blip can replace them mid-stop, so the
// resume is owed rather than chained: it runs on whichever controls are
// current once they can take it.
import { useCallback, useEffect, useRef, useState } from "react";
import { type ActionOutcome, type SessionControls, useControlsState } from "../sessionControls";

/** How one step went: done, failed at the hub, or refused by the controls
 * before any request went out. A step that went out and lost its controls
 * before the answer counts as done: the request reached the hub. */
export type Attempt = "done" | "failed" | "refused";

/** One restart step, read off the control's own outcome: an accepted step is
 * done, a step the phone refused to send is refused, and a refusal that
 * reached the hub is a failure. The outcome the promise carries is the
 * step's answer; nothing here reads the controls' state. */
export function attemptControl(controls: SessionControls, kind: "forceStop" | "resume"): Promise<Attempt> {
	return controls[kind]().then((outcome: ActionOutcome): Attempt => {
		if (outcome.disposition === "accepted") return "done";
		if (outcome.disposition === "notSent") return "refused";
		return "failed";
	});
}

type Phase = "idle" | "stopping" | "resumeOwed" | "resuming";

export const RESTART_FAILED = "Couldn't restart this session.";
export const RESUME_FAILED = "Stopped, but couldn't start it again.";

/** The notice's restart. `settled` names what else must hold for a resume to
 * go out (the store's status); a refused resume waits for it or the controls
 * to change, then tries again. */
export function useSessionRestart(controls: SessionControls | null, settled: unknown) {
	const [phase, setPhase] = useState<Phase>("idle");
	const [error, setError] = useState<string | null>(null);
	const pending = useControlsState(controls)?.pending ?? null;
	// What a refused resume was refused under, so it isn't retried until
	// something it depends on has changed.
	const refused = useRef<{ controls: SessionControls; settled: unknown } | null>(null);
	const restart = useCallback(async () => {
		if (!controls || phase !== "idle") return;
		setError(null);
		setPhase("stopping");
		const stop = await attemptControl(controls, "forceStop");
		refused.current = null;
		if (stop === "done") setPhase("resumeOwed");
		else {
			setPhase("idle");
			setError(RESTART_FAILED);
		}
	}, [controls, phase]);
	useEffect(() => {
		if (phase !== "resumeOwed" || !controls || pending !== null) return;
		if (refused.current?.controls === controls && refused.current.settled === settled) return;
		setPhase("resuming");
		void attemptControl(controls, "resume").then((outcome) => {
			if (outcome === "refused") {
				refused.current = { controls, settled };
				setPhase("resumeOwed");
				return;
			}
			setPhase("idle");
			if (outcome === "failed") setError(RESUME_FAILED);
		});
	}, [phase, controls, pending, settled]);
	return { busy: phase !== "idle", error, restart };
}
