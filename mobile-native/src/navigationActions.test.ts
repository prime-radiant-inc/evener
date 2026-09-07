import { describe, expect, it } from "vitest";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import type {
	NavigationActionCheckpoint,
	NavigationActionStorage,
	NavigationOperation,
} from "./navigationActionRepository";
import { NavigationActions } from "./navigationActions";

it("does not call an empty-journal reconciliation complete after another model starts a write", async () => {
	const journal = journalFixture();
	let release!: () => void;
	const actions = new NavigationActions(
		{} as ConversationClientLike,
		async () => {},
		() => true,
		async () => {
			await new Promise<void>((resolve) => {
				release = resolve;
			});
		},
		journal,
	);
	const read = actions.reconcile();
	const other = journal.begin({
		kind: "unpin",
		params: { sessionRef: "local:new" },
	});
	release();
	await read;
	expect(journal.load()).toBe(other);
	expect(actions.getSnapshot().uncertain).toBe(true);
});
it.each(["acknowledge", "finish"] as const)(
	"keeps recovery when %s fails after acknowledgement",
	async (method) => {
		const journal = journalFixture();
		const original = journal[method];
		journal[method] = () => {
			throw Error("disk unavailable");
		};
		let requests = 0;
		const client = {
			request: async () => {
				requests++;
				return { ok: true, navigation: { generation_id: "g", targets: [] } };
			},
		} as unknown as ConversationClientLike;
		const actions = new NavigationActions(
			client,
			async () => {},
			() => true,
			async () => {},
			journal,
		);
		await actions.unpin({ sessionRef: "local:s" });
		expect(journal.load()).not.toBeNull();
		expect(actions.getSnapshot()).toMatchObject({
			uncertain: true,
			storageUnavailable: true,
		});
		await actions.unpin({ sessionRef: "local:s" });
		expect(requests).toBe(1);
		Object.assign(journal, { [method]: original });
		await actions.reconcile();
		expect(journal.load()).toBeNull();
		expect(requests).toBe(1);
	},
);

function journalFixture() {
	let checkpoint: NavigationActionCheckpoint | null = null;
	let nextId = 0;
	const storage: NavigationActionStorage = {
		load: () => checkpoint,
		begin(operation: NavigationOperation) {
			if (checkpoint) throw new Error("unresolved");
			checkpoint = { id: String(++nextId), operation, receipt: null };
			return checkpoint;
		},
		acknowledge(value, receipt) {
			if (checkpoint !== value) throw new Error("replaced");
			checkpoint = { ...value, receipt };
			return checkpoint;
		},
		finish(value) {
			if (checkpoint !== value) return false;
			checkpoint = null;
			return true;
		},
	};
	return storage;
}

describe("durable navigation actions", () => {
	const receipt = { generation_id: "g", targets: [] };
	it("checkpoints the target before dispatch and restores it without replay after disposal", async () => {
		const journal = journalFixture();
		let complete!: (value: unknown) => void;
		let requests = 0;
		const client = {
			request: () => {
				requests++;
				expect(journal.load()?.operation).toEqual({
					kind: "assignPin",
					params: { sessionRef: "local:s", sectionName: "Focus" },
				});
				return new Promise((resolve) => {
					complete = resolve;
				});
			},
		} as unknown as ConversationClientLike;
		const first = new NavigationActions(
			client,
			async () => {},
			() => true,
			async () => {},
			journal,
		);
		const pending = first.assignPin({
			sessionRef: "local:s",
			sectionName: "Focus",
		});
		first.dispose();
		let readTarget: unknown;
		const next = new NavigationActions(
			client,
			async () => {},
			() => true,
			async (checkpoint) => {
				readTarget = checkpoint?.operation;
			},
			journal,
		);
		expect(next.getSnapshot()).toMatchObject({
			pending: false,
			uncertain: true,
		});
		await next.unpin({ sessionRef: "local:s" });
		expect(requests).toBe(1);
		complete({ ok: true, navigation: receipt });
		await pending;
		expect(journal.load()).not.toBeNull();
		await next.reconcile();
		expect(readTarget).toEqual({
			kind: "assignPin",
			params: { sessionRef: "local:s", sectionName: "Focus" },
		});
		expect(journal.load()).toBeNull();
		expect(next.getSnapshot()).toMatchObject({
			pending: false,
			uncertain: false,
		});
		expect(requests).toBe(1);
	});
	it("persists an acknowledged receipt until target readback succeeds", async () => {
		const journal = journalFixture();
		const client = {
			request: async () => ({ ok: true, navigation: receipt }),
		} as unknown as ConversationClientLike;
		const first = new NavigationActions(
			client,
			async () => {
				throw Error("offline");
			},
			() => true,
			async () => {},
			journal,
		);
		await first.renamePinSection({ sectionId: "focus", name: "Work" });
		expect(journal.load()?.receipt).toEqual(receipt);
		first.dispose();
		let readCheckpoint: unknown;
		const next = new NavigationActions(
			client,
			async () => {},
			() => true,
			async (checkpoint) => {
				readCheckpoint = checkpoint;
				throw Error("read failed");
			},
			journal,
		);
		await next.reconcile();
		expect(readCheckpoint).toMatchObject({
			receipt,
			operation: {
				kind: "renamePinSection",
				params: { sectionId: "focus", name: "Work" },
			},
		});
		expect(next.getSnapshot().uncertain).toBe(true);
		expect(journal.load()).not.toBeNull();
	});
	it("does not dispatch when local recovery cannot be saved", async () => {
		const journal = journalFixture();
		journal.begin = () => {
			throw Error("disk full /private/secret");
		};
		let requests = 0;
		const client = {
			request: async () => {
				requests++;
			},
		} as unknown as ConversationClientLike;
		const actions = new NavigationActions(
			client,
			async () => {},
			() => true,
			async () => {},
			journal,
		);
		await actions.unpin({ sessionRef: "local:s" });
		expect(requests).toBe(0);
		expect(actions.getSnapshot()).toMatchObject({
			pending: false,
			storageUnavailable: true,
		});
		expect(actions.getSnapshot().error).not.toContain("/private/secret");
	});
	it("blocks writes on corrupt storage and recovers only after a successful explicit reread", async () => {
		const journal = journalFixture();
		const originalLoad = journal.load;
		journal.load = () => {
			throw Error("corrupt");
		};
		let requests = 0;
		const client = {
			request: async () => {
				requests++;
				return { ok: true, navigation: receipt };
			},
		} as unknown as ConversationClientLike;
		const actions = new NavigationActions(
			client,
			async () => {},
			() => true,
			async () => {},
			journal,
		);
		await actions.unpin({ sessionRef: "local:s" });
		expect(actions.getSnapshot().storageUnavailable).toBe(true);
		expect(requests).toBe(0);
		journal.load = originalLoad;
		await actions.reconcile();
		expect(actions.getSnapshot()).toMatchObject({
			storageUnavailable: false,
			uncertain: false,
		});
		await actions.unpin({ sessionRef: "local:s" });
		expect(requests).toBe(1);
		expect(journal.load()).toBeNull();
	});
	it("does not clear another model's pending target after a delayed reconciliation", async () => {
		const journal = journalFixture();
		const old = journal.begin({
			kind: "unpin",
			params: { sessionRef: "local:old" },
		});
		let release!: () => void;
		const client = {} as ConversationClientLike;
		const actions = new NavigationActions(
			client,
			async () => {},
			() => true,
			async () => {
				await new Promise<void>((resolve) => {
					release = resolve;
				});
			},
			journal,
		);
		const read = actions.reconcile();
		journal.finish(old);
		const newer = journal.begin({
			kind: "unpin",
			params: { sessionRef: "local:new" },
		});
		release();
		await read;
		expect(journal.load()).toBe(newer);
		expect(actions.getSnapshot().uncertain).toBe(true);
	});
	it("checks the shared hub journal before dispatch from an older screen model", async () => {
		const journal = journalFixture();
		let requests = 0;
		const client = {
			request: async () => {
				requests++;
				return { ok: true, navigation: receipt };
			},
		} as unknown as ConversationClientLike;
		const actions = new NavigationActions(
			client,
			async () => {},
			() => true,
			async () => {},
			journal,
		);
		const newer = journal.begin({
			kind: "unpin",
			params: { sessionRef: "local:new" },
		});
		await actions.favorite("project", true);
		expect(requests).toBe(0);
		expect(actions.getSnapshot()).toMatchObject({
			uncertain: true,
			recovery: newer,
		});
	});
});

describe("navigation organization actions", () => {
	it("sends canonical project identity once and awaits projection before success", async () => {
		const sent: unknown[] = [];
		let finish!: (value: unknown) => void;
		let reloaded = false;
		const receipt = { generation_id: "g", targets: [] };
		const client = {
			request: (method: string, params: unknown) => {
				sent.push({ method, params });
				return new Promise<unknown>((resolve) => {
					finish = resolve;
				});
			},
		} as unknown as ConversationClientLike;
		const actions = new NavigationActions(
			client,
			async (value) => {
				expect(value).toEqual(receipt);
				reloaded = true;
			},
			() => true,
			async () => {},
		);
		const run = actions.archive(
			{ kind: "project", id: "project-key", workingDir: "/workspace" },
			true,
		);
		await actions.favorite("project-key", true);
		expect(sent).toEqual([
			{
				method: "evener/archive/set",
				params: {
					kind: "project",
					id: "project-key",
					workingDir: "/workspace",
					archived: true,
				},
			},
		]);
		expect(reloaded).toBe(false);
		finish({ ok: true, navigation: receipt });
		await run;
		expect(reloaded).toBe(true);
		expect(actions.getSnapshot().pending).toBe(false);
	});
	it("rejects stale confirmations and does not refresh after disposal", async () => {
		let current = false,
			calls = 0,
			refreshes = 0;
		let finish!: (value: unknown) => void;
		const client = {
			request: () => {
				calls++;
				return new Promise<unknown>((resolve) => {
					finish = resolve;
				});
			},
		} as unknown as ConversationClientLike;
		const actions = new NavigationActions(
			client,
			async () => {
				refreshes++;
			},
			() => current,
			async () => {},
		);
		await actions.archive({ kind: "session", id: "remote:session" }, false);
		expect(calls).toBe(0);
		current = true;
		const request = actions.favorite("project", false);
		actions.dispose();
		finish({ ok: true, navigation: { generation_id: "g", targets: [] } });
		await request;
		expect(refreshes).toBe(0);
	});
	it("retains an uncertain failure without replaying the mutation", async () => {
		let attempts = 0;
		const client = {
			request: async () => {
				attempts++;
				throw new Error("Disconnected");
			},
		} as unknown as ConversationClientLike;
		const actions = new NavigationActions(
			client,
			async () => {},
			() => true,
			async () => {},
		);
		await actions.archive({ kind: "session", id: "local:s" }, true);
		expect(attempts).toBe(1);
		expect(actions.getSnapshot().error).not.toBeNull();
		expect(actions.getSnapshot().pending).toBe(false);
		expect(actions.getSnapshot().uncertain).toBe(true);
	});
	it("supports pin mutations and clears uncertainty only after a read reconcile", async () => {
		const calls: unknown[] = [];
		let reconcile = 0;
		const client = {
			request: async (method: string, params: unknown) => {
				calls.push({ method, params });
				return {
					ok: true,
					changed: false,
					navigation: { generation_id: "g", targets: [] },
				};
			},
		} as unknown as ConversationClientLike;
		const actions = new NavigationActions(
			client,
			async () => {},
			() => true,
			async () => {
				reconcile += 1;
			},
		);
		await actions.assignPin({ sessionRef: "local:s", sectionName: "Focus" });
		await actions.unpin({ sessionRef: "local:s" });
		await actions.renamePinSection({ sectionId: "focus", name: "Work" });
		await actions.deletePinSection({ sectionId: "focus" });
		expect(calls).toEqual([
			{
				method: "evener/session-pin/assign",
				params: { sessionRef: "local:s", sectionName: "Focus" },
			},
			{ method: "evener/session-pin/unpin", params: { sessionRef: "local:s" } },
			{
				method: "evener/pin-section/rename",
				params: { sectionId: "focus", name: "Work" },
			},
			{ method: "evener/pin-section/delete", params: { sectionId: "focus" } },
		]);
		let attempts = 0;
		const broken = new NavigationActions(
			{
				request: async () => {
					attempts++;
					throw Error("lost reply");
				},
			} as unknown as ConversationClientLike,
			async () => {},
			() => true,
			async () => {
				reconcile += 1;
			},
		);
		await broken.assignPin({ sessionRef: "local:s", sectionId: "focus" });
		await broken.assignPin({ sessionRef: "local:s", sectionId: "focus" });
		expect(broken.getSnapshot().uncertain).toBe(true);
		expect(attempts).toBe(1);
		await broken.reconcile();
		expect(reconcile).toBe(1);
		expect(broken.getSnapshot().uncertain).toBe(false);
	});

	it("keeps a deferred request uncertain when the current scope changes", async () => {
		let current = true;
		let finish!: (value: unknown) => void;
		let refreshes = 0;
		const client = {
			request: () => new Promise<unknown>((resolve) => (finish = resolve)),
		} as unknown as ConversationClientLike;
		const actions = new NavigationActions(
			client,
			async () => {
				refreshes++;
			},
			() => current,
			async () => {},
		);
		const run = actions.archive({ kind: "project", id: "p" }, true);
		current = false;
		finish({ ok: true, navigation: { generation_id: "g", targets: [] } });
		await run;
		expect(refreshes).toBe(0);
		expect(actions.getSnapshot()).toMatchObject({
			pending: false,
			uncertain: true,
		});
	});

	it("does not report success after a deferred receipt refresh crosses scope", async () => {
		let current = true;
		let finishRefresh!: () => void;
		let markRefreshStarted!: () => void;
		const refreshStarted = new Promise<void>(
			(resolve) => (markRefreshStarted = resolve),
		);
		const client = {
			request: async () => ({
				ok: true,
				navigation: { generation_id: "g", targets: [] },
			}),
		} as unknown as ConversationClientLike;
		const actions = new NavigationActions(
			client,
			async () => {
				markRefreshStarted();
				await new Promise<void>((resolve) => (finishRefresh = resolve));
			},
			() => current,
			async () => {},
		);
		const run = actions.favorite("project", true);
		await refreshStarted;
		current = false;
		finishRefresh();
		await run;
		expect(actions.getSnapshot()).toMatchObject({
			pending: false,
			uncertain: true,
		});
	});

	it("ends reconciliation as uncertain when its scope changes during the read", async () => {
		let current = true;
		let finishReconcile!: () => void;
		const actions = new NavigationActions(
			{
				request: async () => {
					throw new Error("unexpected mutation");
				},
			} as unknown as ConversationClientLike,
			async () => {},
			() => current,
			async () => {
				await new Promise<void>((resolve) => (finishReconcile = resolve));
			},
		);
		const reconcile = actions.reconcile();
		current = false;
		finishReconcile();
		await reconcile;
		expect(actions.getSnapshot()).toMatchObject({
			pending: false,
			uncertain: true,
		});
	});

	it("blocks another mutation while reconciliation is pending", async () => {
		let finishReconcile!: () => void;
		let requests = 0;
		const client = {
			request: async () => {
				requests++;
				throw new Error("lost reply");
			},
		} as unknown as ConversationClientLike;
		const actions = new NavigationActions(
			client,
			async () => {},
			() => true,
			async () => {
				await new Promise<void>((resolve) => (finishReconcile = resolve));
			},
		);
		await actions.archive({ kind: "session", id: "s" }, true);
		const reconcile = actions.reconcile();
		await actions.favorite("project", true);
		expect(requests).toBe(1);
		finishReconcile();
		await reconcile;
		expect(actions.getSnapshot()).toMatchObject({
			pending: false,
			uncertain: false,
		});
	});

	it("retains uncertainty after reconcile failure and never replays the mutation", async () => {
		let requests = 0;
		const client = {
			request: async () => {
				requests++;
				throw new Error("lost reply");
			},
		} as unknown as ConversationClientLike;
		const actions = new NavigationActions(
			client,
			async () => {},
			() => true,
			async () => {
				throw new Error("refresh failed");
			},
		);
		await actions.archive({ kind: "session", id: "s" }, true);
		await actions.reconcile();
		await actions.archive({ kind: "session", id: "s" }, true);
		expect(requests).toBe(1);
		expect(actions.getSnapshot()).toMatchObject({
			pending: false,
			uncertain: true,
		});
	});

	it("waits for refresh even when the mutation reports changed false", async () => {
		let release!: () => void;
		let refreshed = false;
		const client = {
			request: async () => ({
				ok: true,
				changed: false,
				navigation: { generation_id: "g", targets: [] },
			}),
		} as unknown as ConversationClientLike;
		const actions = new NavigationActions(
			client,
			async () => {
				await new Promise<void>((resolve) => (release = resolve));
				refreshed = true;
			},
			() => true,
			async () => {},
		);
		const run = actions.assignPin({ sessionRef: "s", sectionName: "Focus" });
		await Promise.resolve();
		expect(refreshed).toBe(false);
		expect(actions.getSnapshot().pending).toBe(true);
		release();
		await run;
		expect(refreshed).toBe(true);
		expect(actions.getSnapshot().uncertain).toBe(false);
	});
});
