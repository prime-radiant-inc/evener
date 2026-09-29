// How many plugins the hub has installed (evener/plugin/list), for the Hub
// home's Plugins row. It reads when it gets a client and again on each
// evener/plugin/updated, and a failed read keeps the last count: the home
// stays calm while the hub is away. A failed read also tries again on its own
// a little later, until one lands.
import type { AppwireClient } from "@evener/appwire-client";
import { useEffect, useState } from "react";

/** How long after a failed read the next one starts. */
export const PLUGIN_COUNT_RETRY_MS = 5_000;

export function useInstalledPluginCount(
	client: Pick<AppwireClient, "request" | "onNotification"> | null,
): number | null {
	const [count, setCount] = useState<number | null>(null);
	useEffect(() => {
		if (!client) return;
		let live = true;
		// Only the newest read lands: an older answer arriving late would put
		// back a count the hub has since changed.
		let latest = 0;
		let retry: ReturnType<typeof setTimeout> | undefined;
		const read = () => {
			clearTimeout(retry);
			const seq = ++latest;
			client.request("evener/plugin/list", {}).then(
				(result) => {
					// Go sends an empty (nil) slice as null.
					if (live && seq === latest) setCount(result.plugins?.length ?? 0);
				},
				() => {
					if (live && seq === latest) retry = setTimeout(read, PLUGIN_COUNT_RETRY_MS);
				},
			);
		};
		read();
		const stop = client.onNotification((notification) => {
			if (notification.method === "evener/plugin/updated") read();
		});
		return () => {
			live = false;
			clearTimeout(retry);
			stop();
		};
	}, [client]);
	return count;
}
