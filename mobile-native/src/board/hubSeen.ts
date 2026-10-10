// The hub's seen marker on the phone (S4). A row that carries a readable
// turn_ended_at is the hub's to decide: it is Finished while the hub says
// unseen. Marks this phone makes go to the hub through
// evener/session/seen/set and show at once through a pending map until the
// hub's rows catch up. A row without a readable turn_ended_at (an older hub,
// or a daemon that hasn't stamped a turn end) keeps the device's own
// SeenMarkers.
import {
	isMethodNotFound,
	type NavigationSessionSummary,
	type SessionSeenMark,
	WireError,
} from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { hubTime } from "./attention";
import type { SeenMarkers } from "./boardMemory";
import { perHub } from "./perHub";

/** The hub caps one seen/set call at 500 marks. */
const MAX_MARKS_PER_CALL = 500;
// The hub's answers that mean a mark will never land there (appwire/errors.go):
// an older hub without seen/set, and a mark it rejects as malformed or naming
// an unknown host. Its other errors (a store error, an unavailable navigation
// service) say nothing about the mark, which goes again on the next flush.
const INVALID_PARAMS = -32602;

interface PendingEntry {
	/** The turn end the mark reads the session through, in ms. */
	seenThrough: number;
	/** The hub answered a call carrying this very entry. */
	acknowledged: boolean;
}
type HubRow = Pick<NavigationSessionSummary, "ref" | "turn_ended_at" | "unseen">;

// Clients whose hub answered seen/set with method not found: an older hub.
// Per client object, so a reconnect to an upgraded hub tries again.
const withoutSeenSet = new WeakSet<ConversationClientLike>();

/** One hub's pending seen marks and the calls that carry them. The hub keeps
 * the newest seen-through it is sent, so order can't undo a mark; calls go one
 * at a time to keep the acknowledgement bookkeeping simple. The call is
 * idempotent, so a mark whose call failed in transit is simply sent again on
 * the next flush; there is no recovery journal. */
export class HubSeenMarks {
	private pending = new Map<string, PendingEntry>();
	private revision = 0;
	private listeners = new Set<() => void>();
	/** The client marks go out on, or null while there is no ready one. */
	private client: ConversationClientLike | null = null;
	private sending = false;

	/** Whether the hub counts this row as seen, with this phone's pending marks
	 * applied; null when the row has no readable turn_ended_at and the device
	 * decides. */
	isSeenOnHub(row: HubRow): boolean | null {
		const ended = hubTurnEnd(row);
		if (ended === null) return null;
		const entry = this.pending.get(row.ref);
		if (entry && ended <= entry.seenThrough) return true;
		return row.unseen !== true;
	}

	/** This phone's pending seen-through mark for a ref, if it has one. */
	pendingSeenThrough(ref: string): number | undefined {
		return this.pending.get(ref)?.seenThrough;
	}

	/** Records seen marks and sends them. A mark that doesn't advance a pending
	 * seen mark for the same ref records and sends nothing, so opening a
	 * session twice costs one request. */
	markSeen(client: ConversationClientLike | null, marks: readonly { ref: string; seenThrough: number }[]): void {
		let changed = false;
		for (const { ref, seenThrough } of marks) {
			if (!Number.isFinite(seenThrough) || seenThrough <= 0) continue;
			const current = this.pending.get(ref);
			if (current && current.seenThrough >= seenThrough) continue;
			this.pending.set(ref, { seenThrough, acknowledged: false });
			changed = true;
		}
		if (changed) this.changed();
		if (changed && client) this.flush(client);
	}

	/** Sends every mark the hub hasn't acknowledged over this client; null
	 * says there is no ready client, and marks wait for one. */
	flush(client: ConversationClientLike | null): void {
		this.client = client;
		if (!client || this.sending) return;
		void this.send();
	}

	/** Drops each pending mark the hub's rows show landed, or show no longer
	 * applies: once the row reads seen or a newer turn ended. Only a row the
	 * hub decides can show either, so a row without a readable turn_ended_at
	 * is skipped. */
	prune(rows: Iterable<HubRow>): void {
		let changed = false;
		for (const row of rows) {
			const entry = this.pending.get(row.ref);
			if (!entry) continue;
			const ended = hubTurnEnd(row);
			if (ended === null) continue;
			if (row.unseen !== true || ended > entry.seenThrough) {
				this.pending.delete(row.ref);
				changed = true;
			}
		}
		if (changed) this.changed();
	}

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => this.listeners.delete(listener);
	};

	getRevision = (): number => this.revision;

	/** Sends until nothing unacknowledged is left or there is no client to
	 * send on. `sending` resets in the same tick the loop ends: a flush right
	 * after one that found nothing to send must start a loop of its own, and
	 * a finally chained on the returned promise would run a microtask late. */
	private async send(): Promise<void> {
		this.sending = true;
		try {
			for (;;) {
				const client = this.client;
				if (!client || withoutSeenSet.has(client)) return;
				const batch = [...this.pending].filter(([, entry]) => !entry.acknowledged).slice(0, MAX_MARKS_PER_CALL);
				if (batch.length === 0) return;
				const sessions: SessionSeenMark[] = batch.map(([ref, { seenThrough }]) => ({ ref, seenThrough }));
				// An entry replaced while its call was out is a newer mark: only the
				// entry that was sent takes the call's outcome.
				const stillSent = ([ref, entry]: [string, PendingEntry]) => this.pending.get(ref) === entry;
				try {
					await client.request("evener/session/seen/set", { sessions });
					for (const [, entry] of batch.filter(stillSent)) entry.acknowledged = true;
				} catch (error) {
					const code = error instanceof WireError ? error.code : null;
					if (isMethodNotFound(error)) withoutSeenSet.add(client);
					// The connection changed while the call was out: whatever
					// the failure, the marks go again over the current one.
					if (this.client !== client) continue;
					// Closed, timed out or failed for now: the marks go again
					// on the next flush.
					if (!isMethodNotFound(error) && code !== INVALID_PARAMS) return;
					// The hub refused for good: resending is pointless, so the rows
					// show the hub's own state again.
					const refused = batch.filter(stillSent);
					for (const [ref] of refused) this.pending.delete(ref);
					if (refused.length) this.changed();
				}
			}
		} finally {
			this.sending = false;
		}
	}

	private changed(): void {
		this.revision++;
		for (const listener of [...this.listeners]) listener();
	}
}

/** Whether the row's hub tracks seen-through marks, so a mark can follow the
 * session's motion as well as its turn end: an older hub sends no
 * seen_through. */
export function tracksSeenThrough(row: NavigationSessionSummary): boolean {
	return row.seen_through !== undefined;
}

/** When the row's last turn ended, in ms, if the hub decides the row: a row
 * without a readable turn_ended_at is the device's to decide. */
function hubTurnEnd(row: Pick<HubRow, "turn_ended_at">): number | null {
	return hubTime(row.turn_ended_at);
}

// One controller per hub, in memory only, like seenMarkers(hubId).
const controllers = perHub(() => new HubSeenMarks());

export function hubSeenMarks(hubId: string): HubSeenMarks {
	return controllers.get(hubId);
}

export function forgetHubSeenMarks(hubId: string): void {
	controllers.forget(hubId);
}

/** The Board's seen state over both paths: the hub decides a row that
 * carries a readable turn_ended_at, and the device's SeenMarkers decides any
 * other. Opening a row marks it read through the turn it showed, in one call
 * for all its hub rows. */
export class BoardSeen {
	constructor(
		private readonly markers: SeenMarkers,
		private readonly hub: HubSeenMarks,
	) {}

	isSeen(row: NavigationSessionSummary): boolean {
		return this.hub.isSeenOnHub(row) ?? this.markers.isSeen(row);
	}

	/** Whether the session's tree moved after the hub's seen-through mark for
	 * it (or this phone's newer pending one): output the person hasn't seen,
	 * mid-turn included. False without both times: an older hub sends no
	 * seen_through, and a session that hasn't moved since its daemon began
	 * serving it reports no last-moved time. */
	movedSinceSeen(row: NavigationSessionSummary, lastMovedAt: number | undefined): boolean {
		const mark = hubTime(row.seen_through);
		if (lastMovedAt === undefined || mark === null) return false;
		return lastMovedAt > Math.max(mark, this.hub.pendingSeenThrough(row.ref) ?? 0);
	}

	/** Marks rows seen through the later of their turn end and their last
	 * motion (lastMovedAt), on the hub when it decides the row or tracks its
	 * seen-through mark, else with the device's own marker. */
	markRead(
		client: ConversationClientLike | null,
		rows: readonly NavigationSessionSummary[],
		lastMovedAt: (row: NavigationSessionSummary) => number | undefined = () => undefined,
	): void {
		const marks: { ref: string; seenThrough: number }[] = [];
		for (const row of rows) {
			const ended = hubTurnEnd(row);
			const moved = tracksSeenThrough(row) ? lastMovedAt(row) : undefined;
			if (ended === null) this.markers.markSeen(row);
			const through = Math.max(ended ?? 0, moved ?? 0);
			if (through > 0) marks.push({ ref: row.ref, seenThrough: through });
		}
		this.hub.markSeen(client, marks);
	}
}
