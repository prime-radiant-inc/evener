// A session read that failed while connected tries again on its own (spec
// 14): at once, then after 1, 2 and 4 seconds, and on up to every 30 seconds.
// There is no button to press. The count it returns drives the one quiet line
// the transcript shows from the third failure in a row.
import { useEffect, useRef, useState } from "react";
import type { ConversationStatus } from "../../../mobile/src/state/conversation";
import { reconnectDelay } from "../hubConnection";

export function useReadRetry({
	status,
	readFailed,
	readFailedNow,
	active,
	resetKey,
	readStatus,
	resume,
}: {
	/** The conversation's status, as the screen last rendered it. */
	status: ConversationStatus;
	/** Whether the store's latest read failed even though readable rows remain.
	 * A rehydrate failure keeps `status: 'open'`; the actor must still retry. */
	readFailed?: boolean;
	/** The store's read-failure fact right now, which may be ahead of the render. */
	readFailedNow?: () => boolean;
	/** Connected, with a service, and in front: reads may run. */
	active: boolean;
	/** The session this count belongs to; a new one starts from zero. */
	resetKey: string;
	/** The store's status right now, which may be ahead of the render. */
	readStatus: () => ConversationStatus;
	/** Reads the session again. It may resolve on an error, or reject. */
	resume: () => Promise<unknown>;
}): number {
	const [failures, setFailures] = useState(0);
	// Bumped on a new session and on unmount, so a retry still in flight
	// counts only for the session it was made for.
	const generation = useRef(0);
	const inFlight = useRef(false);
	const failed = readFailed ?? status === "error";
	// Read through a ref so the timer effect keeps its one arm per step; a
	// caller's fresh callback identity must not re-arm the backoff.
	const failedNow = useRef<() => boolean>(() => false);
	failedNow.current = () => readFailedNow?.() ?? readStatus() === "error";
	useEffect(() => {
		generation.current += 1;
		inFlight.current = false;
		setFailures(0);
	}, [resetKey]);
	useEffect(
		() => () => {
			generation.current += 1;
		},
		[],
	);
	// A failed read starts the count and a successful one clears it. After
	// that each retry counts its own outcome: a retry can go from "error" to
	// "error" without the screen rendering the "opening" between, or reject.
	useEffect(() => {
		if (failed) setFailures((count) => (count === 0 ? 1 : count));
		else if (status === "open") setFailures(0);
	}, [failed, status]);
	useEffect(() => {
		if (failures === 0 || !active || !failed) return;
		const retry = setTimeout(
			() => {
				if (inFlight.current || !failedNow.current()) return;
				inFlight.current = true;
				const attempt = generation.current;
				const settle = () => {
					if (attempt !== generation.current) return;
					inFlight.current = false;
					// Only a retry that left the store on an error counts. One that a
					// newer read took over ("opening") doesn't: if that read fails too,
					// the status turns back to "error" and the retry re-arms at the
					// same step.
					if (failedNow.current()) setFailures((count) => count + 1);
				};
				resume().then(settle, settle);
			},
			reconnectDelay(failures - 1),
		);
		return () => clearTimeout(retry);
	}, [failures, active, failed, readStatus, readFailedNow, resume]);
	return failures;
}
