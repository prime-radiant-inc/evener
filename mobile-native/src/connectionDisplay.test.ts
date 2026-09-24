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
import { recordClientReadyHub } from "./connectionIdentity";
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
	const client = {} as AppwireClient;
	let state: ConnectionState = "ready";
	const hook = renderHook(() => useConnectionDisplay("hub-1", state, false, client));
	expect(hook.result.current).toBe("none");
	state = "reconnecting";
	hook.rerender();
	expect(hook.result.current).toBe("banner");
});

it("useConnectionDisplay: reconnecting -> ready removes the banner", () => {
	const client = {} as AppwireClient;
	let state: ConnectionState = "ready";
	const hook = renderHook(() => useConnectionDisplay("hub-1", state, false, client));
	state = "reconnecting";
	hook.rerender();
	expect(hook.result.current).toBe("banner");
	state = "ready";
	hook.rerender();
	expect(hook.result.current).toBe("none");
});

it("useConnectionDisplay: fatal shows the wall even for a hub that was ready a moment ago", () => {
	const client = {} as AppwireClient;
	let state: ConnectionState = "ready";
	let fatal = false;
	const hook = renderHook(() => useConnectionDisplay("hub-1", state, fatal, client));
	expect(hook.result.current).toBe("none");
	state = "closed";
	fatal = true;
	hook.rerender();
	expect(hook.result.current).toBe("wall");
});

it("useConnectionDisplay: keeps the wall through a fatal retry until ready", () => {
	const client = {} as AppwireClient;
	let state: ConnectionState = "ready";
	let fatal = false;
	const hook = renderHook(() => useConnectionDisplay("hub-1", state, fatal, client));
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
	const client = {} as AppwireClient;
	const state: ConnectionState = "connecting";
	const hook = renderHook(() => useConnectionDisplay("hub-1", state, false, client));
	expect(hook.result.current).toBe("wall");
});

// This is the reset's own coverage (the hub-change-resets-everReady case):
// hub-1 must first be ready for the hub-2 wall to prove anything. A sibling
// case whose state never reached "ready" passed with the reset deleted, so
// the panel's review retired it as tautological and left this test as the
// one that actually exercises the reset.
it("useConnectionDisplay: everReady for the previous hub does not banner the next hub before it is ready", () => {
	const client = {} as AppwireClient;
	let hubId = "hub-1";
	let state: ConnectionState = "ready";
	const hook = renderHook(() => useConnectionDisplay(hubId, state, false, client));
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
	// The mount's landing render refuses — a pairing is never born-settled by
	// the render it arrives in — so the first committed render whose callback
	// the settled birth authorizes is the same-props update after the settle.
	act(() => {
		tree.update(createElement(Owner));
	});
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

it("useLiveReadiness refuses a ready-captured callback while the connection state drops", () => {
	const first = {} as AppwireClient;
	let hubId = "hub-1";
	let client: AppwireClient | null = first;
	let state: ConnectionState = "ready";
	// Sentinel defaults keep the closure-assigned callbacks callable for the
	// type checker; the first render overwrites both before any assertion.
	let current: LiveReadiness = () => false;
	let stale: LiveReadiness = () => false;
	let staleCaptured = false;
	let windowVerdict: boolean | null = null;
	// The same child-layout-effect probe as the identity-move window test:
	// it observes the commit-to-effect window of every committed render.
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
	// The mount's landing render refuses — a pairing is never born-settled by
	// the render it arrives in — so the first committed render whose callback
	// the settled birth authorizes is the same-props update after the settle.
	act(() => {
		tree.update(createElement(Owner));
	});
	// hub-1's connection is genuinely ready: the captured callback authorizes.
	expect(windowVerdict).toBe(true);

	// The connection drops: only `state` moves — the hub and client pairing
	// is unchanged. A callback captured while ready must refuse the moment
	// the dropped render commits, not one effect-settle later: the settled
	// snapshot still reports "ready" through the window.
	state = "reconnecting";
	act(() => {
		tree.update(createElement(Owner));
	});
	expect(windowVerdict).toBe(false);
	// After the settle the snapshot reports the truth: the fresh callback
	// stays refused while the connection is down.
	expect(current()).toBe(false);

	// Recovery: a transition back into ready authorizes again once settled —
	// the refusal is a window guard, not a permanent poisoning.
	state = "ready";
	act(() => {
		tree.update(createElement(Owner));
	});
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
	const display = renderHook(() => useConnectionDisplay(hubId, state, false, client));
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
	const display = renderHook(() => useConnectionDisplay(hubId, state, false, client));
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

// Round 32's High: a re-keyed remount lands while the connection still
// reports the previous hub's ready client — the store's selection moves
// before the connection re-points — and none of the guards may treat the
// props a mount was born with as earned. The wiring's own update path walls
// that pairing; the mount must not serve, trust, or authorize it on the
// render it lands with.
it("a re-keyed remount refuses the previous hub's pairing on the render it lands with", () => {
	const first = {} as AppwireClient;
	// The landing render's products, captured where that render itself would
	// use them: the first layout effect observes the landing commit before
	// any settling effect adopts anything. The settle re-renders the
	// state-driven hooks trigger would otherwise overwrite the capture
	// before it could be read.
	let landedDisplay: ReturnType<typeof useConnectionDisplay> | null = null;
	let landedClient: AppwireClient | null = null;
	let probed = false;
	let landingDisplay: ReturnType<typeof useConnectionDisplay> | null = null;
	let landingClient: AppwireClient | null = null;
	let deferred: LiveReadiness = () => false;
	let windowVerdict: boolean | null = null;
	function Probe() {
		useLayoutEffect(() => {
			if (probed) {
				return;
			}
			probed = true;
			landingDisplay = landedDisplay;
			landingClient = landedClient;
			windowVerdict = deferred();
		});
		return null;
	}
	function Owner() {
		landedDisplay = useConnectionDisplay("hub-2", "ready", false, first);
		landedClient = useRenderClient(first, "ready", "hub-2");
		deferred = useLiveReadiness("hub-2", first, "ready");
		return createElement(Probe);
	}
	render(createElement(Owner));
	// None of the guards treats the props the mount was born with as earned.
	expect(landingDisplay).toBe("wall");
	expect(landingClient).toBeNull();
	expect(windowVerdict).toBe(false);

	// After the settle the pairing is adopted — the residual the hooks cannot
	// close on their own for a client NO mount ever adopted: none of them can
	// see which hub a never-adopted client belongs to, so the connection
	// layer's re-point is what retires it. A client some mount DID adopt is
	// refused by the persistent record (round 56, the tests below).
	expect(landedDisplay).toBe("none");
	expect(landedClient).toBe(first);
	expect(deferred()).toBe(true);
});

// Round 56's High: the hooks' own state seeds empty at a keyed remount, so
// the settle adopted whatever ready pairing the remount landed on — the
// previous hub's still-ready client when the store's re-key outran the
// connection's re-point. The adoption record now persists above the remount
// boundary: a client that proved ready under another hub is refused until
// the connection re-points, while a client the record has never seen
// adopts normally — the genuinely new client arrives exactly as unknown.
it("useRenderClient refuses a previous hub's adopted client at a remount until the connection re-points", () => {
	const first = {} as AppwireClient;
	const replacement = {} as AppwireClient;
	// The connection layer recorded the client for hub-1 when it dialed it —
	// dial success is the record's only writer (hubConnection, round 59).
	recordClientReadyHub(first, "hub-1");
	// hub-1's live screen adopts the client while ready.
	const adopted = renderHook(() => useRenderClient(first, "ready", "hub-1"));
	adopted.rerender();
	expect(adopted.result.current).toBe(first);

	// The store re-keys the screen to hub-2 while the connection still
	// reports hub-1's ready client: the remount lands on the stale pairing.
	let client = first;
	const remounted = renderHook(() => useRenderClient(client, "ready", "hub-2"));
	remounted.rerender();
	// The known-foreign client must not be re-authorized under hub-2.
	expect(remounted.result.current).toBeNull();

	// The connection re-points and reports hub-2's own client: it adopts.
	client = replacement;
	remounted.rerender();
	expect(remounted.result.current).toBe(replacement);
});

// The same remount residual at the readiness hook: the birth record re-birth
// the previous hub's client under the new hub, authorizing requests against
// it. The persistent record refuses the re-birth until the re-point.
it("useLiveReadiness refuses a previous hub's adopted client at a remount until the connection re-points", () => {
	const first = {} as AppwireClient;
	const replacement = {} as AppwireClient;
	// The connection layer recorded the client for hub-1 when it dialed it —
	// dial success is the record's only writer (hubConnection, round 59).
	recordClientReadyHub(first, "hub-1");
	// hub-1's live screen births the client while ready.
	const adopted = renderHook(() => useLiveReadiness("hub-1", first, "ready"));
	expect(adopted.result.current()).toBe(true);

	// The remount lands on the stale pairing: the client may not re-birth
	// under hub-2.
	let client = first;
	const remounted = renderHook(() => useLiveReadiness("hub-2", client, "ready"));
	expect(remounted.result.current()).toBe(false);

	// The connection re-points and reports hub-2's own client: it births.
	client = replacement;
	remounted.rerender();
	expect(remounted.result.current()).toBe(true);
});

// Round 63's Medium: the display hook's trust arm was client-blind, so the
// mount's own ready state — the connection still reporting the previous
// hub's ready client through the re-key window — earned the new hub its
// screen, and a keyed remount rendered the normal surface while the client
// and readiness hooks refused the pairing. The trust arm now requires the
// same identity record the client hooks consult: a client the record knows
// under another hub vouches for nothing here, and the display walls until
// the re-point's own dial delivers a client this hub can claim.
it("useConnectionDisplay refuses the previous hub's recorded client for the new hub's remount until the re-point", () => {
	const first = {} as AppwireClient;
	const replacement = {} as AppwireClient;
	// The connection layer recorded the client for hub-1 at its dial.
	recordClientReadyHub(first, "hub-1");
	let client: AppwireClient | null = first;
	let state: ConnectionState = "ready";
	const display = renderHook(() => useConnectionDisplay("hub-2", state, false, client));
	// The landing render walls (round 32); the settle must not earn trust
	// for hub-2 off the previous hub's ready client either.
	display.rerender();
	expect(display.result.current).toBe("wall");

	// The connection re-points: its own dial for hub-2 closes the old
	// client, goes connecting, and transitions into ready on the new client —
	// recorded for hub-2 at ITS dial. The display earns its screen.
	recordClientReadyHub(replacement, "hub-2");
	client = null;
	state = "reconnecting";
	display.rerender();
	client = replacement;
	state = "ready";
	display.rerender();
	expect(display.result.current).toBe("none");
});

// Round 49's undefined-hub Medium: useRenderClient seeds adoption.scope as
// undefined, so a mount whose hubId is also undefined passed the ready-path
// scope check on its very first render and served the live client prop with
// no settling effect behind it — the mount-borne pairing the round-32 empty
// seed exists to withhold, reopened at the undefined end of the hub-id
// space (the store's selection can clear while the connection still reports
// the last hub's ready client). The ready path must also require an adopted
// client before it serves anything.
it("useRenderClient withholds an undefined hub's ready pairing until the settle adopts it", () => {
	const first = {} as AppwireClient;
	let landedClient: AppwireClient | null = null;
	let settledClient: AppwireClient | null = null;
	let probed = false;
	function Probe() {
		useLayoutEffect(() => {
			if (probed) {
				return;
			}
			probed = true;
			landedClient = settledClient;
		});
		return null;
	}
	function Owner() {
		settledClient = useRenderClient(first, "ready", undefined);
		return createElement(Probe);
	}
	render(createElement(Owner));
	// The landing render serves nothing: the empty adoption seed withholds
	// the mount-borne pairing even though adoption.scope === undefined ===
	// hubId would pass the bare scope check.
	expect(landedClient).toBeNull();
	// After the settle the pairing is adopted under the undefined scope and
	// the ready path serves the live client prop again — a replacement that
	// arrives ready is never delayed a render behind the stores keyed on it
	// (round 32).
	expect(settledClient).toBe(first);
});

// Round 50's undefined-hub Low: useConnectionDisplay seeds trustedScope as
// undefined, so a mount whose hubId is also undefined passed the ready
// render's trustedScope !== hubId gate (undefined !== undefined reads false)
// and showed the screen instead of the documented fail-closed wall — the
// same empty-seed collision round 49 closed for useRenderClient, at the
// display hook. Trust must be born before it unlocks anything: the explicit
// boolean never compares equal to a real scope.
it("useConnectionDisplay walls an undefined hub's ready mount until the settle earns trust", () => {
	const first = {} as AppwireClient;
	let landedDisplay: ReturnType<typeof useConnectionDisplay> | null = null;
	let settledDisplay: ReturnType<typeof useConnectionDisplay> | null = null;
	let probed = false;
	function Probe() {
		useLayoutEffect(() => {
			if (probed) {
				return;
			}
			probed = true;
			landedDisplay = settledDisplay;
		});
		return null;
	}
	function Owner() {
		settledDisplay = useConnectionDisplay(undefined, "ready", false, first);
		return createElement(Probe);
	}
	render(createElement(Owner));
	// The landing render walls: the empty trust seed must not collide with an
	// undefined hubId the way the bare scope comparison let it.
	expect(landedDisplay).toBe("wall");
	// After the settle the mount-already-ready transition earns trust and
	// the screen shows.
	expect(settledDisplay).toBe("none");
});

// Round 32's retention Medium: the effect that resets `everReady` on a hub
// move re-armed it in the same run when the moved render still carried the
// previous hub's ready state, so the new hub retained content it never
// showed. The re-arm must be gated on the same render's scope move.
it("useConnectionDisplay: a hub move while ready does not retain content the next hub never showed", () => {
	const client = {} as AppwireClient;
	let hubId = "hub-1";
	let state: ConnectionState = "ready";
	const display = renderHook(() => useConnectionDisplay(hubId, state, false, client));
	// Mount-already-ready: the settle earns trust and retention for hub-1.
	display.rerender();
	expect(display.result.current).toBe("none");

	// The re-key: hubId moves while the connection still reports ready.
	hubId = "hub-2";
	display.rerender();
	expect(display.result.current).toBe("wall");

	// The state drops after the move settled: hub-2 has never been ready, so
	// nothing it never showed may stay mounted behind a banner.
	state = "reconnecting";
	display.rerender();
	expect(display.result.current).toBe("wall");
});

// Round 52's retention Low: the re-arm gate's bare `!scopeMoved` term reads
// true on every post-settle render — the scope ref has already moved — so a
// fatal update arriving while the stale state stays ready re-armed
// `everReady` for the new hub in the very run that should have kept the
// fatal wall. When the fatal flag then clears on the reconnect, the dialing
// replacement showed a banner: retained content for a hub that never
// completed a trusted ready transition, and a remount of ready-only children
// against the closed client the fatal wall exists to prevent. The re-arm
// must key on readiness the new hub actually earned.
it("useConnectionDisplay: a fatal update after a re-key does not retain a hub trust never vouched for", () => {
	const client = {} as AppwireClient;
	let hubId = "hub-1";
	let state: ConnectionState = "ready";
	let fatal = false;
	const display = renderHook(() => useConnectionDisplay(hubId, state, fatal, client));
	// Mount-already-ready: the settle earns trust and retention for hub-1.
	display.rerender();
	expect(display.result.current).toBe("none");

	// The re-key: hubId moves while the connection still reports ready.
	hubId = "hub-2";
	display.rerender();
	expect(display.result.current).toBe("wall");

	// The fatal close arrives while the stale state still reports ready:
	// the ready render walls on the trust mismatch either way, but this
	// effect run must not arm retention for the new hub.
	fatal = true;
	display.rerender();
	expect(display.result.current).toBe("wall");

	// The replacement starts dialing: the fatal flag clears and the state
	// drops. The wall must hold — hub-2 never completed a trusted ready
	// transition, so nothing it never showed may banner, and the ready-only
	// children must not remount against the closed client.
	fatal = false;
	state = "reconnecting";
	display.rerender();
	expect(display.result.current).toBe("wall");
});

// Round 33's retention Medium: the hub move and the genuine transition into
// ready can land in ONE committed render — the connection re-points and
// reports ready together — and that is the new hub's own readiness, not the
// stale re-key shape the move gate exists to refuse. The screen it shows must
// retain through the next flap like any other genuinely-ready hub.
it("useConnectionDisplay: a hub move that lands with a genuine ready transition retains the screen", () => {
	const client = {} as AppwireClient;
	let hubId = "hub-1";
	let state: ConnectionState = "ready";
	const display = renderHook(() => useConnectionDisplay(hubId, state, false, client));
	// Mount-already-ready: the settle earns trust and retention for hub-1.
	display.rerender();
	expect(display.result.current).toBe("none");

	// hub-1's connection drops, then re-points to hub-2 and becomes ready in
	// one committed render: the moved render carries the transition itself.
	state = "reconnecting";
	display.rerender();
	expect(display.result.current).toBe("banner");
	hubId = "hub-2";
	state = "ready";
	display.rerender();
	// The moved render walls on the not-yet-settled trust; the transition
	// earns it and the settle shows.
	expect(display.result.current).toBe("none");

	// The reconnect: hub-2 genuinely showed its screen, so the flap retains
	// it behind a banner instead of walling.
	state = "reconnecting";
	display.rerender();
	expect(display.result.current).toBe("banner");
});

// Round 41's retention Low: a scope that moves from hub A to B while ready and
// returns to A before another ready transition never re-arms retention — both
// moves reset everReady and no transition occurs to earn it back — even though
// the return to A lands on a still-trusted scope whose screen shows. The next
// reconnect must banner that shown content, not wall it.
it("useConnectionDisplay: returning to a still-trusted hub re-arms the retention it showed under", () => {
	const client = {} as AppwireClient;
	let hubId = "hub-1";
	let state: ConnectionState = "ready";
	const display = renderHook(() => useConnectionDisplay(hubId, state, false, client));
	// Mount-already-ready: the settle earns trust and retention for hub-1.
	display.rerender();
	expect(display.result.current).toBe("none");

	// The re-key to hub-2 while the connection still reports hub-1 ready:
	// hub-2 never showed anything, so the moved renders wall.
	hubId = "hub-2";
	display.rerender();
	expect(display.result.current).toBe("wall");

	// The scope returns to hub-1 before any ready transition: the trust
	// never moved, so hub-1's ready state still vouches for it and the
	// screen shows again.
	hubId = "hub-1";
	display.rerender();
	expect(display.result.current).toBe("none");

	// The reconnect: hub-1 showed its screen, so the flap retains it behind
	// a banner instead of walling.
	state = "reconnecting";
	display.rerender();
	expect(display.result.current).toBe("banner");
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

// Round 59's Medium: the hooks recorded the pairing they observed against
// the ROUTE scope for ANY readiness state, so a connecting mount under
// hub-2's route attributed hub-1's client to hub-2 — and the record, which
// the store and the sign-in resume gate consult, then refused that client
// for its OWN hub until a reconnection re-dialed it. The record's writer is
// the connection layer (dial success, hubConnection); the hooks only consult.
it("never attributes a client to a hub it did not dial for", () => {
	const first = {} as AppwireClient;
	// A screen keyed to hub-2 mounts while the connection still reports
	// hub-1's client, mid-dial — the re-key outran the connection's
	// re-point, and nothing has proved ready under hub-2.
	const connecting = renderHook(() => useLiveReadiness("hub-2", first, "connecting"));
	connecting.rerender();
	// The pairing turns ready under hub-1 — the hub the connection dialed
	// the client for. The client must serve it: a connecting mount under
	// another hub's route must not have poisoned the record.
	const ready = renderHook(() => useLiveReadiness("hub-1", first, "ready"));
	expect(ready.result.current()).toBe(true);
});

// Round 59's Low: the bump compares against the last pairing the RENDER
// PHASE bumped for, so a same-pairing render cannot bump twice and
// over-invalidate the first committed render's callback. The memoized
// callback's identity is the observable: only a pairing move rebuilds it.
it("does not rebuild the readiness callback for a same-pairing rerender", () => {
	const first = {} as AppwireClient;
	const hook = renderHook(() => useLiveReadiness("hub-1", first, "ready"));
	// The settle births the pairing; the first same-pairing rerender hands
	// back the settled callback.
	act(() => {
		hook.rerender();
	});
	const settled = hook.result.current;
	expect(hook.result.current()).toBe(true);
	hook.rerender();
	expect(hook.result.current).toBe(settled);
	expect(hook.result.current()).toBe(true);
});
