import { useCallback, useEffect, useMemo, useReducer, useSyncExternalStore } from "react";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { ACTIVITY_POLL_MS, ActivityPoll, isFreshRead } from "./activityPoll";

const noSubscription = () => () => {};
const noRevision = () => 0;

/** S5's activity (activityPoll.ts), polled while connected and in front: for
 * every live session (the Board), or for the one session `sessionRef` names (a
 * session's tray). A poll is bound to the client it was made with, so
 * each client gets a fresh one, and there is none without a client. The
 * revision changes whenever the poll's report does: a read lands, a stop
 * forgets it, or the hub turns out to predate S5.
 *
 * Stopping (idle, out of front, disconnected) forgets the poll's read
 * (ActivityPoll.stop), but the render that sees `connected` turn false comes
 * before the effect that stops it, and a hub that reports ready but has
 * stopped delivering reads leaves stale data behind without ever
 * disconnecting at all - reading either as current would let a
 * read merely aging past isFreshRead's threshold read as "stuck", a false
 * alarm about the connection or the hub rather than the session (Jesse's
 * ruling). Gating the RETURNED reading on `connected` AND freshness, and
 * never handing back the poll itself, means no caller can read around this:
 * everything drawn from it (the Board's rows, their meters and the Working
 * order; a session's tray) falls back to its pre-S5 appearance the moment
 * either one fails, and agrees since there is only the one gate. Nothing
 * re-renders the caller when a read merely ages, so while a fresh read is on
 * screen the recheck below re-renders at the polling cadence, dropping the
 * read within one interval of its going stale, whatever becomes of the poll
 * meanwhile. With no fresh read on screen there is nothing to expire, so it
 * doesn't run: not before the first read lands, not while reads keep failing,
 * and never on a hub that predates S5.
 * The returned `tick` is that same recheck's counter: the Board's `bands`'
 * isStuck sort closes over `msSinceRead`, so it needs this to re-sort Working
 * within one poll interval of a row crossing into stuck from elapsed time
 * alone, in step with its why-line (which reads `msSinceRead` live on every
 * render). */
export function useActivityPoll(
	client: ConversationClientLike | null,
	connected: boolean,
	inFront: boolean,
	sessionRef?: string,
) {
	const poll = useMemo(
		() => (client ? new ActivityPoll(client, sessionRef === undefined ? undefined : [sessionRef]) : null),
		[client, sessionRef],
	);
	const revision = useSyncExternalStore(poll?.subscribe ?? noSubscription, poll?.getRevision ?? noRevision);
	useEffect(() => {
		if (!poll || !connected || !inFront) return;
		poll.start();
		return () => poll.stop();
	}, [poll, connected, inFront]);
	const msSinceRead = poll?.msSinceRead() ?? null;
	const reading = connected && isFreshRead(msSinceRead) ? poll : null;
	const [tick, recheck] = useReducer((n: number) => n + 1, 0);
	useEffect(() => {
		if (!reading || !inFront) return;
		const timer = setInterval(recheck, ACTIVITY_POLL_MS);
		return () => clearInterval(timer);
	}, [reading, inFront]);
	const activityOf = useCallback((ref: string) => reading?.activity(ref), [reading]);
	return { revision, activityOf, msSinceRead: reading ? msSinceRead : null, tick };
}
