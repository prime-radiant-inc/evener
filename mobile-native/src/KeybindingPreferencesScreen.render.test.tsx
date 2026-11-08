import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import type { AnyNotification, AppwireClient, InitializeResponse, KeybindingsRule } from "@evener/appwire-client";
import { KeybindingPreferencesScreen } from "./KeybindingPreferencesScreen";
import { textContrast } from "./design/contrast.testkit";
import { palettes } from "./design/tokens";
import { markers } from "./markerTestUtils";
import { NativePreferencesProvider } from "./NativePreferencesProvider";
import { dropped, render, screenConnection } from "./renderNative.testkit";

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
		connection: {} as Record<string, unknown>,
		scheme: "light" as "light" | "dark",
		fontScale: 1,
		storage,
		values,
	};
});

vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	KeyboardAvoidingView: "KeyboardAvoidingView",
	useColorScheme: () => harness.scheme,
	useWindowDimensions: () => ({ width: 320, height: 568, scale: 2, fontScale: harness.fontScale }),
}));
vi.mock("react-native-safe-area-context", () => ({
	SafeAreaView: "SafeAreaView",
	useSafeAreaInsets: () => ({ top: 0, bottom: 0, left: 0, right: 0 }),
}));
vi.mock("expo-sqlite/kv-store", () => ({ Storage: harness.storage }));
vi.mock("./ConnectionProvider", () => ({
	useConnection: () => harness.connection,
}));

const initialize: InitializeResponse = {
	features: { keybindingsSettings: true, transcriptDisplaySettings: false },
} as InitializeResponse;
const hub = { id: "hub-1", name: "Work hub" };
const navigation = { setParams: vi.fn() };

type Editor = { actionId: string; chord: string };

function screen(editor?: Editor) {
	const route = { params: { hubId: hub.id, ...(editor === undefined ? {} : { editor }) } };
	return (
		<KeybindingPreferencesScreen
			route={route as unknown as ComponentProps<typeof KeybindingPreferencesScreen>["route"]}
			navigation={navigation as unknown as ComponentProps<typeof KeybindingPreferencesScreen>["navigation"]}
		/>
	);
}

function app(editor?: Editor) {
	return <NativePreferencesProvider>{screen(editor)}</NativePreferencesProvider>;
}

function alertNodes(tree: ReturnType<typeof render>) {
	return tree.root.findAllByProps({ accessibilityRole: "alert" });
}

const getMethod = "evener/settings/keybindings/get";
const patchMethod = "evener/settings/keybindings/patch";

function deferred<T>() {
	let resolve!: (value: T) => void;
	const promise = new Promise<T>((done) => {
		resolve = done;
	});
	return { promise, resolve };
}

function clientFixture(initialRules: KeybindingsRule[] = []) {
	const readyListeners = new Set<(value: InitializeResponse) => void>();
	const notifications = new Set<(value: AnyNotification) => void>();
	const requests: Array<{ method: string; params: unknown }> = [];
	const handlers = new Map<string, (params: unknown) => unknown>([
		[getMethod, () => ({ version: 1, revision: 1, rules: initialRules })],
		[
			patchMethod,
			(params) => {
				const patch = params as { expectedRevision: number; config: { version: 1; rules: KeybindingsRule[] } };
				return { version: 1, revision: patch.expectedRevision + 1, rules: patch.config.rules };
			},
		],
	]);
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
		request: async (method: string, params: unknown) => {
			requests.push({ method, params });
			requestWaiter?.();
			requestWaiter = null;
			const handler = handlers.get(method);
			if (handler === undefined) throw new Error(`Unexpected request: ${method}`);
			return handler(params);
		},
	} as unknown as AppwireClient;
	return {
		client,
		connected,
		requests,
		handlers,
		nextRequest: () =>
			new Promise<void>((resolve) => {
				requestWaiter = resolve;
			}),
	};
}

async function openReader(connection: ReturnType<typeof clientFixture>, editor?: Editor) {
	harness.connection = screenConnection(connection.client, "ready");
	const initial = connection.nextRequest();
	const tree = render(app(editor));
	await act(async () => {
		await connection.connected;
		await initial;
	});
	return tree;
}

beforeEach(() => {
	harness.values.clear();
	harness.scheme = "light";
	harness.fontScale = 1;
	navigation.setParams.mockClear();
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

it("applies an edited shortcut with one immediate PATCH and no save step", async () => {
	const connection = clientFixture();
	const tree = await openReader(connection, { actionId: "palette.open", chord: "Meta+P" });
	// There is no draft to confirm: the only way to apply an edit is the
	// editor's own action, which PATCHes immediately.
	expect(tree.root.findAllByProps({ accessibilityLabel: "Save changes" })).toHaveLength(0);

	const patched = connection.nextRequest();
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Use shortcut" }).props.onPress();
		await patched;
	});

	expect(connection.requests.filter((request) => request.method === patchMethod)).toEqual([
		{
			method: patchMethod,
			params: { expectedRevision: 1, config: { version: 1, rules: [{ action: "palette.open", chord: "Meta+P" }] } },
		},
	]);
	expect(navigation.setParams).toHaveBeenCalledWith({ editor: undefined });
	// The phone holds no shortcut draft record: the hub confirmed the edit and
	// the store carries it.
	expect([...harness.values.keys()].filter((key) => key.includes("keybinding-draft"))).toEqual([]);
	act(() => tree.unmount());
});

it("shows a failed immediate edit without closing the editor", async () => {
	const connection = clientFixture();
	connection.handlers.set(patchMethod, () => {
		throw new Error("hub request failed");
	});
	const tree = await openReader(connection, { actionId: "palette.open", chord: "Meta+P" });

	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Use shortcut" }).props.onPress();
	});

	expect(alertNodes(tree).length).toBeGreaterThan(0);
	expect(markers(tree, "keybinding-error")).toEqual(["action"]);
	expect(tree.root.findByType("TextInput" as never).props.value).toBe("Meta+P");
	expect(navigation.setParams).not.toHaveBeenCalledWith({ editor: undefined });
	act(() => tree.unmount());
});

it("recovers a pre-ruling keybinding draft through the immediate path", async () => {
	const saved = {
		id: "draft-1",
		baseRevision: 1,
		rules: [{ action: "palette.open", chord: "Meta+Shift+P" }],
		writeUncertain: true,
	};
	const key = `evener.native.keybinding-draft.${hub.id}`;
	harness.values.set(key, JSON.stringify(saved));
	const connection = clientFixture();
	const tree = await openReader(connection);

	// The saved proposal is visible but has not been replayed.
	expect(tree.root.findByProps({ accessibilityLabel: "Apply saved changes" })).toBeDefined();
	expect(connection.requests.filter((request) => request.method === patchMethod)).toHaveLength(0);

	const patched = connection.nextRequest();
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Apply saved changes" }).props.onPress();
		await patched;
	});

	expect(connection.requests.filter((request) => request.method === patchMethod)).toEqual([
		{
			method: patchMethod,
			params: { expectedRevision: 1, config: { version: 1, rules: saved.rules } },
		},
	]);
	expect(harness.values.has(key)).toBe(false);
	expect(tree.root.findAllByProps({ accessibilityLabel: "Apply saved changes" })).toHaveLength(0);
	act(() => tree.unmount());
});

it("discards an unreadable pre-ruling keybinding draft with no hub connected", () => {
	const key = `evener.native.keybinding-draft.${hub.id}`;
	harness.values.set(key, "{not json");
	harness.connection = { activeProfile: hub, client: null, state: "ready", retry: vi.fn() };
	const tree = render(app());

	expect(tree.root.findAllByProps({ accessibilityLabel: "Apply saved changes" })).toHaveLength(0);
	const action = tree.root.findByProps({ accessibilityLabel: "Discard saved changes" });
	act(() => action.props.onPress());

	expect(harness.values.has(key)).toBe(false);
	expect(tree.root.findAllByProps({ accessibilityLabel: "Discard saved changes" })).toHaveLength(0);
	act(() => tree.unmount());
});

it("does not let an earlier edit's reply close an editor opened since", async () => {
	const connection = clientFixture();
	const reply = deferred<unknown>();
	connection.handlers.set(patchMethod, () => reply.promise);
	const tree = await openReader(connection, { actionId: "palette.open", chord: "Meta+P" });
	const patched = connection.nextRequest();
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Use shortcut" }).props.onPress();
		await patched;
	});
	// The user cancelled and opened another action while the write was out.
	act(() => tree.update(app({ actionId: "rail.toggle", chord: "Meta+R" })));
	navigation.setParams.mockClear();

	reply.resolve({ version: 1, revision: 2, rules: [{ action: "palette.open", chord: "Meta+P" }] });
	await act(async () => {});

	expect(navigation.setParams).not.toHaveBeenCalledWith({ editor: undefined });
	expect(tree.root.findByProps({ accessibilityLabel: "Use shortcut" })).toBeDefined();
	act(() => tree.unmount());
});

it.each(["light", "dark"] as const)("retains readable shortcut values while offline in %s", async (scheme) => {
	harness.scheme = scheme;
	const connection = clientFixture();
	const tree = await openReader(connection);
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

it("keeps the hub identity, shortcut values and unknown-rule diagnostics selectable", async () => {
	const unknown = { action: "fixture.unknown-action", chord: "Meta+U" };
	const connection = clientFixture([unknown]);
	const tree = await openReader(connection);
	const text = tree.root.findAllByType("Text" as never);
	const identity = text.find((node) => node.props.children === hub.name);
	expect(identity?.props.selectable).toBe(true);
	const shortcutValues = tree.root.findAllByProps({ testID: "row-sub" }).slice(1);
	expect(shortcutValues.length).toBeGreaterThan(0);
	for (const value of shortcutValues) expect(value.props.selectable).toBe(true);
	const diagnostic = text.find((node) => String(node.props.children).includes(unknown.action));
	expect(diagnostic?.props.selectable).toBe(true);
	act(() => tree.unmount());
});

it("keeps a retained editor's action identifier and shortcut preview selectable", async () => {
	const connection = clientFixture();
	const tree = await openReader(connection, { actionId: "fixture.unknown-action", chord: "Control+U" });
	const identifier = tree.root
		.findAllByType("Text" as never)
		.find((node) => node.props.children === "fixture.unknown-action");
	expect(identifier?.props.selectable).toBe(true);
	const preview = tree.root.findByProps({ label: "Current preview" });
	expect(preview.findByProps({ testID: "row-sub" }).props.selectable).toBe(true);
	act(() => tree.unmount());
});

it("reveals every refusal of the same invalid shortcut without dropping the editor", async () => {
	harness.fontScale = 3.1;
	const connection = clientFixture();
	harness.connection = screenConnection(connection.client, "ready");
	const initial = connection.nextRequest();
	const scrollTo = vi.fn();
	const tree = render(app({ actionId: "palette.open", chord: "Meta+" }), {
		createNodeMock: (element) => (element.type === "ScrollView" ? { scrollTo } : null),
	});
	await act(async () => {
		await connection.connected;
		await initial;
	});
	for (let attempt = 0; attempt < 2; attempt += 1) {
		scrollTo.mockClear();
		await act(async () => tree.root.findByProps({ accessibilityLabel: "Use shortcut" }).props.onPress());
		expect(scrollTo).toHaveBeenCalledWith({ y: 0, animated: true });
		expect(tree.root.findByType("ScrollView" as never).findAllByType("Text" as never)[0]?.props.accessibilityRole).toBe(
			"alert",
		);
		expect(tree.root.findByType("TextInput" as never).props.value).toBe("Meta+");
	}
	act(() => tree.unmount());
});

it("keeps unknown confirmed rules selectable when they have no editor", async () => {
	const unknown = { action: "fixture.unknown-action", chord: "Control+U" };
	const connection = clientFixture([unknown]);
	const tree = await openReader(connection);
	const text = tree.root.findAllByType("Text" as never);
	const diagnostic = text.find((node) => String(node.props.children).includes(unknown.action));
	expect(diagnostic?.props.selectable).toBe(true);
	act(() => tree.unmount());
});
