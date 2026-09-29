import type { AnyNotification, AuthStatusResponse } from "@evener/appwire-client";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { renderHook } from "../renderNative.testkit";
import { useAuthStatuses } from "./useAuthStatuses";

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
	expect(result.current.get("codex-jesse-fsck.com")?.needsLogin).toBe(true);
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
	expect(result.current.get("codex-jesse-fsck.com")?.needsLogin).toBe(false);
});

it("keeps the last statuses through a failed read", async () => {
	const fake = hub([[expired], new Error("the hub is restarting")]);
	const { result } = renderHook(() => useAuthStatuses(fake.client));
	await act(settle);
	fake.notify("evener/auth/updated");
	await act(settle);
	expect(fake.methods).toHaveLength(2);
	expect(result.current.get("codex-jesse-fsck.com")?.needsLogin).toBe(true);
});

it("reads nothing without a client", async () => {
	const { result } = renderHook(() => useAuthStatuses(null));
	await act(settle);
	expect(result.current.size).toBe(0);
});

it("stops listening when it unmounts", async () => {
	const fake = hub([[expired]]);
	const { unmount } = renderHook(() => useAuthStatuses(fake.client));
	await act(settle);
	unmount();
	expect(fake.handlers.size).toBe(0);
});
