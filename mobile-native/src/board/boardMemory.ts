// What this device remembers about one hub's Board: which sessions you have
// seen, which sections you folded, how you organize projects, and what you
// searched for. Kept in expo-sqlite's kv-store under per-hub keys that
// ConnectionProvider.removeHub clears.
import { isPlainObject } from "@evener/appwire-client";
import type { SyncStringStorage } from "../syncStringStorage";
import { hubTime } from "./attention";

const seenKey = (hubId: string) => `evener.native.seen.${hubId}`;
const foldedKey = (hubId: string) => `evener.native.board-sections.${hubId}`;
const organizeKey = (hubId: string) => `evener.native.board-organize.${hubId}`;
const recentSearchesKey = (hubId: string) => `evener.native.recent-searches.${hubId}`;
const MARK_LIMIT = 500;
const RECENT_LIMIT = 8;

function readJson(storage: SyncStringStorage, key: string): unknown {
	try {
		const raw = storage.getItemSync(key);
		return raw ? JSON.parse(raw) : null;
	} catch {
		return null;
	}
}
function writeJson(storage: SyncStringStorage, key: string, value: unknown): void {
	try {
		storage.setItemSync(key, JSON.stringify(value));
	} catch {
		// The in-memory copy still serves this launch.
	}
}
interface SeenRecord {
	through?: string;
	unread?: true;
}
interface SeenState {
	/** First run is done: the Board read this hub once and took the newest
	 * updated_at it saw as the epoch, or none when no row carried one. */
	adopted: boolean;
	epoch: string | null;
	sessions: Record<string, SeenRecord>;
}

function parseSeen(value: unknown): SeenState {
	const state: SeenState = { adopted: false, epoch: null, sessions: {} };
	if (!isPlainObject(value)) return state;
	// First run stands only with an epoch that reads as a hub time, or with
	// none because the fleet it read had no timestamps. A garbled epoch runs
	// first run again rather than flooding Finished.
	const epoch = typeof value.epoch === "string" && hubTime(value.epoch) !== null ? value.epoch : null;
	if (value.adopted === true && (epoch !== null || value.epoch === null)) {
		state.adopted = true;
		state.epoch = epoch;
	}
	if (isPlainObject(value.sessions))
		for (const [ref, record] of Object.entries(value.sessions)) {
			if (!isPlainObject(record)) continue;
			if (record.unread === true) state.sessions[ref] = { unread: true };
			else if (typeof record.through === "string" && hubTime(record.through) !== null)
				state.sessions[ref] = { through: record.through };
		}
	return state;
}

/** Whether you have opened each session since its last turn ended: Finished
 * until seen, then Idle (spec 13.1). Every comparison is between the hub's
 * own timestamps (a row's updated_at against the updated_at stored when you
 * opened it), so the phone's clock never matters. The first load on a device
 * adopts the newest updated_at it sees as an epoch, so sessions that ended
 * before this device ever showed the Board don't all arrive as unseen. A
 * first load with no timestamps (an empty fleet) completes first run too, so
 * the first session to finish after it arrives unseen. S4 replaces this with
 * a marker on the hub. */
export class SeenMarkers {
	private state: SeenState;
	private revision = 0;
	private listeners = new Set<() => void>();

	constructor(
		private readonly storage: SyncStringStorage,
		private readonly hubId: string,
	) {
		this.state = parseSeen(readJson(storage, seenKey(hubId)));
	}

	/** First run is done for this hub on this device: adoptEpoch has run. */
	get adopted(): boolean {
		return this.state.adopted;
	}

	isSeen(row: { ref: string; updated_at?: string }): boolean {
		const record = this.state.sessions[row.ref];
		if (record?.unread) return false;
		if (!this.state.adopted) return true;
		const updated = hubTime(row.updated_at);
		if (updated === null) return true;
		const through = Math.max(
			hubTime(record?.through) ?? Number.NEGATIVE_INFINITY,
			hubTime(this.state.epoch) ?? Number.NEGATIVE_INFINITY,
		);
		return updated <= through;
	}

	adoptEpoch(rows: readonly { updated_at?: string }[]): void {
		if (this.state.adopted) return;
		let newest: { value: string; time: number } | null = null;
		for (const row of rows) {
			const time = hubTime(row.updated_at);
			if (time !== null && row.updated_at && (newest === null || time > newest.time))
				newest = { value: row.updated_at, time };
		}
		this.state.adopted = true;
		this.state.epoch = newest?.value ?? null;
		this.save();
	}

	markSeen(row: { ref: string; updated_at?: string }): void {
		if (row.updated_at) this.state.sessions[row.ref] = { through: row.updated_at };
		else delete this.state.sessions[row.ref];
		this.save();
	}

	markUnread(ref: string): void {
		this.state.sessions[ref] = { unread: true };
		this.save();
	}

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => this.listeners.delete(listener);
	};

	getRevision = (): number => this.revision;

	private save(): void {
		const entries = Object.entries(this.state.sessions);
		if (entries.length > MARK_LIMIT) {
			// 500 marks in all, unread kept first: an unread mark is a choice you
			// made, so past the limit the oldest seen marks go first (they matter
			// least: the epoch covers old sessions).
			entries.sort(
				([, a], [, b]) =>
					(a.unread ? 0 : 1) - (b.unread ? 0 : 1) ||
					(hubTime(b.through) ?? 0) - (hubTime(a.through) ?? 0),
			);
			this.state.sessions = Object.fromEntries(entries.slice(0, MARK_LIMIT));
		}
		writeJson(this.storage, seenKey(this.hubId), this.state);
		this.revision++;
		for (const listener of [...this.listeners]) listener();
	}
}

/** Which Board sections you folded, per device and hub (spec 7.1). */
export class FoldedSections {
	private folded: Record<string, boolean> = {};

	constructor(
		private readonly storage: SyncStringStorage,
		private readonly hubId: string,
	) {
		const value = readJson(storage, foldedKey(hubId));
		if (isPlainObject(value))
			for (const [section, folded] of Object.entries(value))
				if (typeof folded === "boolean") this.folded[section] = folded;
	}

	isFolded(section: string, byDefault: boolean): boolean {
		return this.folded[section] ?? byDefault;
	}

	setFolded(section: string, folded: boolean): void {
		this.folded[section] = folded;
		writeJson(this.storage, foldedKey(this.hubId), this.folded);
	}
}

/** How the Board's Projects section nests sessions once the hub has more
 * than one host (spec 7.1's "Organize by"): by project, then host (the
 * default, as on the web), or by host, then project. */
export type OrganizeBy = "project-host" | "host-project";

/** The Organize by choice, per device and hub, stored as a JSON string. It
 * has its own key: FoldedSections stores a flat map of fold flags, and a mode
 * is not a fold. */
export class OrganizeByPreference {
	private value: OrganizeBy;

	constructor(
		private readonly storage: SyncStringStorage,
		private readonly hubId: string,
	) {
		// Unreadable storage, or a value this build doesn't know, reads as the default.
		this.value = readJson(storage, organizeKey(hubId)) === "host-project" ? "host-project" : "project-host";
	}

	get(): OrganizeBy {
		return this.value;
	}

	set(value: OrganizeBy): void {
		this.value = value;
		writeJson(this.storage, organizeKey(this.hubId), value);
	}
}

/** The last queries you searched and opened a result from, most recent
 * first, per device and hub. */
export class RecentSearches {
	private queries: string[];

	constructor(
		private readonly storage: SyncStringStorage,
		private readonly hubId: string,
	) {
		const value = readJson(storage, recentSearchesKey(hubId));
		this.queries = Array.isArray(value)
			? value.filter((query): query is string => typeof query === "string" && query !== "").slice(0, RECENT_LIMIT)
			: [];
	}

	list(): string[] {
		return this.queries;
	}

	add(text: string): void {
		const query = text.trim();
		if (!query) return;
		this.queries = [query, ...this.queries.filter((other) => other !== query)].slice(0, RECENT_LIMIT);
		writeJson(this.storage, recentSearchesKey(this.hubId), this.queries);
	}

	clear(): void {
		this.queries = [];
		writeJson(this.storage, recentSearchesKey(this.hubId), this.queries);
	}
}

export function forgetBoard(storage: SyncStringStorage, hubId: string): void {
	let failed = false;
	for (const key of [seenKey(hubId), foldedKey(hubId), organizeKey(hubId), recentSearchesKey(hubId)])
		try {
			storage.removeItemSync(key);
		} catch {
			// Keep trying the other keys: a storage failure orphans this one (hub
			// ids are fresh UUIDs, never reused, so nothing reads it again), but
			// the caller must still hear about it. ConnectionProvider's removeHub
			// cleanup runs this last, alongside cleanups that surface their own
			// storage failures the same way, so rethrowing here shows the user
			// the same "could not be deleted" message instead of a silently
			// incomplete removal.
			failed = true;
		}
	if (failed) throw new Error("forgetBoard: could not remove board memory from storage");
}
