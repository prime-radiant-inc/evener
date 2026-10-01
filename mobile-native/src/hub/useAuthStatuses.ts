// The hub's sign-in statuses by provider (evener/auth/list), for the words
// Providers shows beside each instance (ruling 6). This is the package's
// shared createAuthStatusesStore (state/credentials/authStatuses.ts) over this
// screen's connection: the store reads when the hook first has a client, reads
// again on evener/auth/updated, and a failed read keeps the last statuses and
// records why, so the page stays calm while the hub is away. Because the
// store's own triggers are a notification or a connection transition, and a
// read can fail while the socket stays ready with neither to follow, this hook
// retries a failed read on its own, waiting twice as long each time up to a
// minute, until one lands. Until a first read lands it answers null: nothing
// is known, so no account sign-in is called "Signed in" (statusOf). A client
// the screen replaces builds a fresh store, so a reply of the hub the previous
// one named never lands on the new one.
import type { AuthStatusResponse } from "@evener/appwire-client";
import { type AuthStatusesClient, createAuthStatusesStore } from "@evener/appwire-client/state/credentials";
import { useEffect, useState } from "react";

/** How long after a first failed read the hook's next one starts; each further
 * failure doubles it, up to AUTH_RETRY_MAX_MS. */
export const AUTH_RETRY_MS = 5_000;
export const AUTH_RETRY_MAX_MS = 60_000;

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
		let retry: ReturnType<typeof setTimeout> | undefined;
		let wait = AUTH_RETRY_MS;
		// The statuses the last landed read published. Each read publishes a
		// fresh Map, so this tells a landing from the error clearing at the
		// start of the next attempt, which must not reset the backoff.
		let landed: ReadonlyMap<string, AuthStatusResponse> | null = null;
		const stop = store.subscribe((state) => {
			setStatuses(state.authStatuses);
			if (state.authStatuses !== null && state.authStatuses !== landed) {
				// A read landed, so the next failure starts over.
				landed = state.authStatuses;
				wait = AUTH_RETRY_MS;
				clearTimeout(retry);
				retry = undefined;
			}
			// A failed read records its error and schedules nothing: with no
			// notification or connection transition to follow, this hook is
			// what asks again. `fetchAuthStatuses` never rejects, so the retry
			// is driven off the error the store records, not a rejected
			// promise, and one timer is enough however many writes follow.
			if (state.authStatusesError === null || retry !== undefined) return;
			const delay = wait;
			wait = Math.min(wait * 2, AUTH_RETRY_MAX_MS);
			retry = setTimeout(() => {
				retry = undefined;
				void store.getState().fetchAuthStatuses();
			}, delay);
		});
		// This first read populates the list. The store re-reads it on every
		// later evener/auth/updated (debounced), and `wantsList` - a list read
		// or a read that failed - is what makes a reconnect re-read it.
		void store.getState().fetchAuthStatuses();
		return () => {
			clearTimeout(retry);
			stop();
			// Drops every reply still in flight: a read of the client being
			// replaced must not publish against the next one.
			store.dispose();
		};
	}, [client]);
	return statuses;
}
