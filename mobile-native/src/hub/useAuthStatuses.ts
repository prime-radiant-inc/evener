// The hub's sign-in statuses by provider (evener/auth/list), for the words
// Providers shows beside each instance (ruling 6). It reads when it gets a
// client and again on each evener/auth/updated, and a failed read keeps the
// last statuses: the page stays calm while the hub is away. A failed read
// also tries again on its own a little later, until one lands, so a failed
// first read can't leave an expired sign-in reading "Signed in".
import type { AuthStatusResponse } from "@evener/appwire-client";
import { useEffect, useState } from "react";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { authByProvider } from "../providers/providerStatus";

const NONE: ReadonlyMap<string, AuthStatusResponse> = new Map();

/** How long after a failed read the next one starts. */
export const AUTH_RETRY_MS = 5_000;

export function useAuthStatuses(client: ConversationClientLike | null): ReadonlyMap<string, AuthStatusResponse> {
	const [statuses, setStatuses] = useState(NONE);
	useEffect(() => {
		if (!client) return;
		let live = true;
		// Only the newest read lands: an older answer arriving late would
		// otherwise put back a sign-in the hub has since renewed.
		let latest = 0;
		let retry: ReturnType<typeof setTimeout> | undefined;
		const read = () => {
			clearTimeout(retry);
			const seq = ++latest;
			client.request("evener/auth/list", {}).then(
				(result) => {
					// Go sends an empty (nil) slice as null.
					if (live && seq === latest) setStatuses(authByProvider(result.providers ?? []));
				},
				() => {
					if (live && seq === latest) retry = setTimeout(read, AUTH_RETRY_MS);
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
