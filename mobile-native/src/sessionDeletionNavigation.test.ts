import { expect, it } from "vitest";
import { WireError } from "../../cmd/evener-hub/frontend/src/protocol/errors";
import {
	manifest,
	wireV2,
} from "../../cmd/evener-hub/frontend/src/stores/navigation/testing";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import type { NavigationActionCheckpoint } from "./navigationActionRepository";
import { readSessionDeletion } from "./sessionDeletionNavigation";

const id = "034Kc9793pXlhHyCRXdeAk",
	ref = `local:${id}`;
const pending: NavigationActionCheckpoint = {
	id: "intent",
	operation: { kind: "deleteSession", params: { ref } },
	receipt: null,
};
function fixture() {
	let missing = false,
		status = "notLoaded",
		generation = "g",
		revision = 40;
	let threadError: unknown;
	const calls: { method: string; params: Record<string, unknown> }[] = [];
	const client = {
		request: async (method: string, params: Record<string, unknown>) => {
			calls.push({ method, params });
			if (method === "thread/read") {
				if (threadError) throw threadError;
				if (missing)
					throw new WireError(`thread not found: ${id}`, -32000, {
						evenerErrorInfo: "sessionUnavailable",
					});
				return {
					thread: {
						id,
						name: "Saved session",
						status: { type: status },
						evener: { ref, instanceId: "instance" },
					},
				};
			}
			return wireV2(
				params as never,
				params.resource === "manifest"
					? manifest()
					: { pin_sections: [], remaining: 0 },
				'"fresh"',
				params.resource === "manifest" ? 2 : revision,
				generation,
			);
		},
	} as unknown as ConversationClientLike;
	return {
		client,
		calls,
		missing: () => {
			missing = true;
		},
		live: () => {
			status = "idle";
		},
		fail: (error: unknown) => {
			threadError = error;
		},
		restart: () => {
			generation = "next";
		},
		stale: () => {
			revision = 39;
		},
	};
}
it("reads stopped metadata without subscribing or starting a runtime", async () => {
	const f = fixture();
	expect(
		await readSessionDeletion(f.client, ref, undefined, () => true),
	).toMatchObject({ eligible: true, missing: false, settled: true });
	expect(f.calls.filter((call) => call.method === "thread/read")).toEqual([
		{
			method: "thread/read",
			params: { ref, includeTurns: false, subscribe: false },
		},
	]);
	f.live();
	expect(
		await readSessionDeletion(f.client, ref, undefined, () => true),
	).toMatchObject({ eligible: false });
});
it("keeps an unknown-present delete unresolved and settles only after authoritative absence", async () => {
	const f = fixture();
	expect(
		await readSessionDeletion(f.client, ref, pending, () => true),
	).toMatchObject({ missing: false, settled: false });
	f.missing();
	expect(
		await readSessionDeletion(f.client, ref, pending, () => true),
	).toMatchObject({ missing: true, settled: true });
	expect(
		f.calls.every((call) =>
			["thread/read", "evener/navigation/read"].includes(call.method),
		),
	).toBe(true);
});
it("retains skipped reasons and does not equate an acknowledgement with absence", async () => {
	const f = fixture();
	const skipped: NavigationActionCheckpoint = {
		...pending,
		receipt: { generation_id: "g", targets: [] },
		deletion: { kind: "skipped", reason: "resumed live" },
	};
	expect(
		await readSessionDeletion(f.client, ref, skipped, () => true),
	).toMatchObject({
		missing: false,
		settled: true,
		skippedReason: "resumed live",
	});
	for (const kind of ["deleted", "missing"] as const)
		await expect(
			readSessionDeletion(
				f.client,
				ref,
				{ ...skipped, deletion: { kind } },
				() => true,
			),
		).rejects.toThrow();
});
it("compares receipt revisions only with their own resource", async () => {
	const f = fixture();
	f.missing();
	const checkpoint: NavigationActionCheckpoint = {
		...pending,
		receipt: {
			generation_id: "g",
			targets: [{ kind: "pin_catalog", revision: 40 }],
		},
		deletion: { kind: "deleted" },
	};
	expect(
		await readSessionDeletion(f.client, ref, checkpoint, () => true, true),
	).toMatchObject({ missing: true });
	f.stale();
	await expect(
		readSessionDeletion(f.client, ref, checkpoint, () => true, true),
	).rejects.toThrow();
	f.restart();
	await expect(
		readSessionDeletion(f.client, ref, checkpoint, () => true, true),
	).rejects.toThrow();
	expect(
		await readSessionDeletion(f.client, ref, checkpoint, () => true),
	).toMatchObject({ generationId: "next", missing: true });
});
it("never treats transport unavailability or another target's error as deletion", async () => {
	const f = fixture();
	for (const error of [
		new Error("disconnected"),
		new WireError("navigation unavailable", -32000),
		new WireError("thread not found: another", -32000, {
			evenerErrorInfo: "sessionUnavailable",
		}),
	]) {
		f.fail(error);
		await expect(
			readSessionDeletion(f.client, ref, pending, () => true),
		).rejects.toThrow();
	}
});
it("rejects wrong target recovery and scope replacement without confirming", async () => {
	const f = fixture();
	await expect(
		readSessionDeletion(
			f.client,
			ref,
			{ ...pending, operation: { kind: "unpin", params: { sessionRef: ref } } },
			() => true,
		),
	).rejects.toThrow();
	let current = true;
	const reading = readSessionDeletion(f.client, ref, pending, () => current);
	current = false;
	await expect(reading).rejects.toThrow();
});
