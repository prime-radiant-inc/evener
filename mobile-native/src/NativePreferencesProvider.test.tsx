import { createElement } from "react";
import { act, create, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type {
	AnyNotification,
	AppwireClient,
	InitializeResponse,
} from "@evener/appwire-client";
import {
	NativePreferencesProvider,
	useNativePreferences,
} from "./NativePreferencesProvider";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
	true;

const harness = vi.hoisted(() => {
	const values = new Map<string, string>();
	const storage = {
		throwOnGet: false,
		getItemSync(key: string) {
			if (storage.throwOnGet) throw new Error("storage unavailable");
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

vi.mock("expo-crypto", () => ({ randomUUID: () => "provider-test-id" }));
vi.mock("expo-sqlite/kv-store", () => ({ Storage: harness.storage }));
vi.mock("./ConnectionProvider", () => ({
	useConnection: () => harness.connection,
}));

const initialize: InitializeResponse = {
	features: { keybindingsSettings: true, transcriptDisplaySettings: false },
} as InitializeResponse;

function clientFixture({ deferred = false, failed = false } = {}) {
	const readyListeners = new Set<(value: InitializeResponse) => void>();
	const notifications = new Set<(value: AnyNotification) => void>();
	let resolveConnected!: () => void;
	const connected = new Promise<void>((resolve) => {
		resolveConnected = resolve;
	});
	let resolveConnect!: (value: InitializeResponse) => void;
	const connectResult = deferred
		? new Promise<InitializeResponse>((resolve) => {
				resolveConnect = resolve;
			})
		: Promise.resolve(initialize);
	const requests: string[] = [];
	const client = {
		connect: async () => {
			if (failed) {
				throw new Error("handshake failed");
			}
			if (!deferred) {
				for (const listener of readyListeners) listener(initialize);
				resolveConnected();
			} else {
				await connectResult;
			}
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
			requests.push(method);
			if (method === "evener/settings/keybindings/get")
				return { version: 1, revision: 1, rules: [] };
			throw new Error(`Unexpected request: ${method}`);
		},
	} as unknown as AppwireClient;
	return {
		client,
		connected,
		requests,
		resolveConnect: (value = initialize) => {
			for (const listener of readyListeners) listener(value);
			resolveConnect(value);
			resolveConnected();
		},
	};
}

function draftKey(hubId: string): string {
	return `evener.native.keybinding-draft.${hubId}`;
}

function writeDraft(hubId: string, value: unknown): void {
	harness.values.set(draftKey(hubId), JSON.stringify(value));
}

function mountProvider() {
	let current!: ReturnType<typeof useNativePreferences>;
	function Probe() {
		current = useNativePreferences();
		return null;
	}
	const app = () =>
		createElement(NativePreferencesProvider, null, createElement(Probe));
	let tree!: ReactTestRenderer;
	act(() => {
		tree = create(app());
	});
	return {
		get current() {
			return current;
		},
		rerender() {
			act(() => tree.update(app()));
		},
		unmount() {
			act(() => tree.unmount());
		},
	};
}

async function settleConnection(connection: ReturnType<typeof clientFixture>) {
	await act(async () => {
		await connection.connected;
	});
}

beforeEach(() => {
	harness.values.clear();
	harness.storage.throwOnGet = false;
	harness.connection = {
		activeProfile: null,
		client: null,
		state: "closed",
	};
});

afterEach(() => {
	harness.storage.throwOnGet = false;
});

describe("NativePreferencesProvider offline draft plumbing", () => {
	it("probes an unreadable record and clears it through the store-free discard", () => {
		writeDraft("hub-a", "{not json");
		harness.connection = {
			activeProfile: { id: "hub-a" },
			client: null,
			state: "closed",
		};
		const mounted = mountProvider();

		expect(mounted.current.offlineDraftUnreadable).toBe(true);
		let outcome: unknown;
		act(() => {
			outcome = mounted.current.discardUnreadableKeybindingsDraft();
		});
		expect(outcome).toBe("removed");
		expect(harness.values.has(draftKey("hub-a"))).toBe(false);
		expect(mounted.current.offlineDraftUnreadable).toBe(false);
		mounted.unmount();
	});

	it("treats a stored JSON null as unreadable and clears it offline", () => {
		writeDraft("hub-null", null);
		harness.connection = {
			activeProfile: { id: "hub-null" },
			client: null,
			state: "closed",
		};
		const mounted = mountProvider();

		expect(mounted.current.offlineDraftUnreadable).toBe(true);
		let outcome: unknown;
		act(() => {
			outcome = mounted.current.discardUnreadableKeybindingsDraft();
		});
		expect(outcome).toBe("removed");
		expect(harness.values.has(draftKey("hub-null"))).toBe(false);
		expect(mounted.current.offlineDraftUnreadable).toBe(false);
		mounted.unmount();
	});

	it("preserves a readable replacement instead of nudging a matching live model", async () => {
		writeDraft("hub-a", {
			id: "draft-1",
			baseRevision: 1,
			rules: [],
			writeUncertain: false,
		});
		const connection = clientFixture();
		harness.connection = {
			activeProfile: { id: "hub-a" },
			client: connection.client,
			state: "ready",
		};
		const mounted = mountProvider();
		await settleConnection(connection);

		expect(mounted.current.model?.getSnapshot().keybindings.draft).not.toBeNull();
		let outcome: unknown;
		act(() => {
			outcome = mounted.current.discardUnreadableKeybindingsDraft();
		});
		expect(outcome).toBe("refused");
		expect(harness.values.has(draftKey("hub-a"))).toBe(true);
		expect(mounted.current.model?.getSnapshot().keybindings.draft).not.toBeNull();
		mounted.unmount();
	});

	it("reclassifies a live unreadable model when a readable replacement is found", async () => {
		writeDraft("hub-a", "{not json");
		const connection = clientFixture();
		harness.connection = {
			activeProfile: { id: "hub-a" },
			client: connection.client,
			state: "ready",
		};
		const mounted = mountProvider();
		await settleConnection(connection);

		expect(mounted.current.model?.getSnapshot().keybindings).toMatchObject({
			draft: null,
			storageUnavailable: true,
		});
		writeDraft("hub-a", {
			id: "replacement",
			baseRevision: 2,
			rules: [],
			writeUncertain: false,
		});

		let outcome: unknown;
		act(() => {
			outcome = mounted.current.discardUnreadableKeybindingsDraft();
		});
		expect(outcome).toBe("refused");
		expect(harness.values.has(draftKey("hub-a"))).toBe(true);
		expect(mounted.current.model?.getSnapshot().keybindings).toMatchObject({
			draft: { revision: 2, rules: [] },
			storageUnavailable: false,
		});
		mounted.unmount();
	});

	it("reconciles a readable replacement into the retained snapshot while the same-hub client waits", async () => {
		writeDraft("hub-a", "{not json");
		const first = clientFixture();
		harness.connection = {
			activeProfile: { id: "hub-a" },
			client: first.client,
			state: "ready",
		};
		const mounted = mountProvider();
		await settleConnection(first);
		const confirmed = mounted.current.snapshot?.keybindings.confirmed;
		expect(confirmed).toEqual({ version: 1, revision: 1, rules: [] });

		writeDraft("hub-a", {
			id: "replacement",
			baseRevision: 2,
			rules: [],
			writeUncertain: true,
		});
		const replacement = clientFixture({ deferred: true });
		harness.connection = {
			activeProfile: { id: "hub-a" },
			client: replacement.client,
			state: "closed",
		};
		mounted.rerender();

		expect(mounted.current.model).toBeNull();
		expect(mounted.current.snapshot?.keybindings).toMatchObject({
			confirmed,
			draft: { revision: 2, rules: [] },
			writeUncertain: true,
			storageUnavailable: false,
		});
		mounted.unmount();
	});

	it("clears the retained unreadable projection after a failed same-hub replacement removes the record", async () => {
		writeDraft("hub-a", "{not json");
		const first = clientFixture();
		harness.connection = {
			activeProfile: { id: "hub-a" },
			client: first.client,
			state: "ready",
		};
		const mounted = mountProvider();
		await settleConnection(first);
		const confirmed = mounted.current.snapshot?.keybindings.confirmed;
		expect(mounted.current.snapshot?.keybindings.storageUnavailable).toBe(true);

		const replacement = clientFixture({ failed: true });
		harness.connection = {
			activeProfile: { id: "hub-a" },
			client: replacement.client,
			state: "closed",
		};
		mounted.rerender();
		let outcome: unknown;
		act(() => {
			outcome = mounted.current.discardUnreadableKeybindingsDraft();
		});

		expect(outcome).toBe("removed");
		expect(mounted.current.model).toBeNull();
		expect(mounted.current.snapshot?.keybindings).toMatchObject({
			confirmed,
			draft: null,
			writeUncertain: false,
			storageUnavailable: false,
		});
		mounted.unmount();
	});

	it("marks only the retained draft projection unavailable when replacement storage throws", async () => {
		writeDraft("hub-a", {
			id: "draft-1",
			baseRevision: 1,
			rules: [],
			writeUncertain: false,
		});
		const first = clientFixture();
		harness.connection = {
			activeProfile: { id: "hub-a" },
			client: first.client,
			state: "ready",
		};
		const mounted = mountProvider();
		await settleConnection(first);
		const confirmed = mounted.current.snapshot?.keybindings.confirmed;
		const draft = mounted.current.snapshot?.keybindings.draft;

		const replacement = clientFixture({ failed: true });
		harness.storage.throwOnGet = true;
		harness.connection = {
			activeProfile: { id: "hub-a" },
			client: replacement.client,
			state: "closed",
		};
		mounted.rerender();

		expect(mounted.current.model).toBeNull();
		expect(mounted.current.snapshot?.keybindings).toMatchObject({
			confirmed,
			draft,
			storageUnavailable: true,
		});
		mounted.unmount();
	});

	it("reports storage failure from the probe and discard", () => {
		harness.storage.throwOnGet = true;
		harness.connection = {
			activeProfile: { id: "hub-a" },
			client: null,
			state: "closed",
		};
		const mounted = mountProvider();

		expect(mounted.current.offlineStorageUnavailable).toBe(true);
		let outcome: unknown;
		act(() => {
			outcome = mounted.current.discardUnreadableKeybindingsDraft();
		});
		expect(outcome).toBe("storageUnavailable");
		expect(mounted.current.offlineStorageUnavailable).toBe(true);
		mounted.unmount();
	});

	it("resets the probed hub classification when selection clears and does not nudge an old model", async () => {
		writeDraft("hub-a", {
			id: "draft-a",
			baseRevision: 1,
			rules: [],
			writeUncertain: false,
		});
		const first = clientFixture();
		harness.connection = {
			activeProfile: { id: "hub-a" },
			client: first.client,
			state: "ready",
		};
		const mounted = mountProvider();
		await settleConnection(first);
		const oldModel = mounted.current.model;
		expect(mounted.current.model?.getSnapshot().keybindings.draft).not.toBeNull();

		writeDraft("hub-b", {
			id: "draft-b",
			baseRevision: 2,
			rules: [],
			writeUncertain: false,
		});
		const second = clientFixture({ deferred: true });
		harness.storage.throwOnGet = true;
		harness.connection = {
			activeProfile: { id: "hub-b" },
			client: second.client,
			state: "closed",
		};
		mounted.rerender();
		expect(mounted.current.offlineDraftUnreadable).toBe(false);
		expect(mounted.current.offlineStorageUnavailable).toBe(true);

		harness.storage.throwOnGet = false;
		let outcome: unknown;
		act(() => {
			outcome = mounted.current.discardUnreadableKeybindingsDraft();
		});
		expect(outcome).toBe("refused");
		expect(harness.values.has(draftKey("hub-b"))).toBe(true);
		expect(oldModel?.getSnapshot().keybindings.draft).not.toBeNull();

		harness.connection = {
			activeProfile: null,
			client: null,
			state: "closed",
		};
		mounted.rerender();
		expect(mounted.current.offlineDraftUnreadable).toBe(false);
		expect(mounted.current.offlineStorageUnavailable).toBe(false);
		expect(oldModel?.getSnapshot().keybindings.draft).not.toBeNull();
		mounted.unmount();
	});
});
