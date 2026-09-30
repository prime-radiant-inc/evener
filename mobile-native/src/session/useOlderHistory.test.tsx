import { type Thread } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { createElement, Suspense, useState } from "react";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { createConversationService } from "../../../mobile/src/services/conversation";
import { createActivityStore } from "../../../mobile/src/state/activity";
import { createConversationStore } from "../../../mobile/src/state/conversation";
import { render, renderHook } from "../renderNative.testkit";
import { type FindState, newFind } from "./findInSession";
import { forgetHistoryForHub, historyForSession } from "./historyMemory";
import { useOlderHistory } from "./useOlderHistory";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
beforeEach(() => forgetHistoryForHub("hub"));
afterEach(() => {
	forgetHistoryForHub("hub");
	vi.useRealTimers();
});

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
		service: service as typeof service | null,
		active: false,
		hubId: "hub",
		ref: "history",
		binding: undefined as string | undefined,
	};
	return { thread, client, store, service, sink, input };
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

async function openedReader() {
	const reader = historyReader();
	await reader.store.getState().openProjected(reader.service, reader.sink, "history");
	reader.input.binding = reader.store.getState().conversation?.instanceId;
	reader.input.active = true;
	return reader;
}

it("reopens pending browse with a fresh store and service, then reclaims the settled session", async () => {
	const first = await openedReader();
	first.client.on("thread/turns/list", () => {
		throw new Error("temporary");
	});
	vi.useFakeTimers();
	const hook = renderHook(() => useOlderHistory(first.input));
	act(() => hook.result.current.loadOlder());
	await act(async () => {
		await vi.advanceTimersByTimeAsync(0);
	});
	hook.unmount();
	const held = historyForSession("hub", "history");
	expect(held?.paging.getSnapshot().pending).toBe(true);
	expect(held?.store).toBeNull();
	expect(held?.service).toBeNull();
	expect(vi.getTimerCount()).toBe(0);
	await act(async () => {
		await vi.advanceTimersByTimeAsync(60_000);
	});
	expect(first.client.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(1);
	const returned = await openedReader();
	const reopened = renderHook(() => useOlderHistory(returned.input));
	await act(async () => {
		await vi.advanceTimersByTimeAsync(1000);
	});
	expect(returned.client.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(1);
	expect(reopened.result.current.state.pending).toBe(false);
	reopened.unmount();
	expect(historyForSession("hub", "history")).toBeUndefined();
});

it.each(["cancel", "remove hub", "new binding"])("retires disposed demand after %s", async (stop) => {
	const first = await openedReader();
	first.client.on("thread/turns/list", () => {
		throw new Error("temporary");
	});
	vi.useFakeTimers();
	const hook = renderHook(() => useOlderHistory(first.input));
	act(() => hook.result.current.loadOlder());
	await act(async () => {
		await vi.advanceTimersByTimeAsync(0);
	});
	if (stop === "cancel") act(() => hook.result.current.cancelReader());
	hook.unmount();
	if (stop === "remove hub") forgetHistoryForHub("hub");
	if (stop !== "new binding") expect(historyForSession("hub", "history")).toBeUndefined();
	const returned = await openedReader();
	if (stop === "new binding") returned.input.binding = "confirmed-new-instance";
	const reopened = renderHook(() => useOlderHistory(returned.input));
	await act(async () => {
		await vi.advanceTimersByTimeAsync(60_000);
	});
	expect(returned.client.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(0);
	expect(reopened.result.current.state.pending).toBe(false);
	reopened.unmount();
	expect(historyForSession("hub", "history")).toBeUndefined();
	expect(vi.getTimerCount()).toBe(0);
});

it("an abandoned store render cannot redirect retained demand to its uncommitted store", async () => {
	const first = await openedReader();
	let attempts = 0;
	first.client.on("thread/turns/list", () => {
		if (++attempts === 1) throw new Error("temporary");
		return { data: [], nextCursor: undefined };
	});
	vi.useFakeTimers();
	const reader = mountReader(first.input);
	act(() => reader.result.current?.loadOlder());
	await act(async () => {
		await vi.advanceTimersByTimeAsync(0);
	});
	const uncommitted = await openedReader();
	first.input.store = uncommitted.store;
	first.input.service = uncommitted.service;
	reader.rerender(true);
	await act(async () => {
		await vi.advanceTimersByTimeAsync(1000);
	});
	expect(attempts).toBe(2);
	expect(uncommitted.client.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(0);
	reader.unmount();
	expect(historyForSession("hub", "history")).toBeUndefined();
});

it("keeps a late failing page dormant after disposal and resumes it against the reopened store", async () => {
	const first = await openedReader();
	let rejectPage: ((error: Error) => void) | undefined;
	first.client.on(
		"thread/turns/list",
		() =>
			new Promise((_, reject) => {
				rejectPage = reject;
			}),
	);
	vi.useFakeTimers();
	const hook = renderHook(() => useOlderHistory(first.input));
	act(() => hook.result.current.loadOlder());
	await act(async () => {
		await vi.advanceTimersByTimeAsync(0);
	});
	hook.unmount();
	await act(async () => {
		rejectPage?.(new Error("late temporary"));
	});
	const held = historyForSession("hub", "history");
	expect(held?.paging.getSnapshot()).toMatchObject({ pending: true, loading: false });
	expect(held?.store).toBeNull();
	expect(held?.service).toBeNull();
	expect(vi.getTimerCount()).toBe(0);
	const returned = await openedReader();
	const reopened = renderHook(() => useOlderHistory(returned.input));
	await act(async () => {
		await vi.advanceTimersByTimeAsync(1000);
	});
	expect(returned.client.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(1);
	reopened.unmount();
	expect(historyForSession("hub", "history")).toBeUndefined();
});

it.each(["failed", "quiet", "deferred success"])(
	"restores browse page B after %s on a disposed store and fresh page A hydration",
	async (outcome) => {
		const first = await openedReader();
		let finishPage: ((value: { data: []; nextCursor?: string }) => void) | undefined;
		first.client.on("thread/turns/list", ({ cursor }) => {
			if (cursor === "page") return { data: [], nextCursor: "page-B" };
			if (outcome === "failed") throw new Error("temporary page B");
			if (outcome === "quiet") return { data: [], nextCursor: "page-B" };
			return new Promise((resolve) => {
				finishPage = resolve;
			});
		});
		vi.useFakeTimers();
		const hook = renderHook(() => useOlderHistory(first.input));
		act(() => hook.result.current.loadOlder());
		await act(async () => {
			await vi.advanceTimersByTimeAsync(0);
		});
		expect(first.store.getState().olderCursor).toBe("page-B");
		act(() => hook.result.current.loadOlder());
		await act(async () => {
			await vi.advanceTimersByTimeAsync(0);
		});
		hook.unmount();
		first.store.getState().close();
		const returned = await openedReader();
		const cursors: unknown[] = [];
		returned.client.on("thread/turns/list", ({ cursor }) => {
			cursors.push(cursor);
			return { data: [], nextCursor: cursor === "page" ? "page-B" : undefined };
		});
		const reopened = renderHook(() => useOlderHistory(returned.input));
		try {
			if (outcome === "deferred success")
				await act(async () => {
					finishPage?.({ data: [] });
				});
			await act(async () => {
				await vi.advanceTimersByTimeAsync(60_000);
			});
			expect(cursors).toEqual(["page", "page-B"]);
			expect(returned.store.getState().olderCursor).toBeNull();
			expect(reopened.result.current.state.pending).toBe(false);
		} finally {
			reopened.unmount();
		}
	},
);

it("keeps simultaneously mounted same-session readers' routing and Find cancellation independent", async () => {
	const first = await openedReader();
	first.client.on("thread/turns/list", () => {
		throw new Error("first view temporary");
	});
	vi.useFakeTimers();
	const firstHook = renderHook(() => useOlderHistory(first.input));
	act(() => firstHook.result.current.findOlder());
	await act(async () => {
		await vi.advanceTimersByTimeAsync(0);
	});
	first.input.active = false;
	firstHook.rerender();
	const second = await openedReader();
	second.client.on("thread/turns/list", () => {
		throw new Error("second view temporary");
	});
	const secondHook = renderHook(() => useOlderHistory(second.input));
	try {
		expect(secondHook.result.current.state.pending).toBe(false);
		act(() => secondHook.result.current.findOlder());
		await act(async () => {
			await vi.advanceTimersByTimeAsync(0);
		});
		expect(second.client.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(1);
		act(() => firstHook.result.current.cancelFind());
		expect(secondHook.result.current.state.pending).toBe(true);
		secondHook.unmount();
		first.input.active = true;
		firstHook.rerender();
		await act(async () => {
			await vi.advanceTimersByTimeAsync(60_000);
		});
		expect(first.client.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(1);
	} finally {
		firstHook.unmount();
		secondHook.unmount();
	}
});

it("claims detached intent only once when two same-session screens render together", async () => {
	const departed = await openedReader();
	departed.client.on("thread/turns/list", () => {
		throw new Error("temporary");
	});
	vi.useFakeTimers();
	const pending = renderHook(() => useOlderHistory({ ...departed.input, find: newFind("saved query") }));
	act(() => pending.result.current.findOlder());
	await act(async () => {
		await vi.advanceTimersByTimeAsync(0);
	});
	pending.unmount();
	const first = await openedReader();
	first.input.active = false;
	const second = await openedReader();
	second.client.on("thread/turns/list", () => {
		throw new Error("second view temporary");
	});
	const results: {
		history: ReturnType<typeof useOlderHistory>;
		find: FindState | null;
		setFind(find: FindState | null): void;
	}[] = [];
	function Reader({ input, index }: { input: Parameters<typeof useOlderHistory>[0]; index: number }) {
		const [find, setFind] = useState(() => historyForSession("hub", "history")?.find ?? null);
		const history = useOlderHistory({ ...input, find, setFind });
		results[index] = { history, find, setFind };
		return null;
	}
	const tree = render(
		createElement(
			"Readers",
			null,
			createElement(Reader, { input: first.input, index: 0 }),
			createElement(Reader, { input: second.input, index: 1 }),
		),
	);
	try {
		const one = results[0];
		const two = results[1];
		if (!one || !two) throw new Error("missing reader");
		expect(one.find?.query).toBe("saved query");
		expect(one.history.state.pending).toBe(true);
		expect(two.find).toBeNull();
		expect(two.history.state.pending).toBe(false);
		act(() => {
			two.setFind(newFind("second query"));
			two.history.findOlder();
		});
		await act(async () => {
			await vi.advanceTimersByTimeAsync(0);
		});
		act(() => {
			one.setFind(null);
			one.history.cancelFind();
			one.history.cancelReader();
		});
		expect(results[1]?.find?.query).toBe("second query");
		expect(results[1]?.history.state.pending).toBe(true);
		expect(second.client.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(1);
	} finally {
		act(() => tree.unmount());
	}
});

it("forgets the saved page boundary when a reopened reader authoritatively has no older history", async () => {
	const first = await openedReader();
	first.client.on("thread/turns/list", () => {
		throw new Error("temporary");
	});
	vi.useFakeTimers();
	const hook = renderHook(() => useOlderHistory(first.input));
	act(() => hook.result.current.loadOlder());
	await act(async () => {
		await vi.advanceTimersByTimeAsync(0);
	});
	hook.unmount();
	const held = historyForSession("hub", "history");
	expect(held?.requestedPage).not.toBeNull();
	const returned = historyReader();
	returned.client.on("thread/read", () => ({ thread: returned.thread }));
	await returned.store.getState().openProjected(returned.service, returned.sink, "history");
	returned.input.binding = returned.store.getState().conversation?.instanceId;
	returned.input.active = true;
	const reopened = renderHook(() => useOlderHistory(returned.input));
	await act(async () => {
		await vi.advanceTimersByTimeAsync(1000);
	});
	expect(held?.requestedPage).toBeNull();
	expect(reopened.result.current.state.pending).toBe(false);
	expect(returned.client.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(0);
	reopened.unmount();
	expect(historyForSession("hub", "history")).toBeUndefined();
});

it("a committed new session binding retires every old same-session reader's demand", async () => {
	const first = await openedReader();
	first.client.on("thread/turns/list", () => {
		throw new Error("old binding temporary");
	});
	vi.useFakeTimers();
	const clearFind = vi.fn();
	const oldMounted = renderHook(() =>
		useOlderHistory({ ...first.input, find: newFind("old query"), setFind: clearFind }),
	);
	act(() => oldMounted.result.current.findOlder());
	await act(async () => {
		await vi.advanceTimersByTimeAsync(0);
	});
	first.input.active = false;
	oldMounted.rerender();
	const second = await openedReader();
	second.input.active = false;
	const oldDetached = renderHook(() => useOlderHistory(second.input));
	act(() => oldDetached.result.current.loadOlder());
	oldDetached.unmount();
	const fresh = historyReader();
	fresh.thread.evener.instanceId = "new-authoritative-instance";
	await fresh.store.getState().openProjected(fresh.service, fresh.sink, "history");
	fresh.input.binding = fresh.store.getState().conversation?.instanceId;
	fresh.input.active = true;
	const current = renderHook(() => useOlderHistory(fresh.input));
	try {
		expect(current.result.current.state.pending).toBe(false);
		expect(oldMounted.result.current.state.pending).toBe(false);
		expect(clearFind).toHaveBeenCalledWith(null);
		expect(oldMounted.result.current.isCurrentReader()).toBe(false);
		first.input.service = second.service;
		oldMounted.rerender();
		expect(current.result.current.isCurrentReader()).toBe(true);
		act(() => current.result.current.loadOlder());
		await act(async () => {
			await vi.advanceTimersByTimeAsync(0);
		});
		expect(fresh.client.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(1);
		first.input.active = true;
		oldMounted.rerender();
		act(() => oldMounted.result.current.findOlder());
		await act(async () => {
			await vi.advanceTimersByTimeAsync(60_000);
		});
		expect(first.client.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(1);
	} finally {
		current.unmount();
		oldMounted.unmount();
	}
	expect(historyForSession("hub", "history")).toBeUndefined();
});

it.each(["loadOlder", "findOlder"] as const)(
	"quietly retains %s demand when its committed service is unavailable",
	async (demand) => {
		const reader = await openedReader();
		const service = reader.input.service;
		reader.input.service = null;
		vi.useFakeTimers();
		const hook = renderHook(() => useOlderHistory(reader.input));
		act(() => hook.result.current[demand]());
		await act(async () => {
			await vi.advanceTimersByTimeAsync(0);
		});
		expect(hook.result.current.state).toMatchObject({ pending: true, loading: false, error: null, permanent: false });
		expect(reader.client.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(0);
		await act(async () => {
			await vi.advanceTimersByTimeAsync(999);
		});
		expect(hook.result.current.state.pending).toBe(true);
		reader.input.service = service;
		hook.rerender();
		await act(async () => {
			await vi.advanceTimersByTimeAsync(1);
		});
		expect(reader.client.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(1);
		expect(hook.result.current.state.pending).toBe(false);
		hook.unmount();
	},
);

it("hub removal clears mounted Find and cannot revive it through service changes or a reader return", async () => {
	const reader = await openedReader();
	const service = reader.input.service;
	reader.client.on("thread/turns/list", () => {
		throw new Error("temporary");
	});
	const clearFind = vi.fn();
	vi.useFakeTimers();
	const hook = renderHook(() =>
		useOlderHistory({ ...reader.input, find: newFind("removed hub query"), setFind: clearFind }),
	);
	act(() => hook.result.current.findOlder());
	await act(async () => {
		await vi.advanceTimersByTimeAsync(0);
	});
	act(() => forgetHistoryForHub("hub"));
	expect(clearFind).toHaveBeenCalledWith(null);
	reader.input.service = null;
	reader.input.active = false;
	hook.rerender();
	reader.input.service = service;
	reader.input.active = true;
	hook.rerender();
	act(() => {
		hook.result.current.findOlder();
		hook.result.current.loadOlder();
	});
	await act(async () => {
		await vi.advanceTimersByTimeAsync(60_000);
	});
	expect(hook.result.current.isCurrentReader()).toBe(false);
	expect(reader.client.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(1);
	hook.unmount();
	expect(historyForSession("hub", "history")).toBeUndefined();
	expect(vi.getTimerCount()).toBe(0);
});
