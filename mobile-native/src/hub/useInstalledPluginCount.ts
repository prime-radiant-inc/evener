// How many plugins the hub has installed (evener/plugin/list), for the Hub
// home's Plugins row. It reads when it gets a client and again on each
// evener/plugin/updated, and a failed read keeps the last count: the home
// stays calm while the hub is away.
import type { AppwireClient } from "@evener/appwire-client";
import { useEffect, useState } from "react";

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
		const read = () => {
			const seq = ++latest;
			client.request("evener/plugin/list", {}).then(
				(result) => {
					// Go sends an empty (nil) slice as null.
					if (live && seq === latest) setCount(result.plugins?.length ?? 0);
				},
				() => {},
			);
		};
		read();
		const stop = client.onNotification((notification) => {
			if (notification.method === "evener/plugin/updated") read();
		});
		return () => {
			live = false;
			stop();
		};
	}, [client]);
	return count;
}
