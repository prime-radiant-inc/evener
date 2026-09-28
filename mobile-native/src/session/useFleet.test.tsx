// The Session's own read of the fleet (ruling 33), against the real Board
// controller and the real seen markers.
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { seenMarkers } from "../board/nativeBoardMemory";
import { renderHook } from "../renderNative.testkit";
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

/** A hub that answers from `fleet`, failing the first read of each
 * section `failOnce` names. */
function hub({ failOnce = [] as string[] } = {}) {
	const methods: string[] = [];
	const toFail = new Set(failOnce);
	const client: ConversationClientLike = {
		request: (method, params) => {
			methods.push(method);
			const section = (params as { section?: string }).section;
			if (section && toFail.delete(section)) return Promise.reject(new Error("request timed out"));
			return Promise.resolve(answerFleetRead(fleet, method, params) as never);
		},
		onNotification: () => () => {},
	};
	return { client, methods };
}

async function settle() {
	for (let round = 0; round < 10; round += 1) await Promise.resolve();
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
	await act(settle);
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
	await act(settle);
	expect(methods).toEqual([]);
	view.inFront = true;
	hook.rerender();
	await act(settle);
	expect(methods).toContain("evener/navigation/read");
	hook.unmount();
});

it("retries a failed read on its own, so Back's count appears", async () => {
	vi.useFakeTimers();
	try {
		const { client } = hub({ failOnce: ["live", "needs_you"] });
		const { hook } = mount(client);
		await act(settle);
		expect(othersNeedingYou(hook.result.current.bands, "local:here")).toEqual([]);
		// The Board's backoff: the first retry waits a second.
		await act(async () => {
			await vi.advanceTimersByTimeAsync(1000);
		});
		await act(settle);
		expect(othersNeedingYou(hook.result.current.bands, "local:here").map((row) => row.ref)).toEqual(["local:fail"]);
		hook.unmount();
	} finally {
		vi.useRealTimers();
	}
});
