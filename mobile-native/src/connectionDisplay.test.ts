// The decision every ready-only screen's wall used to make inline
// (`!client || state !== "ready"`), extracted so it is testable without
// mounting a screen (mobile-native has no RTL harness yet - #1908).
import { expect, it } from "vitest";
import type { AppwireClient, ConnectionState } from "@evener/appwire-client";
import {
	connectionDisplay,
	isReady,
	useConnectionDisplay,
	useRenderClient,
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
	const hook = renderHook(() => useConnectionDisplay(state, false));
	expect(hook.result.current).toBe("none");
	state = "reconnecting";
	hook.rerender();
	expect(hook.result.current).toBe("banner");
});

it("useConnectionDisplay: reconnecting -> ready removes the banner", () => {
	let state: ConnectionState = "ready";
	const hook = renderHook(() => useConnectionDisplay(state, false));
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
	const hook = renderHook(() => useConnectionDisplay(state, fatal));
	expect(hook.result.current).toBe("none");
	state = "closed";
	fatal = true;
	hook.rerender();
	expect(hook.result.current).toBe("wall");
});

it("useConnectionDisplay: never having been ready is a wall, not a banner, even before any failure", () => {
	const state: ConnectionState = "connecting";
	const hook = renderHook(() => useConnectionDisplay(state, false));
	expect(hook.result.current).toBe("wall");
});

it("useRenderClient: falls back to the last client through a null gap", () => {
	const first = {} as AppwireClient;
	let client: AppwireClient | null = first;
	const hook = renderHook(() => useRenderClient(client));
	expect(hook.result.current).toBe(first);
	client = null;
	hook.rerender();
	expect(hook.result.current).toBe(first);
});
