/** Run one async job at a time on request. Requests made while a run is in
 * flight, or while `idle()` says the owner is busy, coalesce into a single
 * trailing run; `drain()` lets the owner retry once its state settles. */
export function singleFlight(
	run: () => Promise<unknown>,
	idle: () => boolean = () => true,
) {
	let requested = false,
		running = false;
	const drain = () => {
		if (!requested || running || !idle()) return;
		requested = false;
		running = true;
		void run().finally(() => {
			running = false;
			drain();
		});
	};
	return {
		drain,
		request() {
			requested = true;
			drain();
		},
		get running() {
			return running;
		},
	};
}
