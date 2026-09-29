import type { AnyNotification, AuthStatusResponse } from "@evener/appwire-client";
import { act } from "react-test-renderer";
import { afterEach, expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { renderHook } from "../renderNative.testkit";
import { AUTH_RETRY_MAX_MS, AUTH_RETRY_MS, useAuthStatuses } from "./useAuthStatuses";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

const settle = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

/** A hub that answers evener/auth/list from `answers`, one per read (the last
 * repeats), and hands the test its notification feed. */
function hub(answers: (AuthStatusResponse[] | Error)[]) {
	const methods: string[] = [];
	const handlers = new Set<(n: AnyNotification) => void>();
	let reads = 0;
	const client = {
		request: async (method: string) => {
			methods.push(method);
			const answer = answers[Math.min(reads, answers.length - 1)];
			reads += 1;
			if (answer instanceof Error) throw answer;
			return { providers: answer };
		},
		onNotification: (handler: (n: AnyNotification) => void) => {
			handlers.add(handler);
			return () => {
				handlers.delete(handler);
			};
		},
	} as unknown as ConversationClientLike;
	return {
		client,
		methods,
		handlers,
		notify: (method: string) => {
			for (const handler of handlers) handler({ method, params: {} } as AnyNotification);
		},
	};
}

const expired = { provider: "codex-jesse-fsck.com", needsLogin: true } as AuthStatusResponse;
const renewed = { provider: "codex-jesse-fsck.com", needsLogin: false } as AuthStatusResponse;

it("reads the hub's sign-in statuses on a client, by provider", async () => {
	const fake = hub([[expired]]);
	const { result } = renderHook(() => useAuthStatuses(fake.client));
	await act(settle);
	expect(fake.methods).toEqual(["evener/auth/list"]);
	expect(result.current?.get("codex-jesse-fsck.com")?.needsLogin).toBe(true);
});

it("reads again on evener/auth/updated", async () => {
	const fake = hub([[expired], [renewed]]);
	const { result } = renderHook(() => useAuthStatuses(fake.client));
	await act(settle);
	fake.notify("evener/instance/updated");
	await act(settle);
	expect(fake.methods).toEqual(["evener/auth/list"]);
	fake.notify("evener/auth/updated");
	await act(settle);
	expect(fake.methods).toEqual(["evener/auth/list", "evener/auth/list"]);
	expect(result.current?.get("codex-jesse-fsck.com")?.needsLogin).toBe(false);
});

it("keeps the last statuses through a failed read", async () => {
	const fake = hub([[expired], new Error("the hub is restarting")]);
	const { result } = renderHook(() => useAuthStatuses(fake.client));
	await act(settle);
	fake.notify("evener/auth/updated");
	await act(settle);
	expect(fake.methods).toHaveLength(2);
	expect(result.current?.get("codex-jesse-fsck.com")?.needsLogin).toBe(true);
});

it("reads again on its own after a failed read, until one lands", async () => {
	// A failed first read would otherwise leave the words to the instance rows
	// alone, which call an expired sign-in "Signed in" (RoboRev, #3040).
	vi.useFakeTimers();
	const fake = hub([new Error("the hub is busy"), new Error("still busy"), [expired]]);
	const { result } = renderHook(() => useAuthStatuses(fake.client));
	await act(async () => {
		await vi.advanceTimersByTimeAsync(0);
	});
	expect(fake.methods).toHaveLength(1);
	await act(async () => {
		await vi.advanceTimersByTimeAsync(AUTH_RETRY_MS);
	});
	expect(fake.methods).toHaveLength(2);
	// Each wait doubles, so a hub that keeps refusing isn't asked every 5s.
	await act(async () => {
		await vi.advanceTimersByTimeAsync(AUTH_RETRY_MS);
	});
	expect(fake.methods).toHaveLength(2);
	await act(async () => {
		await vi.advanceTimersByTimeAsync(AUTH_RETRY_MS);
	});
	expect(fake.methods).toHaveLength(3);
	expect(result.current?.get("codex-jesse-fsck.com")?.needsLogin).toBe(true);
	// Once a read lands, nothing more is scheduled.
	await act(async () => {
		await vi.advanceTimersByTimeAsync(AUTH_RETRY_MAX_MS * 3);
	});
	expect(fake.methods).toHaveLength(3);
});
afterEach(() => {
	vi.useRealTimers();
});

it("waits no longer than a minute between tries", async () => {
	vi.useFakeTimers();
	const fake = hub([new Error("refused")]);
	const { unmount } = renderHook(() => useAuthStatuses(fake.client));
	await act(async () => {
		await vi.advanceTimersByTimeAsync(0);
	});
	// 5, 10, 20, 40, then 60 seconds each.
	for (const wait of [5, 10, 20, 40, 60, 60]) {
		const before = fake.methods.length;
		await act(async () => {
			await vi.advanceTimersByTimeAsync(wait * 1000);
		});
		expect(fake.methods).toHaveLength(before + 1);
	}
	expect(AUTH_RETRY_MAX_MS).toBe(60_000);
	unmount();
});

it("reads nothing without a client, and knows nothing yet", async () => {
	const { result } = renderHook(() => useAuthStatuses(null));
	await act(settle);
	expect(result.current).toBeNull();
});

it("knows nothing until a first read lands", async () => {
	const fake = hub([new Error("the hub is busy")]);
	const { result, unmount } = renderHook(() => useAuthStatuses(fake.client));
	await act(settle);
	expect(result.current).toBeNull();
	unmount();
});

it("stops listening when it unmounts", async () => {
	const fake = hub([[expired]]);
	const { unmount } = renderHook(() => useAuthStatuses(fake.client));
	await act(settle);
	unmount();
	expect(fake.handlers.size).toBe(0);
});
