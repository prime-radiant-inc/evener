// Screen-level tests for the hub settings screen's reconnect recovery: the
// overview store keeps the last successful load through a failed refresh
// (hubOverview.ts), so a screen that survives a flap behind a banner must
// re-read once the connection is ready again - the wall this screen used to
// show remounted the store instead, and a manual retry's replacement client
// is still connecting when the stores around it first read. Mirrors
// ProvidersScreen.test.tsx's mocking; the focus effect stands in for
// @react-navigation/native's the way the installed hook (7.3.18) behaves -
// it runs the callback on mount and on every identity change while the
// screen is focused, calling the returned cleanup first, which is the re-run
// a client replacement triggers under a focused screen - and the upgrade
// controller is stubbed so the overview read is the only hub traffic the
// assertions count.
import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { ConnectionState } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { HubSettingsScreen } from "./HubSettingsScreen";
import type { UpgradeState } from "./hubUpgrade";
import {
	alertRequests,
	nativeModuleMock,
	render,
	renderedText,
	screenConnection as connection,
} from "./renderNative.testkit";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
	focused: true,
}));
const reconciles = vi.hoisted(() => ({ count: 0 }));
const starts = vi.hoisted(() => ({ count: 0 }));
const upgrade = vi.hoisted(() => ({
	snapshot: { kind: "idle" } as UpgradeState,
}));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	RefreshControl: "RefreshControl",
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "fixture-uuid" }));
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
vi.mock("./nativeHubUpgrade", () => ({ nativeHubUpgradeStorage: {} }));
vi.mock("./hubUpgrade", () => ({
	// useSyncExternalStore re-checks getSnapshot after every render, so the
	// snapshot must be one stable reference until a test swaps it; a fresh
	// object each call reads as a store that never stops changing.
	createHubUpgradeController: () => ({
		subscribe: () => () => {},
		getSnapshot: () => upgrade.snapshot,
		start: () => {
			starts.count += 1;
		},
		reconcileAfterReconnect: () => {
			reconciles.count += 1;
		},
		reviewAnotherUpdate: async () => null,
		rearm: () => {},
		dispose: () => {},
	}),
}));

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

	harness.connection = connection(hub, "reconnecting");
	await act(async () => {
		tree.update(<HubSettingsScreen {...props} />);
	});
	// Stale-but-shown: the banner sits over the last successful load, not a
	// wall; the hub may have moved on while this client was away.
	expect(renderedText(tree)).toContain("Evener 1.2.3");
	expect(renderedText(tree)).toContain("reconnecting");

	harness.connection = connection(hub, "ready");
	await act(async () => {
		tree.update(<HubSettingsScreen {...props} />);
	});
	await act(async () => {});
	await act(async () => {});
	expect(reads).toBe(2);
	expect(renderedText(tree)).toContain("Evener 9.9.9");
	// The upgrade section reconciles against the hub the reconnection left
	// behind, not the version the banner preserved.
	expect(reconciles.count).toBeGreaterThan(0);
	reconciles.count = 0;
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

	// A manual retry hands the screen a fresh client while it is still
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
	expect(reconciles.count).toBeGreaterThan(0);
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
		retry: () => {},
	};
	await act(async () => {
		tree.update(<HubSettingsScreen {...forHub("hub-2")} />);
	});
	const rekeyed = renderedText(tree);
	expect(rekeyed).not.toContain("Evener 1.2.3");
	expect(rekeyed).toContain("to view hub settings.");

	// The new hub is a fresh mount: its own overview renders once its
	// connection is ready.
	fakeB.state = "ready";
	harness.connection = {
		activeProfile: { id: "hub-2", name: "Two hub" },
		client: fakeB as unknown as ConversationClientLike,
		state: "ready",
		fatal: false,
		retry: () => {},
	};
	await act(async () => {
		tree.update(<HubSettingsScreen {...forHub("hub-2")} />);
	});
	await act(async () => {});
	expect(renderedText(tree)).toContain("Evener 9.9.9");
});

it("gates the upgrade start while the connection is away, not the recovery reads", async () => {
	const hub = new FakeClient("ready");
	hub.on("evener/settings/overview", () => ({
		hub: { version: "1.2.3", daemonIdleTimeoutMillis: 3600000 },
	}));
	// The controls exist only behind a banner - a flap the screen survived
	// after showing something - so each tree mounts ready, opens the hub
	// update section (it starts collapsed, per Section's own state), and
	// only then drops to reconnecting.
	async function mountFlapping() {
		harness.connection = connection(hub, "ready");
		const tree = render(<HubSettingsScreen {...props} />);
		await act(async () => {});
		const section = tree.root.find(
			(node) => node.props.accessibilityLabel === "Hub update",
		);
		act(() => {
			section.props.onPress();
		});
		harness.connection = connection(hub, "reconnecting");
		await act(async () => {
			tree.update(<HubSettingsScreen {...props} />);
		});
		return tree;
	}
	const tree = await mountFlapping();

	// The start is the one control that persists a checkpoint before its RPC
	// (hubUpgrade.ts's start): pressed while the connection is away, it
	// strands a false "uncertain" upgrade in storage - the RPC never had a
	// chance to reach the hub, and only a manual refresh recovers it.
	const start = tree.root.find(
		(node) => node.props.accessibilityLabel === "Upgrade hub",
	);
	expect(start.props.disabled).toBe(true);

	// The reads stay pressable: they fail honestly while away, and the
	// refresh is reconcileAfterReconnect - the remedy path itself.
	upgrade.snapshot = {
		kind: "uncertain",
		message: "An upgrade may have been installed. Reconnect and verify.",
	};
	const remedies = await mountFlapping();
	for (const label of ["Refresh running version", "Review another update"]) {
		const control = remedies.root.find(
			(node) => node.props.accessibilityLabel === label,
		);
		expect(control.props.disabled).toBe(false);
	}
	upgrade.snapshot = { kind: "idle" };
});

it("refuses an upgrade confirmation that outlives the connection it was opened on", async () => {
	const hub = new FakeClient("ready");
	hub.on("evener/settings/overview", () => ({
		hub: { version: "1.2.3", daemonIdleTimeoutMillis: 3600000 },
	}));
	starts.count = 0;
	harness.connection = connection(hub, "ready");
	const tree = render(<HubSettingsScreen {...props} />);
	await act(async () => {});
	const section = tree.root.find(
		(node) => node.props.accessibilityLabel === "Hub update",
	);
	act(() => {
		section.props.onPress();
	});
	const open = tree.root.find(
		(node) => node.props.accessibilityLabel === "Upgrade hub",
	);
	act(() => {
		open.props.onPress();
	});

	// The connection drops while the confirmation is still open. The alert
	// holds the callback it was opened with, but that callback must read
	// readiness when it fires, not when the alert opened - or confirming now
	// persists a checkpoint for an RPC that cannot reach the hub
	// (hubUpgrade.ts start).
	harness.connection = connection(hub, "reconnecting");
	await act(async () => {
		tree.update(<HubSettingsScreen {...props} />);
	});
	const request = alertRequests.at(-1);
	const confirm = request?.buttons?.find((button) => button.text === "Upgrade");
	const confirmPress = confirm?.onPress;
	if (!confirmPress) throw new Error("the upgrade confirmation was not opened");
	act(() => {
		confirmPress();
	});
	expect(starts.count).toBe(0);
});

it("reads the overview and reconcile once on a mount that is already ready", async () => {
	// A pending upgrade checkpoint is what makes the mount's reconcile a real
	// wire read: with one in storage the controller's first snapshot is
	// "uncertain" and every reconcileAfterReconnect reads the overview
	// (hubUpgrade.ts), so an already-ready mount owes exactly one read-set -
	// the mount's own focus read. The ref that arms the ready-TRANSITION read
	// seeds false, so a mount that was already ready at first render counted
	// as a transition too and fired the transition read on top of the mount
	// read: two overview/reconcile reads where one was owed.
	upgrade.snapshot = {
		kind: "uncertain",
		message: "An upgrade may have been installed. Reconnect and verify.",
	};
	reconciles.count = 0;
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
	// One overview refresh (the store joins a second refresh into the
	// in-flight one) and one upgrade reconcile: a mount that was already
	// ready at first render has no ready transition to recover from.
	expect(reads).toBe(1);
	expect(reconciles.count).toBe(1);
	upgrade.snapshot = { kind: "idle" };
});

it("reconciles a replacement recovery exactly once while the screen is focused", async () => {
	const first = new FakeClient("ready");
	first.on("evener/settings/overview", () => ({
		hub: { version: "1.2.3", daemonIdleTimeoutMillis: 3600000 },
	}));
	harness.connection = connection(first, "ready");
	const tree = render(<HubSettingsScreen {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Evener 1.2.3");
	reconciles.count = 0;

	// A manual retry hands the screen a fresh client while it is still
	// connecting, and the screen stays focused through the whole gap: the
	// replacement re-runs the focus effect in the same commit the ready
	// transition recovers in.
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
	// One event, one read-set: the controller does not deduplicate
	// concurrent reconciles (hubUpgrade.ts bumps its generation per call and
	// drops the earlier answer), so the two recovery paths have to
	// coordinate - a replacement that becomes ready under a focused screen
	// must not issue two reconciles that race to invalidate each other.
	expect(reconciles.count).toBe(1);
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
	reconciles.count = 0;

	// Coming back to the screen is its own event: the refocus read has to
	// survive the transition's recovery - the coordination must suppress
	// only the focus re-run that coincides with a transition, never the one
	// a user's return owes.
	harness.focused = true;
	await act(async () => {
		tree.update(<HubSettingsScreen {...props} />);
	});
	await act(async () => {});
	expect(reconciles.count).toBe(1);
	harness.focused = true;
});

it("recovers the overview while the screen is mounted but not focused", async () => {
	// An unfocused, mounted settings screen behind another screen can hold a
	// valid ready connection: the live predicate authorizes, but no focus
	// effect runs to read with it, and the recovery arm skipped on the
	// authorization alone - so nothing read until a later focus. The arm must
	// read exactly when no focus effect handled the same connection.
	reconciles.count = 0;
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
	reconciles.count = 0;
	conn.state = "ready";
	await act(async () => {
		tree.update(<HubSettingsScreen {...props} />);
	});
	await act(async () => {});
	expect(reads).toBe(2);
	expect(renderedText(tree)).toContain("Evener 9.9.9");
	expect(reconciles.count).toBeGreaterThan(0);
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
