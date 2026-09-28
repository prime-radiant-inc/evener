// The stop requests you sent a coordinator (spec 9's "Ask coordinator to stop
// it"), per hub on this device. The row says "Stop requested from the
// coordinator" while the subagent still works, then "Stopped at your request"
// once it (or something it started) stopped, so the request visibly completes
// (round 4). A request whose subagent finished on its own is forgotten.
import { isPlainObject } from "@evener/appwire-client";
import { readJson, removeKeys, writeJson } from "../deviceStorage";
import type { SyncStringStorage } from "../syncStringStorage";
import { type SubagentRow, subtreeStopped } from "./subagentModel";

const storageKey = (hubId: string) => `evener.native.subagent-stops.${hubId}`;
const LIMIT = 200;

interface StopRecord {
	coordinatorRef: string;
	requestedAt: number;
	stopped?: true;
	/** Sent as a direct stop (S6), which ends the subagent's own run and
	 * leaves its subagents running; else asked of the coordinator. */
	direct?: true;
}

export type StopRequestView = "requested" | "stopped" | null;

export class StopRequests {
	private records: Record<string, StopRecord> = {};
	private revision = 0;
	private listeners = new Set<() => void>();

	constructor(
		private readonly storage: SyncStringStorage,
		private readonly hubId: string,
	) {
		const value = readJson(storage, storageKey(hubId));
		if (isPlainObject(value))
			for (const [id, record] of Object.entries(value))
				if (isPlainObject(record) && typeof record.coordinatorRef === "string" && typeof record.requestedAt === "number")
					this.records[id] = {
						coordinatorRef: record.coordinatorRef,
						requestedAt: record.requestedAt,
						...(record.stopped === true ? { stopped: true as const } : {}),
						...(record.direct === true ? { direct: true as const } : {}),
					};
	}

	/** You asked for this subagent to stop: of its coordinator, or directly
	 * (S6). */
	request(coordinatorRef: string, row: SubagentRow, now: number, { direct = false } = {}): void {
		this.records[row.id] = { coordinatorRef, requestedAt: now, ...(direct ? { direct: true as const } : {}) };
		this.save();
	}

	/** Pending while what you asked to stop still works; stopped once it
	 * stopped after you asked. */
	view(row: SubagentRow): StopRequestView {
		const record = this.records[row.id];
		if (!record) return null;
		if (record.stopped) return "stopped";
		return stillWorking(record, row) ? "requested" : null;
	}

	/** Whether your request was a direct stop (S6) rather than a request of
	 * the coordinator. */
	direct(row: SubagentRow): boolean {
		return this.records[row.id]?.direct === true;
	}

	/** Settles requests against a fresh read of one coordinator's tree, and
	 * returns the subagents that just stopped at your request, each once, for
	 * the toast. A subagent this read doesn't hold keeps its request. */
	reconcile(coordinatorRef: string, rows: readonly SubagentRow[]): SubagentRow[] {
		const stopped: SubagentRow[] = [];
		let changed = false;
		for (const row of rows) {
			const record = this.records[row.id];
			if (!record || record.coordinatorRef !== coordinatorRef || record.stopped || stillWorking(record, row)) continue;
			if (subtreeStopped(row.delegate)) {
				record.stopped = true;
				stopped.push(row);
			} else delete this.records[row.id];
			changed = true;
		}
		if (changed) this.save();
		return stopped;
	}

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	getRevision = (): number => this.revision;

	private save(): void {
		const entries = Object.entries(this.records);
		if (entries.length > LIMIT) {
			entries.sort(([, a], [, b]) => b.requestedAt - a.requestedAt);
			this.records = Object.fromEntries(entries.slice(0, LIMIT));
		}
		writeJson(this.storage, storageKey(this.hubId), this.records);
		this.revision += 1;
		for (const listener of [...this.listeners]) listener();
	}
}

/** What a request asked to stop is still working. A coordinator's stop can
 * take the subagent's whole subtree, so it waits on any of it; a direct stop
 * ends only the subagent's own run (S6), so it waits on that alone. */
function stillWorking(record: StopRecord, row: SubagentRow): boolean {
	return record.direct ? row.state === "running" : row.active;
}

export function forgetStopRequests(storage: SyncStringStorage, hubId: string): void {
	removeKeys(storage, [storageKey(hubId)]);
}
