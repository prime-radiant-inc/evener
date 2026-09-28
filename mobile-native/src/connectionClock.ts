// Spec 14's clock for the connection status: how long the app has been in
// front without a live connection, and when its data was last live. Time in
// the background never counts as down: ConnectionProvider closes the
// connection there and dials again on the way back, so a return after an
// hour starts a fresh 2-second grace instead of flashing "Offline". The
// data's age does count the background: it is how old what you see is.

export interface ConnectionObservation {
	hubId: string | null;
	live: boolean;
	foreground: boolean;
}

export interface ConnectionTimes {
	/** When this stretch in front without a live connection began; null while
	 * live or in the background. */
	downSince: number | null;
	/** When this hub's connection last stopped being live in front (it
	 * dropped, or the app left the front); null until that has happened since
	 * the app launched or the hub was chosen. */
	lastLiveAt: number | null;
}

export class ConnectionClock {
	private last: ConnectionObservation | null = null;
	private times: ConnectionTimes = { downSince: null, lastLiveAt: null };

	observe(next: ConnectionObservation, now: number): ConnectionTimes {
		const last = this.last;
		this.last = next;
		const liveInFront = next.live && next.foreground;
		let { downSince, lastLiveAt } = this.times;
		if (next.hubId === null) {
			// No hub chosen: there is no connection to be down.
			downSince = null;
			lastLiveAt = null;
		} else if (last === null || last.hubId !== next.hubId) {
			// A new hub, or the first observation: nothing of it was live yet.
			lastLiveAt = null;
			downSince = next.live || !next.foreground ? null : now;
		} else {
			// The data stops being live when the connection drops or the app
			// leaves the front, whichever the provider reports first: it closes
			// the connection in the background.
			if (last.live && last.foreground && !liveInFront) lastLiveAt = now;
			if (next.live || !next.foreground) downSince = null;
			else if (!last.foreground || downSince === null) downSince = now;
		}
		this.times = { downSince, lastLiveAt };
		return this.times;
	}
}
