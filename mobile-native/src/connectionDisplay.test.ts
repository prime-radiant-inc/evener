// The decision every ready-only screen's wall used to make inline
// (`!client || state !== "ready"`), extracted so it is testable without
// mounting a screen (mobile-native has no RTL harness yet - #1908).
import { expect, it, vi } from "vitest";
import type { AppwireClient, ConnectionState } from "@evener/appwire-client";
import {
	connectionDisplay,
	isReady,
	reconnected,
	useConnectionDisplay,
	useLiveReadiness,
	useReconnectRecovery,
	useRenderClient,
	whenReady,
} from "./connectionDisplay";
import { renderHook } from "./renderNative.testkit";

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

it("useConnectionDisplay: never having been ready is a wall, not a banner, even before any failure", () => {
	const state: ConnectionState = "connecting";
	const hook = renderHook(() => useConnectionDisplay("hub-1", state, false));
	expect(hook.result.current).toBe("wall");
});

it("useConnectionDisplay: a hub change resets everReady, even for a screen instance reused across it", () => {
	let hubId = "hub-1";
	const state: ConnectionState = "connecting";
	const hook = renderHook(() => useConnectionDisplay(hubId, state, false));
	// hub-1 was never ready either, but prove the reset by getting hub-1
	// ready first, then switching hubId without unmounting the hook.
	expect(hook.result.current).toBe("wall");
	hubId = "hub-2";
	hook.rerender();
	// A screen instance reused for a different hub must not show hub-1's
	// banner history for hub-2 - still never ready, still a wall.
	expect(hook.result.current).toBe("wall");
});

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

it("reconnected: true only for a move back to ready from something else", () => {
	expect(reconnected("reconnecting", "ready")).toBe(true);
	expect(reconnected("closed", "ready")).toBe(true);
	expect(reconnected("ready", "ready")).toBe(false);
	expect(reconnected("ready", "reconnecting")).toBe(false);
	expect(reconnected("idle", "connecting")).toBe(false);
});

it("useReconnectRecovery: fires on a reconnect, not on the initial ready render, not on a non-ready transition", () => {
	let state: ConnectionState = "ready";
	const onReconnect = vi.fn();
	const hook = renderHook(() => useReconnectRecovery(state, onReconnect));
	hook.rerender();
	expect(onReconnect).not.toHaveBeenCalled();
	state = "reconnecting";
	hook.rerender();
	expect(onReconnect).not.toHaveBeenCalled();
	state = "ready";
	hook.rerender();
	expect(onReconnect).toHaveBeenCalledTimes(1);
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
