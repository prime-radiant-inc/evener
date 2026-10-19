// The Session's nav bar title over a live session (spec 8.1). The title
// itself is presentational (SessionTitle); this wrapper owns the clock and
// builds the state line from the session, so the live line's seconds tick
// without re-rendering the transcript under it: one second while the agent
// works, the tray's old cadence, and every thirty seconds at rest for an age
// like "Idle · 35m ago".
import { useEffect, useMemo, useReducer, useSyncExternalStore } from "react";
import type { MobileConversation } from "../projectedRows";
import type { NavigationSessionSummary, SessionActivity } from "@evener/appwire-client";
import { SessionTitle } from "./SessionTitle";
import { sessionStateLine, type StateSource } from "./sessionState";
import { FrameCounter, type TraySource, trayLine } from "./trayLine";

/** The conversation store the title reads: its own subscription, so the header
 * options that render the title never depend on the conversation object, which
 * is new on every streamed frame. */
export interface FrameSource {
	getState(): { conversation: MobileConversation | null };
	subscribe(listener: () => void): () => void;
}

/** One FrameCounter per conversation binding. It records this phone's clock
 * each time the session's lastFrameAt moves, which the reducer restamps on
 * every streamed frame (spec 16.4). */
export function useFrameCounter(store: FrameSource): FrameCounter {
	// biome-ignore lint/correctness/useExhaustiveDependencies: A new binding starts a new count.
	const frames = useMemo(() => new FrameCounter(), [store]);
	useEffect(() => {
		let last = store.getState().conversation?.lastFrameAt;
		return store.subscribe(() => {
			const at = store.getState().conversation?.lastFrameAt;
			if (at === last) return;
			last = at;
			if (at !== undefined) frames.record(Date.now());
		});
	}, [store, frames]);
	return frames;
}

export function LiveSessionTitle({
	runMs,
	activity,
	row,
	store,
	...title
}: Omit<Parameters<typeof SessionTitle>[0], "line" | "perMinute"> & {
	/** A subagent's run time, as its Subagents row shows it. */
	runMs(now: number): number | null;
	/** The hub's activity read for this session, which the screen polls while
	 * it is in front. */
	activity?: SessionActivity;
	/** The session's navigation row, whose subagent tally stands in without a
	 * fresh activity read, as on the Board. */
	row?: NavigationSessionSummary;
	/** The conversation store, whose lastFrameAt the meter counts. */
	store: FrameSource;
}) {
	const frames = useFrameCounter(store);
	// The title subscribes to the conversation itself: the screen's header
	// options then depend only on `hasConversation`, not on the conversation
	// object, which is new on every streamed frame.
	const conversation = useSyncExternalStore(store.subscribe, () => store.getState().conversation);
	const session = conversation as (TraySource & StateSource) | null;
	const [, tick] = useReducer((count: number) => count + 1, 0);
	const active = session?.status.type === "active";
	// A boolean, not the session itself: the session object is new on every
	// streamed frame, and a dependency on it would restart the clock each time.
	const shown = session !== null;
	useEffect(() => {
		if (!shown) return;
		const clock = setInterval(tick, active ? 1000 : 30_000);
		return () => clearInterval(clock);
	}, [shown, active]);
	if (!session) return null;
	const now = Date.now();
	const live = trayLine(session, now, activity, row);
	const line = sessionStateLine(session, now, runMs(now), live);
	return (
		<SessionTitle {...title} line={line} perMinute={live && frames.hasFrames() ? frames.perMinute(now) : undefined} />
	);
}
