// A coordinator's state, read by a screen above one of its subagents without
// taking the connection's subscription: once per connection, and never from a
// connection the screen has left.
import type { ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { renderHook } from "../renderNative.testkit";
import { useCoordinatorState } from "./useCoordinatorState";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
// SessionLink's module also holds the durable outbox, which opens sqlite.
vi.mock("expo-sqlite", () => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "test-uuid", getRandomValues: (array: Uint8Array) => array }));

const read = (status: string): ThreadReadResponse =>
	({
		thread: {
			id: "coord",
			status: { type: status },
			modelProvider: "glm",
			evener: {
				ref: "local:coord",
				instanceId: "instance-coord",
				capabilities: { send: true },
				queue: { revision: 1 },
			},
		},
	}) as ThreadReadResponse;

it("reads the coordinator without following it, and says when it can't", async () => {
	const client = new FakeClient("ready");
	client.on("thread/read", () => read("active"));
	const hook = renderHook(() => useCoordinatorState(client, "local:coord"));
	await act(async () => {});
	expect(hook.result.current).toMatchObject({ status: "active", instanceId: "instance-coord" });
	expect(client.calls[0]?.params).toEqual({ ref: "local:coord", includeTurns: false });

	const failing = new FakeClient("ready");
	failing.on("thread/read", () => Promise.reject(new Error("offline")));
	const other = renderHook(() => useCoordinatorState(failing, "local:coord"));
	await act(async () => {});
	expect(other.result.current).toBe("unreadable");
});

it("forgets a read from a connection it left, even one that answers late", async () => {
	let answer: (value: ThreadReadResponse) => void = () => {};
	const old = new FakeClient("ready");
	old.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (answer = resolve)));
	let client: FakeClient | null = old;
	const hook = renderHook(() => useCoordinatorState(client, "local:coord"));
	// The fake client takes the request on a microtask.
	await act(async () => {});
	client = null;
	hook.rerender();
	await act(async () => answer(read("active")));
	expect(hook.result.current).toBeNull();
});
