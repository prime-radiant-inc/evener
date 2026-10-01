import type { AnyNotification, AuthStatusResponse } from "@evener/appwire-client";
import { AUTH_STATUSES_REFETCH_DEBOUNCE_MS } from "@evener/appwire-client/state/credentials";
import { act } from "react-test-renderer";
import { afterEach, expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { renderHook } from "../renderNative.testkit";
import { AUTH_RETRY_MS, useAuthStatuses } from "./useAuthStatuses";

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

/** Runs the shared store's evener/auth/updated debounce out. */
async function afterDebounce() {
	await act(async () => {
		await vi.advanceTimersByTimeAsync(AUTH_STATUSES_REFETCH_DEBOUNCE_MS);
	});
}

/** Lets the shared store's first read go out and land under fake timers. */
async function flushInitial() {
	await act(async () => {
		await vi.advanceTimersByTimeAsync(0);
	});
}

it("reads the hub's sign-in statuses on a client, by provider", async () => {
	const fake = hub([[expired]]);
	const { result } = renderHook(() => useAuthStatuses(fake.client));
	await act(settle);
	expect(fake.methods).toEqual(["evener/auth/list"]);
	expect(result.current?.get("codex-jesse-fsck.com")?.needsLogin).toBe(true);
});

it("reads again on evener/auth/updated", async () => {
	vi.useFakeTimers();
	const fake = hub([[expired], [renewed]]);
	const { result } = renderHook(() => useAuthStatuses(fake.client));
	await flushInitial();
	fake.notify("evener/instance/updated");
	await afterDebounce();
	expect(fake.methods).toEqual(["evener/auth/list"]);
	fake.notify("evener/auth/updated");
	await afterDebounce();
	expect(fake.methods).toEqual(["evener/auth/list", "evener/auth/list"]);
	expect(result.current?.get("codex-jesse-fsck.com")?.needsLogin).toBe(false);
});

// The read is the package's shared createAuthStatusesStore
// (state/credentials/authStatuses.ts), whose evener/auth/updated follow
// coalesces a burst through AUTH_STATUSES_REFETCH_DEBOUNCE_MS rather than
// reading once per frame.
it("coalesces a burst of evener/auth/updated through the shared store's debounce", async () => {
	vi.useFakeTimers();
	const fake = hub([[expired], [renewed], [expired]]);
	const { result } = renderHook(() => useAuthStatuses(fake.client));
	await flushInitial();
	expect(fake.methods).toHaveLength(1);
	fake.notify("evener/auth/updated");
	fake.notify("evener/auth/updated");
	await act(async () => {
		await vi.advanceTimersByTimeAsync(AUTH_STATUSES_REFETCH_DEBOUNCE_MS - 1);
	});
	expect(fake.methods).toHaveLength(1);
	await act(async () => {
		await vi.advanceTimersByTimeAsync(1);
	});
	expect(fake.methods).toHaveLength(2);
	expect(result.current?.get("codex-jesse-fsck.com")?.needsLogin).toBe(false);
});

it("keeps the last statuses through a failed read", async () => {
	vi.useFakeTimers();
	const fake = hub([[expired], new Error("the hub is restarting")]);
	const { result } = renderHook(() => useAuthStatuses(fake.client));
	await flushInitial();
	fake.notify("evener/auth/updated");
	await afterDebounce();
	expect(fake.methods).toHaveLength(2);
	expect(result.current?.get("codex-jesse-fsck.com")?.needsLogin).toBe(true);
});

it("reads again on a later update after the first read failed", async () => {
	vi.useFakeTimers();
	const fake = hub([new Error("the hub is busy"), [expired]]);
	const { result } = renderHook(() => useAuthStatuses(fake.client));
	await flushInitial();
	expect(result.current).toBeNull();
	fake.notify("evener/auth/updated");
	await afterDebounce();
	expect(result.current?.get("codex-jesse-fsck.com")?.needsLogin).toBe(true);
});

// The store's own triggers are a notification or a connection transition, and
// neither follows a read that fails while the socket stays ready. The hook
// retries the failed read itself rather than leaving the statuses null.
it("retries a failed read on its own, with no notification", async () => {
	vi.useFakeTimers();
	const fake = hub([new Error("the hub is busy"), [expired]]);
	const { result } = renderHook(() => useAuthStatuses(fake.client));
	await flushInitial();
	expect(fake.methods).toHaveLength(1);
	expect(result.current).toBeNull();
	await act(async () => {
		await vi.advanceTimersByTimeAsync(AUTH_RETRY_MS);
	});
	expect(fake.methods).toHaveLength(2);
	expect(result.current?.get("codex-jesse-fsck.com")?.needsLogin).toBe(true);
});

it("retries a failed refetch after a read has landed", async () => {
	vi.useFakeTimers();
	const fake = hub([[expired], new Error("the hub is busy"), [renewed]]);
	const { result } = renderHook(() => useAuthStatuses(fake.client));
	await flushInitial();
	fake.notify("evener/auth/updated");
	await afterDebounce();
	expect(fake.methods).toHaveLength(2);
	// The failed refetch keeps the last statuses and, with no further
	// notification, the hook reads again on its own.
	expect(result.current?.get("codex-jesse-fsck.com")?.needsLogin).toBe(true);
	await act(async () => {
		await vi.advanceTimersByTimeAsync(AUTH_RETRY_MS);
	});
	expect(fake.methods).toHaveLength(3);
	expect(result.current?.get("codex-jesse-fsck.com")?.needsLogin).toBe(false);
});

it("keeps the replaced client's reply off the new client's statuses", async () => {
	let answerA: (r: { providers: AuthStatusResponse[] }) => void = () => {};
	const clientA = {
		request: () =>
			new Promise<{ providers: AuthStatusResponse[] }>((resolve) => {
				answerA = resolve;
			}),
		onNotification: () => () => {},
	} as unknown as ConversationClientLike;
	const b = hub([[expired]]);
	let current: ConversationClientLike | null = clientA;
	const { result, rerender } = renderHook(() => useAuthStatuses(current));
	await act(settle);
	current = b.client;
	rerender();
	await act(settle);
	expect(result.current?.get("codex-jesse-fsck.com")?.needsLogin).toBe(true);
	// A's read lands after B is the client: the replaced store is disposed, so
	// it publishes nothing and B's statuses stand.
	await act(async () => {
		answerA({ providers: [renewed] });
		await settle();
	});
	expect(result.current?.get("codex-jesse-fsck.com")?.needsLogin).toBe(true);
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

afterEach(() => {
	vi.useRealTimers();
});
