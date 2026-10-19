import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import type { AnyNotification, AppwireClient, InitializeResponse } from "@evener/appwire-client";
import { KeybindingPreferencesScreen } from "./KeybindingPreferencesScreen";
import { textContrast } from "./design/contrast.testkit";
import { palettes } from "./design/tokens";
import { markers } from "./markerTestUtils";
import { NativePreferencesProvider } from "./NativePreferencesProvider";
import { dropped, render, screenConnection } from "./renderNative.testkit";

const harness = vi.hoisted(() => {
	const values = new Map<string, string>();
	const storage = {
		throwKey: null as string | null,
		getItemSync(key: string) {
			if (storage.throwKey === key) throw new Error("storage unavailable");
			return values.get(key) ?? null;
		},
		setItemSync(key: string, value: string) {
			values.set(key, value);
		},
		removeItemSync(key: string) {
			values.delete(key);
		},
	};
	return {
		connection: {} as Record<string, unknown>,
		scheme: "light" as "light" | "dark",
		storage,
		values,
	};
});

vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	KeyboardAvoidingView: "KeyboardAvoidingView",
	useColorScheme: () => harness.scheme,
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "screen-test-id" }));
vi.mock("expo-sqlite/kv-store", () => ({ Storage: harness.storage }));
vi.mock("./ConnectionProvider", () => ({
	useConnection: () => harness.connection,
}));

const initialize: InitializeResponse = {
	features: { keybindingsSettings: true, transcriptDisplaySettings: false },
} as InitializeResponse;
const hub = { id: "hub-1", name: "Work hub" };
const route = { params: { hubId: hub.id } };
const navigation = { setParams: vi.fn() };

function screen() {
	return (
		<KeybindingPreferencesScreen
			route={route as ComponentProps<typeof KeybindingPreferencesScreen>["route"]}
			navigation={navigation as unknown as ComponentProps<typeof KeybindingPreferencesScreen>["navigation"]}
		/>
	);
}

function app() {
	return <NativePreferencesProvider>{screen()}</NativePreferencesProvider>;
}

function alertNodes(tree: ReturnType<typeof render>) {
	return tree.root.findAllByProps({ accessibilityRole: "alert" });
}

function draftKey(hubId: string): string {
	return `evener.native.keybinding-draft.${hubId}`;
}

function writeDraft(hubId: string, raw: string): void {
	harness.values.set(draftKey(hubId), raw);
}

function clientFixture() {
	const readyListeners = new Set<(value: InitializeResponse) => void>();
	const notifications = new Set<(value: AnyNotification) => void>();
	let resolveConnected!: () => void;
	const connected = new Promise<void>((resolve) => {
		resolveConnected = resolve;
	});
	let requestWaiter: (() => void) | null = null;
	const client = {
		connect: async () => {
			for (const listener of readyListeners) listener(initialize);
			resolveConnected();
			return initialize;
		},
		onReady: (listener: (value: InitializeResponse) => void) => {
			readyListeners.add(listener);
			return () => readyListeners.delete(listener);
		},
		onNotification: (listener: (value: AnyNotification) => void) => {
			notifications.add(listener);
			return () => notifications.delete(listener);
		},
		request: async (method: string) => {
			requestWaiter?.();
			requestWaiter = null;
			if (method === "evener/settings/keybindings/get") return { version: 1, revision: 1, rules: [] };
			throw new Error(`Unexpected request: ${method}`);
		},
	} as unknown as AppwireClient;
	return {
		client,
		connected,
		nextRequest: () =>
			new Promise<void>((resolve) => {
				requestWaiter = resolve;
			}),
	};
}

beforeEach(() => {
	harness.values.clear();
	harness.storage.throwKey = null;
	harness.scheme = "light";
	harness.connection = {
		activeProfile: hub,
		client: null,
		state: "closed",
		retry: vi.fn(),
	};
});

it("lets a drag of the page put the keyboard away", () => {
	harness.connection = { activeProfile: hub, client: null, state: "ready", retry: vi.fn() };
	const tree = render(app());
	expect(tree.root.findAllByType("ScrollView" as never)[0]?.props.keyboardDismissMode).toBe("on-drag");
	expect(tree.root.findAllByType("ScrollView" as never)[0]?.props.automaticallyAdjustKeyboardInsets).toBe(true);
	expect(
		tree.root.findAllByType("KeyboardAvoidingView" as never).filter((node) => node.props.behavior === "padding"),
	).toHaveLength(0);
	act(() => tree.unmount());
});

it("keeps draft Save outside the scrolling settings and disabled offline", async () => {
	writeDraft(hub.id, JSON.stringify({ id: "draft-1", baseRevision: 1, rules: [], writeUncertain: false }));
	const connection = clientFixture();
	harness.connection = screenConnection(connection.client, "ready");
	const request = connection.nextRequest();
	const tree = render(app());
	await act(async () => {
		await connection.connected;
		await request;
	});
	const action = () => tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Save changes" });
	expect(action().props.disabled).toBe(false);
	for (let parent = action().parent; parent; parent = parent.parent) {
		expect(String(parent.type)).not.toBe("ScrollView");
	}
	act(() => {
		harness.connection = dropped(harness.connection);
		tree.update(app());
	});
	expect(action().props.disabled).toBe(true);
	expect(harness.values.has(draftKey(hub.id))).toBe(true);
	act(() => tree.unmount());
});

it.each(["light", "dark"] as const)("retains readable shortcut values while offline in %s", async (scheme) => {
	harness.scheme = scheme;
	const connection = clientFixture();
	harness.connection = screenConnection(connection.client, "ready");
	const request = connection.nextRequest();
	const tree = render(app());
	await act(async () => {
		await connection.connected;
		await request;
	});
	const shortcutRows = () =>
		tree.root
			.findAllByProps({ accessibilityRole: "button" })
			.filter((node) => node.findAllByProps({ testID: "row-sub" }).length > 0);
	const summaries = () => shortcutRows().map((node) => node.findByProps({ testID: "row-sub" }).props.children);
	const before = summaries();
	expect(before.length).toBeGreaterThan(0);
	for (const row of shortcutRows()) expect(row.props.disabled).toBe(false);
	act(() => {
		harness.connection = dropped(harness.connection);
		tree.update(app());
	});
	expect(summaries()).toEqual(before);
	for (const row of shortcutRows()) {
		expect(row.props.disabled).toBe(true);
		for (const node of row.findAllByType("Text" as never)) {
			expect(textContrast(node, palettes[scheme].surface)).toBeGreaterThanOrEqual(4.5);
		}
	}
	await act(async () => {
		harness.connection = screenConnection(connection.client, "ready");
		tree.update(app());
	});
	expect(summaries()).toEqual(before);
	for (const row of shortcutRows()) expect(row.props.disabled).toBe(false);
	act(() => tree.unmount());
});

it("shows and invokes cold-offline discard for an unreadable stored record", () => {
	writeDraft(hub.id, "{not json");
	const tree = render(app());
	const action = tree.root.findByProps({
		accessibilityLabel: "Discard unreadable draft",
	});

	expect(action.props.disabled).toBe(false);
	act(() => action.props.onPress());

	expect(harness.values.has(draftKey(hub.id))).toBe(false);
	expect(tree.root.findAllByProps({ accessibilityLabel: "Discard unreadable draft" })).toHaveLength(0);
	act(() => tree.unmount());
});

it("brings a failed pinned save into view without dropping the keyboard draft", async () => {
	writeDraft(hub.id, JSON.stringify({ id: "draft-1", baseRevision: 1, rules: [], writeUncertain: false }));
	const connection = clientFixture();
	harness.connection = screenConnection(connection.client, "ready");
	const request = connection.nextRequest();
	const scrollTo = vi.fn();
	const tree = render(app(), { createNodeMock: (element) => (element.type === "ScrollView" ? { scrollTo } : null) });
	await act(async () => {
		await connection.connected;
		await request;
	});
	await act(async () =>
		tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Save changes" }).props.onPress(),
	);
	expect(alertNodes(tree).length).toBeGreaterThan(0);
	expect(scrollTo).toHaveBeenCalledWith({ y: 0, animated: true });
	expect(harness.values.has(draftKey(hub.id))).toBe(true);
	act(() => tree.unmount());
});

it("keeps the storage-unavailable diagnostic after a discard-time storage throw", () => {
	// The probe that classified the record succeeded; the discard itself is
	// what hits a dead port. The outcome must not read as a successful
	// discard - the record is still stored and the unreadable-draft block
	// still offers the action - and the storage-unavailable diagnostic says
	// why nothing was removed, in the storage seam's own words rather than
	// the generic action-failed copy.
	writeDraft(hub.id, "{not json");
	const tree = render(app());
	const action = tree.root.findByProps({
		accessibilityLabel: "Discard unreadable draft",
	});

	harness.storage.throwKey = draftKey(hub.id);
	act(() => action.props.onPress());

	expect(harness.values.has(draftKey(hub.id))).toBe(true);
	expect(tree.root.findAllByProps({ accessibilityLabel: "Discard unreadable draft" })).toHaveLength(1);
	const alerts = alertNodes(tree);
	expect(alerts).toHaveLength(1);
	expect(markers(tree, "keybinding-error")).toEqual(["storage"]);
	act(() => tree.unmount());
});

it("drops the offline storage diagnostic after connected live recovery", async () => {
	harness.storage.throwKey = draftKey(hub.id);
	harness.connection = {
		activeProfile: hub,
		client: null,
		state: "ready",
		retry: vi.fn(),
	};
	const tree = render(app());
	expect(alertNodes(tree)).toHaveLength(1);

	const connection = clientFixture();
	harness.storage.throwKey = null;
	harness.connection = {
		activeProfile: hub,
		client: connection.client,
		state: "ready",
		retry: vi.fn(),
	};
	const initialRequest = connection.nextRequest();
	act(() => tree.update(app()));
	await act(async () => {
		await connection.connected;
		await initialRequest;
	});

	const checkCurrentShortcuts = tree.root.findByProps({
		accessibilityLabel: "Check current shortcuts",
	});
	expect(alertNodes(tree)).toHaveLength(0);

	const recoveryRequest = connection.nextRequest();
	await act(async () => {
		checkCurrentShortcuts.props.onPress();
		await recoveryRequest;
	});

	expect(alertNodes(tree)).toHaveLength(0);
	act(() => tree.unmount());
});

it("takes the store-free discard when the connection drops with a model retained", async () => {
	// #1860, the #1791 round-4 branch: once the connection drops, the client
	// object (and so `preferences.model`) can stay non-null while
	// `preferences.connected` is false. The screen must route the discard
	// through the store-free path in that state - the live path's `run` bails
	// on `!connected`, which would leave the unreadable record in place behind
	// a dead button.
	writeDraft(hub.id, "{not json");
	const connection = clientFixture();
	const live = screenConnection(connection.client, "ready");
	harness.connection = live;
	const initialRequest = connection.nextRequest();
	const tree = render(app());
	await act(async () => {
		await connection.connected;
		await initialRequest;
	});
	expect(tree.root.findAllByProps({ accessibilityLabel: "Discard unreadable draft" })).toHaveLength(1);

	// The connection drops; the same client object (and its model) is retained.
	harness.connection = dropped(live, "closed");
	act(() => tree.update(app()));
	// "Check current shortcuts" renders only for a non-null model, so its
	// presence is what makes this the retained-model case rather than a
	// never-connected one.
	expect(tree.root.findAllByProps({ accessibilityLabel: "Check current shortcuts" })).toHaveLength(1);

	const action = tree.root.findByProps({ accessibilityLabel: "Discard unreadable draft" });
	expect(action.props.disabled).toBe(false);
	act(() => {
		action.props.onPress();
	});

	// The store-free discard ran (the live path would have been dropped).
	expect(harness.values.has(draftKey(hub.id))).toBe(false);
	expect(tree.root.findAllByProps({ accessibilityLabel: "Discard unreadable draft" })).toHaveLength(0);
	act(() => tree.unmount());
});
