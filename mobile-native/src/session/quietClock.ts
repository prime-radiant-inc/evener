// A running agent's line turns Quiet once it has gone AGENT_QUIET_AFTER_MS
// without an update, but a silent agent sends nothing to re-render it. Rather
// than tick, a reader wakes once, when the soonest silence crosses the
// threshold, so the line turns Quiet on time and nothing runs in between.
import { useEffect, useState } from "react";
import { AGENT_QUIET_AFTER_MS } from "../board/attention";

/** The caller's `now`, moved on to the moment the soonest of its running
 * agents' silences crosses into Quiet. `silencesAt` gives those silences, in
 * ms, as of a given time; undefined is an agent with no silence to time. */
export function useNowPastQuiet(now: number, silencesAt: (now: number) => Iterable<number | undefined>): number {
	const [woke, setWoke] = useState(0);
	const at = Math.max(now, woke);
	let soonest = Number.POSITIVE_INFINITY;
	for (const silence of silencesAt(at)) {
		if (silence !== undefined && silence < AGENT_QUIET_AFTER_MS) {
			soonest = Math.min(soonest, AGENT_QUIET_AFTER_MS - silence);
		}
	}
	const crossing = Number.isFinite(soonest) ? at + soonest : undefined;
	useEffect(() => {
		if (crossing === undefined) return;
		// The caller's clock can lag the wall clock (the minute clock, a tree's
		// arrival), so the wait is to the crossing on the wall clock; waking at
		// no earlier than the crossing is what moves the silence past it.
		const timer = setTimeout(() => setWoke(Math.max(Date.now(), crossing)), Math.max(0, crossing - Date.now()));
		return () => clearTimeout(timer);
	}, [crossing]);
	return at;
}
