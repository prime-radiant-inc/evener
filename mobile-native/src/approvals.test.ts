import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { describe, expect, it, vi } from "vitest";
import type { SandboxEscalationRequested, Thread } from "@evener/appwire-client";
import { type ConversationClientLike, createConversationService } from "./conversation/services/conversation";
import { createConversationStore } from "./conversation/state/conversation";
import { ApprovalControls } from "./approvalControls";

const pending: SandboxEscalationRequested = {
	threadId: "thread",
	ref: "local:s",
	escalationId: "approval",
	tool: "exec",
	mode: "workspace",
	kind: "read",
	deniedPath: "/outside",
	command: "cat /outside",
	partiallyRan: true,
	outputSoFar: "partial",
};
function boundary() {
	const thread: Thread = {
		id: "thread",
		sessionId: "s",
		preview: "",
		ephemeral: false,
		modelProvider: "scripted",
		createdAt: 1,
		updatedAt: 1,
		status: { type: "active" },
		cwd: "/workspace",
		cliVersion: "test",
		source: "local",
		turns: [],
		evener: {
			ref: "local:s",
			instanceId: "instance",
			queue: { revision: 0 },
			capabilities: {
				send: true,
				steer: true,
				interrupt: true,
				compact: true,
				clear: true,
				forkFromTurn: true,
				shutdown: true,
				changeModel: true,
				changeVisionModel: false,
				sharedNotes: false,
				queue: true,
				goal: true,
				rename: true,
			},
			pendingEscalations: [pending],
		},
	};
	const client = new FakeClient("ready");
	client.on("thread/read", () => ({ thread }));
	client.on("thread/unsubscribe", () => ({}));
	return { client, service: createConversationService(client) };
}
describe("native approval projection", () => {
	it("hydrates pending harness approvals and applies only matching live resolutions", async () => {
		const { service } = boundary();
		const store = createConversationStore();
		await store.getState().open(service, "local:s");
		expect(store.getState().conversation?.pendingEscalations).toEqual([pending]);
		store.getState().applyNotification({
			method: "evener/sandbox/escalation/resolved",
			params: { threadId: "other", ref: "other:s", escalationId: "approval" },
		});
		expect(store.getState().conversation?.pendingEscalations).toHaveLength(1);
		store.getState().applyNotification({
			method: "evener/sandbox/escalation/requested",
			params: { ...pending, deniedPath: "/updated" },
		});
		expect(store.getState().conversation?.pendingEscalations).toEqual([{ ...pending, deniedPath: "/updated" }]);
		store.getState().applyNotification({
			method: "evener/sandbox/escalation/resolved",
			params: pending,
		});
		expect(store.getState().conversation?.pendingEscalations).toEqual([]);
	});
});

describe("approval decisions", () => {
	// A reconnect can replace these controls while the decision is out. The
	// receipt is the hub's answer, and it is what settles the decision: a
	// controller that reports nothing because it was replaced hides a decision
	// the hub took, and the dock leaves the reader unsure about something that
	// already happened. Only the catch-up read needs the live binding.
	it("confirms a decision the hub took even after its controls were replaced", async () => {
		let answer!: () => void;
		const sent: unknown[] = [];
		const controls = new ApprovalControls(
			{
				request: (method: string, params: unknown) => {
					sent.push({ method, params });
					return new Promise<object>((resolve) => {
						answer = () => resolve({});
					});
				},
			} as unknown as ConversationClientLike,
			"local:s",
			() => [pending],
			() => true,
			async () => {},
			() => true,
		);
		const decision = controls.resolve(pending, true);
		await Promise.resolve();
		expect(sent).toHaveLength(1);
		controls.dispose();
		answer();
		await expect(decision).resolves.toBe(true);
	});

	// Admitting a decision is the reader's question. A press from a pane another
	// conversation now covers reaches nobody the reader can see, so it must not
	// reach the hub either; the decision is still there when they come back.
	it("admits no decision from a covered pane, and dispatches nothing", async () => {
		const sent: unknown[] = [];
		const client = {
			request: async (method: string, params: unknown) => {
				sent.push({ method, params });
				return {};
			},
		} as unknown as ConversationClientLike;
		let inFront = false;
		const controls = new ApprovalControls(
			client,
			"local:s",
			() => [pending],
			() => true,
			async () => {},
			// Nothing is in front of this pane: another conversation is.
			() => inFront,
		);
		const covered = controls.resolve(pending, true);
		await expect(covered).resolves.toBe(false);
		expect(sent).toEqual([]);
		expect(controls.getSnapshot().pending).toBeNull();
		// Back in front, the same decision is admitted and sent once.
		inFront = true;
		await expect(controls.resolve(pending, true)).resolves.toBe(true);
		expect(sent).toHaveLength(1);
	});
	it("sends the displayed current approval once and refreshes after acknowledgement", async () => {
		let finish!: () => void;
		const sent: unknown[] = [];
		let refreshed = 0;
		const client = {
			request: async (method: string, params: unknown) => {
				sent.push({ method, params });
				await new Promise<void>((done) => {
					finish = done;
				});
				return {};
			},
		} as unknown as ConversationClientLike;
		const controls = new ApprovalControls(
			client,
			"local:s",
			() => [pending],
			() => true,
			async () => {
				refreshed++;
			},
			() => true,
		);
		const request = controls.resolve(pending, true);
		await controls.resolve(pending, false);
		await controls.refresh();
		expect(refreshed).toBe(0);
		expect(sent).toEqual([
			{
				method: "evener/sandbox/escalation/resolve",
				params: { ref: "local:s", escalationId: "approval", approve: true },
			},
		]);
		finish();
		await request;
		expect(refreshed).toBe(1);
		expect(controls.getSnapshot().pending).toBeNull();
	});
	it("rejects resolved, changed and old-owner approvals before dispatch", async () => {
		let calls = 0;
		let values = [pending];
		let current = true;
		const displayed = pending;
		const client = {
			request: async () => {
				calls++;
				return {};
			},
		} as unknown as ConversationClientLike;
		const controls = new ApprovalControls(
			client,
			"local:s",
			() => values,
			() => current,
			async () => {},
			() => true,
		);
		values = [{ ...displayed, deniedPath: "/different" }];
		await controls.resolve(displayed, true);
		values = [];
		await controls.resolve(displayed, false);
		values = [displayed];
		current = false;
		await controls.resolve(displayed, true);
		current = true;
		controls.dispose();
		await controls.resolve(displayed, true);
		expect(calls).toBe(0);
	});
	// A press after a failure tries again: the hub refuses a resolve for an
	// escalation it already settled (agent/session_escalation.go's
	// ResolveSandboxEscalation, a Conflict), so trying again never decides
	// twice, and a failure never latches the decision off.
	it("retains failure, and a new press tries again and clears it", async () => {
		let calls = 0;
		const client = {
			request: async () => {
				calls++;
				throw new Error("Lost acknowledgement");
			},
		} as unknown as ConversationClientLike;
		const controls = new ApprovalControls(
			client,
			"local:s",
			() => [pending],
			() => true,
			async () => {},
			() => true,
		);
		await controls.resolve(pending, false);
		expect(calls).toBe(1);
		expect(controls.getSnapshot().error).not.toBeNull();
		const seen: (string | null)[] = [];
		controls.subscribe(() => seen.push(controls.getSnapshot().error));
		await controls.resolve(pending, true);
		expect(calls).toBe(2);
		// The new try clears the old error as it sends.
		expect(seen[0]).toBeNull();
	});
	it.each([undefined, null, [], "ok", 0, false].map((receipt) => ({ receipt })))(
		"treats malformed resolution receipt $receipt as uncertain",
		async ({ receipt }) => {
			let calls = 0;
			const client = {
				request: async () => {
					calls++;
					return receipt;
				},
			} as unknown as ConversationClientLike;
			let refreshed = 0;
			const controls = new ApprovalControls(
				client,
				"local:s",
				() => [pending],
				() => true,
				async () => {
					refreshed++;
				},
				() => true,
			);
			await controls.resolve(pending, true);
			expect(refreshed).toBe(0);
			expect(controls.getSnapshot().error).not.toBeNull();
			await controls.resolve(pending, false);
			expect(calls).toBe(2);
			expect(controls.getSnapshot().pending).toBeNull();
		},
	);
	it("settles a decision the hub acknowledged when its readback fails", async () => {
		let calls = 0;
		const controls = new ApprovalControls(
			{
				request: async () => {
					calls++;
					return {};
				},
			} as unknown as ConversationClientLike,
			"local:s",
			() => [pending],
			() => true,
			async () => {
				// The receipt already said the hub took it, so its catch-up read
				// failing leaves the decision settled rather than unconfirmed.
				throw new Error("The read lost its connection.");
			},
			() => true,
		);
		expect(await controls.resolve(pending, true)).toBe(true);
		expect(calls).toBe(1);
		expect(controls.getSnapshot()).toMatchObject({ pending: null, error: null });
	});
	it("tries a decision again after a failure, whether or not a re-read succeeded", async () => {
		let dispatchFails = true;
		let refreshFails = true;
		let calls = 0;
		const controls = new ApprovalControls(
			{
				request: async () => {
					calls++;
					if (dispatchFails) throw new Error("lost reply");
					return {};
				},
			} as unknown as ConversationClientLike,
			"local:s",
			() => [pending],
			() => true,
			async () => {
				if (refreshFails) throw new Error("offline");
			},
			() => true,
		);
		// Nothing came back for this decision, so it stays unconfirmed and a
		// press tries again.
		expect(await controls.resolve(pending, true)).toBe(false);
		expect(controls.getSnapshot().error).not.toBeNull();
		await controls.refresh();
		expect(controls.getSnapshot().error).not.toBeNull();
		dispatchFails = false;
		expect(await controls.resolve(pending, false)).toBe(true);
		expect(calls).toBe(2);
		// The receipt settled this one even though its readback could not run;
		// the session's own read catches up.
		expect(controls.getSnapshot().error).toBeNull();
		refreshFails = false;
		await controls.refresh();
		expect(controls.getSnapshot().error).toBeNull();
	});
	it("does not dispatch a card removed by the recovery read", async () => {
		let values = [pending];
		let calls = 0;
		const controls = new ApprovalControls(
			{
				request: async () => {
					calls++;
					throw new Error("lost reply");
				},
			} as unknown as ConversationClientLike,
			"local:s",
			() => values,
			() => true,
			async () => {
				values = [];
			},
			() => true,
		);
		await controls.resolve(pending, true);
		await controls.refresh();
		expect(controls.getSnapshot().error).toBeNull();
		await controls.resolve(pending, false);
		expect(calls).toBe(1);
	});
	it("serializes recovery reads, retains uncertainty when the binding changes, and tries again on a new press", async () => {
		let current = true;
		let calls = 0;
		let reads = 0;
		let release!: () => void;
		const controls = new ApprovalControls(
			{
				request: async () => {
					calls++;
					return {};
				},
			} as unknown as ConversationClientLike,
			"local:s",
			() => [pending],
			() => current,
			() => {
				reads++;
				return new Promise<void>((resolve) => {
					release = resolve;
				});
			},
			() => true,
		);
		const reading = controls.refresh();
		expect(controls.getSnapshot().refreshing).toBe(true);
		await controls.refresh();
		await controls.resolve(pending, true);
		expect(reads).toBe(1);
		expect(calls).toBe(0);
		current = false;
		release();
		await reading;
		expect(controls.getSnapshot().refreshing).toBe(false);
		expect(controls.getSnapshot().error).not.toBeNull();
		// Back on the current binding, a new press tries the decision, then
		// re-reads the session.
		current = true;
		const retry = controls.resolve(pending, false);
		await vi.waitFor(() => expect(reads).toBe(2));
		expect(calls).toBe(1);
		release();
		await retry;
		expect(controls.getSnapshot().error).toBeNull();
	});
	it("ignores late acknowledgments and reads after disposal", async () => {
		let release!: () => void;
		let reads = 0;
		const controls = new ApprovalControls(
			{
				request: () =>
					new Promise((resolve) => {
						release = () => resolve({});
					}),
			} as unknown as ConversationClientLike,
			"local:s",
			() => [pending],
			() => true,
			async () => {
				reads++;
			},
			() => true,
		);
		const request = controls.resolve(pending, true);
		const before = controls.getSnapshot();
		controls.dispose();
		release();
		await request;
		await controls.refresh();
		expect(reads).toBe(0);
		expect(controls.getSnapshot()).toBe(before);
	});
});

// A resolution delivered while a reread is in flight precedes that reread's
// response, and AppWire cuts the snapshot after it: the response arrives
// without the card, and the store commits that snapshot as authoritative.
it("does not resurrect an approval resolved while an older snapshot is in flight", async () => {
	const base = boundary();
	let reads = 0;
	let release!: () => void;
	let started!: () => void;
	const began = new Promise<void>((resolve) => {
		started = resolve;
	});
	const client = Object.assign(new FakeClient("ready"), {
		request: async () => {
			const response = await base.client.request("thread/read", {
				ref: "local:s",
				includeTurns: true,
			});
			if (++reads !== 2) return response;
			started();
			await new Promise<void>((resolve) => {
				release = resolve;
			});
			const thread = response.thread;
			return {
				...response,
				thread: { ...thread, evener: { ...thread.evener, pendingEscalations: [] } },
			};
		},
	});
	const service = createConversationService(client),
		store = createConversationStore();
	await store.getState().openProjected(service, "local:s");
	const refresh = store.getState().rehydrate(service);
	await began;
	store.getState().applyNotification({
		method: "evener/sandbox/escalation/resolved",
		params: pending,
	});
	release();
	await refresh;
	expect(store.getState().conversation?.pendingEscalations).toEqual([]);
});
// A resolution delivered before the initial read's response precedes the
// snapshot cut: the response arrives without the card, and that snapshot is
// what the open commits — no buffer, no replay.
it("keeps resolutions delivered between the initial snapshot and its response", async () => {
	const base = boundary();
	let release!: () => void;
	let started!: () => void;
	let notification: Parameters<ConversationClientLike["onNotification"]>[0] = () => {};
	const began = new Promise<void>((resolve) => {
		started = resolve;
	});
	const client = Object.assign(new FakeClient("ready"), {
		request: async () => {
			const response = await base.client.request("thread/read", {
				ref: "local:s",
				includeTurns: true,
			});
			started();
			await new Promise<void>((resolve) => {
				release = resolve;
			});
			const thread = response.thread;
			return {
				...response,
				thread: { ...thread, evener: { ...thread.evener, pendingEscalations: [] } },
			};
		},
		onNotification: (listener: typeof notification) => {
			notification = listener;
			return () => {};
		},
	});
	const service = createConversationService(client),
		store = createConversationStore();
	const open = store.getState().openProjected(service, "local:s");
	await began;
	notification({
		method: "evener/sandbox/escalation/resolved",
		params: pending,
	});
	release();
	await open;
	expect(store.getState().conversation?.pendingEscalations).toEqual([]);
});
it("unsubscribes a failed initial read", async () => {
	let unsubscribed = 0;
	const client = Object.assign(new FakeClient("ready"), {
		request: async () => {
			throw new Error("Read failed");
		},
		onNotification: () => () => {
			unsubscribed++;
		},
	});
	const store = createConversationStore();
	await store.getState().openProjected(createConversationService(client), "local:s");
	expect(store.getState().status).toBe("error");
	expect(unsubscribed).toBe(1);
});
