// useHubConnection is the piece of ConnectionProvider that adopts the
// package's createConnectionStore: the client-swap safety and
// state-following, wired to whichever hub profile the caller names. These
// tests fake the client createHubClient would otherwise build over a real
// socket, and drive it exactly the way a hub connection transitions, to prove
// the hook still reports the same {client, state} shape ConnectionProvider's
// consumers have always read.
import { act } from "react-test-renderer";
import { afterEach, expect, it, vi } from "vitest";
import type { ConnectionState, TerminalReason } from "@evener/appwire-client";
import { connectionFailure } from "./connectionRecovery";
import { type HubConnection, useHubConnection } from "./hubConnection";
import { renderHook } from "./renderNative.testkit";

const harness = vi.hoisted(() => ({ client: null as unknown }));
vi.mock("./connection", () => ({
	createHubClient: () => harness.client,
}));

/** Just enough of AppwireClient's surface for the hook to drive: a
 * connect() a test settles by calling succeed()/fail(), state transitions
 * broadcast to every onStateChange listener the way the real client's do
 * (synchronously, before connect()'s own promise settles). */
class FakeHubClient {
	state: ConnectionState = "idle";
	terminalReason: TerminalReason = null;
	private handlers = new Set<(s: ConnectionState) => void>();
	private settle: ((ok: boolean) => void) | null = null;
	get listenerCount(): number {
		return this.handlers.size;
	}
	onStateChange(cb: (s: ConnectionState) => void): () => void {
		this.handlers.add(cb);
		return () => this.handlers.delete(cb);
	}
	connect(): Promise<void> {
		this.transition("connecting");
		return new Promise((resolve, reject) => {
			this.settle = (ok) => (ok ? resolve() : reject(new Error("closed")));
		});
	}
	close(): void {
		this.transition("closed");
	}
	succeed(): void {
		this.transition("ready");
		this.settle?.(true);
	}
	fail(reason: TerminalReason = null): void {
		this.terminalReason = reason;
		this.transition("closed");
		this.settle?.(false);
	}
	private transition(next: ConnectionState): void {
		this.state = next;
		for (const handler of [...this.handlers]) handler(next);
	}
}

afterEach(() => {
	vi.restoreAllMocks();
});

it("stays idle with no client until a profile, origin and foreground are all present", () => {
	const setError = vi.fn();
	const repository = { token: async () => "" };
	const hook = renderHook(() =>
		useHubConnection(repository, undefined, undefined, true, 0, setError),
	);
	expect(hook.result.current).toEqual({ client: null, state: "idle" });
	expect(setError).toHaveBeenCalledWith(null);
});

it("connects and follows the client's transitions the same way native has always read them", async () => {
	const fake = new FakeHubClient();
	harness.client = fake;
	const setError = vi.fn();
	const repository = { token: async (id: string) => `tok-${id}` };
	const hook = renderHook(() =>
		useHubConnection(
			repository,
			"hub-a",
			"https://hub.test",
			true,
			0,
			setError,
		),
	);
	// Before the token fetch resolves there is still no client, but the hub
	// is known, so the same "connecting" default PluginsScreen etc. have
	// always read applies.
	expect(hook.result.current).toEqual({ client: null, state: "connecting" });
	await act(async () => {});
	expect(hook.result.current).toEqual({ client: fake, state: "connecting" });
	act(() => fake.succeed());
	expect(hook.result.current).toEqual({ client: fake, state: "ready" });
	expect(setError).toHaveBeenLastCalledWith(null);
	await act(async () => {
		fake.fail("protocol");
	});
	// The client stays put on close - only the reconnect wall's `state` check
	// leaves "ready"; the failed connection is still what a retry recycles.
	expect(hook.result.current).toEqual({ client: fake, state: "closed" });
	// Structural: the branch connectionFailure("protocol") selects, not its
	// prose (docs/developing-evener/testing.md, "Prompt Prose Is Not a Test
	// Oracle" - this message is UI copy, not a wire contract).
	expect(setError).toHaveBeenLastCalledWith(connectionFailure("protocol").message);
});

it("never returns the previous hub's client once activeId already names a newer one", async () => {
	const first = new FakeHubClient();
	harness.client = first;
	const setError = vi.fn();
	const repository = { token: async (id: string) => `tok-${id}` };
	let activeId = "hub-a";
	let activeOrigin = "https://a.test";
	// Every render this test performs, in order, paired with which hub it was
	// FOR at that render - not just the final settled value renderHook's
	// result.current exposes, since the race is about an intermediate render.
	const renders: { forHub: string; result: HubConnection }[] = [];
	const hook = renderHook(() => {
		const result = useHubConnection(
			repository,
			activeId,
			activeOrigin,
			true,
			0,
			setError,
		);
		renders.push({ forHub: activeId, result });
		return result;
	});
	await act(async () => {});
	act(() => first.succeed());
	expect(hook.result.current).toEqual({ client: first, state: "ready" });

	const second = new FakeHubClient();
	harness.client = second;
	activeId = "hub-b";
	activeOrigin = "https://b.test";
	renders.length = 0;
	// The teardown effect for hub-a has not run yet at the moment this
	// rerender's first pass happens (passive effects fire after commit), so
	// this is exactly the window the store could still hold hub-a's client
	// while every input already says hub-b.
	hook.rerender();
	for (const { forHub, result } of renders)
		if (forHub === "hub-b") expect(result.client).not.toBe(first);
	await act(async () => {});
	act(() => second.succeed());
	expect(hook.result.current).toEqual({ client: second, state: "ready" });
});

it("a bumped attempt reopens the hub through a fresh client, tearing down the old one first", async () => {
	const first = new FakeHubClient();
	harness.client = first;
	const setError = vi.fn();
	const repository = { token: async (id: string) => `tok-${id}` };
	let attempt = 0;
	const hook = renderHook(() =>
		useHubConnection(
			repository,
			"hub-a",
			"https://hub.test",
			true,
			attempt,
			setError,
		),
	);
	await act(async () => {});
	act(() => first.succeed());
	expect(hook.result.current).toEqual({ client: first, state: "ready" });
	const second = new FakeHubClient();
	harness.client = second;
	attempt = 1;
	hook.rerender();
	// Teardown runs before the new attempt's effect body: the retired
	// client's listener is gone and its socket is closed.
	expect(first.listenerCount).toBe(0);
	expect(first.state).toBe("closed");
	await act(async () => {});
	expect(hook.result.current).toEqual({ client: second, state: "connecting" });
	act(() => second.succeed());
	expect(hook.result.current).toEqual({ client: second, state: "ready" });
});

it("releases the client's listener on unmount", async () => {
	const fake = new FakeHubClient();
	harness.client = fake;
	const setError = vi.fn();
	const repository = { token: async (id: string) => `tok-${id}` };
	const hook = renderHook(() =>
		useHubConnection(
			repository,
			"hub-a",
			"https://hub.test",
			true,
			0,
			setError,
		),
	);
	await act(async () => {});
	expect(fake.listenerCount).toBeGreaterThan(0);
	hook.unmount();
	expect(fake.listenerCount).toBe(0);
});
