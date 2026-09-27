// The production pending-row seam: the adapter that turns the landed native
// runtime's scoped durable read + storage subscription into the store's
// ConversationMutationPendingPort, under the operator's client-owned ruling.
//
// These are the seam's own contracts, isolated from the store: the read narrows
// to the two record families the projection consumes, the subscription follows
// only the bound target, and the ownership rule claims this client's own
// durable outbox (a record a prior process stamped with an earlier
// originClientId included) - the fact that lets the ruling resolve cross-restart
// ownership without exposing the runtime's private ClientIdentity.

import { describe, expect, it } from "vitest";
import type {
	MutationAttachmentRef,
	MutationOptimisticRecord,
	MutationOutboxRecord,
	MutationPersistenceSnapshot,
	MutationRecoveryRecord,
} from "@evener/appwire-client/state/mutation";
import {
	createConversationMutationPendingPort,
	type ConversationMutationPendingRuntime,
} from "./conversationMutation";

// The composite storage target key the native runtime scopes records by: the
// same JSON [hub, ref] shape nativeMutationRuntime.nativeMutationTargetKey
// produces. Built here rather than imported so this store-side suite carries no
// expo/native dependency; the native suite pins the format itself.
function storageKey(hubId: string, ref: string): string {
	return JSON.stringify([hubId, ref]);
}

function outbox(over: Partial<MutationOutboxRecord> = {}): MutationOutboxRecord {
	return {
		version: 1,
		clientMutationId: "cmid-1",
		targetRef: storageKey("hub-a", "ref-1"),
		method: "turn/start",
		payload: {},
		attachments: [],
		optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "hello" }] },
		intentSequence: 0,
		createdAt: 7,
		state: "submitting",
		...over,
	};
}

function optimistic(over: Partial<MutationOptimisticRecord> = {}): MutationOptimisticRecord {
	return { ...outbox(), state: "accepted", ...over };
}

function recovery(over: Partial<MutationRecoveryRecord> = {}): MutationRecoveryRecord {
	return { ...outbox(), recoveryKind: "rejected", ...over };
}

// A faithful runtime double for the seam's contracts: the scoped read filters
// to the exact storage target (what MutationOutboxSQLite.listOutbox(targetRef)
// does) and returns the whole snapshot shape; subscribeStorage publishes the
// target refs a change touched.
function fakeRuntime(snapshot: Partial<MutationPersistenceSnapshot<MutationAttachmentRef>> = {}) {
	const listeners = new Set<(targetRefs: readonly string[]) => void>();
	const reads: string[] = [];
	let current: MutationPersistenceSnapshot<MutationAttachmentRef> = {
		outbox: [],
		optimistic: [],
		recovery: [],
		...snapshot,
	};
	const runtime: ConversationMutationPendingRuntime = {
		read: async (targetRef) => {
			reads.push(targetRef);
			return {
				outbox: current.outbox.filter((record) => record.targetRef === targetRef),
				optimistic: current.optimistic.filter((record) => record.targetRef === targetRef),
				recovery: current.recovery.filter((record) => record.targetRef === targetRef),
			};
		},
		subscribeStorage: (listener) => {
			listeners.add(listener);
			return () => {
				listeners.delete(listener);
			};
		},
	};
	return {
		runtime,
		reads,
		publish(targetRefs: readonly string[], next: Partial<MutationPersistenceSnapshot<MutationAttachmentRef>>) {
			current = { outbox: [], optimistic: [], recovery: [], ...next };
			for (const listener of [...listeners]) listener(targetRefs);
		},
		listenerCount: () => listeners.size,
	};
}

describe("the production pending-row seam — client-owned outbox", () => {
	it("reads only the target's outbox and optimistic rows, never the recovery family", async () => {
		const key = storageKey("hub-a", "ref-1");
		const runtime = fakeRuntime({
			outbox: [outbox()],
			optimistic: [optimistic({ clientMutationId: "cmid-2" })],
			recovery: [recovery({ clientMutationId: "cmid-3" })],
		});
		const port = createConversationMutationPendingPort(runtime.runtime, key);

		const snapshot = await port.read();
		expect(snapshot.optimistic.map((record) => record.clientMutationId)).toEqual(["cmid-2"]);
		expect(snapshot.outbox.map((record) => record.clientMutationId)).toEqual(["cmid-1"]);
		// The recovery family belongs to the recovery surface: it must not ride
		// the pending seam at all, not merely be ignored downstream.
		expect("recovery" in snapshot).toBe(false);
		expect(runtime.reads).toEqual([key]);
	});

	it("keys the durable rows by the composite storage target, not the wire ref", async () => {
		const key = storageKey("hub-a", "ref-1");
		const runtime = fakeRuntime({ outbox: [outbox()] });
		const port = createConversationMutationPendingPort(runtime.runtime, key);
		expect(port.targetRef).toBe(key);
		expect(port.targetRef).not.toBe("ref-1");
		await port.read();
		expect(runtime.reads).toEqual([key]);
	});

	it("follows only the bound target's storage changes", () => {
		const key = storageKey("hub-a", "ref-1");
		const other = storageKey("hub-b", "ref-1");
		const runtime = fakeRuntime();
		const port = createConversationMutationPendingPort(runtime.runtime, key);
		let notified = 0;
		const unsubscribe = port.subscribe(() => {
			notified += 1;
		});

		// Same wire ref, another hub: the target key differs, so it is not ours.
		runtime.publish([other], { outbox: [outbox({ targetRef: other })] });
		expect(notified).toBe(0);
		runtime.publish([key], { outbox: [outbox()] });
		expect(notified).toBe(1);

		unsubscribe();
		runtime.publish([key], { outbox: [outbox()] });
		expect(notified).toBe(1);
		expect(runtime.listenerCount()).toBe(0);
	});

	it("claims every record in this client's durable outbox, including a prior process's stamp", () => {
		const runtime = fakeRuntime();
		const port = createConversationMutationPendingPort(runtime.runtime, storageKey("hub-a", "ref-1"));

		// Client-owned: the outbox is this app instance's own store, so a record
		// is ours regardless of the originClientId a prior process wrote. This is
		// the cross-restart ownership the operator's ruling resolves - and why no
		// runtime ClientIdentity has to be exposed to answer it.
		expect(port.isOwnMutationRecord({ originClientId: undefined })).toBe(true);
		expect(port.isOwnMutationRecord({ originClientId: "mutation-client-prior-process" })).toBe(true);
		expect(port.isOwnMutationRecord({ originClientId: "mutation-client-someone-else" })).toBe(true);
	});
});
