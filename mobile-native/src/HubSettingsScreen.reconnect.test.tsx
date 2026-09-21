// Screen-level tests for the hub settings screen's reconnect recovery: the
// overview store keeps the last successful load through a failed refresh
// (hubOverview.ts), so a screen that survives a flap behind a banner must
// re-read once the connection is ready again - the wall this screen used to
// show remounted the store instead, and a manual retry's replacement client
// is still connecting when the stores around it first read. Mirrors
// ProvidersScreen.test.tsx's mocking; the focus effect stands in for
// @react-navigation/native's, and the upgrade controller is stubbed so the
// overview read is the only hub traffic the assertions count.
import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { ConnectionState } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { HubSettingsScreen } from "./HubSettingsScreen";
import { nativeModuleMock, render, renderedText } from "./renderNative.testkit";

const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown> }));
const reconciles = vi.hoisted(() => ({ count: 0 }));
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
		useFocusEffect: (effect: () => void | (() => void)) => useEffect(effect, []),
	};
});
vi.mock("./nativeHubUpgrade", () => ({ nativeHubUpgradeStorage: {} }));
vi.mock("./hubUpgrade", () => ({
	createHubUpgradeController: () => {
		// useSyncExternalStore re-checks getSnapshot after every render; a
		// fresh object each call reads as a store that never stops changing.
		const idle = { kind: "idle" };
		return {
			subscribe: () => () => {},
			getSnapshot: () => idle,
			start: async () => {},
			reconcileAfterReconnect: () => {
				reconciles.count += 1;
			},
			reviewAnotherUpdate: async () => null,
			rearm: () => {},
			dispose: () => {},
		};
	},
}));

const props = {
	route: { params: { hubId: "hub-1" } },
	navigation: { navigate: () => {} },
} as unknown as ComponentProps<typeof HubSettingsScreen>;

function connection(client: unknown, state: ConnectionState) {
	return {
		activeProfile: { id: "hub-1", name: "Work hub" },
		client,
		state,
		fatal: false,
		retry: () => {},
	};
}

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
