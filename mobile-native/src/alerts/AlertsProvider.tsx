// In-app alerts for the whole app (spec 13.3): one alert center on the real
// clock, fed from the active hub by a Board controller of its own, told what
// is on screen, and read by the banner host and the screens that hold
// banners back.
//
// The controller is the Board's, in its attention scope (Live, Needs you, the
// manifest and the sign-ins), and it is never paused: the Board and the
// Session pause theirs out of view (phase 3 ruling 33), and alerts must hear
// about sessions wherever you are. That is a second set of navigation reads
// while the Board or a Session is in front.
import { type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { liveBands } from "../board/attention";
import { createBoardController } from "../board/boardData";
import { boardSeen } from "../board/nativeBoardMemory";
import { type Notice, notices } from "../board/notices";
import { useConnection } from "../ConnectionProvider";
import { haptic } from "../haptics";
import { AlertCenter, type AlertTimer } from "./alertCenter";
import { AlertFeed } from "./alertEvents";
import { type Alerts, AlertsContext, type ReportedRoute } from "./alertsContext";
import { alertScreenFor, coversBanners } from "./alertScreen";
import { alertPreferences } from "./nativeAlertPreferences";

const realClock: AlertTimer = {
	now: () => Date.now(),
	setTimeout: (callback, ms) => setTimeout(callback, ms),
	clearTimeout: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
};

export function AlertsProvider({ children }: { children: ReactNode }) {
	const { client, state, activeProfile, profiles } = useConnection();
	const hubId = activeProfile?.id ?? null;
	const [center] = useState(() => new AlertCenter(realClock, haptic));
	const [feed] = useState(() => new AlertFeed(center));
	const noticesByKey = useRef(new Map<string, Notice>());

	// Hub > In-app alerts, as it changes.
	useEffect(() => {
		const preferences = alertPreferences();
		center.setPreferences(preferences.getSnapshot());
		return preferences.subscribe(() => center.setPreferences(preferences.getSnapshot()));
	}, [center]);

	// What is on screen, and whether a sheet or modal covers it (ruling 7).
	const routes = useRef<readonly ReportedRoute[]>([]);
	const screenHub = useRef(hubId);
	const uncover = useRef<(() => void) | null>(null);
	const reportRoutes = useCallback(
		(next: readonly ReportedRoute[]) => {
			routes.current = next;
			center.setScreen(alertScreenFor(next, screenHub.current));
			const covered = coversBanners(next.at(-1));
			if (covered && uncover.current === null) uncover.current = center.hold("covered");
			else if (!covered && uncover.current !== null) {
				uncover.current();
				uncover.current = null;
			}
		},
		[center],
	);
	// Another hub: nothing carries over, and the reset forgot the screen, so
	// report it again for the new hub.
	useEffect(() => {
		if (screenHub.current === hubId) return;
		screenHub.current = hubId;
		center.reset();
		noticesByKey.current = new Map();
		reportRoutes(routes.current);
	}, [center, hubId, reportRoutes]);

	// A removed hub took its New session draft with it, so a failed start's
	// alert for it would open nothing: it goes with the hub (#3104).
	const savedHubs = useRef<ReadonlySet<string>>(new Set());
	useEffect(() => {
		const now = new Set(profiles.map((profile) => profile.id));
		for (const id of savedHubs.current) if (!now.has(id)) center.startFailureSeen(id);
		savedHubs.current = now;
	}, [center, profiles]);

	// One controller per client object (ruling 2). A new client (a return
	// from the background, another hub, or a closed connection dialed again)
	// starts a new baseline, and the old controller's reads die with it, so a
	// late answer never reaches the feed. A drop the same client recovers
	// from keeps both, and its fresh reads are diffed.
	const board = useMemo(() => (client ? createBoardController({ scope: "attention" }) : null), [client]);
	useEffect(() => () => board?.dispose(), [board]);
	useEffect(() => {
		feed.rebaseline();
		if (!board || hubId === null) return;
		const seen = boardSeen(hubId);
		// Read in the callback, never in a render: a snapshot a render took
		// before the client changed could otherwise reach the new baseline.
		const stop = board.subscribe(() => {
			const snapshot = board.getSnapshot();
			const { live, needsYou } = snapshot;
			const rows = [...live.rows, ...needsYou.rows];
			// A Needs you section still paging is never observed, so its later
			// pages never alert old news. Live is read to its first page only,
			// by design (ruling 1): every session that needs you is in the
			// complete Needs you section anyway, and a session that moves onto
			// Live's first page in any other state is no news, since only
			// entering a needs-you state or finishing from Working alerts.
			if (live.loaded && needsYou.loaded && needsYou.remaining === 0)
				feed.observeSessions(
					liveBands(live.rows, needsYou.rows, (row) => seen.isSeen(row)),
					new Set(rows.filter((row) => row.offline).map((row) => row.ref)),
				);
			// The notice baseline is this controller's own first notice read.
			if (snapshot.noticesRead && snapshot.manifest) {
				const list = notices({ hubNotices: snapshot.notices, sources: snapshot.manifest.sources ?? [] });
				noticesByKey.current = new Map(list.map((notice) => [notice.key, notice]));
				feed.observeNotices(list);
			}
		});
		return stop;
	}, [board, feed, hubId]);
	// A client refuses requests until it's ready; ready again after a drop, it
	// gets fresh readers that catch up on what it missed.
	useEffect(() => {
		board?.setClient(state === "ready" ? client : null);
	}, [board, client, state]);

	const value = useMemo<Alerts>(
		() => ({ center, reportRoutes, noticeFor: (key) => noticesByKey.current.get(key) }),
		[center, reportRoutes],
	);
	return <AlertsContext.Provider value={value}>{children}</AlertsContext.Provider>;
}
