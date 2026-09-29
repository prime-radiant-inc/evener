import type { SyncStringStorage } from "./syncStringStorage";
import { rowTurnId, type TimelineRow } from "./timeline";

export interface ReaderAnchor {
	hubId: string;
	sessionRef: string;
	conversationInstance?: string;
	itemKey: string;
	itemPosition?: { entry: number; item: number };
	withinItemOffset: number;
	touchedAt: number;
	// The latest turn id loaded when this anchor was captured (ruling 31),
	// so opening the session can tell whether a newer reply arrived since.
	// Absent on an anchor saved before this field existed.
	turnsSeen?: string;
}

export interface ReaderMeasurement {
	key: string;
	y: number;
	height: number;
}

const key = "evener.reader-positions";
const limit = 100;
type Stored = Record<string, ReaderAnchor>;
const memory = new WeakMap<SyncStringStorage, Stored>();

function mergeStored(...maps: readonly Stored[]): Stored {
	const merged: Stored = {};
	for (const map of maps)
		for (const [entryKey, candidate] of Object.entries(map)) {
			const prior = merged[entryKey];
			if (!prior || candidate.touchedAt >= prior.touchedAt) merged[entryKey] = candidate;
		}
	return merged;
}

function position(value: unknown): value is { entry: number; item: number } {
	return (
		typeof value === "object" &&
		value !== null &&
		!Array.isArray(value) &&
		typeof (value as { entry?: unknown }).entry === "number" &&
		Number.isInteger((value as { entry: number }).entry) &&
		(value as { entry: number }).entry >= 0 &&
		typeof (value as { item?: unknown }).item === "number" &&
		Number.isInteger((value as { item: number }).item) &&
		(value as { item: number }).item >= 0
	);
}
function valid(value: unknown): value is ReaderAnchor {
	if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
	const v = value as Partial<ReaderAnchor>;
	return (
		typeof v.hubId === "string" &&
		typeof v.sessionRef === "string" &&
		typeof v.itemKey === "string" &&
		v.itemKey.length > 0 &&
		(v.conversationInstance === undefined || typeof v.conversationInstance === "string") &&
		(v.itemPosition === undefined || position(v.itemPosition)) &&
		typeof v.withinItemOffset === "number" &&
		Number.isFinite(v.withinItemOffset) &&
		v.withinItemOffset >= 0 &&
		typeof v.touchedAt === "number" &&
		Number.isFinite(v.touchedAt) &&
		(v.turnsSeen === undefined || typeof v.turnsSeen === "string")
	);
}
export function readerKey(row: TimelineRow): string {
	if (row.kind === "details" || row.kind === "time") return row.id;
	if (row.kind === "assistant" && row.callKey) return row.callKey;
	if (row.kind === "assistant" && row.roundKey) return row.roundKey;
	return row.transcriptKey ?? row.id;
}
export function readerPosition(row: TimelineRow) {
	if (row.kind === "details") return row.entries[0]?.position;
	if (row.kind === "time") return undefined;
	return row.position;
}
export function comparePosition(a?: { entry: number; item: number }, b?: { entry: number; item: number }) {
	if (!a && !b) return 0;
	if (!a) return 1;
	if (!b) return -1;
	return a.entry - b.entry || a.item - b.item;
}
// Whether the anchor was saved on this item, where the item has no row of its
// own: a step folded into a run, or a member of a clustered activity row.
function matchesAnchor(
	item: {
		id: string;
		transcriptKey?: string;
		position?: { entry: number; item: number };
	},
	anchor: ReaderAnchor,
): boolean {
	return (
		(item.transcriptKey ?? item.id) === anchor.itemKey ||
		(anchor.itemPosition !== undefined &&
			item.position !== undefined &&
			comparePosition(item.position, anchor.itemPosition) === 0)
	);
}

export function resolveReaderAnchor(anchor: ReaderAnchor, rows: readonly TimelineRow[]): number | null {
	const exact = rows.findIndex((row) => readerKey(row) === anchor.itemKey);
	if (exact >= 0) return exact;
	if (anchor.itemPosition) {
		const itemPosition = anchor.itemPosition;
		const byPosition = rows.findIndex((row) => {
			const candidate = readerPosition(row);
			return candidate !== undefined && comparePosition(candidate, itemPosition) === 0;
		});
		if (byPosition >= 0) return byPosition;
	}
	// A position saved on a step that now sits inside a folded run resolves to
	// the run (Review Focus 4).
	const inRun = rows.findIndex((row) => row.kind === "run" && row.steps.some((step) => matchesAnchor(step, anchor)));
	return inRun >= 0 ? inRun : null;
}

export type OpeningTarget = { kind: "live" } | { kind: "anchor" } | { kind: "row"; index: number };

/** Where a session opens (spec 7.3, ruling 31). While a question or approval
 * waits: the live end, where the dock is. After a reply that finished since
 * you last reached the end: the start of that reply, past your own message,
 * its time marker and any attachments row (a step's images ride inside its
 * run, and any other attachments row follows the row that produced it).
 * Otherwise where you left off, and failing that the live end. Turn ids,
 * never clocks, decide what is newer. */
export function openingTarget(
	anchor: ReaderAnchor | null,
	rows: readonly TimelineRow[],
	turnIds: readonly string[],
	askPending: boolean,
): OpeningTarget {
	if (askPending) return { kind: "live" };
	if (anchor?.turnsSeen) {
		const seen = turnIds.indexOf(anchor.turnsSeen);
		const newer = seen === -1 ? undefined : turnIds[seen + 1];
		if (newer !== undefined) {
			const index = rows.findIndex(
				(row) => rowTurnId(row) === newer && row.kind !== "user" && row.kind !== "time" && row.kind !== "attachments",
			);
			if (index !== -1) return { kind: "row", index };
		}
	}
	return anchor ? { kind: "anchor" } : { kind: "live" };
}
export function isReaderAnchorLoaded(
	anchor: ReaderAnchor,
	items: readonly import("./projectedRows").MobileTimelineItem[],
): boolean {
	for (const item of items) {
		if (resolveReaderAnchor(anchor, [item]) !== null) return true;
		if (item.kind === "notice" && anchor.itemKey === `details:${item.id}`) return true;
		if (item.kind === "activity" && item.members?.some((member) => matchesAnchor(member, anchor))) return true;
	}
	return false;
}

/** An anchor at the top of `row`: where a session that opens there (spec
 * 7.3) will restore, before the row has been laid out. */
export function readerAnchorAt(
	hubId: string,
	sessionRef: string,
	row: TimelineRow,
	withinItemOffset: number,
	touchedAt: number,
	conversationInstance?: string,
	turnsSeen?: string,
): ReaderAnchor {
	const itemPosition = readerPosition(row);
	return {
		hubId,
		sessionRef,
		...(conversationInstance ? { conversationInstance } : {}),
		itemKey: readerKey(row),
		...(itemPosition ? { itemPosition } : {}),
		withinItemOffset,
		touchedAt,
		...(turnsSeen ? { turnsSeen } : {}),
	};
}

/** The row a scroll to `y` anchors on: the first one reaching past `y`. A
 * time marker is passed over, since loading an older page can remove it. */
export function readerAnchorRow(
	rows: readonly TimelineRow[],
	measurements: ReadonlyMap<string, ReaderMeasurement>,
	y: number,
): TimelineRow | undefined {
	return rows.find((row) => {
		if (row.kind === "time") return false;
		const measurement = measurements.get(readerKey(row));
		return measurement && measurement.y + measurement.height > y;
	});
}

export function captureReaderAnchor(
	hubId: string,
	sessionRef: string,
	row: TimelineRow,
	contentOffset: number,
	measurements: readonly ReaderMeasurement[],
	touchedAt: number,
	conversationInstance?: string,
	turnsSeen?: string,
): ReaderAnchor | null {
	const measurement = measurements.find((candidate) => candidate.key === readerKey(row));
	if (!measurement) return null;
	return readerAnchorAt(
		hubId,
		sessionRef,
		row,
		Math.min(measurement.height, Math.max(0, contentOffset - measurement.y)),
		touchedAt,
		conversationInstance,
		turnsSeen,
	);
}
export type ReaderRestoreCommand =
	| { kind: "exact"; index: number; viewOffset: number }
	| { kind: "approximate"; offset: number };

export class ReaderRestoreAttempts {
	private approximateOffset: number | null = null;
	private approximateMeasurementProgress: number | null = null;
	private highWaterMeasurementProgress = -1;
	private failures = 0;

	reset() {
		this.approximateOffset = null;
		this.approximateMeasurementProgress = null;
		this.highWaterMeasurementProgress = -1;
		this.failures = 0;
	}
	begin(command: ReaderRestoreCommand, measurementProgress = 0): boolean {
		if (command.kind === "exact") {
			// A measured row ends the search; reflow may require a fresh search later.
			this.reset();
			return true;
		}
		if (this.approximateOffset === command.offset && (this.approximateMeasurementProgress ?? -1) >= measurementProgress)
			return false;
		if (measurementProgress > this.highWaterMeasurementProgress) {
			this.highWaterMeasurementProgress = measurementProgress;
			this.failures = 0;
		}
		this.approximateOffset = command.offset;
		this.approximateMeasurementProgress = measurementProgress;
		return true;
	}
	retryUnmeasured(measurementProgress = -1): boolean {
		if (measurementProgress > this.highWaterMeasurementProgress) {
			this.highWaterMeasurementProgress = measurementProgress;
			this.failures = 0;
		}
		if (this.failures >= 3) return false;
		this.failures += 1;
		this.approximateOffset = null;
		this.approximateMeasurementProgress = null;
		return true;
	}
}

export function furthestMeasuredRowBeforeTarget(
	rows: readonly TimelineRow[],
	targetIndex: number,
	measurements: readonly ReaderMeasurement[],
) {
	const measured = new Set(measurements.map((measurement) => measurement.key));
	let furthest = -1;
	for (let index = 0; index < targetIndex; index += 1) if (measured.has(readerKey(rows[index]))) furthest = index;
	return furthest;
}

/** The exact restore last applied for the reading anchor: its row and that
 * row's height then, where the list went, and whether a list not yet long
 * enough cut it short of its target. */
export interface AppliedRestore {
	key: string;
	height: number;
	offset: number;
	clamped: boolean;
}

/** Whether an exact restore of the anchor's row to `offset` is due. Once per
 * anchor: after one lands, the row's measured y moving (a virtualized list
 * re-estimating rows it unmounted, which older pages provoke) never moves the
 * list again, since chasing it made the list ping-pong; the list's own
 * maintainVisibleContentPosition keeps the first visible row, the anchor, in
 * place instead. It is due again when the anchor's own row reflows (a
 * text-size change), or when a restore cut short can now go further. */
export function exactRestoreDue(
	applied: AppliedRestore | null,
	anchorRow: { key: string; height: number },
	offset: number,
): boolean {
	if (!applied || applied.key !== anchorRow.key) return true;
	if (applied.height !== anchorRow.height) return true;
	return applied.clamped && offset !== applied.offset;
}
// A virtualized list may not yet extend far enough to reach the saved position.
export function reachableReaderOffset(desiredOffset: number, contentHeight: number, viewportHeight: number): number {
	return Math.min(Math.max(0, desiredOffset), Math.max(0, contentHeight - viewportHeight));
}
export function restoreReaderCommand(
	anchor: ReaderAnchor,
	rows: readonly TimelineRow[],
	measurements: readonly ReaderMeasurement[],
	averageItemHeight: number,
	allowRestore = true,
): ReaderRestoreCommand | null {
	if (!allowRestore) return null;
	const index = resolveReaderAnchor(anchor, rows);
	if (index === null) return null;
	const currentKey = readerKey(rows[index]);
	const measurement = measurements.find((candidate) => candidate.key === currentKey);
	if (measurement)
		return {
			kind: "exact",
			index,
			viewOffset: -Math.min(anchor.withinItemOffset, measurement.height),
		};
	return {
		kind: "approximate",
		offset: Math.max(0, index * Math.max(1, averageItemHeight) + anchor.withinItemOffset),
	};
}
export function storageKey(hubId: string, sessionRef: string) {
	return `${hubId}\u0000${sessionRef}`;
}
export class ReaderPositionRepository {
	constructor(private readonly storage: SyncStringStorage) {}
	read(hubId: string, sessionRef: string): ReaderAnchor | null {
		let raw: string | null;
		try {
			raw = this.storage.getItemSync(key);
		} catch {
			const cached = memory.get(this.storage)?.[storageKey(hubId, sessionRef)];
			return cached ?? null;
		}
		if (!raw) return memory.get(this.storage)?.[storageKey(hubId, sessionRef)] ?? null;
		let value: unknown;
		try {
			value = JSON.parse(raw);
		} catch {
			return null;
		}
		if (typeof value !== "object" || value === null || Array.isArray(value)) return null;
		const disk = Object.fromEntries(Object.entries(value).filter(([, candidate]) => valid(candidate))) as Stored;
		const stored = mergeStored(disk, memory.get(this.storage) ?? {});
		memory.set(this.storage, stored);
		const candidate = stored[storageKey(hubId, sessionRef)];
		return valid(candidate) && candidate.hubId === hubId && candidate.sessionRef === sessionRef ? candidate : null;
	}
	save(anchor: ReaderAnchor | null) {
		if (!anchor) return;
		let raw: string | null;
		try {
			raw = this.storage.getItemSync(key);
		} catch {
			const stored = memory.get(this.storage) ?? {};
			stored[storageKey(anchor.hubId, anchor.sessionRef)] = anchor;
			memory.set(this.storage, stored);
			return;
		}
		let disk: Stored = {};
		if (raw) {
			try {
				const value: unknown = JSON.parse(raw);
				if (typeof value === "object" && value !== null && !Array.isArray(value))
					disk = Object.fromEntries(Object.entries(value).filter(([, candidate]) => valid(candidate)));
			} catch {
				disk = {};
			}
		}
		const stored = mergeStored(disk, memory.get(this.storage) ?? {});
		stored[storageKey(anchor.hubId, anchor.sessionRef)] = anchor;
		const entries = Object.entries(stored)
			.sort(([, a], [, b]) => b.touchedAt - a.touchedAt)
			.slice(0, limit);
		memory.set(this.storage, Object.fromEntries(entries));
		try {
			this.storage.setItemSync(key, JSON.stringify(Object.fromEntries(entries)));
		} catch {
			// Keep the caller's in-memory anchor when persistence is unavailable.
		}
	}
	removeHub(hubId: string) {
		const raw = this.storage.getItemSync(key);
		const cached = memory.get(this.storage) ?? {};
		const remainingCached = Object.fromEntries(
			Object.entries(cached).filter(([entryKey]) => !entryKey.startsWith(`${hubId}\u0000`)),
		);
		const disk: Stored = {};
		if (raw) {
			try {
				const value: unknown = JSON.parse(raw);
				if (typeof value === "object" && value !== null && !Array.isArray(value))
					for (const [entryKey, candidate] of Object.entries(value)) if (valid(candidate)) disk[entryKey] = candidate;
			} catch {
				// Invalid persisted data is discarded as it is during save.
			}
		}
		const remaining = Object.fromEntries(
			Object.entries(disk).filter(([entryKey]) => !entryKey.startsWith(`${hubId}\u0000`)),
		);
		if (Object.keys(remaining).length === Object.keys(disk).length) {
			memory.set(this.storage, remainingCached);
			return;
		}
		if (Object.keys(remaining).length > 0) this.storage.setItemSync(key, JSON.stringify(remaining));
		else this.storage.removeItemSync(key);
		memory.set(this.storage, remainingCached);
	}
}
