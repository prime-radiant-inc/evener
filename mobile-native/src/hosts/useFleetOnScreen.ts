// The hub's hosts as a page on screen reads them (the Hub's Hosts pages, New
// session's host picker): the rows, polled while the page is on screen, and
// each host's live-session count, read on focus and on each return of the
// connection, so a read that failed while it was down recovers.
import { useFocusEffect } from "@react-navigation/native";
import { useCallback } from "react";
import type { HostsController, HostsState } from "./hostsController";
import { type LiveSessionsReader, liveCountsByHost } from "./liveCounts";
import { useOptionalSnapshot } from "./useHubFleet";

export interface FleetOnScreen {
	/** Null until the hub has listed its hosts once. */
	state: HostsState | null;
	/** Why the hub hasn't listed its hosts yet, when it refused. */
	loadError: string | null;
	liveCount(host: string): number;
}

export function useFleetOnScreen(
	hosts: HostsController | null,
	live: LiveSessionsReader | null,
	ready: boolean,
): FleetOnScreen {
	const state = useOptionalSnapshot(hosts);
	const counts = liveCountsByHost(useOptionalSnapshot(live)?.rows ?? []);
	useFocusEffect(useCallback(() => hosts?.start(), [hosts]));
	useFocusEffect(
		useCallback(() => {
			if (ready) void live?.load();
		}, [live, ready]),
	);
	return {
		state: state?.rows ? state : null,
		loadError: state?.rows ? null : (state?.error ?? null),
		liveCount: (host) => counts.get(host) ?? 0,
	};
}
