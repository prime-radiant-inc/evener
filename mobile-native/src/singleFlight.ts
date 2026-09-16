/** Run one async job at a time on request. Requests made while a run is in
 * flight, or while `idle()` says the owner is busy, coalesce into a single
 * trailing run; `drain()` lets the owner retry once its state settles, and
 * `settle()` drops a pending request the owner has satisfied another way. */
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
		try {
			void run().finally(() => {
				running = false;
				drain();
			});
		} catch (error) {
			// A run that throws before returning its promise must not leave the
			// flight marked running, or every later request would be dropped.
			running = false;
			throw error;
		}
	};
	return {
		drain,
		request() {
			requested = true;
			drain();
		},
		settle() {
			requested = false;
		},
		get running() {
			return running;
		},
	};
}
