// The hub's seen marker on the phone (S4). A row that carries turn_ended_at is
// the hub's to decide: it is Finished while the hub says unseen. Marks this
// phone makes go to the hub through evener/session/seen/set and show at once
// through a pending map until the hub's rows catch up. A row without
// turn_ended_at (an older hub, or a daemon that hasn't stamped a turn end)
// keeps the device's own SeenMarkers.
//
// This module must not import expo-sqlite/kv-store: the session screen
// imports it, and its test harnesses mock kv-store only partly.
import { type NavigationSessionSummary, type SessionSeenMark, WireError } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { hubTime } from "./attention";
import type { SeenMarkers } from "./boardMemory";

/** The hub caps one seen/set call at 500 marks. */
const MAX_MARKS_PER_CALL = 500;
const METHOD_NOT_FOUND = -32601;

/** A mark as seen/set carries it, less its ref. */
type PendingMark = { seenThrough: number } | { unread: true };
interface PendingEntry {
	mark: PendingMark;
	/** The hub answered a call carrying this very entry. */
	acknowledged: boolean;
}
type HubRow = Pick<NavigationSessionSummary, "ref" | "turn_ended_at" | "unseen">;

// Clients whose hub answered seen/set with method not found: an older hub.
// Per client object, so a reconnect to an upgraded hub tries again.
const withoutSeenSet = new WeakSet<ConversationClientLike>();

/** One hub's pending seen marks and the calls that carry them. Calls go one
 * at a time and in order, because the hub may handle two requests at once and
 * a quick "mark seen, then mark unread" must never land reversed. The call is
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
	 * applied; null when the row has no turn_ended_at and the device decides. */
	isSeenOnHub(row: HubRow): boolean | null {
		if (!row.turn_ended_at) return null;
		const entry = this.pending.get(row.ref);
		if (entry) {
			if ("unread" in entry.mark) return false;
			if (Date.parse(row.turn_ended_at) <= entry.mark.seenThrough) return true;
		}
		return row.unseen !== true;
	}

	/** Records seen marks and sends them. A mark that doesn't advance a pending
	 * seen mark for the same ref records and sends nothing, so opening a
	 * session twice costs one request. */
	markSeen(client: ConversationClientLike | null, marks: readonly { ref: string; seenThrough: number }[]): void {
		let changed = false;
		for (const { ref, seenThrough } of marks) {
			if (!Number.isFinite(seenThrough) || seenThrough <= 0) continue;
			const current = this.pending.get(ref)?.mark;
			if (current && "seenThrough" in current && current.seenThrough >= seenThrough) continue;
			this.pending.set(ref, { mark: { seenThrough }, acknowledged: false });
			changed = true;
		}
		if (changed) this.changed();
		if (changed && client) this.flush(client);
	}

	/** Records unread marks and sends them. */
	markUnread(client: ConversationClientLike | null, refs: readonly string[]): void {
		let changed = false;
		for (const ref of refs) {
			const current = this.pending.get(ref)?.mark;
			if (current && "unread" in current) continue;
			this.pending.set(ref, { mark: { unread: true }, acknowledged: false });
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
		this.sending = true;
		void this.send().finally(() => {
			this.sending = false;
		});
	}

	/** Drops each pending mark the hub's rows show landed, or show no longer
	 * applies: a seen mark once the row reads seen or a newer turn ended, an
	 * unread mark once the row reads unseen. */
	prune(rows: Iterable<HubRow>): void {
		let changed = false;
		for (const row of rows) {
			const entry = this.pending.get(row.ref);
			if (!entry) continue;
			const ended = hubTime(row.turn_ended_at);
			const done =
				"unread" in entry.mark
					? row.unseen === true
					: row.unseen !== true || (ended !== null && ended > entry.mark.seenThrough);
			if (done) {
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

	private async send(): Promise<void> {
		for (;;) {
			const client = this.client;
			if (!client || withoutSeenSet.has(client)) return;
			const batch = [...this.pending].filter(([, entry]) => !entry.acknowledged).slice(0, MAX_MARKS_PER_CALL);
			if (batch.length === 0) return;
			const sessions: SessionSeenMark[] = batch.map(([ref, { mark }]) => ({ ref, ...mark }));
			// An entry replaced while its call was out is a newer mark: only the
			// entry that was sent takes the call's outcome.
			const stillSent = ([ref, entry]: [string, PendingEntry]) => this.pending.get(ref) === entry;
			try {
				await client.request("evener/session/seen/set", { sessions });
				for (const [, entry] of batch.filter(stillSent)) entry.acknowledged = true;
			} catch (error) {
				if (!(error instanceof WireError)) {
					// Closed or timed out: the marks go again on the next flush, or
					// now if a new connection arrived while this call was out.
					if (this.client === client) return;
					continue;
				}
				// The hub refused: resending is pointless, so the rows show the
				// hub's own state again.
				if (error.code === METHOD_NOT_FOUND) withoutSeenSet.add(client);
				const refused = batch.filter(stillSent);
				for (const [ref] of refused) this.pending.delete(ref);
				if (refused.length) this.changed();
			}
		}
	}

	private changed(): void {
		this.revision++;
		for (const listener of [...this.listeners]) listener();
	}
}

// One controller per hub, in memory only, like seenMarkers(hubId).
const controllers = new Map<string, HubSeenMarks>();

export function hubSeenMarks(hubId: string): HubSeenMarks {
	let marks = controllers.get(hubId);
	if (!marks) {
		marks = new HubSeenMarks();
		controllers.set(hubId, marks);
	}
	return marks;
}

export function forgetHubSeenMarks(hubId: string): void {
	controllers.delete(hubId);
}

/** The Board's seen state over both paths: the hub decides a row that
 * carries turn_ended_at, and the device's SeenMarkers decides any other.
 * Mark as read and Mark as unread (part 3's long-press menu and select mode)
 * call markRead and markUnread; each sends one call for all its hub rows. */
export class BoardSeen {
	constructor(
		private readonly markers: SeenMarkers,
		private readonly hub: HubSeenMarks,
	) {}

	isSeen(row: NavigationSessionSummary): boolean {
		return this.hub.isSeenOnHub(row) ?? this.markers.isSeen(row);
	}

	/** Opening a row marks it seen through the turn it showed. */
	open(client: ConversationClientLike | null, row: NavigationSessionSummary): void {
		this.markRead(client, [row]);
	}

	markRead(client: ConversationClientLike | null, rows: readonly NavigationSessionSummary[]): void {
		const marks: { ref: string; seenThrough: number }[] = [];
		for (const row of rows) {
			if (row.turn_ended_at) marks.push({ ref: row.ref, seenThrough: Date.parse(row.turn_ended_at) });
			else this.markers.markSeen(row);
		}
		if (marks.length) this.hub.markSeen(client, marks);
	}

	markUnread(client: ConversationClientLike | null, rows: readonly NavigationSessionSummary[]): void {
		const refs: string[] = [];
		for (const row of rows) {
			if (row.turn_ended_at) refs.push(row.ref);
			else this.markers.markUnread(row.ref);
		}
		if (refs.length) this.hub.markUnread(client, refs);
	}
}
