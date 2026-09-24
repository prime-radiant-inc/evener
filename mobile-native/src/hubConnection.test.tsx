// useHubConnection is the piece of ConnectionProvider that adopts the
// package's createConnectionStore: the client-swap safety and
// state-following, wired to whichever hub profile the caller names. These
// tests fake the client createHubClient would otherwise build over a real
// socket, and drive it exactly the way a hub connection transitions, to prove
// the hook still reports the same {client, state} shape ConnectionProvider's
// consumers have always read.
import { act } from "react-test-renderer";
import { afterEach, expect, it, type Mock, vi } from "vitest";
import type { ConnectionState, TerminalReason } from "@evener/appwire-client";
import { connectionFailure } from "./connectionRecovery";
import { clientServesHub } from "./connectionIdentity";
import { type HubConnection, type HubTokenSource, useHubConnection } from "./hubConnection";
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

interface HubConnectionInputs {
	repository: HubTokenSource;
	activeId: string | undefined;
	activeOrigin: string | undefined;
	foreground: boolean;
	attempt: number;
	setError: Mock<(message: string | null) => void>;
}

/** Mounts useHubConnection over a mutable input record: a test changes one
 * field on `input` and calls `hook.rerender()` to open a fresh connection,
 * rather than rebuilding the six-argument call each time. `renders`
 * accumulates every render's result, in order - not just the settled
 * `hook.result.current` - for the two tests below where the race is in an
 * intermediate render. */
function mount(overrides: Partial<HubConnectionInputs> = {}) {
	const input: HubConnectionInputs = {
		repository: { token: async (id: string) => `tok-${id}` },
		activeId: "hub-a",
		activeOrigin: "https://a.test",
		foreground: true,
		attempt: 0,
		setError: vi.fn<(message: string | null) => void>(),
		...overrides,
	};
	const renders: HubConnection[] = [];
	const hook = renderHook(() => {
		const result = useHubConnection(
			input.repository,
			input.activeId,
			input.activeOrigin,
			input.foreground,
			input.attempt,
			input.setError,
		);
		renders.push(result);
		return result;
	});
	return { hook, input, renders, setError: input.setError };
}

afterEach(() => {
	vi.restoreAllMocks();
});

it("stays idle with no client until a profile, origin and foreground are all present", () => {
	const { hook, setError } = mount({ activeId: undefined, activeOrigin: undefined });
	expect(hook.result.current).toEqual({ client: null, state: "idle", fatal: false });
	expect(setError).toHaveBeenCalledWith(null);
});

it("connects and follows the client's transitions the same way native has always read them", async () => {
	const fake = new FakeHubClient();
	harness.client = fake;
	const { hook, setError } = mount();
	// Before the token fetch resolves there is still no client, but the hub
	// is known, so the same "connecting" default PluginsScreen etc. have
	// always read applies.
	expect(hook.result.current).toEqual({ client: null, state: "connecting", fatal: false });
	await act(async () => {});
	expect(hook.result.current).toEqual({ client: fake, state: "connecting", fatal: false });
	act(() => fake.succeed());
	expect(hook.result.current).toEqual({ client: fake, state: "ready", fatal: false });
	expect(setError).toHaveBeenLastCalledWith(null);
	await act(async () => {
		fake.fail("protocol");
	});
	// The client stays put on close - only the reconnect wall's `state` check
	// leaves "ready"; the failed connection is still what a retry recycles.
	expect(hook.result.current).toEqual({ client: fake, state: "closed", fatal: true });
	// Structural: the branch connectionFailure("protocol") selects, not its
	// prose (docs/developing-evener/testing.md, "Prompt Prose Is Not a Test
	// Oracle" - this message is UI copy, not a wire contract).
	expect(setError).toHaveBeenLastCalledWith(connectionFailure("protocol").message);
});

it.each([
	{
		label: "a switched hub",
		change: { activeId: "hub-b", activeOrigin: "https://b.test" },
	},
	{ label: "a bumped retry", change: { attempt: 1 } },
])("never returns the previous client once $label has moved past it", async ({ change }) => {
	const first = new FakeHubClient();
	harness.client = first;
	const { hook, input, renders } = mount();
	await act(async () => {});
	act(() => first.succeed());
	expect(hook.result.current).toEqual({ client: first, state: "ready", fatal: false });

	const second = new FakeHubClient();
	harness.client = second;
	Object.assign(input, change);
	renders.length = 0;
	// The teardown effect for the previous generation has not run yet at the
	// moment this rerender's first pass happens (passive effects fire after
	// commit), so this is exactly the window the store could still hold the
	// previous client while every input already names the new generation.
	hook.rerender();
	for (const result of renders) expect(result.client).not.toBe(first);
	await act(async () => {});
	act(() => second.succeed());
	expect(hook.result.current).toEqual({ client: second, state: "ready", fatal: false });
});

it("a bumped attempt reopens the hub through a fresh client, tearing down the old one first", async () => {
	const first = new FakeHubClient();
	harness.client = first;
	const { hook, input } = mount();
	await act(async () => {});
	act(() => first.succeed());
	expect(hook.result.current).toEqual({ client: first, state: "ready", fatal: false });
	const second = new FakeHubClient();
	harness.client = second;
	input.attempt = 1;
	hook.rerender();
	// Teardown runs before the new attempt's effect body: the retired
	// client's listener is gone and its socket is closed.
	expect(first.listenerCount).toBe(0);
	expect(first.state).toBe("closed");
	await act(async () => {});
	expect(hook.result.current).toEqual({ client: second, state: "connecting", fatal: false });
	act(() => second.succeed());
	expect(hook.result.current).toEqual({ client: second, state: "ready", fatal: false });
});

it("reports fatal only for a protocol close, clearing again once a fresh attempt connects", async () => {
	const first = new FakeHubClient();
	harness.client = first;
	const { hook, input } = mount();
	await act(async () => {});
	act(() => first.succeed());
	expect(hook.result.current.fatal).toBe(false);
	await act(async () => {
		first.fail("protocol");
	});
	expect(hook.result.current.fatal).toBe(true);
	const second = new FakeHubClient();
	harness.client = second;
	input.attempt = 1;
	hook.rerender();
	await act(async () => {});
	// A fresh attempt's own generation starts with no verdict yet - fatal is
	// the PREVIOUS generation's own failure, not carried into a new one.
	expect(hook.result.current.fatal).toBe(false);
	act(() => second.succeed());
	expect(hook.result.current.fatal).toBe(false);
});

it("does not report fatal for an ordinary transport close", async () => {
	const fake = new FakeHubClient();
	harness.client = fake;
	const { hook } = mount();
	await act(async () => {});
	act(() => fake.succeed());
	await act(async () => {
		fake.fail(null);
	});
	expect(hook.result.current).toEqual({
		client: fake,
		state: "closed",
		fatal: false,
	});
});

it("releases the client's listener on unmount", async () => {
	const fake = new FakeHubClient();
	harness.client = fake;
	const { hook } = mount();
	await act(async () => {});
	expect(fake.listenerCount).toBeGreaterThan(0);
	hook.unmount();
	expect(fake.listenerCount).toBe(0);
});

// Round 59's Medium: the record of which hub a client proved ready under
// belongs to the connection layer — written at dial success, for the hub the
// client was dialed for — so a connecting mount under any route can never
// attribute a client to a hub it does not serve (connectionIdentity).
it("records the hub a client was dialed for, at dial success", async () => {
	const fake = new FakeHubClient();
	harness.client = fake;
	const { hook } = mount();
	await act(async () => {});
	act(() => fake.succeed());
	expect(hook.result.current).toEqual({ client: fake, state: "ready", fatal: false });
	// The dialed hub holds; every other hub is refused until a re-dial.
	expect(clientServesHub(fake, "hub-a")).toBe(true);
	expect(clientServesHub(fake, "hub-b")).toBe(false);
});

// Round 61's Low: proving a hub identity is what readiness establishes. A
// client whose dial is still connecting, or failed outright, has proved
// nothing — recording it would let a gated consumer bind a client that never
// reached its hub.
it("does not record a client that never proved ready", async () => {
	const fake = new FakeHubClient();
	harness.client = fake;
	const { hook } = mount();
	await act(async () => {});
	expect(hook.result.current.state).toBe("connecting");
	// A still-connecting client has established nothing, so it must stay
	// unknown to the record. The record only ever withholds a client it
	// KNOWS proved ready under a different hub, so the discriminating probe
	// is a second hub: recorded under hub-a it would be refused for hub-b;
	// unknown, it is not (querying hub-a cannot tell the two apart — the
	// record passes an unknown client exactly as it passes its own).
	expect(clientServesHub(fake, "hub-b")).toBe(true);
	act(() => fake.fail());
	expect(hook.result.current.state).toBe("closed");
	// A failed dial establishes nothing either — the client stays unknown.
	expect(clientServesHub(fake, "hub-b")).toBe(true);
});

// Round 85's follow-up: a token fetch that fails outright is this attempt's
// terminal verdict — the catch writes "closed" to the core with no client
// ever created, and the hook must expose it rather than derive "connecting"
// from the client's absence: a failed reconnect is not a still-connecting
// one, and reading it as connecting keeps the retry affordance hidden behind
// a spinner forever. The initial pre-connection phase keeps its "connecting"
// report, and the verdict never outlives its attempt: a fresh attempt that
// fetches and dials fine leaves "closed" behind.
it("reports closed, not connecting, when token acquisition fails", async () => {
	const fake = new FakeHubClient();
	harness.client = fake;
	const { hook, input, setError } = mount({
		repository: { token: () => Promise.reject(new Error("no token")) },
	});
	// Before the rejection settles: the initial pre-connection "connecting".
	expect(hook.result.current).toEqual({ client: null, state: "connecting", fatal: false });
	await act(async () => {});
	// The token-fetch failure is terminal: closed, not still-connecting.
	expect(hook.result.current).toEqual({ client: null, state: "closed", fatal: false });
	// Structural: the branch connectionFailure(null) selects, not its prose.
	expect(setError).toHaveBeenLastCalledWith(connectionFailure(null).message);
	// The verdict belongs to this attempt only.
	input.repository = { token: async () => "tok-ok" };
	input.attempt = 1;
	hook.rerender();
	expect(hook.result.current.state).toBe("connecting");
	await act(async () => {});
	act(() => fake.succeed());
	expect(hook.result.current).toEqual({ client: fake, state: "ready", fatal: false });
});
