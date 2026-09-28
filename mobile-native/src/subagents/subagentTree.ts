// One coordinator's subagent tree, shared by the Subagents list and the
// subagent screens above it so moving between them never starts from nothing.
// It reads evener/jobs/list through the shared ActivityList and follows every
// page on its own, so an ordinary tree loads whole and its counts are exact
// (spec 9; S3's fallback, ruling 2).
//
// It drives its own reloads instead of calling ActivityList.start(). A page
// requested from a listener while a load is finishing is queued behind a loop
// that has already ended (activityList.ts load: the final publish runs while
// inFlight is still set), so each reload here awaits the root, then each page
// in turn, and notifications during a reload fold into one more.
import { ActivityList, type ActivityTree } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";

export interface SubagentTreeSnapshot {
	tree: ActivityTree | null;
	/** No tree yet, and one is being read. */
	loading: boolean;
	/** No tree yet, and the last read failed; the next focus or reconnect reads again. */
	failed: boolean;
	/** The coordinator's session can't list its activity at all. */
	unsupported: boolean;
	/** The coordinator's session can't be found. */
	ended: boolean;
	/** Settled, and part of the tree couldn't be listed (ruling 2). */
	partial: boolean;
	/** Whose subagents couldn't be listed, by title. */
	missing: string[];
	/** The coordinator's model, for the last line's "only when it differs". */
	coordinatorModel: string | null;
}

// The notifications ActivityList.start() refreshes on (activityList.ts).
const TREE_NOTIFICATIONS = new Set([
	"evener/jobs/treeUpdated",
	"evener/job/started",
	"evener/job/finished",
	"evener/delegate/updated",
	"evener/thread/resync",
]);

export class SubagentTree {
	private client: ConversationClientLike | null = null;
	private list: ActivityList | null = null;
	private detachList: (() => void) | null = null;
	private tree: ActivityTree | null = null;
	private coordinatorModel: string | null = null;
	private settledMissing: string[] = [];
	/** Bumped with each new client, so an old client's reload stops where it stands. */
	private generation = 0;
	private reloading: Promise<void> | null = null;
	private again = false;
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

	/** Binds to the hub's current client, or null while disconnected. The last
	 * tree stays on screen until the new client's read lands. Resolves when
	 * that first read settles.
	 *
	 * The new list starts empty rather than from the last tree: ActivityList
	 * refuses a root older than the tree it holds, and a restarted daemon can
	 * count revisions from lower down, which would pin the screen to the tree
	 * from before the restart. The screen keeps showing `this.tree` until the
	 * new list has its own. */
	setClient(client: ConversationClientLike | null): Promise<void> {
		if (client === this.client) return this.reloading ?? Promise.resolve();
		// A read cut off mid-way leaves the pages it hadn't reached unlisted:
		// say so until a new read settles.
		// A read that hadn't reached its first page knows nothing new, so what
		// the last settled read said stands.
		const unread = this.list?.branches().map((branch) => branch.label) ?? [];
		if (this.reloading && unread.length > 0) this.settledMissing = unread;
		this.detachList?.();
		this.detachList = null;
		this.list = null;
		this.client = client;
		// The old client's reload is abandoned where it stands: it may never
		// answer, and the new client reads on its own.
		this.generation += 1;
		this.reloading = null;
		let read: Promise<void> = Promise.resolve();
		if (client) {
			const list = new ActivityList(client, this.ref, this.threadId);
			const stopState = list.subscribe(() => {
				const tree = list.getSnapshot().tree;
				if (tree) this.tree = tree;
				this.publish();
			});
			const stopNotifications = client.onNotification((notification) => {
				if (!TREE_NOTIFICATIONS.has(notification.method)) return;
				const params = notification.params as { ref?: string; threadId?: string };
				if (params.ref === this.ref && params.threadId === this.threadId) void this.reload();
			});
			this.list = list;
			this.detachList = () => {
				stopState();
				stopNotifications();
				list.dispose();
			};
			read = this.reload();
		}
		this.publish();
		return read;
	}

	/** Makes this connection follow the coordinator, so its tree notifications
	 * arrive (ruling 9), learns its model, and reads the tree again. The
	 * Subagents list calls this whenever it comes into focus. */
	async follow(): Promise<void> {
		const client = this.client;
		if (!client) return;
		try {
			const response = await client.request("thread/read", {
				ref: this.ref,
				includeTurns: false,
				subscribe: true,
				replaceSubscription: true,
			});
			if (client === this.client) this.coordinatorModel = response.thread.modelProvider || null;
		} catch {
			// The tree read below still runs; the next focus follows again.
		}
		await this.reload();
	}

	/** Reads the root, then every page it names, each at most once per reload. */
	reload(): Promise<void> {
		if (this.reloading) {
			this.again = true;
			return this.reloading;
		}
		const generation = this.generation;
		const current = () => generation === this.generation;
		const run: Promise<void> = (async () => {
			do {
				this.again = false;
				const list = this.list;
				if (!list) return;
				const tried = new Set<string>();
				await list.refresh();
				for (;;) {
					if (!current() || this.list !== list || this.again) break;
					const next = list
						.branches()
						.find((branch) => branch.continuation !== undefined && !tried.has(branch.continuation));
					if (next?.continuation === undefined) break;
					tried.add(next.continuation);
					await list.loadMore(next.id, next.continuation);
				}
			} while (current() && this.again && this.list !== null);
		})().finally(() => {
			if (this.reloading === run) this.reloading = null;
			this.publish();
		});
		this.reloading = run;
		this.publish();
		return run;
	}

	private build(): SubagentTreeSnapshot {
		const list = this.list;
		const state = list?.getSnapshot();
		const settled = this.reloading === null && state?.loading !== true;
		// What the last settled read couldn't list stays said until a read
		// settles again, through a reconnect too; while pages are still
		// arriving, the count isn't whole yet either.
		const pending = list ? list.branches().map((branch) => branch.label) : [];
		if (list && settled) this.settledMissing = pending;
		const missing = this.settledMissing;
		return {
			tree: this.tree,
			loading: this.tree === null && this.client !== null && !settled,
			failed: this.tree === null && settled && !!state?.error,
			unsupported: state?.unsupported ?? false,
			ended: state?.ended ?? false,
			partial: this.tree !== null && (missing.length > 0 || (!settled && pending.length > 0)),
			missing,
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
	const key = JSON.stringify([hubId, ref, threadId]);
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
export function holdSubagentTree(tree: SubagentTree): () => void {
	const entry = [...trees.values()].find((candidate) => candidate.tree === tree);
	if (!entry) return () => {};
	entry.holders += 1;
	let released = false;
	return () => {
		if (released) return;
		released = true;
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
