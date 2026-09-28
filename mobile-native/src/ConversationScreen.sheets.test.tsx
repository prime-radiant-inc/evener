// The session screen under its own sheets (ruling 28): a sheet is a native-
// stack route that takes focus, yet the session behind it stays the screen in
// front, so it keeps its thread subscribed. A card pushed over it still takes
// it out of the front. On ConversationScreen.recovery.test.tsx's harness.
import type { ComponentProps, ReactNode } from "react";
import { createElement } from "react";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import type { Thread } from "@evener/appwire-client";
import { render, screenConnection } from "./renderNative.testkit";
import { ConversationScreen } from "./screens";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
}));

// The root stack the screen sits in: whether it is focused, and the routes.
const stack = vi.hoisted(() => ({
	focused: true,
	state: { index: 0, routes: [] as { key: string; name: string }[] },
}));

// One sqlite double per database name, keyed the way the singletons open them.
const sqlite = vi.hoisted(() => ({ ports: new Map<string, unknown>() }));

vi.mock("react-native", async () => {
	const mock = (await import("./renderNative.testkit")).nativeModuleMock();
	return {
		...mock,
		AccessibilityInfo: { announceForAccessibility: vi.fn() },
		ActionSheetIOS: { showActionSheetWithOptions: vi.fn() },
		AppState: {
			currentState: "active",
			addEventListener: () => ({ remove: () => {} }),
		},
		Image: "Image",
		Keyboard: { dismiss: vi.fn() },
		Linking: { openURL: vi.fn() },
		RefreshControl: "RefreshControl",
		StatusBar: "StatusBar",
		// The real Modal renders its children only while visible; the inert host
		// string would render them always, so the panel would look mounted even
		// with the modal closed. This stub keeps the screen's open/closed state
		// observable in the tree: no visible modal, no panel.
		Modal: (props: { visible?: boolean; children?: ReactNode }) =>
			props.visible ? createElement("Modal", null, props.children) : null,
	};
});
vi.mock("react-native-safe-area-context", () => ({
	SafeAreaView: "SafeAreaView",
	SafeAreaProvider: (props: { children?: ReactNode }) => props.children ?? null,
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
}));
vi.mock("react-native-enriched-markdown", () => ({
	EnrichedMarkdownText: "EnrichedMarkdownText",
}));
vi.mock("@react-navigation/elements", () => ({ useHeaderHeight: () => 64 }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => void | (() => void)) =>
			useEffect(effect, []),
		useNavigationState: <T,>(select: (state: typeof stack.state) => T) =>
			select(stack.state),
	};
});
vi.mock("expo-clipboard", () => ({
	setStringAsync: vi.fn(async () => {}),
	getStringAsync: vi.fn(async () => ""),
}));
vi.mock("expo-crypto", () => ({
	randomUUID: () => "recovery-entry-uuid",
	getRandomValues: (array: Uint8Array) => array,
}));
vi.mock("expo-sqlite", async () => {
	const { openSqliteSyncDouble } = await import("./sqliteSync.testkit");
	return {
		openDatabaseSync: (database: string) => {
			let port = sqlite.ports.get(database);
			if (!port) {
				port = openSqliteSyncDouble().port;
				sqlite.ports.set(database, port);
			}
			return port;
		},
	};
});
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: { getItemSync: () => null, setItemSync: () => {} },
}));
vi.mock("expo-file-system", () => ({
	File: class File {
		constructor(public uri: string) {}
	},
}));
vi.mock("expo-image-manipulator", () => ({}));
vi.mock("expo-image-picker", () => ({
	launchImageLibraryAsync: vi.fn(async () => ({ canceled: true, assets: [] })),
	UIImagePickerPreferredAssetRepresentationMode: { Current: "current" },
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: vi.fn(async () => null),
	setItemAsync: vi.fn(async () => {}),
	deleteItemAsync: vi.fn(async () => {}),
}));
vi.mock("./ConnectionProvider", () => ({
	useConnection: () => harness.connection,
}));
vi.mock("./NativePreferencesProvider", () => ({
	useNativePreferences: () => ({
		hubId: null,
		model: null,
		snapshot: null,
		config: null,
		connected: false,
		offlineDraftUnreadable: false,
		offlineStorageUnavailable: false,
		discardUnreadableKeybindingsDraft: () => null,
	}),
}));

type ConversationScreenProps = ComponentProps<typeof ConversationScreen>;

const ref = "local:thread-1";
const session = { key: `conversation-${ref}`, name: "Conversation" };

const route = {
	key: session.key,
	name: "Conversation",
	params: { hubId: "hub-1", ref, title: "Session" },
} as unknown as ConversationScreenProps["route"];

const navigation = {
	isFocused: () => stack.focused,
	getState: () => stack.state,
	navigate: vi.fn(),
	push: vi.fn(),
	pop: vi.fn(),
	goBack: vi.fn(),
	setParams: vi.fn(),
	setOptions: vi.fn(),
};

const thread: Thread = {
	id: "thread-1",
	sessionId: "session-1",
	preview: "saved",
	ephemeral: false,
	modelProvider: "scripted",
	createdAt: 1,
	updatedAt: 1,
	status: { type: "idle" },
	cwd: "/tmp",
	cliVersion: "test",
	source: "local",
	turns: [],
	evener: {
		ref,
		instanceId: "instance-1",
		queue: { revision: 0 },
		tasks: { total: 2, done: 1 },
		capabilities: {
			send: true,
			steer: false,
			interrupt: false,
			compact: false,
			clear: false,
			forkFromTurn: false,
			shutdown: false,
			changeModel: false,
			changeVisionModel: false,
			sharedNotes: false,
			queue: false,
			goal: false,
			rename: false,
		},
	},
};

/** A hub that answers the session's read with `thread` and records every
 * request the screen makes. */
function sessionClient() {
	const requests: { method: string; params: unknown }[] = [];
	const client = {
		state: "ready",
		onStateChange: () => () => {},
		request: async (method: string, params?: unknown) => {
			requests.push({ method, params });
			if (method === "thread/read") return { thread };
			return new Promise<never>(() => {});
		},
		onNotification: () => () => {},
	};
	return { client, requests };
}

async function flush() {
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
}

function mount() {
	const { client, requests } = sessionClient();
	harness.connection = {
		...screenConnection(client, "ready"),
		error: null,
		disconnect: () => {},
	};
	const tree = render(
		<ConversationScreen
			route={route}
			navigation={navigation as unknown as ConversationScreenProps["navigation"]}
		/>,
	);
	return { tree, requests };
}

function subscribedReads(requests: { method: string; params: unknown }[]) {
	return requests.filter(
		(request) =>
			request.method === "thread/read" &&
			(request.params as { subscribe?: boolean }).subscribe === true,
	);
}

beforeEach(() => {
	stack.focused = true;
	stack.state = { index: 0, routes: [session] };
	navigation.navigate.mockClear();
	navigation.setOptions.mockClear();
});

it("opens Tasks from the header menu as the TasksSheet route", async () => {
	const { tree } = mount();
	await flush();

	const options = navigation.setOptions.mock.calls.at(-1)?.[0] as {
		unstable_headerRightItems: () => {
			menu: { items: { label: string; onPress(): void }[] };
		}[];
	};
	const tasks = options
		.unstable_headerRightItems()[0]
		?.menu.items.find((item) => item.label === "Tasks");
	if (!tasks) throw new Error("no Tasks item in the header menu");
	act(() => tasks.onPress());

	expect(navigation.navigate).toHaveBeenCalledWith("TasksSheet", {
		hubId: "hub-1",
		ref,
		threadId: "thread-1",
		hasTasks: true,
	});
	tree.unmount();
});

it("keeps its session subscribed while its own sheet covers it", async () => {
	stack.focused = false;
	stack.state = {
		index: 1,
		routes: [session, { key: "tasks-sheet", name: "TasksSheet" }],
	};
	const { tree, requests } = mount();
	await flush();

	expect(subscribedReads(requests).length).toBeGreaterThan(0);
	tree.unmount();
});

it("follows no session while a screen is pushed over it", async () => {
	stack.focused = false;
	stack.state = {
		index: 1,
		routes: [session, { key: "reader", name: "Reader" }],
	};
	const { tree, requests } = mount();
	await flush();

	expect(requests.filter((request) => request.method === "thread/read")).toEqual(
		[],
	);
	tree.unmount();
});
