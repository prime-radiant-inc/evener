// The in-app alerts' context and the hooks screens use, apart from the
// provider (AlertsProvider.tsx), which reads the fleet and the connection: a
// component that only holds banners (HoldingModal, the composer, the Reader)
// loads none of that.
import { createContext, useContext, useEffect, useSyncExternalStore } from "react";
import type { Notice } from "../board/notices";
import type { AlertCenter, AlertSnapshot, HoldKind } from "./alertCenter";

/** A route of the root stack, as the app reports it (App.tsx). */
export type ReportedRoute = { name: string; params?: object };

export interface Alerts {
	center: AlertCenter;
	reportRoutes(routes: readonly ReportedRoute[]): void;
	noticeFor(key: string): Notice | undefined;
}

export const AlertsContext = createContext<Alerts | null>(null);

function useAlerts(): Alerts {
	const value = useContext(AlertsContext);
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
 * covering view "covered" (ruling 7). Outside an AlertsProvider (a screen
 * rendered on its own) there are no banners, so it holds nothing. */
export function useHoldAlerts(active: boolean, kind: HoldKind): void {
	const center = useContext(AlertsContext)?.center;
	useEffect(() => (active && center ? center.hold(kind) : undefined), [active, kind, center]);
}

const noSubscription = () => () => {};

/** How many alerts wait behind a hold: none outside an AlertsProvider. */
export function useHeldAlertCount(): number {
	const center = useContext(AlertsContext)?.center;
	return useSyncExternalStore(center?.subscribe ?? noSubscription, () => center?.getSnapshot().held ?? 0);
}

/** Tells alerts the stack's routes up to the focused one. */
export function useReportRoutes(): (routes: readonly ReportedRoute[]) => void {
	return useAlerts().reportRoutes;
}

/** The notice alerts last read under a key, for a tapped notice banner. */
export function useNoticeFor(): (key: string) => Notice | undefined {
	return useAlerts().noticeFor;
}
