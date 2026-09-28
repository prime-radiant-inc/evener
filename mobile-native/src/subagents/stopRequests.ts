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
					};
	}

	/** You sent the coordinator a stop request for this subagent. */
	request(coordinatorRef: string, row: SubagentRow, now: number): void {
		this.records[row.id] = { coordinatorRef, requestedAt: now };
		this.save();
	}

	/** Pending while the subagent still works; stopped once it stopped after you asked. */
	view(row: SubagentRow): StopRequestView {
		const record = this.records[row.id];
		if (!record) return null;
		if (record.stopped) return "stopped";
		return row.active ? "requested" : null;
	}

	/** Settles requests against a fresh read of one coordinator's tree, and
	 * returns the subagents that just stopped at your request, each once, for
	 * the toast. A subagent this read doesn't hold keeps its request. */
	reconcile(coordinatorRef: string, rows: readonly SubagentRow[]): SubagentRow[] {
		const stopped: SubagentRow[] = [];
		let changed = false;
		for (const row of rows) {
			const record = this.records[row.id];
			if (!record || record.coordinatorRef !== coordinatorRef || record.stopped || row.active) continue;
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

export function forgetStopRequests(storage: SyncStringStorage, hubId: string): void {
	removeKeys(storage, [storageKey(hubId)]);
}
