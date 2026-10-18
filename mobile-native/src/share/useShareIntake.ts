// The app's side of the share intent (spec: share intent): it listens for the
// extension's open URL, for the app coming forward, and for the hub connection
// turning ready, and hands the oldest pending share to `onShare`. A share is
// offered once; the sheet releases the intake when it closes, so the next one
// can be offered. The URL is only a trigger: the payload comes from the app
// group, which is what makes a failed open recoverable.
import { useEffect, useRef, useState } from "react";
import { AppState, Linking } from "react-native";
import { appGroupSharePort } from "./appGroupSharePort";
import { createShareIntake, type ShareIntake } from "./shareIntake";
import type { SharePort, SharedPayload } from "./sharePayload";

/** The scheme the extension opens. A URL is only a nudge to look again. */
export function isShareURL(url: string | null): boolean {
	return typeof url === "string" && url.startsWith("evener-native://share");
}

export interface ShareIntakeOptions {
	onShare(share: SharedPayload): void;
	/** The share directory. Production reads the app group; tests hand in their
	 * own. */
	port?: SharePort;
	/** Whether the hub connection is ready. Turning ready is a trigger, since a
	 * share that arrived with no hub could not be placed. */
	ready?: boolean;
}

/** Watches for pending shares for as long as the app is mounted. The returned
 * intake lets the sheet release the next share and forget the one it placed. */
export function useShareIntake({ onShare, port, ready = false }: ShareIntakeOptions): ShareIntake {
	// One port for the app's life: the app group directory does not move.
	const [sharePort] = useState<SharePort>(() => port ?? appGroupSharePort());
	// The latest callback, so a rerender does not need a new intake.
	const handler = useRef(onShare);
	handler.current = onShare;
	const [intake] = useState(() => createShareIntake({ port: sharePort, onShare: (share) => handler.current(share) }));
	const wasReady = useRef(ready);
	useEffect(() => () => intake.dispose(), [intake]);
	useEffect(() => {
		let active = true;
		Linking.getInitialURL()
			.then((url) => {
				if (active && isShareURL(url)) void intake.offer();
			})
			.catch(() => {});
		const urlSubscription = Linking.addEventListener("url", (event) => {
			if (isShareURL(event.url)) void intake.offer();
		});
		const stateSubscription = AppState.addEventListener("change", (state) => {
			if (state === "active") void intake.offer();
		});
		// A cold launch after a failed open: no URL, no foreground change, and
		// possibly no hub yet. The scan still finds the share.
		void intake.offer();
		return () => {
			active = false;
			urlSubscription.remove();
			stateSubscription.remove();
		};
	}, [intake]);
	// A hub that came up after the share arrived is the difference between
	// "Connect to a hub" and placing it.
	useEffect(() => {
		if (ready && !wasReady.current) void intake.offer();
		wasReady.current = ready;
	}, [ready, intake]);
	return intake;
}
