import { expect, it } from "vitest";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import {
	type NavigationActionCheckpoint,
	nativeNavigationActions,
} from "./navigationActionRepository";
import { NavigationActions } from "./navigationActions";

function fixture() {
	const values = new Map<string, unknown>();
	let id = 0;
	const storage = nativeNavigationActions("hub", {
		createId: () => String(++id),
		get: (key) => values.get(key),
		set: (key, value) => {
			values.set(key, value);
		},
		deleteIf: (key, value) =>
			JSON.stringify(values.get(key)) === JSON.stringify(value) &&
			values.delete(key),
	});
	const checkpoint = storage.begin({
		kind: "archive",
		params: {
			kind: "project",
			id: "project",
			workingDir: "/workspace",
			archived: true,
		},
	});
	let reads = 0,
		writes = 0,
		fail = false;
	const actions = new NavigationActions(
		{
			request: async () => {
				writes++;
				throw Error("unexpected mutation");
			},
		} as unknown as ConversationClientLike,
		async () => {},
		() => true,
		async (_checkpoint, acceptCurrent) => {
			reads++;
			if (!acceptCurrent || fail) throw Error("review current state");
		},
		storage,
	);
	return {
		storage,
		checkpoint,
		actions,
		reads: () => reads,
		writes: () => writes,
		fail: () => {
			fail = true;
		},
	};
}
it("requires explicit read-only continuation for an unresolved archive", async () => {
	const f = fixture();
	await f.actions.reconcile();
	expect(f.storage.load()).toEqual(f.checkpoint);
	expect(f.actions.getSnapshot().uncertain).toBe(true);
	await f.actions.keepOrganizationState(f.checkpoint);
	expect(f.storage.load()).toBeNull();
	expect(f.actions.getSnapshot().uncertain).toBe(false);
	expect(f.reads()).toBe(2);
	expect(f.writes()).toBe(0);
});
it("keeps the checkpoint when current-state confirmation fails", async () => {
	const f = fixture();
	f.fail();
	await f.actions.keepOrganizationState(f.checkpoint);
	expect(f.storage.load()).toEqual(f.checkpoint);
	expect(f.actions.getSnapshot().uncertain).toBe(true);
	expect(f.writes()).toBe(0);
});
it("retains recovery if navigation changes after the read callback resolves", async () => {
	const f = fixture();
	let finish!: () => void;
	let changed = false;
	const actions = new NavigationActions(
		{} as ConversationClientLike,
		async () => {},
		() => true,
		() =>
			new Promise<void>((resolve) => {
				finish = resolve;
			}),
		f.storage,
		() => {
			if (changed) throw Error("new navigation invalidation");
		},
	);
	const review = actions.keepOrganizationState(f.checkpoint);
	finish();
	changed = true;
	await review;
	expect(f.storage.load()).toEqual(f.checkpoint);
	expect(actions.getSnapshot().uncertain).toBe(true);
});
it("never accepts a replaced checkpoint or an unrelated operation", async () => {
	const f = fixture();
	f.storage.finish(f.checkpoint);
	const next = f.storage.begin({
		kind: "unpin",
		params: { sessionRef: "local:session" },
	});
	for (const expected of [f.checkpoint, next])
		await f.actions.keepOrganizationState(expected);
	expect(f.storage.load()).toEqual(next);
	expect(f.reads()).toBe(0);
	expect(f.writes()).toBe(0);
});
it("retains a newer receipt delivered during explicit current-state review", async () => {
	const f = fixture();
	let release!: () => void;
	let reviewed: NavigationActionCheckpoint | undefined;
	const actions = new NavigationActions(
		{} as ConversationClientLike,
		async () => {},
		() => true,
		async (checkpoint) => {
			reviewed = checkpoint;
			await new Promise<void>((resolve) => {
				release = resolve;
			});
		},
		f.storage,
	);
	const review = actions.keepOrganizationState(f.checkpoint);
	expect(reviewed).toEqual(f.checkpoint);
	const acknowledged = f.storage.acknowledge(f.checkpoint, {
		generation_id: "g",
		targets: [],
	});
	release();
	await review;
	expect(f.storage.load()).toEqual(acknowledged);
	expect(actions.getSnapshot().uncertain).toBe(true);
});
