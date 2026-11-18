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

/** An owed resume for one session, and who currently holds it.
 *
 * `owed` means a stop the hub acknowledged is still waiting for its resume.
 * `claimed` is the hook that is sending it right now: two hooks can be mounted
 * for one session in a reopen race, and without the claim both would send and
 * each would recycle the shared socket.
 *
 * The acknowledgement can arrive after the screen that asked for the restart
 * is gone - closing a conversation does not cancel a stop the hub is already
 * taking - so this outlives the hook that recorded it. It is also what a hook
 * mounting mid-flight subscribes to: a reopen before the acknowledgement picks
 * the obligation up when it lands, and a reopen during dispatch joins the
 * attempt instead of starting a second one.
 *
 * A resume that went out and lost its answer is never owed again: replaying it
 * could resume the session twice. Only a step that never reached the wire
 * releases its claim for another try. */
interface OwedResume {
	owed: boolean;
	claimed: symbol | null;
}
const owedResumes = new Map<string, OwedResume>();
const owedListeners = new Set<() => void>();
let owedRevision = 0;

function recordFor(key: string): OwedResume | undefined {
	return owedResumes.get(key);
}

/** Whether this session still has a resume waiting to be sent by anyone. */
function isOwed(key: string | null): boolean {
	return key !== null && recordFor(key)?.owed === true;
}

/** Records that this session's stop was acknowledged and its resume is owed. */
function owe(key: string): void {
	const existing = recordFor(key);
	if (existing) existing.owed = true;
	else owedResumes.set(key, { owed: true, claimed: null });
	announceOwed();
}

/** Takes the owed resume for sending. One holder at a time: a second hook
 * joining the same dispatch gets nothing and waits for the first's outcome. */
function claim(key: string): symbol | null {
	const record = recordFor(key);
	if (!record?.owed || record.claimed !== null) return null;
	const claimToken = Symbol(key);
	record.claimed = claimToken;
	return claimToken;
}

/** Gives the claim back with its outcome. `retry` leaves the resume owed for
 * the next hook that can send; anything else settles the obligation. */
function settle(key: string, claimToken: symbol, retry: boolean): void {
	const record = recordFor(key);
	if (!record || record.claimed !== claimToken) return;
	record.claimed = null;
	if (!retry) record.owed = false;
	if (!record.owed && record.claimed === null) owedResumes.delete(key);
	announceOwed();
}

function announceOwed(): void {
	owedRevision += 1;
	for (const listener of owedListeners) listener();
}

function subscribeOwed(listener: () => void): () => void {
	owedListeners.add(listener);
	return () => owedListeners.delete(listener);
}

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
	// The record is subscribed, not sampled at mount: a hook that opens before
	// the acknowledgement arrives must pick the obligation up when it lands,
	// and one that opens during a dispatch must join that attempt rather than
	// start a second resume on the same socket.
	const [phase, setPhase] = useState<Phase>(() => (isOwed(key) ? "resumeOwed" : "idle"));
	useEffect(() => {
		const sync = () => setPhase((current) => (current === "idle" && isOwed(key) ? "resumeOwed" : current));
		sync();
		return subscribeOwed(sync);
	}, [key]);
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
			if (key !== null) owe(key);
			setPhase("resumeOwed");
		} else {
			setPhase("idle");
			setError(RESTART_FAILED);
		}
	}, [controls, phase, key]);
	useEffect(() => {
		if (phase !== "resumeOwed" || !controls || pending !== null) return;
		if (refused.current?.controls === controls && refused.current.settled === settled) return;
		// The claim is what keeps a second mounted hook from sending the same
		// resume; if another holds it, this one waits for that attempt's outcome.
		if (key === null) {
			setPhase("resuming");
		} else {
			const claimToken = claim(key);
			if (claimToken === null) return;
			setPhase("resuming");
			void attemptControl(controls, "resume").then((outcome) => {
				if (outcome === "refused") {
					// Never sent: the step is still owed, for this hook or the next.
					settle(key, claimToken, true);
					refused.current = { controls, settled };
					setPhase("resumeOwed");
					return;
				}
				// Done, or the hub refused it: either way the step is settled and
				// no binding is still owed a resume. A failed attempt went out, so
				// it settles too - replaying it could resume the session twice.
				settle(key, claimToken, false);
				setPhase("idle");
				if (outcome === "failed") setError(RESUME_FAILED);
			});
			return;
		}
		void attemptControl(controls, "resume").then((outcome) => {
			setPhase("idle");
			if (outcome === "failed") setError(RESUME_FAILED);
		});
	}, [phase, controls, pending, settled, key]);
	return { busy: phase !== "idle", error, restart };
}
