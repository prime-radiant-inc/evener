import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { renderHook } from "../renderNative.testkit";
import { hostRow, scriptedFleet } from "./hostsTestUtils";
import { useHubFleet, useOptionalSnapshot } from "./useHubFleet";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

function client() {
	const unwatched = { count: 0 };
	const watched = { count: 0 };
	return {
		unwatched,
		watched,
		request: vi.fn(async () => ({ hosts: [] })),
		onNotification: () => {
			watched.count += 1;
			return () => {
				unwatched.count += 1;
			};
		},
	};
}

it("watches the hub's notifications only once the fleet is committed, never during render", () => {
	const only = client();
	const seenInRender: number[] = [];
	const hook = renderHook(() => {
		const fleet = useHubFleet(only as never);
		// A render React may throw away must not have subscribed anything.
		seenInRender.push(only.watched.count);
		return fleet;
	});
	expect(seenInRender[0]).toBe(0);
	expect(only.watched.count).toBe(1);
	hook.unmount();
	expect(only.unwatched.count).toBe(1);
});

it("has nothing to read through before there is a client", () => {
	const hook = renderHook(() => useHubFleet(null));
	expect(hook.result.current).toEqual({ hosts: null, live: null });
	hook.unmount();
});

it("builds the hosts and live sessions once per client, and lets go of a replaced client's", () => {
	const first = client();
	const second = client();
	const current = { client: first as never };
	const hook = renderHook(() => useHubFleet(current.client));
	const firstFleet = hook.result.current;
	hook.rerender();
	expect(hook.result.current).toBe(firstFleet);
	current.client = second as never;
	hook.rerender();
	expect(hook.result.current.hosts).not.toBe(firstFleet.hosts);
	// The replaced reader stopped watching its client's notifications.
	expect(first.unwatched.count).toBe(1);
	hook.unmount();
	expect(second.unwatched.count).toBe(1);
});

it("reads a store's snapshot and follows it, and reads null while there is no store", () => {
	const listeners = new Set<() => void>();
	const store = {
		value: 1,
		subscribe: (listener: () => void) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		getSnapshot: () => store.value,
	};
	const current = { store: null as typeof store | null };
	const hook = renderHook(() => useOptionalSnapshot(current.store));
	expect(hook.result.current).toBeNull();
	current.store = store;
	hook.rerender();
	expect(hook.result.current).toBe(1);
	act(() => {
		store.value = 2;
		for (const listener of listeners) listener();
	});
	expect(hook.result.current).toBe(2);
	hook.unmount();
	expect(listeners.size).toBe(0);
});

it("edits hosts with the mutation ids the sheet hands it", async () => {
	const fleet = scriptedFleet([hostRow("attic")]);
	const hook = renderHook(() => useHubFleet(fleet.client, fleet.newMutationId));
	const hosts = hook.result.current.hosts;
	await hosts?.read();
	await hosts?.update("attic", { address: "attic.lan" }, { generation: 1, incarnationId: "attic-1" });
	expect(fleet.calls.find((call) => call.method === "evener/host/update")?.params).toMatchObject({
		mutationId: "mutation-1",
	});
	hook.unmount();
});
