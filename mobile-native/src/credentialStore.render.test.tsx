// The hook half of the D9 regression test: useCredentialStore's two effects are
// the fix, and this drives them in a mounted tree. The ordering property the
// issue names - a parent's layout effect runs before any child's passive effect
// - is asserted directly, with a child component whose mount effect reads
// through the store exactly as Providers' does; binding in a passive effect
// (the pre-fix wiring) fails that read.
import { useEffect } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { InstanceListResponse } from "@evener/appwire-client";
import type { CredentialInstancesStore } from "@evener/appwire-client/state/credentials";
import { useCredentialStore } from "./credentialStore";
import { render, renderHook, scriptedClient } from "./renderNative.testkit";

const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown> }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "fixture-uuid" }));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => harness.connection }));

const rows: InstanceListResponse = { instances: [], availableProviders: [] };

it("binds the store before a child's mount effect reads through it", async () => {
	const hub = scriptedClient(rows);
	harness.connection = { client: hub.client, state: "ready" };
	const applied: boolean[] = [];
	function Child({ store }: { store: CredentialInstancesStore }) {
		useEffect(() => {
			store.getState().fetch().then(
				(landed) => applied.push(landed),
				() => applied.push(false),
			);
		}, [store]);
		return null;
	}
	function Screen() {
		return <Child store={useCredentialStore()} />;
	}
	render(<Screen />);
	await act(async () => {});
	// true means the read was answered by the client the layout effect bound;
	// a store bound from a passive effect would still be unbound here.
	expect(applied).toEqual([true]);
	expect(hub.methods).toEqual(["evener/instance/list"]);
});

it("closes the store on unmount only, releasing the connection's listener", async () => {
	const hub = scriptedClient(rows);
	harness.connection = { client: hub.client, state: "ready" };
	const hook = renderHook(() => useCredentialStore());
	expect(hub.unsubscribes()).toBe(0);
	hook.unmount();
	expect(hub.unsubscribes()).toBe(1);
	await expect(hook.result.current.getState().fetch()).rejects.toThrow(/no client connected/);
});

it("moves to a replacement client after mount without closing the store", async () => {
	const first = scriptedClient(rows);
	const second = scriptedClient(rows);
	harness.connection = { client: first.client, state: "ready" };
	const hook = renderHook(() => useCredentialStore());
	const store = hook.result.current;
	harness.connection = { client: second.client, state: "ready" };
	hook.rerender();
	expect(first.unsubscribes()).toBe(1);
	await act(async () => {
		expect(await store.getState().fetch()).toBe(true);
	});
	expect(second.methods).toEqual(["evener/instance/list"]);
});
