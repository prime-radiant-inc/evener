import { describe, expect, it } from "vitest";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import {
	type NavigationActionCheckpoint,
	nativeNavigationActions,
} from "./navigationActionRepository";
import { NavigationActions } from "./navigationActions";
import { sessionDeletionResult } from "./sessionDeletionResult";

const id = "034Kc9793pXlhHyCRXdeAk";
const ref = `local:${id}`;
const receipt = { generation_id: "g", targets: [] };
const deleted = { deleted: [id], skipped: [], navigation: receipt };
function boundary(reply: () => Promise<unknown> = async () => deleted) {
	const values = new Map<string, unknown>();
	let next = 0;
	const backend = {
		createId: () => String(++next),
		get: (key: string) => structuredClone(values.get(key) ?? null),
		set: (key: string, value: unknown) => {
			values.set(key, structuredClone(value));
		},
		deleteIf: (key: string, value: unknown) => {
			if (JSON.stringify(values.get(key)) !== JSON.stringify(value))
				return false;
			return values.delete(key);
		},
	};
	const journal = nativeNavigationActions("hub", backend);
	const calls: { method: string; params: unknown }[] = [];
	const reads: (NavigationActionCheckpoint | undefined)[] = [];
	const client = {
		request: async (method: string, params: unknown) => {
			calls.push({ method, params });
			expect(journal.load()?.operation).toEqual({
				kind: "deleteSession",
				params: { ref },
			});
			return reply();
		},
	} as unknown as ConversationClientLike;
	let readFails = true;
	const read = async (checkpoint?: NavigationActionCheckpoint) => {
		reads.push(checkpoint);
		if (readFails) throw Error("read unavailable");
	};
	const create = () =>
		new NavigationActions(
			client,
			(_receipt, checkpoint) => read(checkpoint),
			() => true,
			read,
			journal,
		);
	return {
		backend,
		journal,
		calls,
		reads,
		create,
		actions: create(),
		permitRead: () => {
			readFails = false;
		},
	};
}
describe("session deletion result", () => {
	it.each([
		[deleted, { kind: "deleted" }],
		[{ deleted: [], skipped: [], navigation: receipt }, { kind: "missing" }],
		[
			{
				deleted: [],
				skipped: [{ id, reason: "resumed live" }],
				navigation: receipt,
			},
			{ kind: "skipped", reason: "resumed live" },
		],
	])(
		"decodes the target-specific result without an ok field",
		(response, expected) => {
			expect(sessionDeletionResult(ref, response)).toEqual(expected);
		},
	);
	it.each([
		{ deleted: [ref], skipped: [] },
		{ deleted: [id, id], skipped: [] },
		{ deleted: [id], skipped: [{ id, reason: "live" }] },
		{ deleted: [], skipped: [{ id: "other", reason: "live" }] },
		{ deleted: [], skipped: [{ id, reason: "" }] },
		{ deleted: [], skipped: null },
		{},
	])("rejects malformed or contradictory results", (response) => {
		expect(() => sessionDeletionResult(ref, response)).toThrow();
	});
});
describe("durable session deletion", () => {
	it("requires an explicit exact-checkpoint allowance before a new attempt", async () => {
		const f = boundary(async () => {
			throw Error("lost reply");
		});
		await f.actions.deleteSession({ ref });
		const checkpoint = f.journal.load();
		if (!checkpoint) throw Error("intent missing");
		expect(
			f.actions.allowDeletionRetry({ ...checkpoint, id: "different" }),
		).toBe(false);
		expect(f.journal.load()).toEqual(checkpoint);
		expect(f.actions.allowDeletionRetry(checkpoint)).toBe(true);
		expect(f.calls).toHaveLength(1);
		expect(f.journal.load()).toBeNull();
		await f.actions.deleteSession({ ref });
		expect(f.calls).toHaveLength(2);
	});
	it("will not discard a known result or another organization operation as an unknown delete", () => {
		const f = boundary();
		const pending = f.journal.begin({ kind: "deleteSession", params: { ref } });
		const acknowledged = f.journal.acknowledge(pending, receipt, {
			kind: "deleted",
		});
		expect(f.actions.allowDeletionRetry(acknowledged)).toBe(false);
		expect(f.journal.load()).toEqual(acknowledged);
		expect(f.journal.finish(acknowledged)).toBe(true);
		const pin = f.journal.begin({ kind: "unpin", params: { sessionRef: ref } });
		expect(f.actions.allowDeletionRetry(pin)).toBe(false);
		expect(f.journal.load()).toEqual(pin);
	});
	it("rejects invalid persisted deletion results without changing the existing record", () => {
		const f = boundary();
		const pending = f.journal.begin({ kind: "deleteSession", params: { ref } });
		for (const result of [
			{ kind: "wrong" },
			{ kind: "skipped", reason: "" },
			{ kind: "deleted", reason: "x" },
		]) {
			expect(() =>
				f.journal.acknowledge(pending, receipt, result as never),
			).toThrow();
			expect(f.journal.load()).toEqual(pending);
		}
	});
	it.each([
		deleted,
		{
			deleted: [],
			skipped: [{ id, reason: "resumed live" }],
			navigation: receipt,
		},
		{ deleted: [], skipped: [], navigation: receipt },
	])(
		"retains the exact acknowledgement until readback and never replays",
		async (response) => {
			const f = boundary(async () => response);
			await f.actions.deleteSession({ ref });
			expect(f.calls).toEqual([
				{ method: "evener/session/delete", params: { ref } },
			]);
			expect(f.journal.load()).toMatchObject({
				receipt,
				deletion: sessionDeletionResult(ref, response),
			});
			f.actions.dispose();
			const next = f.create();
			await next.deleteSession({ ref });
			expect(f.calls).toHaveLength(1);
			f.permitRead();
			await next.reconcile();
			expect(f.reads.at(-1)?.deletion).toEqual(
				sessionDeletionResult(ref, response),
			);
			expect(f.journal.load()).toBeNull();
			expect(f.calls).toHaveLength(1);
		},
	);
	it("does not dispatch when the durable intent cannot be saved", async () => {
		const f = boundary();
		f.backend.set = () => {
			throw Error("disk full");
		};
		await f.actions.deleteSession({ ref });
		expect(f.calls).toHaveLength(0);
		expect(f.actions.getSnapshot().storageUnavailable).toBe(true);
	});
	it("persists a late ACK after disposal without refreshing the old screen", async () => {
		let resolve!: (value: unknown) => void;
		const f = boundary(
			() =>
				new Promise((done) => {
					resolve = done;
				}),
		);
		const run = f.actions.deleteSession({ ref });
		f.actions.dispose();
		resolve(deleted);
		await run;
		expect(f.journal.load()).toMatchObject({
			receipt,
			deletion: { kind: "deleted" },
		});
		expect(f.reads).toHaveLength(0);
	});
	it("cannot overwrite a replacement checkpoint with a late ACK", async () => {
		let resolve!: (value: unknown) => void;
		const f = boundary(
			() =>
				new Promise((done) => {
					resolve = done;
				}),
		);
		const run = f.actions.deleteSession({ ref });
		const pending = f.journal.load();
		if (!pending) throw Error("intent missing");
		expect(f.journal.finish(pending)).toBe(true);
		const replacement = f.journal.begin({
			kind: "unpin",
			params: { sessionRef: ref },
		});
		resolve(deleted);
		await run;
		expect(f.journal.load()).toEqual(replacement);
		expect(f.reads).toHaveLength(0);
	});
	it("keeps an unknown checkpoint on lost or malformed replies", async () => {
		for (const reply of [
			async () => {
				throw Error("lost reply");
			},
			async () => ({ ...deleted, deleted: ["other"] }),
		]) {
			const f = boundary(reply);
			await f.actions.deleteSession({ ref });
			expect(f.journal.load()).toMatchObject({ receipt: null });
			expect(f.journal.load()?.deletion).toBeUndefined();
			await f.actions.deleteSession({ ref });
			expect(f.calls).toHaveLength(1);
		}
	});
});
