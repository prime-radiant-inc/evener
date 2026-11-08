import { createElement } from "react";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AnyNotification, AppwireClient, InitializeResponse } from "@evener/appwire-client";
import { NativePreferencesProvider, useNativePreferences } from "./NativePreferencesProvider";
import { render, unmountMountedTrees } from "./renderNative.testkit";

const harness = vi.hoisted(() => {
	const values = new Map<string, string>();
	const storage = {
		getItemSync(key: string) {
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
		connection: null as unknown as {
			activeProfile: { id: string } | null;
			client: AppwireClient | null;
			state: string;
		},
		storage,
		values,
	};
});

vi.mock("expo-sqlite/kv-store", () => ({ Storage: harness.storage }));
vi.mock("./ConnectionProvider", () => ({
	useConnection: () => harness.connection,
}));

const initialize: InitializeResponse = {
	features: { keybindingsSettings: true, transcriptDisplaySettings: false },
} as InitializeResponse;

function clientFixture() {
	const readyListeners = new Set<(value: InitializeResponse) => void>();
	const notifications = new Set<(value: AnyNotification) => void>();
	const requests: Array<{ method: string; params: unknown }> = [];
	let resolveConnected!: () => void;
	const connected = new Promise<void>((resolve) => {
		resolveConnected = resolve;
	});
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
		request: async (method: string, params: unknown) => {
			requests.push({ method, params });
			if (method === "evener/settings/keybindings/get") return { version: 1, revision: 1, rules: [] };
			if (method === "evener/settings/keybindings/patch") {
				const patch = params as { config: { rules: { action: string; chord: string | null }[] } };
				return { version: 1, revision: 2, rules: patch.config.rules };
			}
			throw new Error(`Unexpected request: ${method}`);
		},
	} as unknown as AppwireClient;
	return { client, connected, requests };
}

function mountProvider() {
	let current!: ReturnType<typeof useNativePreferences>;
	function Probe() {
		current = useNativePreferences();
		return null;
	}
	const app = () => createElement(NativePreferencesProvider, null, createElement(Probe));
	const tree = render(app());
	return {
		get current() {
			return current;
		},
		unmount() {
			act(() => tree.unmount());
		},
	};
}

beforeEach(() => {
	harness.values.clear();
	harness.connection = {
		activeProfile: null,
		client: null,
		state: "closed",
	};
});

afterEach(() => {
	unmountMountedTrees();
});

describe("NativePreferencesProvider", () => {
	it("publishes the confirmed shortcuts and applies a host edit through the one PATCH path", async () => {
		const connection = clientFixture();
		harness.connection = { activeProfile: { id: "hub-a" }, client: connection.client, state: "ready" };
		const mounted = mountProvider();
		await act(async () => {
			await connection.connected;
		});
		expect(mounted.current.snapshot?.keybindings.confirmed).toMatchObject({ revision: 1, rules: [] });

		const rules = [{ action: "composer.focus", chord: "Meta+P" }];
		await act(async () => {
			await mounted.current.model?.editKeybinding("composer.focus", "Meta+P");
		});

		expect(mounted.current.snapshot?.keybindings.confirmed).toMatchObject({ revision: 2, rules });
		expect(
			connection.requests.filter((request) => request.method === "evener/settings/keybindings/patch"),
		).toHaveLength(1);
		// The immediate write holds no phone-side draft record.
		expect([...harness.values.keys()].filter((key) => key.includes("keybinding-draft"))).toEqual([]);
		mounted.unmount();
	});

	it("surfaces a pre-ruling keybinding checkpoint for recovery without replaying it", async () => {
		const saved = {
			id: "draft-1",
			baseRevision: 3,
			rules: [{ action: "palette.open", chord: "Meta+Shift+P" }],
			writeUncertain: true,
		};
		harness.values.set("evener.native.keybinding-draft.hub-a", JSON.stringify(saved));
		const connection = clientFixture();
		harness.connection = { activeProfile: { id: "hub-a" }, client: connection.client, state: "ready" };
		const mounted = mountProvider();
		await act(async () => {
			await connection.connected;
		});

		expect(mounted.current.legacyKeybindingDraft).toMatchObject({
			outcome: "readable",
			checkpoint: saved,
		});
		// Merely finding the record must not replay it.
		expect(
			connection.requests.filter((request) => request.method === "evener/settings/keybindings/patch"),
		).toHaveLength(0);

		// The user applies it explicitly through the one immediate path.
		await act(async () => {
			await mounted.current.model?.applySavedKeybindings(saved.rules);
		});
		expect(
			connection.requests.filter((request) => request.method === "evener/settings/keybindings/patch"),
		).toHaveLength(1);

		act(() => {
			expect(mounted.current.discardLegacyKeybindingDraft()).toBe("removed");
		});
		expect(harness.values.has("evener.native.keybinding-draft.hub-a")).toBe(false);
		expect(mounted.current.legacyKeybindingDraft).toEqual({ outcome: "absent" });
		mounted.unmount();
	});

	it("discards an unreadable pre-ruling checkpoint with no hub connected", () => {
		harness.values.set("evener.native.keybinding-draft.hub-a", "{not json");
		harness.connection = { activeProfile: { id: "hub-a" }, client: null, state: "ready" };
		const mounted = mountProvider();

		expect(mounted.current.legacyKeybindingDraft?.outcome).toBe("unreadable");
		act(() => {
			expect(mounted.current.discardLegacyKeybindingDraft()).toBe("removed");
		});
		expect(harness.values.has("evener.native.keybinding-draft.hub-a")).toBe(false);
		expect(mounted.current.legacyKeybindingDraft).toEqual({ outcome: "absent" });
		mounted.unmount();
	});
});
