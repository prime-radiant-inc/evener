// What the Hub's host pages read: the fleet as any page on screen reads it
// (useFleetOnScreen), and the hub's own version for the drift tag.
import { useEffect, useRef, useSyncExternalStore } from "react";
import { type FleetOnScreen, useFleetOnScreen } from "../hosts/useFleetOnScreen";
import { useHubSheet } from "./hubSheetContext";

export interface HostsOnScreen extends FleetOnScreen {
	hubVersion: string | undefined;
}

export function useHostsOnScreen(): HostsOnScreen {
	const { hosts, live, updates, ready } = useHubSheet();
	const hubVersion = useSyncExternalStore(updates.subscribe, updates.getState).check?.currentVersion;
	return { ...useFleetOnScreen(hosts, live, ready), hubVersion };
}

/** Goes back, once, when the host a page shows leaves the hub's list (removed
 * here or elsewhere). Each page on the stack does this for itself: the stack
 * pops only the page that asks, so a page above one that went back would
 * otherwise stay on a host that is gone. */
export function useLeavesWithHost(listed: boolean | null, goBack: () => void): void {
	const left = useRef(false);
	useEffect(() => {
		if (listed !== false || left.current) return;
		left.current = true;
		goBack();
	}, [listed, goBack]);
}
