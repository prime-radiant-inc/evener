// The phone's side of the shared hub-update client (ruling 22): the Hub
// sheet's controller, and how the phone notices a restarted hub. The app
// reconnects on its own (hubConnection.ts), so the restart wait needs no
// clock: the first time the connection is ready again it asks the hub its
// version. A new version is the update installed; the old one is a restart
// that came back on the build it had. While the hub is away the sheet's
// connection line says so (spec 14).
import { type AppwireClientLike, createHubUpdateController, type HubUpdateController } from "@evener/appwire-client";
import { useEffect, useMemo, useState } from "react";

/** A restarted hub that never reconnects must not leave the sheet waiting forever. */
export const RESTART_DEADLINE_MS = 30_000;
/** How often the restart wait re-asks while the old process answers. */
export const RESTART_RECHECK_MS = 1_000;

/** Whether the sheet's connection is ready, and a signal each time it turns
 * ready. The sheet sets it from the connection it renders. */
export interface Readiness {
	isReady(): boolean;
	/** Runs `listener` each time readiness turns true. */
	subscribe(listener: () => void): () => void;
	set(ready: boolean): void;
}

export function createReadiness(): Readiness {
	let ready = false;
	const listeners = new Set<() => void>();
	return {
		isReady: () => ready,
		subscribe(listener) {
			listeners.add(listener);
			return () => {
				listeners.delete(listener);
			};
		},
		set(next) {
			const turnedReady = next && !ready;
			ready = next;
			if (turnedReady) for (const listener of [...listeners]) listener();
		},
	};
}

export interface PhoneHubUpdates {
	controller: HubUpdateController;
	/** Stops the controller and ends any restart wait (it resolves false). */
	dispose(): void;
}

export function createPhoneHubUpdates(
	client: Pick<AppwireClientLike, "request"> | null,
	readiness: Pick<Readiness, "isReady" | "subscribe">,
): PhoneHubUpdates {
	const endWaits = new Set<() => void>();
	const controller = createHubUpdateController({
		client() {
			if (!client) throw new Error("The hub isn't connected.");
			return client;
		},
		// The phone has no health fetch of its own; the check's startedAt is
		// refreshed on every reconnect and is the baseline it uses.
		currentIncarnation: async () => null,
		awaitRestart: (previous) =>
			new Promise<boolean>((resolve) => {
				let done = false;
				let timer: ReturnType<typeof setTimeout>;
				let retry: ReturnType<typeof setTimeout> | undefined;
				let unsubscribe: () => void = () => {};
				const finish = (back: boolean) => {
					if (done) return;
					done = true;
					clearTimeout(timer);
					if (retry) clearTimeout(retry);
					unsubscribe();
					endWaits.delete(end);
					resolve(back);
				};
				const end = () => finish(false);
				const later = () => {
					if (done || retry) return;
					retry = setTimeout(() => {
						retry = undefined;
						checkNow();
					}, RESTART_RECHECK_MS);
				};
				const checkNow = () => {
					if (done) return;
					if (!readiness.isReady()) {
						later();
						return;
					}
					void controller.runCheck().then(() => {
						if (done) return;
						const check = controller.getState().check;
						const sameProcess =
							previous.incarnation !== null
								? check?.startedAt === previous.incarnation
								: check?.currentVersion === previous.version;
						if (check !== null && !sameProcess) {
							finish(true);
							return;
						}
						// The old process may still be waiting on the install
						// lock, and a check can fail while the socket
						// reconnects again: keep observing until the deadline
						// instead of declaring a timeout on the first answer.
						later();
					});
				};
				timer = setTimeout(end, RESTART_DEADLINE_MS);
				// The wait owns the checks the return to ready makes
				// (useHubUpdates skips its own while restarting). An
				// incarnation comparison is what makes a same-version rebuild
				// visible.
				unsubscribe = readiness.subscribe(() => {
					if (readiness.isReady()) checkNow();
				});
				endWaits.add(end);
			}),
	});
	return {
		controller,
		dispose() {
			controller.dispose();
			for (const end of [...endWaits]) end();
		},
	};
}

/** The Hub sheet's update controller: one per client, checked each time the
 * connection is ready with a client to ask. That covers opening the sheet
 * and every reconnect, which is also how a restarted hub is noticed. A ready
 * render can come before the connection's client is adopted, so the check
 * waits for both. */
export function useHubUpdates(client: Pick<AppwireClientLike, "request"> | null, ready: boolean): HubUpdateController {
	const [readiness] = useState(createReadiness);
	const updates = useMemo(() => createPhoneHubUpdates(client, readiness), [client, readiness]);
	useEffect(() => () => updates.dispose(), [updates]);
	const reachable = ready && client !== null;
	useEffect(() => {
		// While a restart waits, its own check answers the return to ready.
		if (reachable && !updates.controller.getState().restarting) void updates.controller.runCheck();
		readiness.set(reachable);
	}, [reachable, readiness, updates]);
	return updates.controller;
}
