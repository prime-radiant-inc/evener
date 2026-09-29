// The Session's transcript list hands FlatList one renderItem and one fork
// callback for the list's lifetime (#2685). A screen re-render that changes
// nothing a row reads — a sheet opening, the composer typing, the reader
// keying — must not hand the list new callbacks, because FlatList rebuilds
// every visible cell when the render function's reference changes.
//
// This mounts the real ConversationScreen (only native edges mocked, as in
// ConversationScreen.send.test.tsx) and pins both references across a
// re-render that carries the same route and the same connection.
import type { ComponentProps, ReactNode } from "react";
import { createElement } from "react";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, expect, it, vi } from "vitest";
import type { AnyNotification, Thread } from "@evener/appwire-client";
import { keyboard, render, screenConnection } from "./renderNative.testkit";
import { ConversationScreen } from "./screens";
import { TimelineItem } from "./TimelineItem";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
}));

// The root stack the screen sits in, read by useScreenInFront and
// screenInFront (screens.tsx), kept at the screen's own route on top.
const navigationState = vi.hoisted(() => ({
	state: { index: 0, routes: [] as { key: string; name: string }[] },
}));

const sqlite = vi.hoisted(() => ({ ports: new Map<string, unknown>() }));

vi.mock("react-native", async () => {
	const mock = (await import("./renderNative.testkit")).nativeModuleMock();
	return {
		...mock,
		ActionSheetIOS: { showActionSheetWithOptions: vi.fn() },
		AppState: {
			currentState: "active",
			addEventListener: () => ({ remove: () => {} }),
		},
		Image: "Image",
		Linking: { openURL: vi.fn() },
		RefreshControl: "RefreshControl",
		StatusBar: "StatusBar",
		Modal: (props: { visible?: boolean; children?: ReactNode }) =>
			props.visible ? createElement("Modal", null, props.children) : null,
	};
});
// How many times a transcript row has rendered, for the keyboard probe below.
const rows = vi.hoisted(() => ({ renders: 0 }));
vi.mock("./TimelineItem", async (original) => {
	const real = await original<typeof import("./TimelineItem")>();
	return {
		...real,
		TimelineItem: (props: ComponentProps<typeof real.TimelineItem>) => {
			rows.renders += 1;
			return createElement(real.TimelineItem, props);
		},
	};
});
vi.mock("react-native-safe-area-context", () => ({
	SafeAreaView: "SafeAreaView",
	SafeAreaProvider: (props: { children?: ReactNode }) => props.children ?? null,
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
}));
vi.mock("react-native-webview", () => ({ WebView: "WebView" }));
vi.mock("react-native-enriched-markdown", () => ({
	EnrichedMarkdownText: "EnrichedMarkdownText",
}));
vi.mock("@react-navigation/elements", () => ({ useHeaderHeight: () => 64 }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => void | (() => void)) => useEffect(effect, []),
		useNavigationState: <T,>(select: (state: typeof navigationState.state) => T) => select(navigationState.state),
	};
});
vi.mock("expo-web-browser", () => ({ openBrowserAsync: vi.fn(async () => ({ type: "dismiss" })) }));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-gesture-handler", async () =>
	(await import("./renderNative.testkit")).gestureDetectorModuleMock(),
);
vi.mock("react-native-gesture-handler/ReanimatedSwipeable", async () =>
	(await import("./renderNative.testkit")).gestureHandlerModuleMock(),
);
vi.mock("expo-clipboard", () => ({
	setStringAsync: vi.fn(async () => {}),
	getStringAsync: vi.fn(async () => ""),
}));
vi.mock("expo-crypto", () => ({
	randomUUID: () => "memoization-test-uuid",
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

const navigation = {
	isFocused: () => true,
	getState: () => navigationState.state,
	navigate: vi.fn(),
	push: vi.fn(),
	goBack: vi.fn(),
	setParams: vi.fn(),
	setOptions: vi.fn(),
} as unknown as ConversationScreenProps["navigation"];

async function flush() {
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
}

async function settle() {
	for (let round = 0; round < 10; round += 1) await flush();
}

const CAPABILITIES = {
	send: true,
	steer: true,
	interrupt: true,
	queue: true,
	compact: false,
	clear: false,
	// The fork affordance is on, so every row is handed the screen's
	// forkMessage callback and this suite can read its identity.
	forkFromTurn: true,
	shutdown: false,
	changeModel: false,
	changeVisionModel: false,
	sharedNotes: false,
	goal: false,
	rename: false,
};

function thread(ref: string): Thread {
	return {
		id: `thread-${ref}`,
		sessionId: `session-${ref}`,
		preview: "",
		ephemeral: false,
		modelProvider: "anthropic",
		createdAt: 0,
		updatedAt: 0,
		status: { type: "idle" },
		cwd: "",
		cliVersion: "",
		source: "",
		turns: [],
		evener: {
			ref,
			instanceId: "instance",
			capabilities: CAPABILITIES,
			queue: { revision: 0, depth: 0, preview: [], texts: [], ids: [] },
		},
	} as unknown as Thread;
}

// Two finished turns, each your message and the agent's reply, so the list
// renders rows and hands each one the fork callback.
function twoTurns(ref: string): Thread {
	const served = thread(ref);
	const turn = (id: string) => ({
		id,
		status: "completed",
		itemsView: "default",
		items: [
			{ id: `u-${id}`, turnId: id, type: "userMessage", status: "completed", text: `ask ${id}` },
			{ id: `a-${id}`, turnId: id, type: "agentMessage", status: "completed", text: `reply ${id}` },
		],
	});
	(served as unknown as { turns: unknown[] }).turns = [turn("turn_1"), turn("turn_2")];
	return served;
}

// The hub: it answers thread/read with `served` and acknowledges every
// mutation. No frame is pushed, so the conversation stays as read.
function hubClient(served: Thread) {
	const listeners = new Set<(notification: AnyNotification) => void>();
	const client = {
		state: "ready",
		onStateChange: () => () => {},
		onNotification: (listener: (notification: AnyNotification) => void) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		resumeThread: async () => {},
		request: async (method: string) => {
			if (method === "thread/read") return { thread: served };
			if (method === "thread/turns/list") return { data: [] };
			return {};
		},
	};
	return client;
}

async function mount(served: Thread) {
	harness.connection = {
		...screenConnection(hubClient(served), "ready"),
		error: null,
		disconnect: () => {},
	};
	const ref = String((served as unknown as { evener: { ref: string } }).evener.ref);
	const route = {
		key: `conversation-${ref}`,
		name: "Conversation",
		params: { hubId: "hub-1", ref, title: "Session" },
	} as unknown as ConversationScreenProps["route"];
	navigationState.state = { index: 0, routes: [route] };
	const tree = render(<ConversationScreen route={route} navigation={navigation} />);
	await settle();
	return { tree, route };
}

// The transcript FlatList is the testkit's function component; its props (data
// and renderItem) are readable off the mounted instance.
function transcriptList(tree: ReactTestRenderer) {
	return tree.root.findAll(
		(node) =>
			typeof node.type === "function" && Array.isArray(node.props.data) && typeof node.props.renderItem === "function",
	)[0];
}

function forkOf(tree: ReactTestRenderer) {
	return tree.root.findAll((node) => node.type === TimelineItem)[0]?.props.fork;
}

// Re-render the screen exactly as a state change on the screen itself would:
// the same route and navigation, a fresh element so React runs the component.
function rerender(tree: ReactTestRenderer, route: ConversationScreenProps["route"]) {
	act(() => tree.update(<ConversationScreen route={route} navigation={navigation} />));
}

it("keeps one renderItem across a re-render that no row reads", async () => {
	const { tree, route } = await mount(twoTurns("ref-memo-render"));
	const before = transcriptList(tree).props.renderItem;
	expect(typeof before).toBe("function");
	rerender(tree, route);
	await settle();
	expect(transcriptList(tree).props.renderItem).toBe(before);
});

it("keeps one fork callback across the same re-render", async () => {
	const { tree, route } = await mount(twoTurns("ref-memo-fork"));
	const before = forkOf(tree);
	expect(typeof before).toBe("function");
	rerender(tree, route);
	await settle();
	expect(forkOf(tree)).toBe(before);
});

afterEach(() => keyboard.reset());

// The keyboard rising or falling changes only what folds or steps aside over
// the composer (the queue, Next, the header's chips); each reads the keyboard
// itself, so the flip never re-renders the screen or its transcript rows. A
// screen-wide commit as the keyboard starts to move holds back the keyboard
// controller's per-frame padding for as long as it takes (#3247).
it("re-renders no transcript row when the keyboard comes up or goes down", async () => {
	const { tree } = await mount(twoTurns("ref-memo-keyboard"));
	expect(tree.root.findAll((node) => node.type === TimelineItem)).not.toEqual([]);
	rows.renders = 0;
	act(() => keyboard.show());
	await settle();
	act(() => keyboard.hide());
	await settle();
	expect(rows.renders).toBe(0);
});

// With a message queued, the fold as the keyboard rises changes the bottom
// bar's height, and the bar's re-layout re-renders the screen. That render
// changes nothing a transcript row reads, so no row re-renders: the list
// hands its cells a stable renderer (strictMode), and they re-render only
// for a new renderItem, new rows, or the extraData they read (#3247).
it("re-renders no transcript row when the bottom bar re-lays out as the keyboard folds the queue", async () => {
	const served = twoTurns("ref-memo-queued");
	(served as unknown as { evener: { queue: unknown } }).evener.queue = {
		revision: 1,
		depth: 1,
		preview: ["check the logs"],
		texts: ["check the logs"],
		ids: ["queue_1"],
	};
	const { tree } = await mount(served);
	const bar = () =>
		tree.root.find((node) => String(node.type) === "View" && node.props.testID === "session-bottom-bar");
	act(() => bar().props.onLayout({ nativeEvent: { layout: { x: 0, y: 500, width: 390, height: 260 } } }));
	await settle();
	rows.renders = 0;
	act(() => keyboard.show());
	// The fold shrinks the bar, which reports its new height.
	act(() => bar().props.onLayout({ nativeEvent: { layout: { x: 0, y: 560, width: 390, height: 200 } } }));
	await settle();
	act(() => keyboard.hide());
	act(() => bar().props.onLayout({ nativeEvent: { layout: { x: 0, y: 500, width: 390, height: 260 } } }));
	await settle();
	expect(rows.renders).toBe(0);
});
