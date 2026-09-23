// The decision every ready-only screen's wall used to make inline
// (`!client || state !== "ready"`), extracted so it is testable without
// mounting a screen (mobile-native has no RTL harness yet - #1908).
import { expect, it, vi } from "vitest";
import { createElement, Suspense, use, useLayoutEffect } from "react";
import { act } from "react-test-renderer";
import type { AppwireClient, ConnectionState } from "@evener/appwire-client";
import {
	connectionDisplay,
	isReady,
	useConnectionDisplay,
	useLiveReadiness,
	useRenderClient,
	whenReady,
} from "./connectionDisplay";
import { render, renderedText, renderHook } from "./renderNative.testkit";
import type { LiveReadiness } from "./connectionDisplay";

it("shows nothing once ready, regardless of history or a fatal flag", () => {
	expect(connectionDisplay("ready", false, false)).toBe("none");
	expect(connectionDisplay("ready", true, true)).toBe("none");
});

it("walls a screen that has never had anything to show", () => {
	for (const state of ["idle", "connecting", "reconnecting", "closed"] as ConnectionState[])
		expect(connectionDisplay(state, false, false)).toBe("wall");
});

it("banners a flap once something has already been shown", () => {
	for (const state of ["idle", "connecting", "reconnecting", "closed"] as ConnectionState[])
		expect(connectionDisplay(state, true, false)).toBe("banner");
});

it("walls a fatal failure even after something has been shown", () => {
	expect(connectionDisplay("closed", true, true)).toBe("wall");
});

it("isReady: only \"ready\" is ready", () => {
	expect(isReady("ready")).toBe(true);
	for (const state of ["idle", "connecting", "reconnecting", "closed"] as ConnectionState[])
		expect(isReady(state)).toBe(false);
});

it("useConnectionDisplay: ready -> reconnecting keeps the screen tree mounted and shows the banner", () => {
	let state: ConnectionState = "ready";
	const hook = renderHook(() => useConnectionDisplay("hub-1", state, false));
	expect(hook.result.current).toBe("none");
	state = "reconnecting";
	hook.rerender();
	expect(hook.result.current).toBe("banner");
});

it("useConnectionDisplay: reconnecting -> ready removes the banner", () => {
	let state: ConnectionState = "ready";
	const hook = renderHook(() => useConnectionDisplay("hub-1", state, false));
	state = "reconnecting";
	hook.rerender();
	expect(hook.result.current).toBe("banner");
	state = "ready";
	hook.rerender();
	expect(hook.result.current).toBe("none");
});

it("useConnectionDisplay: fatal shows the wall even for a hub that was ready a moment ago", () => {
	let state: ConnectionState = "ready";
	let fatal = false;
	const hook = renderHook(() => useConnectionDisplay("hub-1", state, fatal));
	expect(hook.result.current).toBe("none");
	state = "closed";
	fatal = true;
	hook.rerender();
	expect(hook.result.current).toBe("wall");
});

it("useConnectionDisplay: keeps the wall through a fatal retry until ready", () => {
	let state: ConnectionState = "ready";
	let fatal = false;
	const hook = renderHook(() => useConnectionDisplay("hub-1", state, fatal));
	state = "closed";
	fatal = true;
	hook.rerender();
	expect(hook.result.current).toBe("wall");
	// A manual retry clears the fatal flag before the replacement client is
	// ready. The old client must not be remounted in that connecting window.
	state = "connecting";
	fatal = false;
	hook.rerender();
	expect(hook.result.current).toBe("wall");
	state = "ready";
	hook.rerender();
	expect(hook.result.current).toBe("none");
});

it("useConnectionDisplay: never having been ready is a wall, not a banner, even before any failure", () => {
	const state: ConnectionState = "connecting";
	const hook = renderHook(() => useConnectionDisplay("hub-1", state, false));
	expect(hook.result.current).toBe("wall");
});

// This is the reset's own coverage (the hub-change-resets-everReady case):
// hub-1 must first be ready for the hub-2 wall to prove anything. A sibling
// case whose state never reached "ready" passed with the reset deleted, so
// the panel's review retired it as tautological and left this test as the
// one that actually exercises the reset.
it("useConnectionDisplay: everReady for the previous hub does not banner the next hub before it is ready", () => {
	let hubId = "hub-1";
	let state: ConnectionState = "ready";
	const hook = renderHook(() => useConnectionDisplay(hubId, state, false));
	expect(hook.result.current).toBe("none");
	hubId = "hub-2";
	state = "connecting";
	hook.rerender();
	// Without the reset this would read as a banner (everReady still true
	// from hub-1); hub-2 has never been ready, so it must wall.
	expect(hook.result.current).toBe("wall");
});

it("useRenderClient: falls back to the last client through a null gap", () => {
	const first = {} as AppwireClient;
	let client: AppwireClient | null = first;
	let state: ConnectionState = "ready";
	const hook = renderHook(() => useRenderClient(client, state, "hub-1"));
	expect(hook.result.current).toBe(first);
	client = null;
	state = "connecting";
	hook.rerender();
	expect(hook.result.current).toBe(first);
});

it("useLiveReadiness rejects a deferred callback after the client or hub changes", () => {
	const first = {} as AppwireClient;
	const second = {} as AppwireClient;
	let hubId = "hub-1";
	let client: AppwireClient | null = first;
	let state: ConnectionState = "ready";
	const hook = renderHook(() => useLiveReadiness(hubId, client, state));
	const deferred = hook.result.current;
	hubId = "hub-2";
	client = second;
	hook.rerender();
	expect(deferred()).toBe(false);
	expect(hook.result.current()).toBe(true);
	state = "reconnecting";
	hook.rerender();
	expect(hook.result.current()).toBe(false);
});

it("useLiveReadiness refuses a stale callback inside the commit-to-effect window", () => {
	const first = {} as AppwireClient;
	const second = {} as AppwireClient;
	let hubId = "hub-1";
	let client: AppwireClient | null = first;
	const state: ConnectionState = "ready";
	// Sentinel defaults keep the closure-assigned callbacks callable for the
	// type checker; the first render overwrites both before any assertion.
	let current: LiveReadiness = () => false;
	let stale: LiveReadiness = () => false;
	let staleCaptured = false;
	let windowVerdict: boolean | null = null;
	// A child layout effect runs before the hook-owning component's own
	// effects settle, so a callback invoked from it observes exactly the
	// commit-to-effect window: the new render has committed, and a callback
	// captured by the previous render still validates against the previous
	// settle.
	function Probe() {
		useLayoutEffect(() => {
			windowVerdict = stale();
		});
		return null;
	}
	function Owner() {
		current = useLiveReadiness(hubId, client, state);
		if (!staleCaptured) {
			stale = current;
			staleCaptured = true;
		}
		return createElement(Probe);
	}
	const tree = render(createElement(Owner));
	expect(windowVerdict).toBe(true);
	hubId = "hub-2";
	client = second;
	act(() => {
		tree.update(createElement(Owner));
	});
	// The stale callback must refuse the moment the moved render commits,
	// not one effect-settle later.
	expect(windowVerdict).toBe(false);
	// Once the effects settle, the world that actually moved is authorized
	// again — the window closes without poisoning the next callback.
	expect(current()).toBe(true);
});

it("useLiveReadiness recovers when React abandons an identity-changing render", async () => {
	const first = {} as AppwireClient;
	const second = {} as AppwireClient;
	let hubId = "hub-1";
	let client: AppwireClient | null = first;
	const state: ConnectionState = "ready";
	// Sentinel default keeps the closure-assigned callback callable for the
	// type checker; the first render overwrites it before any assertion.
	let current: LiveReadiness = () => false;
	const gate = { suspend: false };
	let release!: () => void;
	const parked = new Promise<void>((resolve) => {
		release = resolve;
	});
	// A gated render parks on the Suspense boundary: the hook has already
	// run (the generation counter bumped in render phase), but the render
	// never commits and its settle effect never runs — the residue an
	// abandoned identity-changing render leaves behind. Releasing the
	// promise retries the parked render, which re-reads the closure: by
	// then it carries the ORIGINAL identity again, so what commits is an
	// unchanged-identity render over the abandoned bump.
	function Owner() {
		current = useLiveReadiness(hubId, client, state);
		if (gate.suspend) {
			use(parked);
		}
		return null;
	}
	const fallback = "connection-loading";
	const boundary = () =>
		createElement(Suspense, { fallback }, createElement(Owner));
	const tree = render(boundary());
	// hub-1's connection is genuinely ready: the settled predicate authorizes.
	expect(current()).toBe(true);

	// The identity-changing render is abandoned midway: its render-phase
	// bump persists in the counter, but nothing commits and no effect runs.
	gate.suspend = true;
	hubId = "hub-2";
	client = second;
	await act(async () => {
		tree.update(boundary());
	});
	expect(renderedText(tree)).toContain(fallback);

	// The next committed render carries the original identity, matching the
	// last committed one, so no new bump happens: the committed callback
	// must be rebuilt from the current generation, or a callback memoized
	// before the bump refuses forever.
	gate.suspend = false;
	hubId = "hub-1";
	client = first;
	await act(async () => {
		release();
	});
	expect(renderedText(tree)).not.toContain(fallback);
	expect(current()).toBe(true);
});

it("useRenderClient: a retry's not-yet-ready replacement never displaces the previous client", () => {
	// hubConnection.ts's own sequence for a manual retry: `client` clears
	// while the fresh one dials, then a NEW client object appears while
	// `state` is still "connecting" - not yet safe to hand to a store.
	const first = {} as AppwireClient;
	const second = {} as AppwireClient;
	let client: AppwireClient | null = first;
	let state: ConnectionState = "ready";
	const hook = renderHook(() => useRenderClient(client, state, "hub-1"));
	expect(hook.result.current).toBe(first);

	client = null;
	state = "connecting";
	hook.rerender();
	expect(hook.result.current).toBe(first);

	client = second;
	// The replacement now exists, but has not reached "ready": still not
	// safe to render.
	hook.rerender();
	expect(hook.result.current).toBe(first);

	state = "ready";
	hook.rerender();
	expect(hook.result.current).toBe(second);
});

it("useRenderClient: a hub change drops the previous hub's client instead of falling back to it", () => {
	const first = {} as AppwireClient;
	let client: AppwireClient | null = first;
	let state: ConnectionState = "ready";
	let hubId = "hub-1";
	const hook = renderHook(() => useRenderClient(client, state, hubId));
	expect(hook.result.current).toBe(first);
	client = null;
	state = "connecting";
	hubId = "hub-2";
	hook.rerender();
	// Without the reset this would fall back to hub-1's client; hub-2 has
	// nothing adopted yet and must get nothing instead.
	expect(hook.result.current).toBeNull();
});

// The re-key window the hooks' own doc comments describe: the navigator has
// re-keyed the screen to hub-2 while the connection still reports hub-1's
// client as "ready". That state is the previous hub's say-so — no request may
// run and nothing may render on that pairing until the connection reports for
// the new hub.
it("a hub re-key with the old client still ready walls, withholds the client, and refuses live readiness", () => {
	const first = {} as AppwireClient;
	let hubId = "hub-1";
	let client: AppwireClient | null = first;
	let state: ConnectionState = "ready";
	const display = renderHook(() => useConnectionDisplay(hubId, state, false));
	const renderClient = renderHook(() => useRenderClient(client, state, hubId));
	const live = renderHook(() => useLiveReadiness(hubId, client, state));
	const deferred = live.result.current;

	// hub-1's connection is genuinely ready: everything reports on its client.
	expect(display.result.current).toBe("none");
	expect(renderClient.result.current).toBe(first);
	expect(deferred()).toBe(true);
	expect(live.result.current()).toBe(true);

	// The re-key: only hubId moves. The client object and state are unchanged
	// — still hub-1's client, still reporting ready — so this render carries
	// no information from hub-2's connection at all.
	hubId = "hub-2";
	display.rerender();
	renderClient.rerender();
	live.rerender();
	expect(display.result.current).toBe("wall");
	expect(renderClient.result.current).toBeNull();
	expect(live.result.current()).toBe(false);
	// A deferred captured before the move stays refused as well.
	expect(deferred()).toBe(false);
});

// The persisting half of the re-key window: the moved render refuses the old
// hub's pairing (the test above), but the settling effects must not then
// re-adopt it. The connection still reports hub-1's client as ready, so every
// render under hub-2 keeps refusing that pairing until the connection reports
// for hub-2 itself - a genuinely new client, which the hooks adopt only once
// its readiness is settled under the scope it arrived in.
it("a hub re-key keeps refusing the previous hub's ready pairing until the connection re-points", () => {
	const first = {} as AppwireClient;
	const second = {} as AppwireClient;
	let hubId = "hub-1";
	let client: AppwireClient | null = first;
	let state: ConnectionState = "ready";
	const display = renderHook(() => useConnectionDisplay(hubId, state, false));
	const renderClient = renderHook(() => useRenderClient(client, state, hubId));
	const live = renderHook(() => useLiveReadiness(hubId, client, state));

	// hub-1's connection is genuinely ready.
	expect(display.result.current).toBe("none");
	expect(renderClient.result.current).toBe(first);
	expect(live.result.current()).toBe(true);

	// The re-key: hubId moves, client and state unchanged.
	hubId = "hub-2";
	display.rerender();
	renderClient.rerender();
	live.rerender();
	expect(display.result.current).toBe("wall");
	expect(renderClient.result.current).toBeNull();
	expect(live.result.current()).toBe(false);

	// The effects have settled; a plain rerender carries the same stale
	// pairing and must refuse it just the same.
	display.rerender();
	renderClient.rerender();
	live.rerender();
	expect(display.result.current).toBe("wall");
	expect(renderClient.result.current).toBeNull();
	expect(live.result.current()).toBe(false);

	// Back under hub-1 with no transition in between: the pairing is hub-1's
	// own again, and every hook serves it immediately.
	hubId = "hub-1";
	display.rerender();
	renderClient.rerender();
	live.rerender();
	expect(display.result.current).toBe("none");
	expect(renderClient.result.current).toBe(first);
	expect(live.result.current()).toBe(true);

	// The connection re-points for hub-2: a null gap while it dials, then a
	// genuinely new client. That client is genuinely hub-2's own - the
	// adoption's settling re-render serves it, and nothing of hub-1's pairing
	// leaks into it.
	hubId = "hub-2";
	client = null;
	state = "connecting";
	display.rerender();
	renderClient.rerender();
	live.rerender();
	expect(display.result.current).toBe("wall");
	expect(renderClient.result.current).toBeNull();
	expect(live.result.current()).toBe(false);
	client = second;
	state = "ready";
	display.rerender();
	renderClient.rerender();
	live.rerender();
	expect(display.result.current).toBe("none");
	expect(renderClient.result.current).toBe(second);
	expect(live.result.current()).toBe(true);
});

it("whenReady: not ready is a no-op, ready calls through with its arguments", () => {
	const handler = vi.fn();
	whenReady(() => false, handler)("a", 1);
	expect(handler).not.toHaveBeenCalled();
	whenReady(() => true, handler)("a", 1);
	expect(handler).toHaveBeenCalledExactlyOnceWith("a", 1);
});

it("whenReady reads readiness when the deferred callback runs", () => {
	let ready = true;
	const handler = vi.fn();
	const deferred = whenReady(() => ready, handler);
	ready = false;
	deferred();
	expect(handler).not.toHaveBeenCalled();
	ready = true;
	deferred();
	expect(handler).toHaveBeenCalledOnce();
});
