// The hook half of the D9 regression test: useCredentialStore's two effects are
// the fix, and this drives them in a mounted tree. The ordering property the
// issue names - a parent's layout effect runs before any child's passive effect
// - is asserted directly, with a child component whose mount effect reads
// through the store exactly as Providers' does; binding in a passive effect
// (the pre-fix wiring) fails that read.
import { useEffect } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { AnyNotification, InstanceListResponse } from "@evener/appwire-client";
import type { CredentialInstancesStore } from "@evener/appwire-client/state/credentials";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { useCredentialStore } from "./credentialStore";
import { render, renderHook } from "./renderNative.testkit";

const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown> }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "fixture-uuid" }));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => harness.connection }));

const rows: InstanceListResponse = { instances: [], availableProviders: [] };

function scriptedClient(methods: string[], onUnsubscribe: () => void = () => {}) {
	return {
		request: async (method: string) => {
			methods.push(method);
			return rows;
		},
		onNotification: (_handler: (n: AnyNotification) => void) => onUnsubscribe,
	} as ConversationClientLike;
}

it("binds the store before a child's mount effect reads through it", async () => {
	const methods: string[] = [];
	harness.connection = { client: scriptedClient(methods), state: "ready" };
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
	expect(methods).toEqual(["evener/instance/list"]);
});

it("closes the store on unmount only, releasing the connection's listener", async () => {
	const methods: string[] = [];
	let unsubscribes = 0;
	harness.connection = {
		client: scriptedClient(methods, () => {
			unsubscribes += 1;
		}),
		state: "ready",
	};
	const hook = renderHook(() => useCredentialStore());
	expect(unsubscribes).toBe(0);
	hook.unmount();
	expect(unsubscribes).toBe(1);
	await expect(hook.result.current.getState().fetch()).rejects.toThrow(/no client connected/);
});

it("moves to a replacement client after mount without closing the store", async () => {
	const firstMethods: string[] = [];
	let firstUnsubscribes = 0;
	const secondMethods: string[] = [];
	harness.connection = {
		client: scriptedClient(firstMethods, () => {
			firstUnsubscribes += 1;
		}),
		state: "ready",
	};
	const hook = renderHook(() => useCredentialStore());
	const store = hook.result.current;
	harness.connection = { client: scriptedClient(secondMethods), state: "ready" };
	hook.rerender();
	expect(firstUnsubscribes).toBe(1);
	await act(async () => {
		expect(await store.getState().fetch()).toBe(true);
	});
	expect(secondMethods).toEqual(["evener/instance/list"]);
});
