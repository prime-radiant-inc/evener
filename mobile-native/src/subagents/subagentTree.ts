// A retained native binding for one coordinator. The shared owner controls
// network reads, retries and membership; this binding projects loaded evidence.
import {
	type ActivityTree,
	type SessionActivitySummary,
	type SessionActivityPresentation,
	type SessionActivitySnapshot,
	SessionActivityStore,
	projectSessionActivity,
	acquireThreadSubscription,
	type ThreadSubscriptionLease,
} from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";

export interface SubagentTreeSnapshot {
	tree: ActivityTree | null;
	summary: SessionActivitySummary | null;
	pages: Pick<SessionActivitySnapshot, "delegates" | "jobs"> | null;
	loading: boolean;
	failed: boolean;
	unsupported: boolean;
	ended: boolean;
	partial: boolean;
	missing: string[];
	hasMore: boolean;
	coordinatorModel: string | null;
}

export class SubagentTree {
	private client: ConversationClientLike | null = null;
	private store: SessionActivityStore | null = null;
	private detachStore: (() => void) | null = null;
	private followLease: ThreadSubscriptionLease | null = null;
	private presentation: SessionActivityPresentation | null = null;
	private retained: SessionActivitySnapshot | null = null;
	private coordinatorModel: string | null = null;
	private modelGeneration = 0;
	private observedSessionID: string | null = null;
	private activityObservers = 0;
	private stopActivity: (() => void)[] = [];
	private snapshot: SubagentTreeSnapshot;
	private listeners = new Set<() => void>();

	constructor(
		readonly ref: string,
		readonly threadId: string,
	) {
		this.snapshot = this.build();
	}
	getSnapshot = (): SubagentTreeSnapshot => this.snapshot;
	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};
	setClient(client: ConversationClientLike | null): Promise<void> {
		if (client === this.client) return Promise.resolve();
		this.modelGeneration += 1;
		this.detachStore?.();
		this.detachStore = null;
		this.stopActivity.forEach((stop) => stop());
		this.stopActivity = [];
		this.retained = this.store?.getSnapshot() ?? this.retained;
		this.store?.dispose();
		this.store = null;
		this.followLease?.release();
		this.followLease = null;
		this.client = client;
		if (!client) {
			this.publish();
			return Promise.resolve();
		}
		const store = new SessionActivityStore(client, this.ref, {
			scope: "subtree",
			retained: this.retained ?? undefined,
		});
		this.store = store;
		this.retained = null;
		this.detachStore = store.subscribe(() => {
			const state = store.getSnapshot();
			const next = projectSessionActivity(state);
			if (next.context) {
				if (this.observedSessionID && next.context.sessionId !== this.observedSessionID) {
					this.presentation = null;
					this.coordinatorModel = null;
					this.modelGeneration += 1;
				}
				this.observedSessionID = next.context.sessionId;
			}
			// Context can land before collections. Keep the retained rows until the
			// replacement supplies membership evidence, including a proven empty set.
			if (next.tree && (state.delegates.rows.length > 0 || state.jobs.rows.length > 0 || next.complete))
				this.presentation = next;
			this.publish();
		});
		store.start();
		this.observeCollections();
		const read = store.refresh();
		this.publish();
		return read;
	}
	observeActivity(): () => void {
		this.activityObservers += 1;
		if (this.activityObservers === 1) this.observeCollections();
		let released = false;
		return () => {
			if (released) return;
			released = true;
			this.activityObservers -= 1;
			if (this.activityObservers === 0) {
				this.stopActivity.forEach((stop) => stop());
				this.stopActivity = [];
			}
		};
	}
	private observeCollections(): void {
		if (this.store && this.activityObservers > 0 && this.stopActivity.length === 0)
			this.stopActivity = [this.store.observe("delegates"), this.store.observe("jobs")];
	}
	async follow(): Promise<void> {
		const client = this.client;
		const modelGeneration = this.modelGeneration;
		if (!client) return;
		this.followLease ??= acquireThreadSubscription(client, this.ref);
		try {
			const response = await this.followLease.read({ includeTurns: false });
			if (
				client === this.client &&
				modelGeneration === this.modelGeneration &&
				(!this.observedSessionID || response.thread.id === this.observedSessionID)
			) {
				this.coordinatorModel = response.thread.modelProvider || null;
				this.publish();
			}
		} catch {
			/* The shared activity owner retains and recovers its own reads. */
		}
	}
	reload(): Promise<void> {
		return this.store?.refresh() ?? Promise.resolve();
	}
	loadMore(resource?: "delegates" | "jobs"): Promise<void> {
		const store = this.store;
		return store
			? Promise.all(
					(resource ? [resource] : (["delegates", "jobs"] as const)).map((name) => store.loadMore(name)),
				).then(() => {})
			: Promise.resolve();
	}
	private build(): SubagentTreeSnapshot {
		const state = this.store?.getSnapshot();
		const current = state ? projectSessionActivity(state) : this.presentation;
		const tree = this.presentation?.tree ?? null;
		const errors = state ? [state.summaryState, state.delegates, state.jobs] : [];
		const missing = [...new Set((current ?? this.presentation)?.issues.map((issue) => issue.ref) ?? [])];
		return {
			tree,
			summary: state?.summary ?? this.presentation?.summary ?? null,
			pages: state ? { delegates: state.delegates, jobs: state.jobs } : null,
			loading: tree === null && this.client !== null && errors.some((read) => read.loading || read.pending),
			failed: tree === null && errors.some((read) => read.error !== null && !read.permanent),
			unsupported: errors.some((read) => read.permanent),
			ended: false,
			partial:
				tree !== null && (current?.complete !== true || errors.some((read) => read.unavailable || read.error !== null)),
			missing,
			hasMore: !!state && (state.delegates.hasMore || state.jobs.hasMore),
			coordinatorModel: this.coordinatorModel,
		};
	}
	private publish(): void {
		this.snapshot = this.build();
		for (const listener of [...this.listeners]) listener();
	}
}

// The most recent coordinators keep their last tree after every screen lets
// go, so coming back shows it at once and reads in place.
const RETAINED = 8;
interface HeldTree {
	hubId: string;
	tree: SubagentTree;
	holders: number;
}
const trees = new Map<string, HeldTree>();

export function subagentTree(hubId: string, ref: string, threadId: string): SubagentTree {
	const key = JSON.stringify([hubId, ref]);
	let entry = trees.get(key);
	if (entry) trees.delete(key);
	else entry = { hubId, tree: new SubagentTree(ref, threadId), holders: 0 };
	trees.set(key, entry);
	for (const [other, candidate] of trees) {
		if (trees.size <= RETAINED) break;
		if (candidate.holders === 0 && candidate !== entry) trees.delete(other);
	}
	return entry.tree;
}

/** Holds a tree for a mounted screen. Once no screen holds it, it stops
 * reading and keeps its tree. */
export function holdSubagentTree(
	tree: SubagentTree,
	{ collections = true }: { collections?: boolean } = {},
): () => void {
	const entry = [...trees.values()].find((candidate) => candidate.tree === tree);
	if (!entry) return () => {};
	entry.holders += 1;
	const releaseActivity = collections ? tree.observeActivity() : () => {};
	let released = false;
	return () => {
		if (released) return;
		released = true;
		releaseActivity();
		entry.holders -= 1;
		if (entry.holders === 0) void entry.tree.setClient(null);
	};
}

export function forgetSubagentTrees(hubId: string): void {
	for (const [key, entry] of trees)
		if (entry.hubId === hubId) {
			void entry.tree.setClient(null);
			trees.delete(key);
		}
}
