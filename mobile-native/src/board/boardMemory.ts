// What this device remembers about one hub's Board: which sessions you have
// seen and which sections you folded. Kept in expo-sqlite's kv-store under
// per-hub keys that ConnectionProvider.removeHub clears.

export interface BoardStorage {
	getItemSync(key: string): string | null;
	setItemSync(key: string, value: string): void;
	removeItemSync(key: string): void;
}

const seenKey = (hubId: string) => `evener.native.seen.${hubId}`;
const foldedKey = (hubId: string) => `evener.native.board-sections.${hubId}`;
const SEEN_LIMIT = 500;

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}
function readJson(storage: BoardStorage, key: string): unknown {
	try {
		const raw = storage.getItemSync(key);
		return raw ? JSON.parse(raw) : null;
	} catch {
		return null;
	}
}
function writeJson(storage: BoardStorage, key: string, value: unknown): void {
	try {
		storage.setItemSync(key, JSON.stringify(value));
	} catch {
		// The in-memory copy still serves this launch.
	}
}
function timeOf(value: string | null | undefined): number | null {
	if (!value) return null;
	const time = Date.parse(value);
	return Number.isFinite(time) ? time : null;
}

interface SeenRecord {
	through?: string;
	unread?: true;
}
interface SeenState {
	epoch: string | null;
	sessions: Record<string, SeenRecord>;
}

function parseSeen(value: unknown): SeenState {
	const state: SeenState = { epoch: null, sessions: {} };
	if (!isRecord(value)) return state;
	if (typeof value.epoch === "string") state.epoch = value.epoch;
	if (isRecord(value.sessions))
		for (const [ref, record] of Object.entries(value.sessions)) {
			if (!isRecord(record)) continue;
			if (record.unread === true) state.sessions[ref] = { unread: true };
			else if (typeof record.through === "string") state.sessions[ref] = { through: record.through };
		}
	return state;
}

/** Whether you have opened each session since its last turn ended: Finished
 * until seen, then Idle (spec 13.1). Every comparison is between the hub's
 * own timestamps (a row's updated_at against the updated_at stored when you
 * opened it), so the phone's clock never matters. The first load on a device
 * adopts the newest updated_at it sees as an epoch, so sessions that ended
 * before this device ever showed the Board don't all arrive as unseen. S4
 * replaces this with a marker on the hub. */
export class SeenMarkers {
	private state: SeenState;
	private revision = 0;
	private listeners = new Set<() => void>();

	constructor(
		private readonly storage: BoardStorage,
		private readonly hubId: string,
	) {
		this.state = parseSeen(readJson(storage, seenKey(hubId)));
	}

	isSeen(row: { ref: string; updated_at?: string }): boolean {
		const record = this.state.sessions[row.ref];
		if (record?.unread) return false;
		if (this.state.epoch === null) return true;
		const updated = timeOf(row.updated_at);
		if (updated === null) return true;
		const through = Math.max(
			timeOf(record?.through) ?? Number.NEGATIVE_INFINITY,
			timeOf(this.state.epoch) ?? Number.NEGATIVE_INFINITY,
		);
		return updated <= through;
	}

	adoptEpoch(rows: readonly { updated_at?: string }[]): void {
		if (this.state.epoch !== null) return;
		let newest: { value: string; time: number } | null = null;
		for (const row of rows) {
			const time = timeOf(row.updated_at);
			if (time !== null && row.updated_at && (newest === null || time > newest.time))
				newest = { value: row.updated_at, time };
		}
		if (!newest) return;
		this.state.epoch = newest.value;
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
		if (entries.length > SEEN_LIMIT) {
			// Unread marks are choices you made; past the limit, the oldest seen
			// marks go first (they matter least: the epoch covers old sessions).
			entries.sort(
				([, a], [, b]) =>
					(a.unread ? 0 : 1) - (b.unread ? 0 : 1) ||
					(timeOf(b.through) ?? 0) - (timeOf(a.through) ?? 0),
			);
			this.state.sessions = Object.fromEntries(entries.slice(0, SEEN_LIMIT));
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
		private readonly storage: BoardStorage,
		private readonly hubId: string,
	) {
		const value = readJson(storage, foldedKey(hubId));
		if (isRecord(value))
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

export function forgetBoard(storage: BoardStorage, hubId: string): void {
	for (const key of [seenKey(hubId), foldedKey(hubId)])
		try {
			storage.removeItemSync(key);
		} catch {
			// Nothing stored to forget.
		}
}
