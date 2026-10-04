// Polls evener/activity/read on an interval (S5): the pulse meter's per-minute
// counts, running-subagent tally and quiet time, for the Board (no refs, every
// live top-level session) or one session screen (refs: [ref]). Decodes with
// decodeActivityRead and keeps the latest read by ref. A response only ever
// applies if it answers the newest poll this instance sent, so a slow answer
// to an older request can never overwrite a fresher one - the caller starts
// and stops this as screen focus and connection state change, a poll already
// in flight when the next tick fires must not win a race against it, and
// stop() itself retires the request tag so an answer already in flight when
// it's called - success or failure alike - lands as a no-op instead of
// reviving a stopped poll's data or verdict.
// A method-not-found answer (the JSON-RPC code the hub's dispatcher returns
// for a method it has never heard of: appwire.CodeMethodNotFound,
// appwire/errors.go) means this hub predates S5: polling stops for good and
// the caller keeps whatever fallback it already shows. Any other rejection
// just retries at the next tick.
import type { AppwireClientLike, SessionActivity } from "@evener/appwire-client";
import { isMethodNotFound } from "../wireErrors";
import { decodeActivityRead } from "@evener/appwire-client";

export const ACTIVITY_POLL_MS = 10_000;
// A read that's aged past two poll intervals is treated as no read at all
// (S5's stale-read rule): a hub that reports ready but has stopped actually
// delivering reads (the failure mode this exists for) must not read as
// "quiet" or "stuck" from staleness alone. Two intervals, not one, gives a
// single missed tick room to self-heal on the very next one before the row
// falls back.
export const STALE_AFTER_MS = 2 * ACTIVITY_POLL_MS;

/** Whether a read taken `msSinceRead` milliseconds ago (null: never landed)
 * is still trustworthy. */
export function isFreshRead(msSinceRead: number | null): boolean {
	return msSinceRead !== null && msSinceRead < STALE_AFTER_MS;
}

export class ActivityPoll {
	private bySession = new Map<string, SessionActivity>();
	private lastReadAt: number | null = null;
	private revision = 0;
	private readonly listeners = new Set<() => void>();
	private timer: ReturnType<typeof setInterval> | undefined;
	private latestRequestId = 0;
	private unsupported = false;

	constructor(
		private readonly client: Pick<AppwireClientLike, "request">,
		private readonly refs?: readonly string[],
		private readonly now: () => number = Date.now,
	) {}

	/** This instance's last-read activity for a session: undefined before its
	 * first successful read, or once a read comes back without it (the hub
	 * withholds an entry it cannot currently answer for; the caller's own
	 * fallback applies meanwhile). */
	activity(ref: string): SessionActivity | undefined {
		return this.bySession.get(ref);
	}

	/** Milliseconds since the read that populated the current activity map,
	 * or null before any read has succeeded. */
	msSinceRead(): number | null {
		return this.lastReadAt === null ? null : Math.max(0, this.now() - this.lastReadAt);
	}

	/** False once a method-not-found answer has confirmed this hub predates
	 * S5. start() refuses to run again once this turns false. */
	get supported(): boolean {
		return !this.unsupported;
	}

	/** Starts polling (immediately, then every ACTIVITY_POLL_MS) unless this
	 * hub is already known not to support S5, or polling is already running.
	 * Idempotent either way. */
	start(): void {
		if (this.unsupported || this.timer !== undefined) return;
		this.poll();
		this.timer = setInterval(() => this.poll(), ACTIVITY_POLL_MS);
	}

	/** Stops polling and forgets the last read: a stopped poll's read says
	 * nothing about now, and restarted before it aged out it would show an old
	 * count (a tray's "Waiting on 3 subagents" from before an idle spell)
	 * until the next read landed. */
	stop(): void {
		if (this.timer === undefined) return;
		this.halt();
		this.bySession = new Map();
		this.lastReadAt = null;
		this.notify();
	}

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => this.listeners.delete(listener);
	};

	getRevision = (): number => this.revision;

	/** Stops the timer, keeping the last read (left to age out on its own
	 * when a hub turns out to predate S5 mid-session). */
	private halt(): void {
		clearInterval(this.timer);
		this.timer = undefined;
		// Invalidates a poll already in flight: its answer, whichever way it
		// lands, must not apply after stopping (a fresh-looking read right
		// after a later restart, or a stale hub predates S5 verdict).
		this.latestRequestId++;
	}

	private async poll(): Promise<void> {
		const requestId = ++this.latestRequestId;
		let response: unknown;
		try {
			response = await this.client.request("evener/activity/read", this.refs?.length ? { refs: [...this.refs] } : {});
		} catch (error) {
			if (requestId !== this.latestRequestId) return; // superseded by a newer poll, or stopped
			if (isMethodNotFound(error)) {
				this.unsupported = true;
				this.halt();
				this.notify();
			}
			return;
		}
		if (requestId !== this.latestRequestId) return; // a newer poll already replaced this one
		let sessions: SessionActivity[];
		try {
			sessions = decodeActivityRead(response);
		} catch {
			return; // an unreadable response keeps the previous read rather than blanking it
		}
		this.bySession = new Map(sessions.map((session) => [session.ref, session]));
		this.lastReadAt = this.now();
		this.notify();
	}

	/** Every change to what this instance reports (a landed read, a stop
	 * forgetting it, or `supported` turning false) bumps the revision and tells subscribers. */
	private notify(): void {
		this.revision++;
		for (const listener of [...this.listeners]) listener();
	}
}
