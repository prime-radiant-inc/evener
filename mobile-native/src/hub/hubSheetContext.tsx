// What every Hub page reads from the sheet around it (ruling 1): the hub, its
// client and the connection's readiness. It lives apart from HubSheet.tsx, the
// navigator, so the pages HubSheet mounts can read it without importing their
// own navigator.
import type { AppwireClient, HubUpdateController } from "@evener/appwire-client";
import { createContext, useContext, useEffect, useRef, useState } from "react";

/** The Hub's pages. Every page names the hub it was opened for. */
export type HubRoutes = {
	HubHome: { hubId: string };
	Alerts: undefined;
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
