import { expect, it } from "vitest";
import type { NavigationMutation } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import {
	type NavigationActionBackend,
	type NavigationOperation,
	nativeNavigationActions,
} from "./navigationActionRepository";

function backend(initial: unknown = null) {
	const values = new Map<string, unknown>(
		initial === null
			? []
			: [["evener.native.navigation-action.hub-a", initial]],
	);
	let next = 0;
	const store: NavigationActionBackend = {
		createId: () => `id-${++next}`,
		get: (key) => values.get(key) ?? null,
		set: (key, value) => {
			values.set(key, value);
		},
		deleteIf: (key, expected) => {
			if (JSON.stringify(values.get(key)) !== JSON.stringify(expected))
				return false;
			values.delete(key);
			return true;
		},
	};
	return {
		store,
		read: () => values.get("evener.native.navigation-action.hub-a"),
	};
}
const operation: NavigationOperation = {
	kind: "assignPin",
	params: { sessionRef: "thread:one", sectionName: "Pinned" },
};
const receipt: NavigationMutation = {
	generation_id: "g1",
	targets: [{ kind: "pin_catalog" }],
};

it("round trips one typed checkpoint and recovers after recreation", () => {
	const b = backend();
	const first = nativeNavigationActions("hub-a", b.store);
	const checkpoint = first.begin(operation);
	expect(first.load()).toEqual(checkpoint);
	expect(nativeNavigationActions("hub-a", b.store).load()).toEqual(checkpoint);
	expect(first.acknowledge(checkpoint, receipt).receipt).toEqual(receipt);
	expect(first.finish({ ...checkpoint, receipt })).toBe(true);
	expect(first.load()).toBeNull();
});

it("fences duplicate begin, stale acknowledgement, and stale finish", () => {
	const b = backend();
	const actions = nativeNavigationActions("hub-a", b.store);
	const checkpoint = actions.begin(operation);
	expect(() =>
		actions.begin({
			kind: "favorite",
			params: { kind: "project", id: "x", favorited: true },
		}),
	).toThrow();
	expect(() =>
		actions.acknowledge({ ...checkpoint, id: "old" }, receipt),
	).toThrow();
	expect(actions.finish({ ...checkpoint, id: "old" })).toBe(false);
});

it("isolates hubs and preserves a pending write when storage reads or writes fail", () => {
	const b = backend();
	const a = nativeNavigationActions("hub-a", b.store);
	const c = nativeNavigationActions("hub-b", b.store);
	const checkpoint = a.begin(operation);
	expect(c.load()).toBeNull();
	const failing: NavigationActionBackend = {
		...b.store,
		get: () => {
			throw new Error("read");
		},
	};
	expect(() => nativeNavigationActions("hub-a", failing).load()).toThrow(
		"read",
	);
	const failingWrite: NavigationActionBackend = {
		...b.store,
		set: () => {
			throw new Error("write");
		},
	};
	expect(() =>
		nativeNavigationActions("hub-b", failingWrite).begin(operation),
	).toThrow("write");
	expect(a.load()).toEqual(checkpoint);
});

it("rejects corrupt checkpoints without erasing them", () => {
	const b = backend({
		id: "bad",
		operation: { kind: "unknown", params: {} },
		receipt: null,
	});
	const actions = nativeNavigationActions("hub-a", b.store);
	expect(() => actions.load()).toThrow();
	expect(b.read()).toEqual({
		id: "bad",
		operation: { kind: "unknown", params: {} },
		receipt: null,
	});
});

it("conditionally finishes only the exact stored checkpoint", () => {
	const b = backend();
	const actions = nativeNavigationActions("hub-a", b.store);
	const checkpoint = actions.begin(operation);
	const acknowledged = actions.acknowledge(checkpoint, receipt);
	const newer = { ...acknowledged, id: "newer" };
	b.store.set("evener.native.navigation-action.hub-a", newer);
	expect(actions.finish(acknowledged)).toBe(false);
	expect(actions.load()).toEqual(newer);
});

it("validates every operation shape", () => {
	const operations: NavigationOperation[] = [
		{ kind: "archive", params: { kind: "project", id: "x", archived: true } },
		{
			kind: "favorite",
			params: { kind: "project", id: "x", favorited: false },
		},
		operation,
		{ kind: "unpin", params: { sessionRef: "thread:x" } },
		{ kind: "renamePinSection", params: { sectionId: "s", name: "New" } },
		{ kind: "deletePinSection", params: { sectionId: "s" } },
	];
	for (const [index, candidate] of operations.entries()) {
		const actions = nativeNavigationActions(`hub-${index}`, backend().store);
		expect(actions.begin(candidate).operation).toEqual(candidate);
	}
});
