import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import type {
	AnyNotification,
	AppwireClient,
	InitializeResponse,
} from "@evener/appwire-client";
import { KeybindingPreferencesScreen } from "./KeybindingPreferencesScreen";
import { NativePreferencesProvider } from "./NativePreferencesProvider";
import { render, renderedText } from "./renderNative.testkit";

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
		storage,
		values,
	};
});

vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	KeyboardAvoidingView: "KeyboardAvoidingView",
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
			navigation={
				navigation as unknown as ComponentProps<typeof KeybindingPreferencesScreen>["navigation"]
			}
		/>
	);
}

function app() {
	return (
		<NativePreferencesProvider>
			{screen()}
		</NativePreferencesProvider>
	);
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
			if (method === "evener/settings/keybindings/get")
				return { version: 1, revision: 1, rules: [] };
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
	harness.connection = {
		activeProfile: hub,
		client: null,
		state: "closed",
		retry: vi.fn(),
	};
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
	expect(
		tree.root.findAllByProps({ accessibilityLabel: "Discard unreadable draft" }),
	).toHaveLength(0);
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
	expect(
		tree.root.findAllByProps({ accessibilityLabel: "Discard unreadable draft" }),
	).toHaveLength(1);
	const alerts = alertNodes(tree);
	expect(alerts).toHaveLength(1);
	const text = renderedText(tree);
	expect(text).toContain(
		"Could not read the saved shortcut draft on this phone. Check current shortcuts to retry.",
	);
	expect(text).not.toContain("The change could not be completed.");
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
