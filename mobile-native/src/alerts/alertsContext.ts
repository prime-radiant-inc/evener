// The in-app alerts' context and the hooks screens use, apart from the
// provider (AlertsProvider.tsx), which reads the fleet and the connection: a
// component that only holds banners (HoldingModal, the composer, the Reader)
// loads none of that.
import { createContext, useCallback, useContext, useEffect, useSyncExternalStore } from "react";
import type { Notice } from "../board/notices";
import type { Alert, AlertCenter, AlertSnapshot, HoldKind } from "./alertCenter";

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

/** Offers an alert to the banner: nothing outside an AlertsProvider (a screen
 * rendered on its own). */
export function useOfferAlert(): (alert: Alert) => void {
	const center = useContext(AlertsContext)?.center;
	return useCallback((alert: Alert) => center?.offer(alert), [center]);
}

/** Tells alerts this hub's New session is open, so an alert that its start
 * failed, which the form now shows itself, goes: nothing outside an
 * AlertsProvider. */
export function useStartFailureSeen(hubId: string): () => void {
	const center = useContext(AlertsContext)?.center;
	return useCallback(() => center?.startFailureSeen(hubId), [center, hubId]);
}

const noSubscription = () => () => {};

/** How many alerts wait behind a hold: none outside an AlertsProvider. */
export function useHeldAlertCount(): number {
	const center = useContext(AlertsContext)?.center;
	return useSyncExternalStore(center?.subscribe ?? noSubscription, () => center?.getSnapshot().held ?? 0);
}

const noRecent: readonly string[] = [];

/** The sessions that alerted you, most recent first, the order Next serves
 * (spec 8.3): none outside an AlertsProvider. */
export function useAlertedRecently(): readonly string[] {
	const center = useContext(AlertsContext)?.center;
	return useSyncExternalStore(center?.subscribe ?? noSubscription, () => center?.getSnapshot().recent ?? noRecent);
}

/** Tells alerts Next moved you on, so what waits behind a hold is Next's to
 * serve (AlertCenter.nextUsed): nothing outside an AlertsProvider. */
export function useNextUsed(): () => void {
	const center = useContext(AlertsContext)?.center;
	return useCallback(() => center?.nextUsed(), [center]);
}

/** Tells alerts the stack's routes up to the focused one. */
export function useReportRoutes(): (routes: readonly ReportedRoute[]) => void {
	return useAlerts().reportRoutes;
}

/** The notice alerts last read under a key, for a tapped notice banner. */
export function useNoticeFor(): (key: string) => Notice | undefined {
	return useAlerts().noticeFor;
}
