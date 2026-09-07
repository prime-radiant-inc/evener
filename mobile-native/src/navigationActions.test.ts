import { describe, expect, it } from "vitest";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { NavigationActions } from "./navigationActions";

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
