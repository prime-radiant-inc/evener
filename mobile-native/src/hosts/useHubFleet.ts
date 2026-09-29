// The hub's hosts and its live sessions for a sheet that shows them (the Hub's
// Hosts pages; New session's host picker): one hosts controller and one Live
// reader per client, let go when the client is replaced or the sheet closes.
import { useEffect, useMemo, useSyncExternalStore } from "react";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { HostsController } from "./hostsController";
import { LiveSessionsReader } from "./liveCounts";

export interface HubFleet {
	hosts: HostsController | null;
	live: LiveSessionsReader | null;
}

/** newMutationId is the random id each host edit or removal carries; the
 * sheet hands in expo-crypto's randomUUID. */
export function useHubFleet(client: ConversationClientLike | null, newMutationId?: () => string): HubFleet {
	const fleet = useMemo<HubFleet>(
		() =>
			client
				? { hosts: new HostsController(client, newMutationId), live: new LiveSessionsReader(client) }
				: { hosts: null, live: null },
		[client, newMutationId],
	);
	useEffect(() => {
		const unwatch = fleet.live?.watch();
		return () => {
			unwatch?.();
			fleet.hosts?.dispose();
			fleet.live?.dispose();
		};
	}, [fleet]);
	return fleet;
}

const noSubscription = () => () => {};
const nothing = () => null;

/** A store's snapshot, or null while there is no store (no client yet). */
export function useOptionalSnapshot<T>(
	store: { subscribe(listener: () => void): () => void; getSnapshot(): T } | null,
): T | null {
	return useSyncExternalStore(store?.subscribe ?? noSubscription, store ? store.getSnapshot : nothing);
}
