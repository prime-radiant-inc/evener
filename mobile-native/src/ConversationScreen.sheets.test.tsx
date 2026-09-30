// The session screen under its own sheets (ruling 28): a sheet is a native-
// stack route that takes focus, yet the session behind it stays the screen in
// front, so it keeps its thread subscribed. A card pushed over it still takes
// it out of the front. Its header's title and ⋯ menu open those sheets and
// act on the session (spec 8.1). On ConversationScreen.recovery.test.tsx's
// harness.
import type {
	NativeStackHeaderItemMenu,
	NativeStackHeaderItemMenuAction,
	NativeStackHeaderItemMenuSubmenu,
	NativeStackNavigationOptions,
} from "@react-navigation/native-stack";
import type { ComponentProps, ReactElement, ReactNode } from "react";
import { createElement } from "react";
import { FlatList } from "react-native";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { type Thread, WireError } from "@evener/appwire-client";
import { alertRequests, keyboard, playedHaptics, render, renderedText, screenConnection } from "./renderNative.testkit";
import { ConversationScreen } from "./screens";
import { detailLevels, forgetDetailLevelsForHub } from "./session/nativeDetailLevels";
import { GlassHeaderPanel } from "./design/GlassHeaderPanel";
import { SessionHeader } from "./session/SessionHeader";
import { SessionTitle } from "./session/SessionTitle";
import { readerKey } from "./readerPosition";
import { sessionInfoHosts } from "./session/SessionInfoSheet";
import { sheetKey } from "./sheet/sheetHosts";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
}));

// The root stack the screen sits in: whether it is focused, and the routes.
const stack = vi.hoisted(() => ({
	focused: true,
	state: { index: 0, routes: [] as { key: string; name: string }[] },
}));

// Every scrollToOffset the screen asks of its list, oldest first.
const listScrolls = vi.hoisted(() => [] as { offset: number; animated?: boolean }[]);
const endScrolls = vi.hoisted(() => vi.fn());
const appStateListeners = vi.hoisted(() => new Set<(state: string) => void>());

// One sqlite double per database name, keyed the way the singletons open them.
const sqlite = vi.hoisted(() => ({ ports: new Map<string, unknown>() }));

vi.mock("react-native", async () => {
	const mock = (await import("./renderNative.testkit")).nativeModuleMock();
	const { useImperativeHandle } = await import("react");
	return {
		...mock,
		// The list with the handle the screen scrolls through: scrollToOffset
		// calls are recorded, and the rest do nothing.
		FlatList: (props: Parameters<typeof mock.FlatList>[0] & { ref?: unknown }) => {
			useImperativeHandle(props.ref as never, () => ({
				scrollToOffset: (options: { offset: number; animated?: boolean }) => listScrolls.push(options),
				scrollToIndex: () => {},
				getScrollResponder: () => ({ scrollToEnd: endScrolls }),
			}));
			return mock.FlatList(props);
		},
		ActionSheetIOS: { showActionSheetWithOptions: vi.fn() },
		AppState: {
			currentState: "active",
			addEventListener: (event: string, listener: (state: string) => void) => {
				if (event === "change") appStateListeners.add(listener);
				return { remove: () => appStateListeners.delete(listener) };
			},
		},
		Image: "Image",
		Linking: { openURL: vi.fn() },
		RefreshControl: "RefreshControl",
		StatusBar: "StatusBar",
		// The real Modal renders its children while visible, its own default
		// being visible; the inert host string would render them always, so the
		// panel would look mounted even with the modal closed. This stub keeps
		// the screen's open/closed state observable in the tree: a modal told to
		// hide renders no panel, and one that says nothing holds, as it does.
		Modal: (props: { visible?: boolean; children?: ReactNode }) =>
			props.visible !== false ? createElement("Modal", null, props.children) : null,
	};
});
vi.mock("react-native-safe-area-context", () => ({
	SafeAreaView: "SafeAreaView",
	SafeAreaProvider: (props: { children?: ReactNode }) => props.children ?? null,
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-gesture-handler", async () =>
	(await import("./renderNative.testkit")).gestureDetectorModuleMock(),
);
vi.mock("react-native-gesture-handler/ReanimatedSwipeable", async () =>
	(await import("./renderNative.testkit")).gestureHandlerModuleMock(),
);
vi.mock("expo-web-browser", () => ({}));
vi.mock("react-native-enriched-markdown", () => ({
	EnrichedMarkdownText: "EnrichedMarkdownText",
}));
vi.mock("@react-navigation/elements", () => ({ useHeaderHeight: () => 64 }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => void | (() => void)) => useEffect(effect, []),
		useIsFocused: () => stack.focused,
		useNavigationState: <T,>(select: (state: typeof stack.state) => T) => select(stack.state),
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
	Storage: { getItemSync: () => null, setItemSync: () => {}, removeItemSync: () => {} },
}));
vi.mock("expo-file-system", () => ({
	File: class File {
		constructor(public uri: string) {}
	},
}));
vi.mock("expo-image-manipulator", () => ({
	ImageManipulator: {
		manipulateAsync: vi.fn(async () => ({ uri: "manipulated" })),
	},
	SaveFormat: { JPEG: "jpeg" },
}));
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

/** What the hub answers beside the session's read: a value, a thrown error
 * for a rejection, or a function of the call's params for a method whose
 * calls answer differently (Undo re-sending the same method with the
 * opposite `archived`). Any other request stays pending. */
type Answers = Record<string, unknown>;

/** A hub that answers the session's read with `read` and records every
 * request the screen makes. */
function sessionClient(read: Thread, answers: Answers) {
	const requests: { method: string; params: unknown }[] = [];
	const client = {
		state: "ready",
		onStateChange: () => () => {},
		request: async (method: string, params?: unknown) => {
			requests.push({ method, params });
			if (method in answers) {
				const answer = answers[method];
				if (typeof answer === "function") return answer(params);
				if (answer instanceof Error) throw answer;
				return answer;
			}
			if (method === "thread/read") return { thread: read };
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

const screen = () => (
	<ConversationScreen route={route} navigation={navigation as unknown as ConversationScreenProps["navigation"]} />
);

function mount(read: Thread = thread, answers: Answers = {}, connection: Record<string, unknown> = {}) {
	const { client, requests } = sessionClient(read, answers);
	harness.connection = {
		...screenConnection(client, "ready"),
		error: null,
		disconnect: () => {},
		...connection,
	};
	const tree = render(screen());
	return { tree, requests, client };
}

function subscribedReads(requests: { method: string; params: unknown }[]) {
	return requests.filter(
		(request) => request.method === "thread/read" && (request.params as { subscribe?: boolean }).subscribe === true,
	);
}

beforeEach(() => {
	stack.focused = true;
	stack.state = { index: 0, routes: [session] };
	navigation.navigate.mockClear();
	navigation.push.mockClear();
	navigation.pop.mockClear();
	navigation.goBack.mockClear();
	navigation.setOptions.mockClear();
	alertRequests.length = 0;
	listScrolls.length = 0;
	// Each test starts with no detail level chosen on this device.
	forgetDetailLevelsForHub("hub-1");
});

/** The header options the screen set last. */
function header(): NativeStackNavigationOptions {
	const calls = navigation.setOptions.mock.calls as [NativeStackNavigationOptions][];
	const options = calls.map(([options]) => options).findLast((options) => options.unstable_headerRightItems);
	if (!options) throw new Error("the screen set no header items");
	return options;
}

/** The ⋯ menu's items as the header offers them now. */
function menuItems(): (NativeStackHeaderItemMenuAction | NativeStackHeaderItemMenuSubmenu)[] {
	const [item] = header().unstable_headerRightItems?.({ canGoBack: true }) ?? [];
	if (item?.type !== "menu") throw new Error("the header has no menu");
	return (item as NativeStackHeaderItemMenu).menu.items;
}

function menuAction(label: string): NativeStackHeaderItemMenuAction {
	const found = menuItems().find((item) => item.label === label);
	if (found?.type !== "action") throw new Error(`no ${label} in the header menu`);
	return found;
}

/** A level in the ⋯ menu's detail-level submenu. */
function levelAction(label: string): NativeStackHeaderItemMenuAction {
	const [detail] = menuItems();
	const [section] = detail?.type === "submenu" ? detail.items : [];
	const found = section?.type === "submenu" ? section.items.find((item) => item.label === label) : undefined;
	if (found?.type !== "action") throw new Error(`no ${label} level in the menu`);
	return found;
}

const withCapabilities = (capabilities: Partial<Thread["evener"]["capabilities"]>): Thread => ({
	...thread,
	evener: { ...thread.evener, capabilities: { ...thread.evener.capabilities, ...capabilities } },
});

// A session with shell jobs and no subagents has an Activity list too, and
// the session can't tell from its read whether it has jobs, so the menu
// offers Activity whenever it's connected, as it offers Tasks.
it("offers Activity from the header menu with no subagents", async () => {
	const { tree } = mount();
	await flush();

	act(() => menuAction("Activity").onPress());

	expect(navigation.navigate).toHaveBeenCalledWith("Subagents", {
		hubId: "hub-1",
		ref,
		threadId: "thread-1",
		title: "Session",
	});
	tree.unmount();
});

it("opens Tasks from the header menu as the TasksSheet route", async () => {
	const { tree } = mount();
	await flush();

	act(() => menuAction("Tasks").onPress());

	expect(navigation.navigate).toHaveBeenCalledWith("TasksSheet", {
		hubId: "hub-1",
		ref,
		threadId: "thread-1",
		hasTasks: true,
	});
	tree.unmount();
});

it("opens Notes & links from the header menu as the NotesSheet route, without focusing the editor", async () => {
	const { tree } = mount(withCapabilities({ sharedNotes: true }));
	await flush();

	act(() => menuAction("Notes & links").onPress());

	expect(navigation.navigate).toHaveBeenCalledWith("NotesSheet", {
		hubId: "hub-1",
		ref,
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

	expect(requests.filter((request) => request.method === "thread/read")).toEqual([]);
	tree.unmount();
});

it("titles the header with the session's state, and opens its info on a press", async () => {
	const { tree } = mount();
	await flush();

	const title = header().headerTitle;
	if (typeof title !== "function") throw new Error("no header title component");
	const element = title({ children: "Session" }) as ReactElement<ComponentProps<typeof SessionTitle>>;
	expect(element.type).toBe(SessionTitle);
	expect(element.props).toMatchObject({ title: "Session", line: { state: "idle", text: "Finished" } });
	act(() => element.props.onPress());

	expect(navigation.navigate).toHaveBeenCalledWith("SessionInfoSheet", { hubId: "hub-1", ref });
	// The sheet reads the session through the host the screen provides.
	expect(sessionInfoHosts.get(sheetKey("hub-1", ref))?.session).toMatchObject({ ref, threadId: "thread-1" });
	tree.unmount();
});

it("opens New session like this one: its host, folder, model and effort", async () => {
	const { tree } = mount({ ...thread, evener: { ...thread.evener, reasoningEffort: "high" } });
	await flush();

	act(() => menuAction("New session like this").onPress());
	expect(navigation.navigate).toHaveBeenCalledWith("NewSession", {
		hubId: "hub-1",
		hubName: expect.any(String),
		like: { host: "local", cwd: "/tmp", model: "scripted", effort: "high" },
	});
	tree.unmount();
});

it("opens Session info from the header menu as the SessionInfoSheet route", async () => {
	const { tree } = mount();
	await flush();

	act(() => menuAction("Session info").onPress());
	expect(navigation.navigate).toHaveBeenCalledWith("SessionInfoSheet", { hubId: "hub-1", ref });
	tree.unmount();
});

/** The Session sheet's host, as the screen provides it now. */
function sessionInfoHost() {
	const host = sessionInfoHosts.get(sheetKey("hub-1", ref));
	if (!host) throw new Error("the screen provides no Session sheet host");
	return host;
}

// The model's name comes from the catalog, which the screen keeps across a
// screen pushed over it and back: its new controls start from it, so the
// Session sheet never names the model by its raw id while the next read is
// out (audit N6).
it("keeps naming the model after a screen pushed over it closes, while the catalog reads again", async () => {
	let modelReads = 0;
	const { tree } = mount(
		{ ...thread, modelProvider: "lunaroute/deepseek-4.1-flash" },
		{
			"model/list": () => {
				modelReads++;
				// The first read answers; the one after the push never does.
				return modelReads === 1
					? { data: [{ provider: "lunaroute", model: "deepseek-4.1-flash", displayName: "DeepSeek 4.1 Flash" }] }
					: new Promise<never>(() => {});
			},
		},
	);
	await flush();
	expect(sessionInfoHost().modelLabel).toBe("DeepSeek 4.1 Flash");
	stack.state = { index: 1, routes: [session, { key: "reader", name: "Reader" }] };
	act(() => tree.update(screen()));
	await flush();
	stack.state = { index: 0, routes: [session] };
	act(() => tree.update(screen()));
	await flush();
	expect({ reads: modelReads, label: sessionInfoHost().modelLabel }).toEqual({ reads: 2, label: "DeepSeek 4.1 Flash" });
	tree.unmount();
});

// The phone switched to another hub while this session stayed open: the
// sheet still names this hub's own machine for this hub, from the hub list,
// and shows it offline (audit N6).
it("names the hub's own machine for its hub after another hub becomes active", async () => {
	const profiles = [
		{ id: "hub-1", name: "Work hub", origin: "https://work.example" },
		{ id: "hub-2", name: "Home hub", origin: "https://home.example" },
	];
	const { tree } = mount(thread, {}, { profiles });
	await flush();
	expect(sessionInfoHost().host("local")).toEqual({ label: "Work hub", online: true });
	harness.connection = { ...harness.connection, activeProfile: { id: "hub-2", name: "Home hub" } };
	act(() => tree.update(screen()));
	await flush();
	expect(sessionInfoHost().host("local")).toEqual({ label: "Work hub", online: false });
	tree.unmount();
});

// A failed catalog read clears the catalog, so the picker offers no stale
// choices; the screen forgets it too, so controls made after a pushed screen
// closes don't bring the old catalog back.
it("brings no catalog back after a failed read and a screen pushed over it", async () => {
	let modelReads = 0;
	const { tree } = mount(
		{ ...thread, modelProvider: "lunaroute/deepseek-4.1-flash" },
		{
			"model/list": () => {
				modelReads++;
				if (modelReads === 1)
					return { data: [{ provider: "lunaroute", model: "deepseek-4.1-flash", displayName: "DeepSeek 4.1 Flash" }] };
				if (modelReads === 2) throw new Error("The hub couldn't list its models.");
				return new Promise<never>(() => {});
			},
		},
	);
	await flush();
	const pushAndReturn = async () => {
		stack.state = { index: 1, routes: [session, { key: "reader", name: "Reader" }] };
		act(() => tree.update(screen()));
		await flush();
		stack.state = { index: 0, routes: [session] };
		act(() => tree.update(screen()));
		await flush();
	};
	await pushAndReturn();
	expect(modelReads).toBe(2);
	await pushAndReturn();
	expect(modelReads).toBe(3);
	expect(sessionInfoHost().controls?.getSnapshot().catalog).toBeNull();
	tree.unmount();
});

it("runs the Session sheet's actions as the menu does, and hands back their toasts (ruling 37)", async () => {
	const { tree, requests } = mount(withCapabilities({ compact: true, shutdown: true }), {
		"evener/archive/set": {},
		"thread/compact/start": {},
		"thread/shutdown": {},
	});
	await flush();

	let archived: unknown;
	await act(async () => {
		archived = await sessionInfoHost().act("archive");
	});
	expect(archived).toMatchObject({ text: "Session archived", action: { label: "Undo" } });
	expect(requests.filter(({ method }) => method === "evener/archive/set").map(({ params }) => params)).toEqual([
		{ kind: "session", id: ref, archived: true },
	]);

	let compacted: unknown;
	await act(async () => {
		compacted = await sessionInfoHost().act("compact");
	});
	expect(compacted).toEqual({ text: "Compacting context" });
	expect(requests.map(({ method }) => method)).toContain("thread/compact/start");

	let stopped: unknown;
	await act(async () => {
		stopped = await sessionInfoHost().act("shutDown");
	});
	expect(stopped).toEqual({ text: "Session shut down" });
	expect(requests.filter(({ method }) => method === "thread/shutdown")).toEqual([
		{ method: "thread/shutdown", params: { ref } },
	]);
	// No second question: the sheet asked before it handed Shut down over.
	expect(alertRequests).toEqual([]);

	// Its toast shows on the session, once the sheet has gone.
	act(() => sessionInfoHost().toast({ text: "Session archived" }));
	expect(renderedText(tree)).toContain("Session archived");
	tree.unmount();
});

it("says so when the hub went away before a Session sheet action could run", async () => {
	const { tree, requests } = mount(withCapabilities({ compact: true, shutdown: true }));
	await flush();
	// The confirmation was up when the connection dropped.
	harness.connection = { ...harness.connection, state: "connecting" };
	act(() => tree.update(screen()));
	await flush();

	let stopped: unknown;
	let compacted: unknown;
	await act(async () => {
		stopped = await sessionInfoHost().act("shutDown");
		compacted = await sessionInfoHost().act("compact");
	});
	expect(stopped).toEqual({ text: "Couldn't shut down this session: the hub isn't connected." });
	expect(compacted).toEqual({ text: "Couldn't compact the context: the hub isn't connected." });
	expect(requests.map(({ method }) => method)).not.toContain("thread/shutdown");
	tree.unmount();
});

it("opens Pin to category… from the Session sheet as its screen", async () => {
	const { tree } = mount();
	await flush();

	await act(async () => {
		await sessionInfoHost().act("pin");
	});
	expect(navigation.navigate).toHaveBeenCalledWith("PinAssignment", { hubId: "hub-1", ref, title: "Session" });
	tree.unmount();
});

it("shows the chosen detail level and confirms it", async () => {
	const { tree } = mount();
	await flush();

	playedHaptics.length = 0;
	act(() => levelAction("Full").onPress());

	// Spec 16.6: a selection tick on a detail level.
	expect(playedHaptics).toEqual(["selection"]);
	expect(detailLevels("hub-1").get(ref)).toBe("full");
	expect(menuItems()[0]).toMatchObject({ label: "Detail level · Full" });
	expect(renderedText(tree)).toContain("Full: everything, including the agent's reasoning");
	tree.unmount();
});

/** The ⋯ button in the header, as the screen last set it. */
function menuButton(): ReactElement<{ onPress(): void }> {
	const button = header().headerRight?.({ canGoBack: true });
	if (!button) throw new Error("the header set no ⋯ button");
	return button as ReactElement<{ onPress(): void }>;
}

it("opens Find in session from the Android ⋯ menu", async () => {
	const { tree } = mount();
	await flush();

	act(() => menuButton().props.onPress());
	const find = tree.root.findAll(
		(node) => String(node.type) === "Pressable" && node.props.accessibilityLabel === "Find in session",
	)[0];
	if (!find) throw new Error("no Find in session in the ⋯ menu");
	act(() => find.props.onPress());
	await flush();

	expect(
		tree.root.findAll(
			(node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === "Find in session",
		),
	).toHaveLength(1);
	tree.unmount();
});

it("shuts the session down after a confirmation and stays on it (ruling 19)", async () => {
	const { tree, requests } = mount(withCapabilities({ shutdown: true }), { "thread/shutdown": {} });
	await flush();

	act(() => menuAction("Shut down").onPress());
	const [confirm] = alertRequests;
	expect(confirm).toMatchObject({
		title: "Shut down this session?",
		message: "It stops now and keeps its history. Sending a message resumes it.",
	});
	expect(confirm?.buttons?.map(({ text, style }) => [text, style])).toEqual([
		["Cancel", "cancel"],
		["Shut down", "destructive"],
	]);
	expect(requests.map(({ method }) => method)).not.toContain("thread/shutdown");
	const reads = () => requests.filter(({ method }) => method === "thread/read").length;
	const readsBefore = reads();
	act(() => confirm?.buttons?.[1]?.onPress?.());
	await flush();

	// It rereads the session it stays on.
	expect(reads()).toBeGreaterThan(readsBefore);

	expect(requests.filter(({ method }) => method === "thread/shutdown")).toEqual([
		{ method: "thread/shutdown", params: { ref } },
	]);
	expect(navigation.pop).not.toHaveBeenCalled();
	expect(navigation.goBack).not.toHaveBeenCalled();
	expect(renderedText(tree)).toContain("Session shut down");
	expect(renderedText(tree)).not.toContain("Runtime stop requested.");
	tree.unmount();
});

it("says so when a shut down can't be confirmed (coordinator ruling: silence reads as success)", async () => {
	const { tree } = mount(withCapabilities({ shutdown: true }), { "thread/shutdown": new Error("refused") });
	await flush();

	act(() => menuAction("Shut down").onPress());
	const [confirm] = alertRequests;
	act(() => confirm?.buttons?.[1]?.onPress?.());
	await flush();

	expect(renderedText(tree)).toContain("Couldn't shut down this session.");
	tree.unmount();
});

it("has no Shut down item on an already shut-down session, even if the hub still reports the capability", async () => {
	// A daemon that hasn't probed capabilities yet reports every capability as
	// true (appsource's fallback), so `capabilities.shutdown` can be true on a
	// session whose own status is already closed/ended.
	for (const status of ["closed", "ended"] as const) {
		const { tree } = mount({ ...withCapabilities({ shutdown: true }), status: { type: status } });
		await flush();

		expect(menuItems().find((item) => item.label === "Shut down")).toBeUndefined();
		tree.unmount();
	}
});

it("archives the session, with an Undo that restores it", async () => {
	const { tree, requests } = mount(thread, { "evener/archive/set": {} });
	await flush();

	act(() => menuAction("Archive").onPress());
	await flush();

	const archives = () => requests.filter(({ method }) => method === "evener/archive/set").map(({ params }) => params);
	expect(archives()).toEqual([{ kind: "session", id: ref, archived: true }]);
	expect(renderedText(tree)).toContain("Session archived");
	const undo = tree.root.find((node) => node.props.accessibilityLabel === "Undo" && node.props.onPress);
	act(() => undo.props.onPress());
	await flush();

	expect(archives()).toEqual([
		{ kind: "session", id: ref, archived: true },
		{ kind: "session", id: ref, archived: false },
	]);
	tree.unmount();
});

it("says so when Undo can't restore the session (coordinator ruling: silence reads as success)", async () => {
	const { tree } = mount(thread, {
		"evener/archive/set": (params: unknown) => {
			if ((params as { archived: boolean }).archived) return {};
			throw new Error("refused");
		},
	});
	await flush();

	act(() => menuAction("Archive").onPress());
	await flush();
	const undo = tree.root.find((node) => node.props.accessibilityLabel === "Undo" && node.props.onPress);
	act(() => undo.props.onPress());
	await flush();

	expect(renderedText(tree)).toContain("Couldn't undo the archive.");
	tree.unmount();
});

it("says so when the hub refuses to archive", async () => {
	const { tree } = mount(thread, { "evener/archive/set": new Error("refused") });
	await flush();

	act(() => menuAction("Archive").onPress());
	await flush();

	expect(renderedText(tree)).toContain("Couldn't archive this session.");
	tree.unmount();
});

it("opens an aside as its own session", async () => {
	const aside = {
		thread: { ...thread, id: "thread-2", name: "Side question", evener: { ...thread.evener, ref: "local:aside" } },
	};
	const { tree } = mount(withCapabilities({ forkFromTurn: true }), { "thread/fork": aside });
	await flush();

	act(() => menuAction("Ask aside…").onPress());
	await flush();

	expect(navigation.push).toHaveBeenCalledWith("Conversation", {
		hubId: "hub-1",
		ref: "local:aside",
		title: "Side question",
	});
	tree.unmount();
});

it("says so when an aside cannot start", async () => {
	const { tree } = mount(withCapabilities({ forkFromTurn: true }), { "thread/fork": new Error("refused") });
	await flush();

	act(() => menuAction("Ask aside…").onPress());
	await flush();

	expect(navigation.push).not.toHaveBeenCalled();
	expect(renderedText(tree)).toContain("Couldn't start an aside.");
	tree.unmount();
});

it("projects the transcript at the level chosen for it, from the menu or elsewhere", async () => {
	const turn = {
		id: "t1",
		status: "completed",
		items: [
			{ id: "u", turnId: "t1", type: "userMessage", text: "look", status: "completed" },
			{ id: "r", turnId: "t1", type: "reasoning", text: "pondering the fix", status: "completed" },
			{
				id: "c",
				turnId: "t1",
				type: "commandExecution",
				toolName: "shell",
				description: "List files",
				argumentsJson: '{"command":"ls"}',
				output: "a.txt",
				status: "completed",
			},
		],
	};
	const { tree } = mount({ ...thread, turns: [turn] } as unknown as Thread);
	await flush();

	act(() => levelAction("Chat").onPress());
	await flush();
	expect(renderedText(tree)).toContain("look");
	expect(renderedText(tree)).not.toContain("shell");

	// Another screen choosing for this session reaches this one too.
	act(() => detailLevels("hub-1").set(ref, "full"));
	await flush();
	expect(renderedText(tree)).toContain("pondering the fix");
	expect(menuItems()[0]).toMatchObject({ label: "Detail level · Full" });
	tree.unmount();
});

it("opens the live run where tool calls show, and leaves it to its line at Intent", async () => {
	const running = {
		...thread,
		status: { type: "active" },
		evener: { ...thread.evener, activeTurnId: "t1" },
		turns: [
			{
				id: "t1",
				status: "inProgress",
				items: [
					{ id: "u", turnId: "t1", type: "userMessage", text: "look", status: "completed" },
					{
						id: "c",
						turnId: "t1",
						type: "commandExecution",
						toolName: "shell",
						argumentsJson: '{"command":"ls"}',
						output: "a.txt",
						status: "completed",
					},
				],
			},
		],
	} as unknown as Thread;
	const { tree } = mount(running);
	await flush();
	// A run's fold control names it collapsed or expanded; a run held open
	// while live has none.
	const folds = () =>
		tree.root.findAll((node) => /^1 step\b.*, (collapsed|expanded)$/.test(String(node.props.accessibilityLabel ?? "")));
	act(() => detailLevels("hub-1").set(ref, "intent"));
	await flush();
	expect(folds()[0]?.props.accessibilityLabel).toMatch(/, collapsed$/);
	act(() => detailLevels("hub-1").set(ref, "tools"));
	await flush();
	expect(folds()).toEqual([]);
	tree.unmount();
});

/** A session with a subagent, tasks, a blocked goal and two queued
 * messages: every context chip. */
const busy = {
	...thread,
	evener: {
		...thread.evener,
		queue: { revision: 1, depth: 2 },
		goal: { objective: "Ship it", status: "blocked", iterations: 2 },
		diagnostics: {
			delegates: [
				{
					delegateId: "d1",
					ownerSessionId: "root",
					rootSessionId: "root",
					childSessionId: "c1",
					transcriptRef: "local:c1",
					type: "subagent",
					lifecycle: "running",
					phase: "running",
					status: "running",
					resumable: false,
					needsAttention: false,
					projectionRevision: 1,
				},
			],
		},
	},
} as unknown as Thread;

/** The screen's header block, its list, and scrolling it. */
function sessionList(tree: ReturnType<typeof render>) {
	const block = () => tree.root.findByType(SessionHeader);
	const list = () => tree.root.findByType(FlatList);
	return {
		block,
		list,
		/** The block's panel reporting a new height, as layout would. */
		measure: (height: number) =>
			act(() =>
				block()
					.findByType(GlassHeaderPanel)
					.props.onLayout({ nativeEvent: { layout: { height } } }),
			),
		// contentSize/layoutMeasurement match ConversationScreen.send.test.tsx's
		// own scrollTo: tall enough that these small offsets never cross the
		// "near the live end" threshold onScroll also checks.
		scroll: (y: number) =>
			act(() =>
				list().props.onScroll({
					nativeEvent: { contentOffset: { y }, contentSize: { height: 4_000 }, layoutMeasurement: { height: 600 } },
				}),
			),
		// As React Native does, the drag's end carries where it let go.
		drag: (y: number) => {
			const event = {
				nativeEvent: { contentOffset: { y }, contentSize: { height: 4_000 }, layoutMeasurement: { height: 600 } },
			};
			act(() => list().props.onScrollBeginDrag(event));
			act(() => list().props.onScroll(event));
			act(() => list().props.onScrollEndDrag(event));
		},
	};
}

it("floats the context chips over the list, opens each one's sheet, and hides them on a downward scroll", async () => {
	const { tree } = mount(busy);
	await flush();
	const session = sessionList(tree);

	// A live connection says nothing, and the old Reconnect row is gone.
	expect(session.block().props.status).toBeNull();
	expect(renderedText(tree)).not.toMatch(/Connected|Reconnect/);
	// The goal is a context chip (spec 8.1) and nothing else: the bottom bar
	// doesn't repeat it as a row, which would cost the transcript a line.
	expect(renderedText(tree)).not.toContain("Goal · blocked");
	const chip = (label: string) => {
		const found = session
			.block()
			.findAll((node) => node.props.accessibilityRole === "button" && node.props.accessibilityLabel === label)[0];
		if (!found) throw new Error(`no ${label} chip`);
		return found;
	};

	// The list reserves the block's height under its own top padding.
	session.measure(48);
	expect(session.list().props.contentContainerStyle).toMatchObject({ paddingTop: 64 });

	act(() => chip("Subagents, 1").props.onPress());
	expect(navigation.navigate).toHaveBeenCalledWith("Subagents", {
		hubId: "hub-1",
		ref,
		threadId: "thread-1",
		title: "Session",
	});
	vi.mocked(navigation.navigate).mockClear();
	act(() => menuAction("Activity").onPress());
	expect(navigation.navigate).toHaveBeenCalledWith("Subagents", {
		hubId: "hub-1",
		ref,
		threadId: "thread-1",
		title: "Session",
	});

	act(() => chip("Tasks, 1 of 2 done").props.onPress());
	expect(navigation.navigate).toHaveBeenCalledWith("TasksSheet", {
		hubId: "hub-1",
		ref,
		threadId: "thread-1",
		hasTasks: true,
	});

	act(() => chip("Goal, blocked").props.onPress());
	expect(navigation.navigate).toHaveBeenCalledWith("SessionInfoSheet", { hubId: "hub-1", ref });

	act(() => chip("2 queued messages").props.onPress());
	expect(navigation.navigate).toHaveBeenCalledWith("QueueSheet", { hubId: "hub-1", ref });

	act(() => session.list().props.onScrollBeginDrag());
	session.scroll(40);
	expect(session.block().props.hidden).toBe(true);
	session.scroll(30);
	expect(session.block().props.hidden).toBe(false);
	// Hiding never moves the list.
	expect(session.list().props.contentContainerStyle).toMatchObject({ paddingTop: 64 });
	tree.unmount();
});

it("hides the Subagents and Tasks chips once the connection bar itself would say something, but keeps the cached Goal and Queue chips", async () => {
	const { tree, client } = mount(busy);
	await flush();
	vi.useFakeTimers();
	try {
		const { block } = sessionList(tree);
		const label = (text: string) => block().findAll((node) => node.props.accessibilityLabel === text);
		expect(label("Subagents, 1")).toHaveLength(1);

		// The connection drops; the thread's cached delegates/tasks survive.
		harness.connection = { ...harness.connection, ...screenConnection(client, "reconnecting") };
		act(() => tree.update(screen()));
		// A blip shorter than the bar's own grace period (spec 14) - the chips
		// stay exactly as visible as they were, since the bar itself says
		// nothing yet either.
		expect(label("Subagents, 1")).toHaveLength(1);

		act(() => {
			vi.advanceTimersByTime(2000);
		});
		act(() => tree.update(screen()));
		expect(label("Subagents, 1")).toEqual([]);
		expect(label("Tasks, 1 of 2 done")).toEqual([]);
		// Goal and Queue need no connection, so they still show.
		expect(label("Goal, blocked")).toHaveLength(1);
		expect(label("2 queued messages")).toHaveLength(1);
	} finally {
		vi.useRealTimers();
	}
	tree.unmount();
});

it("a Subagents/Tasks chip tap still works during a blip shorter than the connection bar's own grace period (Calm)", async () => {
	const { tree, client } = mount(busy);
	await flush();
	vi.useFakeTimers();
	try {
		harness.connection = { ...harness.connection, ...screenConnection(client, "reconnecting") };
		act(() => tree.update(screen()));

		const { block } = sessionList(tree);
		const chip = (label: string) =>
			block().findAll(
				(node) => node.props.accessibilityRole === "button" && node.props.accessibilityLabel === label,
			)[0];
		vi.mocked(navigation.navigate).mockClear();
		act(() => chip("Subagents, 1").props.onPress());
		expect(navigation.navigate).toHaveBeenCalledWith("Subagents", {
			hubId: "hub-1",
			ref,
			threadId: "thread-1",
			title: "Session",
		});
	} finally {
		vi.useRealTimers();
	}
	tree.unmount();
});

// While you type in the composer, the chips and the note row step aside so
// the transcript keeps its room; the nav bar stays (spec 8.1).
// Whether the header's chips-and-note row is slid away behind the nav bar
// (out of VoiceOver's reach while it is), whatever slid it: a downward scroll
// or typing, which the header reads from the keyboard itself.
const slidAway = (block: ReturnType<ReturnType<typeof sessionList>["block"]>) =>
	block.findAll((node) => String(node.type) === "Animated.View")[0]?.props.accessibilityElementsHidden;

/** Focuses the composer's field and raises the keyboard for it. */
function typeInComposer(tree: ReturnType<typeof render>) {
	act(() =>
		tree.root
			.find((node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === "Message")
			.props.onFocus(),
	);
	act(() => keyboard.show());
}

it("steps the chips and note aside while you type, and brings them back when the keyboard lowers", async () => {
	const { tree } = mount(busy);
	await flush();
	const session = sessionList(tree);
	expect(slidAway(session.block())).toBe(false);
	typeInComposer(tree);
	expect(slidAway(session.block())).toBe(true);
	act(() => keyboard.hide());
	expect(slidAway(session.block())).toBe(false);
	tree.unmount();
});

it("keeps the nav bar while you type", async () => {
	const { tree } = mount(busy);
	await flush();
	navigation.setOptions.mockClear();
	act(() => keyboard.show());
	const calls = navigation.setOptions.mock.calls as [NativeStackNavigationOptions][];
	expect(calls.some(([options]) => options.headerShown === false)).toBe(false);
	act(() => keyboard.hide());
	tree.unmount();
});

it("keeps the find bar in place while you type in it", async () => {
	const { tree } = mount(busy);
	await flush();
	const session = sessionList(tree);
	act(() => menuAction("Find in session").onPress());
	act(() => keyboard.show());
	expect(slidAway(session.block())).toBe(false);
	expect(session.block().props.find).toBeDefined();
	act(() => keyboard.hide());
	tree.unmount();
});

it("stays hidden after the keyboard lowers when a downward scroll hid the chips", async () => {
	const { tree } = mount(busy);
	await flush();
	const session = sessionList(tree);
	act(() => session.list().props.onScrollBeginDrag());
	session.scroll(40);
	expect(slidAway(session.block())).toBe(true);
	typeInComposer(tree);
	expect(slidAway(session.block())).toBe(true);
	act(() => keyboard.hide());
	expect(slidAway(session.block())).toBe(true);
	tree.unmount();
});

it("hides the chips only for the person's own drag, never for the app moving the list", async () => {
	const { tree } = mount(busy);
	await flush();
	const session = sessionList(tree);

	// A reading-position restore or following the latest message moves the
	// list with no drag: the chips stay.
	session.scroll(4000);
	expect(session.block().props.hidden).toBe(false);

	// The person's drag, counted from where the list landed.
	session.drag(4020);
	expect(session.block().props.hidden).toBe(true);

	// A coast after the drag counts as the person's too.
	act(() => session.list().props.onMomentumScrollBegin());
	session.scroll(4010);
	act(() =>
		session.list().props.onMomentumScrollEnd({
			nativeEvent: { contentOffset: { y: 4010 }, contentSize: { height: 4_000 }, layoutMeasurement: { height: 600 } },
		}),
	);
	expect(session.block().props.hidden).toBe(false);
	tree.unmount();
});

it("keeps the transcript in place when the connection bar comes and goes", async () => {
	const { tree } = mount(busy);
	await flush();
	const session = sessionList(tree);
	session.measure(48);
	session.drag(500);
	expect(listScrolls).toEqual([]);

	// The bar adds 24pt above the chips: the padding grows and the list
	// scrolls by the same amount, so every row stays where it was on screen.
	session.measure(72);
	expect(session.list().props.contentContainerStyle).toMatchObject({ paddingTop: 88 });
	expect(listScrolls).toEqual([{ offset: 524, animated: false }]);
	session.scroll(524);

	session.measure(48);
	expect(session.list().props.contentContainerStyle).toMatchObject({ paddingTop: 64 });
	expect(listScrolls.at(-1)).toEqual({ offset: 500, animated: false });
	tree.unmount();
});

it("composes two header-height changes correctly even before the list's own onScroll catches up (scrollEventThrottle)", async () => {
	const { tree } = mount(busy);
	await flush();
	const session = sessionList(tree);
	session.measure(48);
	session.drag(500);

	// Two height changes land within the same 100ms onScroll throttle window
	// (a reconnect can change the bar and the chips from different sources),
	// so the second compensation has to build on where the first one is
	// already taking the list, not on the last onScroll the list actually
	// reported.
	session.measure(72);
	session.measure(96);

	expect(listScrolls).toEqual([
		{ offset: 524, animated: false },
		{ offset: 548, animated: false },
	]);
	tree.unmount();
});

it("stays at the top when the header changes there", async () => {
	const { tree } = mount(busy);
	await flush();
	const session = sessionList(tree);
	session.measure(48);
	session.measure(72);
	expect(session.list().props.contentContainerStyle).toMatchObject({ paddingTop: 88 });
	expect(listScrolls).toEqual([]);
	tree.unmount();
});

const OLD_PROTOCOL_ERROR = "This app and hub need compatible versions. Update them together, then reconnect.";
const OLD_TRANSPORT_ERROR = "Could not connect. Check the hub address, token, and network, then retry.";

it("says Update needed, with the spec's hint, when no retry can fix the connection", async () => {
	const { tree } = mount(busy, {}, { state: "closed", fatal: true, error: OLD_PROTOCOL_ERROR });
	await flush();

	expect(sessionList(tree).block().props.status).toBe("Update needed");
	const bar = tree.root.find((node) => node.type === ("Text" as never) && node.props.children === "Update needed");
	expect(bar.props.accessibilityHint).toBe(
		"This app and the hub need compatible versions. Update the app from TestFlight, or update Evener on the hub.",
	);
	expect(renderedText(tree)).not.toContain(OLD_PROTOCOL_ERROR);
	tree.unmount();
});

it("says Reconnecting… and then how old the session is while the hub is out of reach", async () => {
	const { tree, client } = mount(busy);
	await flush();
	vi.useFakeTimers();
	try {
		harness.connection = {
			...harness.connection,
			...screenConnection(client, "reconnecting"),
			error: OLD_TRANSPORT_ERROR,
		};
		act(() => tree.update(screen()));
		const status = () => sessionList(tree).block().props.status;
		const advance = (ms: number) =>
			act(() => {
				vi.advanceTimersByTime(ms);
			});
		expect(status()).toBeNull();
		advance(2000);
		expect(status()).toBe("Reconnecting…");
		advance(28_000);
		expect(status()).toBe("Offline · updated 1m ago");
		// The status re-renders once a minute of the data's age, one tick per
		// act.
		advance(30_000);
		advance(60_000);
		advance(60_000);
		expect(status()).toBe("Offline · updated 3m ago");
		expect(renderedText(tree)).not.toContain(OLD_TRANSPORT_ERROR);
		expect(renderedText(tree)).toContain("Offline · updated 3m ago");

		// Back in reach, the bar goes without a word.
		harness.connection = { ...screenConnection(client, "ready"), error: null, disconnect: () => {} };
		act(() => tree.update(screen()));
		expect(status()).toBeNull();
	} finally {
		vi.useRealTimers();
	}
	tree.unmount();
});

it("marks its session seen through the read's turn end once it has loaded in front (S4)", async () => {
	const endedAt = Date.UTC(2026, 8, 26, 12, 0, 0, 123);
	const { tree, requests } = mount({ ...thread, evener: { ...thread.evener, lastTurnEndedAt: endedAt } });
	await flush();

	expect(requests.filter((request) => request.method === "evener/session/seen/set")).toEqual([
		{ method: "evener/session/seen/set", params: { sessions: [{ ref, seenThrough: endedAt }] } },
	]);
	tree.unmount();
});

afterEach(() => vi.useRealTimers());

function olderHistoryAnswers(page: () => unknown, source: Thread = busy): Answers {
	return {
		"thread/read": {
			thread: {
				...source,
				turns: [
					{
						id: "current-turn",
						status: "completed",
						itemsView: "full",
						items: [
							{
								id: "current-message",
								turnId: "current-turn",
								type: "userMessage",
								status: "completed",
								text: "loaded current message",
							},
						],
					},
				],
			},
			olderCursor: "older-page",
		},
		"thread/turns/list": page,
	};
}

function olderHistoryPage(text: string) {
	return {
		data: [
			{
				id: "older-turn",
				status: "completed",
				itemsView: "full",
				items: [{ id: "older-message", turnId: "older-turn", type: "userMessage", status: "completed", text }],
			},
		],
		nextCursor: null,
	};
}

async function advanceHistory(ms: number) {
	await act(async () => {
		await vi.advanceTimersByTimeAsync(ms);
	});
}

function openFind(tree: ReturnType<typeof render>, query: string) {
	act(() => menuAction("Find in session").onPress());
	const field = tree.root.find(
		(node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === "Find in session",
	);
	act(() => field.props.onChangeText(query));
}

it("recovers prolonged older-history demand without a second scroll and keeps the reader away from live", async () => {
	let attempts = 0;
	const { tree } = mount(
		busy,
		olderHistoryAnswers(() => {
			attempts += 1;
			if (attempts <= 12) throw new Error("temporary history failure");
			return olderHistoryPage("older sought message");
		}),
	);
	await flush();
	const reader = sessionList(tree);
	const loaded = reader.list().props.data;
	expect(loaded.length).toBeGreaterThan(0);
	vi.useFakeTimers();
	endScrolls.mockClear();
	reader.drag(100);
	await advanceHistory(0);
	expect(attempts).toBe(1);
	expect(reader.list().props.data).toEqual(loaded);
	await advanceHistory(999);
	expect(attempts).toBe(1);
	await advanceHistory(300_000);
	expect(attempts).toBe(13);
	expect(renderedText(tree)).toContain("older sought message");
	expect(reader.list().props.data.length).toBeGreaterThan(loaded.length);
	const keys = reader.list().props.data.map(readerKey);
	expect(new Set(keys).size).toBe(keys.length);
	const loadedKeys = loaded.map(readerKey);
	expect(keys.filter((key: string) => loadedKeys.includes(key))).toEqual(loadedKeys);
	expect(endScrolls).not.toHaveBeenCalled();
	act(() => tree.unmount());
});

it("keeps Find incomplete through failure and an inactive screen, then finds the healed page on return", async () => {
	let attempts = 0;
	const { tree } = mount(
		busy,
		olderHistoryAnswers(() => {
			attempts += 1;
			if (attempts <= 2) throw new Error("temporary history failure");
			return olderHistoryPage("unique search needle");
		}),
	);
	await flush();
	vi.useFakeTimers();
	openFind(tree, "unique search needle");
	await advanceHistory(0);
	expect(attempts).toBe(1);
	expect(renderedText(tree)).not.toContain("No matches");
	expect(renderedText(tree)).toContain("Searching older messages");
	act(() => {
		for (const listener of appStateListeners) listener("background");
	});
	await advanceHistory(60_000);
	expect(attempts).toBe(1);
	act(() => {
		for (const listener of appStateListeners) listener("active");
	});
	stack.state = { index: 1, routes: [session, { key: "reader", name: "Reader" }] };
	act(() => tree.update(screen()));
	await advanceHistory(60_000);
	expect(attempts).toBe(1);
	stack.state = { index: 0, routes: [session] };
	act(() => tree.update(screen()));
	await advanceHistory(10_000);
	expect(attempts).toBe(3);
	expect(renderedText(tree)).toContain("unique search needle");
	expect(renderedText(tree)).toContain("1 of 1");
	expect(renderedText(tree)).not.toContain("No matches");
	act(() => tree.unmount());
});

it("explains a proven permanent older-history failure without claiming Find has no matches", async () => {
	let attempts = 0;
	const { tree } = mount(
		busy,
		olderHistoryAnswers(() => {
			attempts += 1;
			throw new WireError("Update this client to read history.", -32000, { evenerErrorInfo: "upgradeRequired" });
		}),
	);
	await flush();
	vi.useFakeTimers();
	openFind(tree, "unknown needle");
	await advanceHistory(0);
	expect(renderedText(tree)).toContain("Search incomplete");
	expect(renderedText(tree)).toContain("Update this client to read history.");
	expect(renderedText(tree)).not.toContain("No matches");
	await advanceHistory(300_000);
	expect(attempts).toBe(1);
	act(() => tree.unmount());
});

it.each([false, true])("closing Find cancels only its demand (browse waiting: %s)", async (browse) => {
	let attempts = 0;
	const { tree } = mount(
		busy,
		olderHistoryAnswers(() => {
			attempts += 1;
			if (attempts <= 1) throw new Error("temporary history failure");
			return olderHistoryPage("unique search needle");
		}),
	);
	await flush();
	vi.useFakeTimers();
	if (browse) {
		sessionList(tree).drag(100);
		await advanceHistory(0);
	}
	openFind(tree, "unique search needle");
	await advanceHistory(0);
	expect(attempts).toBe(1);
	const bar = tree.root.find((node) => typeof node.type === "function" && node.type.name === "FindBar");
	act(() => bar.props.onDone());
	await advanceHistory(60_000);
	expect(attempts).toBe(browse ? 2 : 1);
	expect(renderedText(tree).includes("unique search needle")).toBe(browse);
	act(() => tree.unmount());
});

it.each([false, true])("jumping live cancels browse demand and preserves Find (Find waiting: %s)", async (find) => {
	let attempts = 0;
	const { tree } = mount(
		busy,
		olderHistoryAnswers(() => {
			attempts += 1;
			if (attempts <= 1) throw new Error("temporary history failure");
			return olderHistoryPage("unique search needle");
		}),
	);
	await flush();
	vi.useFakeTimers();
	sessionList(tree).drag(100);
	await advanceHistory(0);
	if (find) {
		openFind(tree, "unique search needle");
		await advanceHistory(0);
	}
	const composer = tree.root.findAll((node) => typeof node.props.onJumpToLive === "function")[0];
	expect(composer).toBeDefined();
	act(() => composer.props.onJumpToLive());
	await advanceHistory(60_000);
	expect(attempts).toBe(find ? 2 : 1);
	expect(renderedText(tree).includes("unique search needle")).toBe(find);
	act(() => tree.unmount());
});

it.each(["closed", "ended"] as const)("Find reads older history for a %s session", async (status) => {
	let attempts = 0;
	const source = { ...busy, status: { type: status } };
	const { tree } = mount(
		source,
		olderHistoryAnswers(() => {
			attempts += 1;
			if (attempts === 1) throw new Error("temporary");
			return olderHistoryPage("finished session needle");
		}, source),
	);
	await flush();
	vi.useFakeTimers();
	openFind(tree, "finished session needle");
	await advanceHistory(10_000);
	expect(attempts).toBe(2);
	expect(renderedText(tree)).toContain("1 of 1");
	act(() => tree.unmount());
});

it.each([
	[false, false],
	[true, false],
	[false, true],
])("handles pending history through a replacement connection (Find: %s, new binding: %s)", async (find, newBinding) => {
	let attempts = 0;
	const page = () => {
		attempts += 1;
		if (attempts === 1) throw new Error("temporary history failure");
		return olderHistoryPage("replacement search needle");
	};
	const answers = olderHistoryAnswers(page);
	const { tree } = mount(busy, answers);
	await flush();
	vi.useFakeTimers();
	if (find) openFind(tree, "replacement search needle");
	else sessionList(tree).drag(100);
	await advanceHistory(0);
	expect(attempts).toBe(1);
	let finishRead: ((value: unknown) => void) | undefined;
	const read = new Promise((resolve) => {
		finishRead = resolve;
	});
	const replacement = sessionClient(busy, { ...answers, "thread/read": () => read });
	harness.connection = { ...screenConnection(replacement.client, "ready"), error: null, disconnect: () => {} };
	act(() => tree.update(screen()));
	await advanceHistory(0);
	if (find) expect(renderedText(tree)).not.toContain("No matches");
	await act(async () => {
		finishRead?.(
			newBinding
				? olderHistoryAnswers(page, { ...busy, evener: { ...busy.evener, instanceId: "replacement-instance" } })[
						"thread/read"
					]
				: answers["thread/read"],
		);
	});
	await advanceHistory(10_000);
	expect(attempts).toBe(newBinding ? 1 : 2);
	expect(renderedText(tree).includes("replacement search needle")).toBe(!newBinding);
	if (find) expect(renderedText(tree)).toContain("1 of 1");
	act(() => tree.unmount());
});
