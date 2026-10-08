import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
// The Session's own read of the fleet (ruling 33), against the real Board
// controller and the real seen markers.
import type { AnyNotification } from "@evener/appwire-client";
import { act } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { seenMarkers } from "../board/nativeBoardMemory";
import { renderHook, settleMicrotasks } from "../renderNative.testkit";
import { othersNeedingYou } from "./fleetOrder";
import { answerFleetRead, type FleetShape, fleetSession } from "./fleetTestUtils";
import { useFleet } from "./useFleet";

const kv = vi.hoisted(() => new Map<string, string>());
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: {
		getItemSync: (key: string) => kv.get(key) ?? null,
		setItemSync: (key: string, value: string) => kv.set(key, value),
		removeItemSync: (key: string) => kv.delete(key),
	},
}));

const at = (minute: number) => new Date(Date.UTC(2026, 8, 26, 12, minute)).toISOString();
const failing = fleetSession("local:fail", { title: "Fix retry loop", state: "errored", updated_at: at(5) });
const read = fleetSession("local:read", { title: "Read already", state: "awaiting", updated_at: at(8) });
const unread = fleetSession("local:unread", { title: "Not read yet", state: "awaiting", updated_at: at(9) });
const fleet: FleetShape = {
	live: [failing, read, unread],
	needsYou: [failing],
	sources: [{ id: "local", label: "Laptop" }],
};

/** A hub that answers from `shape` (the shared fleet unless a test hands
 * in its own), failing the first read of each section `failOnce` names. */
function hub({ failOnce = [] as string[], shape = fleet } = {}) {
	const methods: string[] = [];
	const toFail = new Set(failOnce);
	const listeners = new Set<(event: AnyNotification) => void>();
	// Every subscription a binding makes: a rebind subscribes afresh.
	let subscriptions = 0;
	const client: ConversationClientLike = Object.assign(new FakeClient("ready"), {
		request: (method, params) => {
			methods.push(method);
			const section = (params as { section?: string }).section;
			if (section && toFail.delete(section)) return Promise.reject(new Error("request timed out"));
			return Promise.resolve(answerFleetRead(shape, method, params) as never);
		},
		onNotification: (listener) => {
			subscriptions += 1;
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
	} as Omit<ConversationClientLike, "state" | "onReady" | "onStateChange">);
	/** The hub says Needs you changed, at the shape's revision. */
	const invalidateNeedsYou = (sequence: number) => {
		for (const listener of [...listeners])
			listener({
				method: "evener/navigation/invalidated",
				params: {
					generationId: "generation-test",
					sequence,
					targets: [{ kind: "section", section: "needs_you", revision: shape.revision ?? 1 }],
				},
			} as AnyNotification);
	};
	return { client, methods, invalidateNeedsYou, listening: () => listeners.size, subscriptions: () => subscriptions };
}

let hubCount = 0;
function mount(client: ConversationClientLike | null, inFront = true) {
	hubCount += 1;
	const hubId = `fleet-hub-${hubCount}`;
	// First run happened before these sessions changed; one of them has
	// been opened since.
	kv.set(`evener.native.seen.${hubId}`, JSON.stringify({ adopted: true, epoch: at(0), sessions: {} }));
	seenMarkers(hubId).markSeen(read);
	const view = { client, inFront };
	const hook = renderHook(() => useFleet(hubId, view.client, view.inFront));
	return { hook, view };
}

it("reads nothing without a client", () => {
	const { hook } = mount(null);
	expect(hook.result.current.bands.needsYou).toEqual([]);
	hook.unmount();
});

it("classifies the fleet with the Board's seen function", async () => {
	const { client } = hub();
	const { hook } = mount(client);
	await settleMicrotasks();
	const { bands, sources } = hook.result.current;
	expect(bands.needsYou.map((item) => item.row.ref)).toEqual(["local:fail"]);
	// A session already opened is Idle; one not opened since it changed is
	// Finished.
	expect(bands.idle.map((item) => item.row.ref)).toEqual(["local:read"]);
	expect(bands.finished.map((item) => item.row.ref)).toEqual(["local:unread"]);
	expect(sources?.map((source) => source.label)).toEqual(["Laptop"]);
	hook.unmount();
});

it("reads nothing while another screen is in front", async () => {
	const { client, methods } = hub();
	const { hook, view } = mount(null, false);
	view.client = client;
	hook.rerender();
	await settleMicrotasks();
	expect(methods).toEqual([]);
	view.inFront = true;
	hook.rerender();
	await settleMicrotasks();
	expect(methods).toContain("evener/navigation/read");
	hook.unmount();
});

it("retries a failed read on its own, so Back's count appears", async () => {
	vi.useFakeTimers();
	try {
		const { client } = hub({ failOnce: ["live", "needs_you"] });
		const { hook } = mount(client);
		await settleMicrotasks();
		expect(othersNeedingYou(hook.result.current.bands, "local:here")).toEqual([]);
		// The Board's backoff: the first retry waits a second.
		await act(async () => {
			await vi.advanceTimersByTimeAsync(1000);
		});
		await settleMicrotasks();
		expect(othersNeedingYou(hook.result.current.bands, "local:here").map((row) => row.ref)).toEqual(["local:fail"]);
		hook.unmount();
	} finally {
		vi.useRealTimers();
	}
});

describe("the fleet's lifecycle", () => {
	const here = "local:here";
	const count = (hook: ReturnType<typeof mount>["hook"]) => othersNeedingYou(hook.result.current.bands, here).length;

	it("updates Back's count when the hub says Needs you changed", async () => {
		const shape: FleetShape = { live: [], needsYou: [failing] };
		const { client, invalidateNeedsYou } = hub({ shape });
		const { hook } = mount(client);
		await settleMicrotasks();
		expect(count(hook)).toBe(1);
		const asking = fleetSession("local:ask", { state: "awaiting", ask_pending: true, updated_at: at(3) });
		shape.needsYou = [failing, asking];
		shape.revision = 2;
		act(() => invalidateNeedsYou(1));
		await settleMicrotasks();
		expect(count(hook)).toBe(2);
		hook.unmount();
	});

	it("stops reading once another screen is in front", async () => {
		const shape: FleetShape = { live: [], needsYou: [failing] };
		const { client, methods, invalidateNeedsYou } = hub({ shape });
		const { hook, view } = mount(client);
		await settleMicrotasks();
		view.inFront = false;
		hook.rerender();
		const before = methods.length;
		shape.needsYou = [];
		shape.revision = 2;
		act(() => invalidateNeedsYou(1));
		await settleMicrotasks();
		expect(methods.length).toBe(before);
		expect(count(hook)).toBe(1);
		hook.unmount();
	});

	it("lets go of the hub when the Session goes away", async () => {
		vi.useFakeTimers();
		try {
			const { client, methods, invalidateNeedsYou, listening } = hub();
			const { hook } = mount(client);
			await settleMicrotasks();
			expect(listening()).toBeGreaterThan(0);
			hook.unmount();
			expect(listening()).toBe(0);
			// No plugin poll or retry is left behind.
			expect(vi.getTimerCount()).toBe(0);
			const before = methods.length;
			invalidateNeedsYou(1);
			await vi.advanceTimersByTimeAsync(60_000);
			expect(methods.length).toBe(before);
		} finally {
			vi.useRealTimers();
		}
	});

	it("leaves a read that failed alone while another screen is in front", async () => {
		vi.useFakeTimers();
		try {
			const { client, subscriptions } = hub({ failOnce: ["live", "needs_you"] });
			const { hook, view } = mount(client);
			await settleMicrotasks();
			const bound = subscriptions();
			view.inFront = false;
			hook.rerender();
			await act(async () => {
				await vi.advanceTimersByTimeAsync(60_000);
			});
			expect(subscriptions()).toBe(bound);
			hook.unmount();
		} finally {
			vi.useRealTimers();
		}
	});
});
