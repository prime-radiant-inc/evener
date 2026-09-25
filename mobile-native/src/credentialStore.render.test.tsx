// useCredentialStore's wiring in a mounted tree. React runs a parent's layout
// effects before any child's passive effects, which is what lets the store be
// bound before a child reads through it: the ordering test asserts that
// directly, with a child whose mount effect reads exactly as Providers' does,
// and a store bound from a passive effect fails that read.
import { useEffect } from "react";
import { act } from "react-test-renderer";
import { afterEach, expect, it, vi } from "vitest";
import type { InstanceListResponse } from "@evener/appwire-client";
import type {
	CredentialInstancesClient,
	CredentialInstancesStore,
} from "@evener/appwire-client/state/credentials";
import { useCredentialStore } from "./credentialStore";
import { recordClientReadyHub } from "./connectionIdentity";
import { render, renderHook, scriptedClient } from "./renderNative.testkit";

const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown> }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "fixture-uuid" }));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => harness.connection }));

afterEach(() => {
	vi.restoreAllMocks();
});

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

it("rebinds to a replacement client without closing the store in between", async () => {
	const first = scriptedClient(rows);
	const second = scriptedClient(rows);
	harness.connection = { client: first.client, state: "ready" };
	const hook = renderHook(() => useCredentialStore());
	const store = hook.result.current;
	// Every connection the swap hands the store is recorded. Binding the
	// replacement is the only transition allowed: a null (closed) transition of
	// the old connection is what a single binding effect - one whose cleanup
	// closes on every dependency change - emits first, and it is what cancels
	// reads in flight.
	const bindings: (CredentialInstancesClient | null)[] = [];
	const bind = store.connectionChanged;
	vi.spyOn(store, "connectionChanged").mockImplementation((client, state) => {
		bindings.push(client);
		bind(client, state);
	});
	harness.connection = { client: second.client, state: "ready" };
	hook.rerender();
	expect(bindings).toEqual([second.client]);
	expect(first.unsubscribes()).toBe(1);
	await act(async () => {
		expect(await store.getState().fetch()).toBe(true);
	});
	expect(second.methods).toEqual(["evener/instance/list"]);
});

it("refuses a previous hub's adopted client after a re-key, then binds the re-point", async () => {
	const stale = scriptedClient(rows);
	// The re-key's stale client: it proved ready under hub-1, and the
	// connection still reports it as ready after the store's selection has
	// already re-keyed the route to hub-2 (connectionIdentity's record is
	// the one memory that survives the remount).
	recordClientReadyHub(stale.client, "hub-1");
	harness.connection = {
		activeProfile: { id: "hub-2", name: "New hub" },
		client: stale.client,
		state: "ready",
	};
	const hook = renderHook(() => useCredentialStore());
	// The store must not bind the previous hub's client: its listing read
	// would fetch hub-1's provider rows under hub-2.
	await expect(
		hook.result.current.getState().fetch(),
	).rejects.toThrow(/no client connected/);
	expect(stale.methods).toEqual([]);

	// The connection re-points to hub-2's own client - a new object the
	// record has never seen - and the store binds it exactly as before.
	const replacement = scriptedClient(rows);
	harness.connection = {
		activeProfile: { id: "hub-2", name: "New hub" },
		client: replacement.client,
		state: "ready",
	};
	hook.rerender();
	await act(async () => {
		expect(await hook.result.current.getState().fetch()).toBe(true);
	});
	expect(replacement.methods).toEqual(["evener/instance/list"]);
});
