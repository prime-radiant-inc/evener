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
import { createContext, type ReactNode, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { useSyncExternalStore } from "react";
import { liveBands } from "../board/attention";
import { createBoardController } from "../board/boardData";
import { boardSeen } from "../board/nativeBoardMemory";
import { type Notice, notices } from "../board/notices";
import { useConnection } from "../ConnectionProvider";
import { haptic } from "../haptics";
import { AlertCenter, type AlertSnapshot, type AlertTimer, type HoldKind } from "./alertCenter";
import { AlertFeed } from "./alertEvents";
import { alertScreenFor, coversBanners } from "./alertScreen";
import { alertPreferences } from "./nativeAlertPreferences";

type Route = { name: string; params?: object };

interface Alerts {
	center: AlertCenter;
	reportRoutes(routes: readonly Route[]): void;
	noticeFor(key: string): Notice | undefined;
}

const Context = createContext<Alerts | null>(null);

const realClock: AlertTimer = {
	now: () => Date.now(),
	setTimeout: (callback, ms) => setTimeout(callback, ms),
	clearTimeout: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
};

export function AlertsProvider({ children }: { children: ReactNode }) {
	const { client, state, activeProfile } = useConnection();
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
	const routes = useRef<readonly Route[]>([]);
	const screenHub = useRef(hubId);
	const uncover = useRef<(() => void) | null>(null);
	const reportRoutes = useCallback(
		(next: readonly Route[]) => {
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

	// One controller per client object (ruling 2). A new client (a return
	// from the background, another hub, or a closed connection dialed again)
	// starts a new baseline, and the old controller's reads die with it, so a
	// late answer never reaches the feed. A drop the same client recovers
	// from keeps both, and its fresh reads are diffed.
	const board = useMemo(() => (client ? createBoardController({ scope: "attention" }) : null), [client]);
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
			// pages never alert old news.
			if (live.loaded && needsYou.loaded && needsYou.remaining === 0)
				feed.observeSessions(
					liveBands(live.rows, needsYou.rows, (row) => seen.isSeen(row)),
					new Set(rows.filter((row) => row.offline).map((row) => row.ref)),
				);
			// The notice baseline is this controller's own first sign-in read.
			if (snapshot.authRead && snapshot.manifest) {
				const list = notices({
					auth: snapshot.auth,
					sources: snapshot.manifest.sources ?? [],
					plugins: [],
					loadedRows: rows,
				});
				noticesByKey.current = new Map(list.map((notice) => [notice.key, notice]));
				feed.observeNotices(list);
			}
		});
		return () => {
			stop();
			board.dispose();
		};
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
	return <Context.Provider value={value}>{children}</Context.Provider>;
}

function useAlerts(): Alerts {
	const value = useContext(Context);
	if (!value) throw new Error("AlertsProvider is required.");
	return value;
}

export function useAlertCenter(): AlertCenter {
	return useAlerts().center;
}

export function useAlertSnapshot(): AlertSnapshot {
	const center = useAlertCenter();
	return useSyncExternalStore(center.subscribe, center.getSnapshot);
}

/** Holds banners while `active`: the Reader and typing hold "quiet", a
 * covering view "covered" (ruling 7). */
export function useHoldAlerts(active: boolean, kind: HoldKind): void {
	const center = useAlertCenter();
	useEffect(() => (active ? center.hold(kind) : undefined), [active, kind, center]);
}

/** How many alerts wait behind a hold. */
export function useHeldAlertCount(): number {
	return useAlertSnapshot().held;
}

/** Tells alerts the stack's routes up to the focused one. */
export function useReportRoutes(): (routes: readonly Route[]) => void {
	return useAlerts().reportRoutes;
}

/** The notice alerts last read under a key, for a tapped notice banner. */
export function useNoticeFor(): (key: string) => Notice | undefined {
	return useAlerts().noticeFor;
}
