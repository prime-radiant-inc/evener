// Restarting a session that runs an older Evener (ruling 20): stop it, then
// resume it. The stop's own refresh can replace the session's controls
// before the resume runs, and a blip can replace them mid-stop, so the
// resume is owed rather than chained: it runs on whichever controls are
// current once they can take it.
import { useCallback, useEffect, useRef, useState } from "react";
import { type SessionControls, useControlsState } from "../sessionControls";
import { ResumeNotDispatched } from "../connection";

/** One session's identity, for this map only. The storage layer keys its rows
 * by the hub and ref together, and a ref can repeat across hubs, so the key
 * here does too; nothing outside this module reads it. */
function restartKey(hubId: string, ref: string): string {
	return JSON.stringify([hubId, ref]);
}

/** Restarts whose stop the hub acknowledged and whose resume is still owed,
 * by target. The acknowledgement can arrive after the screen that asked for
 * the restart is gone - closing a conversation does not cancel a stop the hub
 * is already taking - so this outlives the hook that recorded it. Reopening
 * the session mounts a fresh hook, which finds the entry and sends the resume
 * on the first binding that can carry it. Presentation stays with the screen
 * that opened the restart: only whether the resume is still owed lives here.
 *
 * A resume that went out and lost its answer is not owed: replaying it could
 * resume the session twice. Only a step that never reached the wire is. */
const owedResumes = new Map<string, true>();

/** How one step went: done, failed at the hub, or refused by the controls
 * before any request went out (another action pending, or the session not
 * current). A step that went out and lost its controls before the answer
 * counts as done: the request reached the hub. */
export type Attempt = "done" | "failed" | "refused";

export async function attemptControl(controls: SessionControls, kind: "forceStop" | "resume"): Promise<Attempt> {
	// The controls publish the step as pending the moment they send it.
	let sent = false;
	const unsubscribe = controls.subscribe(() => {
		if (controls.getSnapshot().pending === kind) sent = true;
	});
	try {
		if (await (kind === "forceStop" ? controls.forceStop() : controls.resume())) return "done";
	} catch (cause) {
		// A resume the connection never sent is a refusal before dispatch, not
		// the hub's answer: the step is still owed, and the hook retries it on
		// whatever binding can send next. A resume that went out and then
		// failed is not caught here, because replaying it could resume the
		// session twice.
		if (kind === "resume" && cause instanceof ResumeNotDispatched) return "refused";
		throw cause;
	} finally {
		unsubscribe();
	}
	if (!sent) return "refused";
	const after = controls.getSnapshot();
	return after.lastAction === kind && after.error ? "failed" : "done";
}

type Phase = "idle" | "stopping" | "resumeOwed" | "resuming";

export const RESTART_FAILED = "Couldn't restart this session.";
export const RESUME_FAILED = "Stopped, but couldn't start it again.";

/** The notice's restart. `settled` names what else must hold for a resume to
 * go out (the store's status); a refused resume waits for it or the controls
 * to change, then tries again. */
export function useSessionRestart(
	hubId: string | null,
	ref: string | null,
	controls: SessionControls | null,
	settled: unknown,
) {
	const key = hubId === null || ref === null ? null : restartKey(hubId, ref);
	// A fresh hook for a session that already owes a resume starts where the
	// unmounted one left off, so reopening the conversation sends it rather
	// than leaving a stopped daemon behind a restart nobody remembers.
	const [phase, setPhase] = useState<Phase>(() => (key !== null && owedResumes.has(key) ? "resumeOwed" : "idle"));
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
		if (stop === "done") {
			// Recorded before any effect can run and after unmount too: the
			// acknowledgement is the hub's, and the resume it owes must not depend
			// on this screen still being mounted to hear it.
			if (key !== null) owedResumes.set(key, true);
			setPhase("resumeOwed");
		} else {
			setPhase("idle");
			setError(RESTART_FAILED);
		}
	}, [controls, phase, key]);
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
			// Done, or the hub refused it: either way the step is settled and no
			// binding is still owed a resume. Only a step that never left the
			// phone stays owed.
			if (key !== null) owedResumes.delete(key);
			setPhase("idle");
			if (outcome === "failed") setError(RESUME_FAILED);
		});
	}, [phase, controls, pending, settled, key]);
	return { busy: phase !== "idle", error, restart };
}
