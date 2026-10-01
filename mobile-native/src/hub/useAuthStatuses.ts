// The hub's sign-in statuses by provider (evener/auth/list), for the words
// Providers shows beside each instance (ruling 6). This is the package's
// shared createAuthStatusesStore (state/credentials/authStatuses.ts) over this
// screen's connection: the store reads when the hook first has a client, reads
// again on evener/auth/updated, and a failed read keeps the last statuses and
// records why, so the page stays calm while the hub is away. Until a first
// read lands it answers null: nothing is known, so no account sign-in is
// called "Signed in" (statusOf). A client the screen replaces builds a fresh
// store, so a reply of the hub the previous one named never lands on the new
// one.
import type { AuthStatusResponse } from "@evener/appwire-client";
import { type AuthStatusesClient, createAuthStatusesStore } from "@evener/appwire-client/state/credentials";
import { useEffect, useState } from "react";

export function useAuthStatuses(client: AuthStatusesClient | null): ReadonlyMap<string, AuthStatusResponse> | null {
	const [statuses, setStatuses] = useState<ReadonlyMap<string, AuthStatusResponse> | null>(null);
	useEffect(() => {
		if (!client) {
			setStatuses(null);
			return;
		}
		const store = createAuthStatusesStore(client);
		store.start();
		// A fresh store knows nothing, and the replaced client's statuses must
		// not linger against this one.
		setStatuses(null);
		const stop = store.subscribe((state) => setStatuses(state.authStatuses));
		// The store only re-reads a list something has asked for, so this first
		// read is what makes evener/auth/updated matter from here on.
		void store.getState().fetchAuthStatuses();
		return () => {
			stop();
			// Drops every reply still in flight: a read of the client being
			// replaced must not publish against the next one.
			store.dispose();
		};
	}, [client]);
	return statuses;
}
