import { type Thread } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { createElement, Suspense } from "react";
import { act } from "react-test-renderer";
import { afterEach, expect, it, vi } from "vitest";
import { createConversationService } from "../../../mobile/src/services/conversation";
import { createActivityStore } from "../../../mobile/src/state/activity";
import { createConversationStore } from "../../../mobile/src/state/conversation";
import { render, renderHook } from "../renderNative.testkit";
import { useOlderHistory } from "./useOlderHistory";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
afterEach(() => vi.useRealTimers());

function historyReader() {
	const client = new FakeClient("ready");
	const thread: Thread = {
		id: "thread-history",
		sessionId: "session-history",
		preview: "history",
		ephemeral: false,
		modelProvider: "test",
		createdAt: 0,
		updatedAt: 0,
		status: { type: "idle" },
		cwd: "",
		cliVersion: "test",
		source: "local",
		turns: [],
		evener: {
			ref: "history",
			instanceId: "instance-history",
			queue: { revision: 0 },
			capabilities: {
				send: false,
				steer: false,
				interrupt: false,
				compact: false,
				clear: false,
				forkFromTurn: false,
				shutdown: false,
				changeModel: false,
				changeVisionModel: false,
				sharedNotes: false,
				queue: false,
				goal: false,
				rename: false,
			},
		},
	};
	client.on("thread/read", () => ({ thread, olderCursor: "page" }));
	client.on("thread/turns/list", () => ({ data: [], nextCursor: undefined }));
	const service = createConversationService(client);
	const store = createConversationStore();
	const sink = createActivityStore().getState();
	const input = {
		store,
		service,
		active: false,
		resetKey: "hub\u0000history",
		binding: undefined as string | undefined,
	};
	return { client, store, service, sink, input };
}

function mountReader(input: Parameters<typeof useOlderHistory>[0]) {
	const result: { current: ReturnType<typeof useOlderHistory> | undefined } = { current: undefined };
	let suspend = false;
	const never = new Promise<never>(() => {});
	function Reader() {
		const history = useOlderHistory(input);
		if (suspend) throw never;
		result.current = history;
		return null;
	}
	const view = () => createElement(Suspense, { fallback: null }, createElement(Reader));
	const tree = render(view());
	return {
		result,
		rerender(nextSuspend = false) {
			suspend = nextSuspend;
			act(() => tree.update(view()));
		},
		unmount: () => act(() => tree.unmount()),
	};
}

it("retains a requested page through the reader's first binding hydration", async () => {
	const { client, store, service, sink, input } = historyReader();
	const hook = renderHook(() => useOlderHistory(input));
	act(() => hook.result.current.loadOlder());
	expect(hook.result.current.state.pending).toBe(true);
	await act(async () => {
		await store.getState().openProjected(service, sink, "history");
	});
	expect(store.getState().error).toBeNull();
	expect(store.getState().status).toBe("open");
	expect(store.getState().olderCursor).toBe("page");
	vi.useFakeTimers();
	input.binding = store.getState().conversation?.instanceId;
	input.active = true;
	hook.rerender();
	await act(async () => {
		await vi.advanceTimersByTimeAsync(0);
	});
	expect(client.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(1);
	expect(hook.result.current.state.pending).toBe(false);
	hook.unmount();
});

it("an abandoned replacement-binding render cannot change the committed reader's retained demand", async () => {
	const { client, store, service, sink, input } = historyReader();
	await store.getState().openProjected(service, sink, "history");
	expect(store.getState().error).toBeNull();
	expect(store.getState().status).toBe("open");
	expect(store.getState().olderCursor).toBe("page");
	input.binding = store.getState().conversation?.instanceId;
	input.active = true;
	let attempts = 0;
	client.on("thread/turns/list", () => {
		attempts += 1;
		if (attempts === 1) throw new Error("temporary");
		return { data: [], nextCursor: undefined };
	});
	const reader = mountReader(input);
	vi.useFakeTimers();
	act(() => reader.result.current?.loadOlder());
	await act(async () => {
		await vi.advanceTimersByTimeAsync(0);
	});
	expect(attempts).toBe(1);
	input.binding = "uncommitted-instance";
	reader.rerender(true);
	input.binding = undefined;
	input.active = false;
	reader.rerender();
	input.binding = store.getState().conversation?.instanceId;
	input.active = true;
	reader.rerender();
	await act(async () => {
		await vi.advanceTimersByTimeAsync(1000);
	});
	expect(attempts).toBe(2);
	expect(reader.result.current?.state.pending).toBe(false);
	reader.unmount();
});

it("an abandoned service render cannot redirect the committed reader's retry", async () => {
	const { client, store, service, sink, input } = historyReader();
	await store.getState().openProjected(service, sink, "history");
	expect(store.getState().error).toBeNull();
	expect(store.getState().olderCursor).toBe("page");
	input.binding = store.getState().conversation?.instanceId;
	input.active = true;
	let attempts = 0;
	client.on("thread/turns/list", () => {
		attempts += 1;
		if (attempts === 1) throw new Error("temporary");
		return { data: [], nextCursor: undefined };
	});
	const reader = mountReader(input);
	vi.useFakeTimers();
	act(() => reader.result.current?.loadOlder());
	await act(async () => {
		await vi.advanceTimersByTimeAsync(0);
	});
	expect(attempts).toBe(1);
	const replacement = historyReader();
	input.service = replacement.service;
	reader.rerender(true);
	await act(async () => {
		await vi.advanceTimersByTimeAsync(1000);
	});
	expect(attempts).toBe(2);
	expect(replacement.client.calls).toHaveLength(0);
	reader.unmount();
});
