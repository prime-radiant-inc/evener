// What the Hub's host pages read: the hub's host rows, polled while the page
// is on screen, each host's live-session count, and the hub's own version for
// the drift tag.
import { useFocusEffect } from "@react-navigation/native";
import { useCallback, useSyncExternalStore } from "react";
import type { HostsState } from "../hosts/hostsController";
import { liveCountsByHost } from "../hosts/liveCounts";
import { useOptionalSnapshot } from "../hosts/useHubFleet";
import { useHubSheet } from "./hubSheetContext";

export interface HostsOnScreen {
	/** Null until the hub has listed its hosts once. */
	state: HostsState | null;
	/** Why the hub hasn't listed its hosts yet, when it refused. */
	loadError: string | null;
	liveCount(host: string): number;
	hubVersion: string | undefined;
}

export function useHostsOnScreen(): HostsOnScreen {
	const { hosts, live, updates } = useHubSheet();
	const hubVersion = useSyncExternalStore(updates.subscribe, updates.getState).check?.currentVersion;
	const state = useOptionalSnapshot(hosts);
	const counts = liveCountsByHost(useOptionalSnapshot(live)?.rows ?? []);
	useFocusEffect(
		useCallback(() => {
			void live?.load();
			return hosts?.start();
		}, [hosts, live]),
	);
	return {
		state: state?.rows ? state : null,
		loadError: state?.rows ? null : (state?.error ?? null),
		liveCount: (host) => counts.get(host) ?? 0,
		hubVersion,
	};
}
