// Stop request display evidence is scoped to each delegate's authoritative
// child ref and raw ID. A request remains visible until its work settles;
// it never determines lifecycle state or the coordinator mutation target.
import { activityNodeID, isPlainObject } from "@evener/appwire-client";
import { readJson, removeKeys, writeJson } from "../deviceStorage";
import type { SyncStringStorage } from "../syncStringStorage";
import { type SubagentRow, subtreeStops } from "./subagentModel";

const storageKey = (hubId: string) => `evener.native.subagent-stops.${hubId}`;
const LIMIT = 200;

interface StopRecord {
	delegateId: string;
	childRef: string;
	coordinatorRef: string;
	requestedAt: number;
	stopped?: true;
	/** Sent as a direct stop (S6), which ends the subagent's own run and
	 * leaves its subagents running; else asked of the coordinator. */
	direct?: true;
	/** Stops already in its subtree when you asked, which weren't yours. */
	stopsBefore?: number;
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
				if (
					isPlainObject(record) &&
					typeof record.delegateId === "string" &&
					typeof record.childRef === "string" &&
					id === activityNodeID({ kind: "delegate", delegateId: record.delegateId, childRef: record.childRef }) &&
					typeof record.coordinatorRef === "string" &&
					typeof record.requestedAt === "number"
				)
					this.records[id] = {
						delegateId: record.delegateId,
						childRef: record.childRef,
						coordinatorRef: record.coordinatorRef,
						requestedAt: record.requestedAt,
						...(record.stopped === true ? { stopped: true as const } : {}),
						...(record.direct === true ? { direct: true as const } : {}),
						...(typeof record.stopsBefore === "number" ? { stopsBefore: record.stopsBefore } : {}),
					};
	}

	/** You asked for this subagent to stop: of its coordinator, or directly
	 * (S6). */
	request(coordinatorRef: string, row: SubagentRow, now: number, { direct = false } = {}): void {
		const stopsBefore = subtreeStops(row.delegate);
		this.records[activityNodeID({ kind: "delegate", delegate: row.delegate })] = {
			coordinatorRef,
			requestedAt: now,
			delegateId: row.id,
			childRef: row.delegate.childRef,
			...(direct ? { direct: true as const } : {}),
			...(stopsBefore > 0 ? { stopsBefore } : {}),
		};
		this.save();
	}

	/** Pending while what you asked to stop still works; stopped once it
	 * stopped after you asked. */
	view(row: SubagentRow): StopRequestView {
		const record = this.records[activityNodeID({ kind: "delegate", delegate: row.delegate })];
		if (!record) return null;
		if (record.stopped) return "stopped";
		return stillWorking(record, row) ? "requested" : null;
	}

	/** Whether your request was a direct stop (S6) rather than a request of
	 * the coordinator. */
	direct(row: SubagentRow): boolean {
		return this.records[activityNodeID({ kind: "delegate", delegate: row.delegate })]?.direct === true;
	}

	/** Settles requests against a fresh read of one coordinator's tree, and
	 * returns the subagents that just stopped at your request, each once, for
	 * the toast. A subagent this read doesn't hold keeps its request. */
	reconcile(coordinatorRef: string, rows: readonly SubagentRow[]): SubagentRow[] {
		const stopped: SubagentRow[] = [];
		let changed = false;
		for (const row of rows) {
			const key = activityNodeID({ kind: "delegate", delegate: row.delegate });
			const record = this.records[key];
			if (!record || record.coordinatorRef !== coordinatorRef || record.stopped || stillWorking(record, row)) continue;
			// A direct stop ended the subagent's own run, so only its own
			// outcome says it stopped; a request of the coordinator may have
			// stopped anything under it, but not what had stopped before.
			if (record.direct ? row.stopped : subtreeStops(row.delegate) > (record.stopsBefore ?? 0)) {
				record.stopped = true;
				stopped.push(row);
			} else delete this.records[key];
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
