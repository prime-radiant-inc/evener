// The hub's sign-in statuses by provider (evener/auth/list), for the words
// Providers shows beside each instance (ruling 6). It reads when it gets a
// client and again on each evener/auth/updated, and a failed read keeps the
// last statuses: the page stays calm while the hub is away. A failed read
// also tries again on its own, waiting twice as long each time up to a
// minute, until one lands. Until a first read lands it answers null: nothing
// is known, so no account sign-in is called "Signed in" (statusOf).
import type { AuthStatusResponse } from "@evener/appwire-client";
import { useEffect, useState } from "react";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { authByProvider } from "../providers/providerStatus";

/** How long after a first failed read the next one starts; each further
 * failure doubles it, up to AUTH_RETRY_MAX_MS. */
export const AUTH_RETRY_MS = 5_000;
export const AUTH_RETRY_MAX_MS = 60_000;

export function useAuthStatuses(client: ConversationClientLike | null): ReadonlyMap<string, AuthStatusResponse> | null {
	const [statuses, setStatuses] = useState<ReadonlyMap<string, AuthStatusResponse> | null>(null);
	useEffect(() => {
		if (!client) return;
		let live = true;
		// Only the newest read lands: an older answer arriving late would
		// otherwise put back a sign-in the hub has since renewed.
		let latest = 0;
		let retry: ReturnType<typeof setTimeout> | undefined;
		let wait = AUTH_RETRY_MS;
		const read = () => {
			clearTimeout(retry);
			const seq = ++latest;
			client.request("evener/auth/list", {}).then(
				(result) => {
					if (!live || seq !== latest) return;
					wait = AUTH_RETRY_MS;
					// Go sends an empty (nil) slice as null.
					setStatuses(authByProvider(result.providers ?? []));
				},
				() => {
					if (!live || seq !== latest) return;
					retry = setTimeout(read, wait);
					wait = Math.min(wait * 2, AUTH_RETRY_MAX_MS);
				},
			);
		};
		read();
		const stop = client.onNotification((notification) => {
			if (notification.method === "evener/auth/updated") read();
		});
		return () => {
			live = false;
			clearTimeout(retry);
			stop();
		};
	}, [client]);
	return statuses;
}
