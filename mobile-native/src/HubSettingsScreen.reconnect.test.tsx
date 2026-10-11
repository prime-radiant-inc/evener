// Screen-level tests for the hub settings screen's reconnect recovery: the
// overview store keeps the last successful load through a failed refresh
// (hubOverview.ts), so a screen that keeps its data through a flap under the
// status line must re-read once the connection is ready again, and a new
// connection's client is still connecting when the stores around it first
// read. Mirrors
// hub/ProvidersPage.test.tsx's mocking; the focus effect stands in for
// @react-navigation/native's the way the installed hook (7.3.18) behaves -
// it runs the callback on mount and on every identity change while the
// screen is focused, calling the returned cleanup first, which is the re-run
// a client replacement triggers under a focused screen. The overview read is
// the only hub traffic the screen issues, so it is what the assertions count.
import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { ConnectionState } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { INCOMPATIBLE_VERSIONS } from "./connectionRecovery";
import { HubSettingsScreen } from "./HubSettingsScreen";
import { dropped, render, renderedText, screenConnection as connection } from "./renderNative.testkit";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
	focused: true,
}));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	RefreshControl: "RefreshControl",
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => harness.connection }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => void | (() => void)) =>
			useEffect(() => {
				if (!harness.focused) return;
				return effect();
			}, [effect, harness.focused]),
		useIsFocused: () => harness.focused,
	};
});
const props = {
	route: { params: { hubId: "hub-1" } },
	navigation: { navigate: () => {} },
} as unknown as ComponentProps<typeof HubSettingsScreen>;

it("re-reads the overview once a flap the screen survived is ready again", async () => {
	const hub = new FakeClient("ready");
	let reads = 0;
	hub.on("evener/settings/overview", () => {
		reads += 1;
		return {
			hub: {
				version: reads === 1 ? "1.2.3" : "9.9.9",
				daemonIdleTimeoutMillis: 3600000,
			},
		};
	});
	harness.connection = connection(hub, "ready");
	const tree = render(<HubSettingsScreen {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Evener 1.2.3");
	expect(reads).toBe(1);

	harness.connection = dropped(connection(hub, "ready"));
	await act(async () => {
		tree.update(<HubSettingsScreen {...props} />);
	});
	// Stale-but-shown: the banner sits over the last successful load, not a
	// wall; the hub may have moved on while this client was away.
	expect(renderedText(tree)).toContain("Evener 1.2.3");
	expect(renderedText(tree)).toContain("Reconnecting…");

	harness.connection = connection(hub, "ready");
	await act(async () => {
		tree.update(<HubSettingsScreen {...props} />);
	});
	await act(async () => {});
	await act(async () => {});
	expect(reads).toBe(2);
	expect(renderedText(tree)).toContain("Evener 9.9.9");
});

it("reads through a replacement client once its connection is ready", async () => {
	const first = new FakeClient("ready");
	first.on("evener/settings/overview", () => ({
		hub: { version: "1.2.3", daemonIdleTimeoutMillis: 3600000 },
	}));
	harness.connection = connection(first, "ready");
	const tree = render(<HubSettingsScreen {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Evener 1.2.3");

	// A new connection hands the screen a fresh client while it is still
	// connecting; the overview it owes can only land once the connection is
	// ready.
	const second = new FakeClient("connecting");
	second.on("evener/settings/overview", () => ({
		hub: { version: "9.9.9", daemonIdleTimeoutMillis: 3600000 },
	}));
	harness.connection = connection(second, "connecting");
	await act(async () => {
		tree.update(<HubSettingsScreen {...props} />);
	});
	second.state = "ready";
	harness.connection = connection(second, "ready");
	await act(async () => {
		tree.update(<HubSettingsScreen {...props} />);
	});
	await act(async () => {});
	await act(async () => {});
	expect(renderedText(tree)).toContain("Evener 9.9.9");
});

it("treats a route re-keyed to another hub as a fresh screen", async () => {
	const fakeA = new FakeClient("ready");
	fakeA.on("evener/settings/overview", () => ({
		hub: { version: "1.2.3", daemonIdleTimeoutMillis: 3600000 },
	}));
	harness.connection = connection(fakeA, "ready");
	const forHub = (hubId: string) =>
		({
			route: { params: { hubId } },
			navigation: { navigate: () => {} },
		}) as unknown as ComponentProps<typeof HubSettingsScreen>;
	const tree = render(<HubSettingsScreen {...forHub("hub-1")} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Evener 1.2.3");

	// The mounted instance is re-keyed to another hub while that hub's
	// connection is still opening. The retained client and the loaded
	// overview belong to hub-1: hub-2's screen must not render them, and
	// hub-1's client must not hear another request.
	const fakeB = new FakeClient("connecting");
	fakeB.on("evener/settings/overview", () => ({
		hub: { version: "9.9.9", daemonIdleTimeoutMillis: 3600000 },
	}));
	harness.connection = {
		activeProfile: { id: "hub-2", name: "Two hub" },
		client: null,
		state: "connecting",
		fatal: false,
		downSince: null,
		lastLiveAt: null,
	};
	await act(async () => {
		tree.update(<HubSettingsScreen {...forHub("hub-2")} />);
	});
	const rekeyed = renderedText(tree);
	expect(rekeyed).not.toContain("Evener 1.2.3");
	expect(rekeyed).toContain("Connecting to Two hub…");

	// The new hub is a fresh mount: its own overview renders once its
	// connection is ready.
	fakeB.state = "ready";
	harness.connection = {
		activeProfile: { id: "hub-2", name: "Two hub" },
		client: fakeB as unknown as ConversationClientLike,
		state: "ready",
		fatal: false,
		downSince: null,
		lastLiveAt: null,
	};
	await act(async () => {
		tree.update(<HubSettingsScreen {...forHub("hub-2")} />);
	});
	await act(async () => {});
	expect(renderedText(tree)).toContain("Evener 9.9.9");
});

it("reads the overview once on a mount that is already ready", async () => {
	// The ref that arms the ready-TRANSITION read seeds false, so a mount that
	// was already ready at first render could count as a transition too and
	// fire the transition read on top of the mount's own focus read.
	const hub = new FakeClient("ready");
	let reads = 0;
	hub.on("evener/settings/overview", () => {
		reads += 1;
		return {
			hub: { version: "1.2.3", daemonIdleTimeoutMillis: 3600000 },
		};
	});
	harness.connection = connection(hub, "ready");
	const tree = render(<HubSettingsScreen {...props} />);
	await act(async () => {});
	await act(async () => {});
	expect(renderedText(tree)).toContain("Evener 1.2.3");
	expect(reads).toBe(1);
});

it("reads a replacement recovery once while the screen is focused", async () => {
	const first = new FakeClient("ready");
	first.on("evener/settings/overview", () => ({
		hub: { version: "1.2.3", daemonIdleTimeoutMillis: 3600000 },
	}));
	harness.connection = connection(first, "ready");
	const tree = render(<HubSettingsScreen {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Evener 1.2.3");

	// A new connection hands the screen a fresh client while it is still
	// connecting, and the screen stays focused through the whole gap: the
	// replacement re-runs the focus effect in the same commit the ready
	// transition recovers in.
	const second = new FakeClient("connecting");
	let reads = 0;
	second.on("evener/settings/overview", () => {
		reads += 1;
		return {
			hub: { version: "9.9.9", daemonIdleTimeoutMillis: 3600000 },
		};
	});
	harness.connection = connection(second, "connecting");
	await act(async () => {
		tree.update(<HubSettingsScreen {...props} />);
	});
	second.state = "ready";
	harness.connection = connection(second, "ready");
	await act(async () => {
		tree.update(<HubSettingsScreen {...props} />);
	});
	await act(async () => {});
	await act(async () => {});
	expect(renderedText(tree)).toContain("Evener 9.9.9");
	// One event, one read: the replacement becoming ready under a focused
	// screen re-runs the focus effect in the same commit the transition
	// recovers in, and the two recovery paths coordinate to read once.
	expect(reads).toBe(1);
});

it("still reads on refocus after a replacement recovered while the screen was away", async () => {
	const first = new FakeClient("ready");
	first.on("evener/settings/overview", () => ({
		hub: { version: "1.2.3", daemonIdleTimeoutMillis: 3600000 },
	}));
	harness.focused = true;
	harness.connection = connection(first, "ready");
	const tree = render(<HubSettingsScreen {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Evener 1.2.3");

	// The screen loses focus before the retry, and the replacement becomes
	// ready while it is away: the transition recovers on its own, and no
	// focus read co-runs with it to consume or duplicate.
	harness.focused = false;
	const second = new FakeClient("connecting");
	let reads = 0;
	second.on("evener/settings/overview", () => {
		reads += 1;
		return {
			hub: { version: "9.9.9", daemonIdleTimeoutMillis: 3600000 },
		};
	});
	harness.connection = connection(second, "connecting");
	await act(async () => {
		tree.update(<HubSettingsScreen {...props} />);
	});
	second.state = "ready";
	harness.connection = connection(second, "ready");
	await act(async () => {
		tree.update(<HubSettingsScreen {...props} />);
	});
	await act(async () => {});
	const beforeRefocus = reads;

	// Coming back to the screen is its own event: the refocus read has to
	// survive the transition's recovery - the coordination must suppress
	// only the focus re-run that coincides with a transition, never the one
	// a user's return owes.
	harness.focused = true;
	await act(async () => {
		tree.update(<HubSettingsScreen {...props} />);
	});
	await act(async () => {});
	expect(reads).toBe(beforeRefocus + 1);
	harness.focused = true;
});

it("recovers the overview while the screen is mounted but not focused", async () => {
	// An unfocused, mounted settings screen behind another screen can hold a
	// valid ready connection: the live predicate authorizes, but no focus
	// effect runs to read with it, and the recovery arm skipped on the
	// authorization alone - so nothing read until a later focus. The arm must
	// read exactly when no focus effect handled the same connection.
	const hub = new FakeClient("ready");
	let reads = 0;
	hub.on("evener/settings/overview", () => {
		reads += 1;
		return {
			hub: {
				version: reads === 1 ? "1.2.3" : "9.9.9",
				daemonIdleTimeoutMillis: 3600000,
			},
		};
	});
	harness.focused = false;
	harness.connection = connection(hub, "ready");
	const tree = render(<HubSettingsScreen {...props} />);
	await act(async () => {});
	await act(async () => {});
	// The mount under an authorized, unfocused screen still reads: no focus
	// effect exists to owe this screen's first read.
	expect(reads).toBe(1);
	expect(renderedText(tree)).toContain("Evener 1.2.3");

	// The flap lands entirely in the unfocused stretch; the transition back
	// to ready is recovered the same way.
	const conn = harness.connection as { state: ConnectionState };
	conn.state = "reconnecting";
	await act(async () => {
		tree.update(<HubSettingsScreen {...props} />);
	});
	conn.state = "ready";
	await act(async () => {
		tree.update(<HubSettingsScreen {...props} />);
	});
	await act(async () => {});
	expect(reads).toBe(2);
	expect(renderedText(tree)).toContain("Evener 9.9.9");
	harness.focused = true;
});

it("recovers a client replaced while the connection stays ready", async () => {
	const first = new FakeClient("ready");
	first.on("evener/settings/overview", () => ({
		hub: { version: "1.2.3", daemonIdleTimeoutMillis: 3600000 },
	}));
	harness.focused = true;
	harness.connection = connection(first, "ready");
	const tree = render(<HubSettingsScreen {...props} />);
	await act(async () => {});
	await act(async () => {});
	expect(renderedText(tree)).toContain("Evener 1.2.3");

	// The retry hands the screen a fresh client with the connection never
	// leaving ready: the readiness never transitions, so a state-keyed
	// recovery gate never resets - and the new pairing's focus read is
	// refused while it settles, with no later event to re-run it. The new
	// client is its own recovery event.
	const second = new FakeClient("ready");
	let reads = 0;
	second.on("evener/settings/overview", () => {
		reads += 1;
		return {
			hub: { version: "9.9.9", daemonIdleTimeoutMillis: 3600000 },
		};
	});
	harness.connection = connection(second, "ready");
	await act(async () => {
		tree.update(<HubSettingsScreen {...props} />);
	});
	await act(async () => {});
	await act(async () => {});
	expect(reads).toBe(1);
	expect(renderedText(tree)).toContain("Evener 9.9.9");
});

it("says it is connecting, with no wall and nothing to press, before the first load", async () => {
	harness.connection = { ...connection(null, "connecting"), client: null };
	const tree = render(<HubSettingsScreen {...props} />);
	await act(async () => {});
	const text = renderedText(tree);
	expect(text).toContain("Connecting to Work hub…");
	expect(text).not.toMatch(/\bReconnect\b|Connect to Work hub/);
});

it("says why when the hub's version doesn't match before anything loaded", async () => {
	harness.connection = { ...connection(null, "closed"), client: null, fatal: true };
	const tree = render(<HubSettingsScreen {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain(INCOMPATIBLE_VERSIONS);
	expect(renderedText(tree)).not.toContain("Connecting to Work hub");
});

it("keeps the last hub information under the status line when the versions stop matching", async () => {
	const hub = new FakeClient("ready");
	hub.on("evener/settings/overview", () => ({ hub: { version: "1.2.3", daemonIdleTimeoutMillis: 3600000 } }));
	harness.connection = connection(hub, "ready");
	const tree = render(<HubSettingsScreen {...props} />);
	await act(async () => {});
	harness.connection = { ...dropped(connection(hub, "ready"), "closed"), fatal: true };
	await act(async () => {
		tree.update(<HubSettingsScreen {...props} />);
	});
	const text = renderedText(tree);
	expect(text).toContain("Evener 1.2.3");
	expect(text).toContain("Update needed");
});

it("offers no pull to refresh: the screen keeps itself current", async () => {
	const hub = new FakeClient("ready");
	hub.on("evener/settings/overview", () => ({ hub: { version: "1.2.3", daemonIdleTimeoutMillis: 3600000 } }));
	harness.connection = connection(hub, "ready");
	const tree = render(<HubSettingsScreen {...props} />);
	await act(async () => {});
	// The list's own pull-to-refresh is a prop, not a child it renders.
	expect(
		tree.root.findAll((node) => node.props.refreshControl !== undefined || node.props.onRefresh !== undefined),
	).toEqual([]);
});

it("says plainly when the hub's information didn't load, and loads it on Retry", async () => {
	const hub = new FakeClient("ready");
	let fail = true;
	hub.on("evener/settings/overview", () => {
		if (fail) throw new Error("overview unavailable");
		return { hub: { version: "1.2.3", daemonIdleTimeoutMillis: 3600000 } };
	});
	harness.connection = connection(hub, "ready");
	const tree = render(<HubSettingsScreen {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Could not load hub information.");
	expect(renderedText(tree)).not.toMatch(/Try again when connected/);

	fail = false;
	const retry = tree.root.findAll((node) => node.props.accessibilityRole === "button" && node.props.onPress)[0];
	await act(async () => retry?.props.onPress());
	await act(async () => {});
	expect(renderedText(tree)).toContain("Evener 1.2.3");
});
