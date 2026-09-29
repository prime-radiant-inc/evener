// What every Hub page reads from the sheet around it (ruling 1): the hub, its
// client and the connection's readiness. It lives apart from HubSheet.tsx, the
// navigator, so the pages HubSheet mounts can read it without importing their
// own navigator.
import type { AppwireClient, HubUpdateController } from "@evener/appwire-client";
import type { HostsController } from "../hosts/hostsController";
import type { LiveSessionsReader } from "../hosts/liveCounts";
import type { How } from "../hubs/AddHub";
import { createContext, useContext, useEffect, useRef, useState } from "react";

/** The Hub's pages. A page about the connected hub names the hub it was
 * opened for; Hubs and its pages are about the phone's saved hubs. */
export type HubRoutes = {
	HubHome: { hubId: string };
	Alerts: { hubId: string };
	Display: { hubId: string };
	DetailLevel: { hubId: string };
	/** focus opens that host's detail once. */
	Hosts: { hubId: string; focus?: string };
	HostDetail: { hubId: string; name: string };
	HostEdit: { hubId: string; name: string };
	/** focus opens that provider's detail once; signIn starts its sign-in instead. */
	Providers: { hubId: string; focus?: string; signIn?: boolean };
	/** focus opens that plugin's detail once. */
	Plugins: { hubId: string; focus?: { plugin: string; marketplace: string } };
	Hubs: undefined;
	AddHub: { how: How };
	HubDetails: { id: string };
	/** Today's settings screens, pushed inside the sheet until their grouped
	 * pages land (#2539). */
	KeybindingPreferences: { hubId: string; editor?: { actionId: string; chord: string } };
	LaunchSettings: { hubId: string; projectCwd?: string };
	HubSettings: { hubId: string };
};

export interface HubSheetContextValue {
	hubId: string;
	hubName: string;
	/** The client pages read through: the live one while ready, the last ready
	 * one while a retry dials (useRenderClient's contract, connectionDisplay.ts). */
	client: AppwireClient | null;
	ready: boolean;
	/** Whether a control that needs the hub may act right now. */
	canUseConnection: () => boolean;
	/** The hub's own update: the header's "up to date" or "Update available",
	 * and About's "Update hub" (ruling 22). */
	updates: HubUpdateController;
	/** The hub's hosts and its live sessions, per client; null before one. */
	hosts: HostsController | null;
	live: LiveSessionsReader | null;
}

const HubSheetContext = createContext<HubSheetContextValue | null>(null);
export const HubSheetProvider = HubSheetContext.Provider;

export function useHubSheet(): HubSheetContextValue {
	const value = useContext(HubSheetContext);
	if (!value) throw new Error("A Hub page must render inside HubSheet.");
	return value;
}

/** The Hub is always the selected hub's: selecting another hub calls `close`,
 * and no hub selected (the selected one was removed, or none was when it
 * opened) calls `leave`, for the first-run screen. It acts at most once. */
export function useClosesOnHubChange(hubId: string, close: () => void, leave: () => void) {
	const [openedFor] = useState(hubId);
	const handled = useRef(false);
	useEffect(() => {
		if (handled.current) return;
		if (hubId === "") {
			handled.current = true;
			leave();
		} else if (hubId !== openedFor) {
			handled.current = true;
			close();
		}
	}, [hubId, openedFor, close, leave]);
}
