/** Run one async job at a time on request. Requests made while a run is in
 * flight, or while `idle()` says the owner is busy, coalesce into a single
 * trailing run; `drain()` lets the owner retry once its state settles, and
 * `settle()` drops a pending request the owner has satisfied another way.
 * `abandon()` stops counting the in-flight run as running for an owner that
 * dropped the work and whose promise may never settle; the next request starts
 * a fresh run, and the abandoned run's own late settle must not disturb it. */
export function singleFlight(run: () => Promise<unknown>, idle: () => boolean = () => true) {
	let requested = false,
		running = false,
		generation = 0;
	const drain = () => {
		if (!requested || running || !idle()) return;
		requested = false;
		running = true;
		const gen = ++generation;
		try {
			void run().finally(() => {
				if (gen !== generation) return;
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
		abandon() {
			// The owner dropped the run in flight; it may never settle, so a
			// later drain must be free to start a replacement. Bumping the
			// generation keeps the dropped run's late finally from clearing the
			// replacement's flags.
			generation++;
			running = false;
		},
		get running() {
			return running;
		},
	};
}
