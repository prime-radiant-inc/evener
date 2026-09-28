// The phone's side of the shared hub-update client (ruling 22): the Hub
// sheet's controller, and how the phone notices a restarted hub. The app
// reconnects on its own (hubConnection.ts), so the restart wait needs no
// clock: each time the connection is ready again it asks the hub its version,
// and it is done once that differs from the version the update replaced.
// While the hub is away the sheet's connection line says so (spec 14).
import { type AppwireClientLike, createHubUpdateController, type HubUpdateController } from "@evener/appwire-client";

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
		awaitRestart: (previousVersion) =>
			new Promise<boolean>((resolve) => {
				let done = false;
				const finish = (back: boolean) => {
					if (done) return;
					done = true;
					unsubscribe();
					endWaits.delete(end);
					resolve(back);
				};
				const end = () => finish(false);
				const unsubscribe = readiness.subscribe(() => {
					if (!client || !readiness.isReady()) return;
					client
						.request("evener/update/check", { channel: controller.getState().channel ?? "" })
						.then((check) => {
							if (check.currentVersion !== previousVersion) finish(true);
						})
						// A check that fails while the hub settles waits for the
						// next time the connection is ready.
						.catch(() => {});
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
