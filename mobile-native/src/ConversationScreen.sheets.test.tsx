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
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import type { Thread } from "@evener/appwire-client";
import {
	alertRequests,
	render,
	renderedText,
	screenConnection,
} from "./renderNative.testkit";
import { ConversationScreen } from "./screens";
import { detailLevels } from "./session/nativeDetailLevels";
import { SessionTitle } from "./session/SessionTitle";
import { SessionSheet } from "./SessionSheet";

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
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-enriched-markdown", () => ({
	EnrichedMarkdownText: "EnrichedMarkdownText",
}));
vi.mock("@react-navigation/elements", () => ({ useHeaderHeight: () => 64 }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => void | (() => void)) =>
			useEffect(effect, []),
		useIsFocused: () => stack.focused,
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
type Answers = Record<string, unknown | ((params: unknown) => unknown)>;

/** A hub that answers the session's read with `read` and records every
 * request the screen makes. */
function sessionClient(read: Thread, answers: Answers) {
	const requests: { method: string; params: unknown }[] = [];
	const client = {
		state: "ready",
		onStateChange: () => () => {},
		request: async (method: string, params?: unknown) => {
			requests.push({ method, params });
			if (method === "thread/read") return { thread: read };
			if (method in answers) {
				const answer = answers[method];
				if (typeof answer === "function") return answer(params);
				if (answer instanceof Error) throw answer;
				return answer;
			}
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

function mount(read: Thread = thread, answers: Answers = {}) {
	const { client, requests } = sessionClient(read, answers);
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
	navigation.push.mockClear();
	navigation.pop.mockClear();
	navigation.goBack.mockClear();
	navigation.setOptions.mockClear();
	alertRequests.length = 0;
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

const withCapabilities = (capabilities: Partial<Thread["evener"]["capabilities"]>): Thread => ({
	...thread,
	evener: { ...thread.evener, capabilities: { ...thread.evener.capabilities, ...capabilities } },
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

it("titles the header with the session's state, and opens its info on a press", async () => {
	const { tree } = mount();
	await flush();

	const title = header().headerTitle;
	if (typeof title !== "function") throw new Error("no header title component");
	const element = title({ children: "Session" }) as ReactElement<ComponentProps<typeof SessionTitle>>;
	expect(element.type).toBe(SessionTitle);
	expect(element.props).toMatchObject({ title: "Session", line: { state: "idle", text: "Finished" } });
	expect(tree.root.findAllByType(SessionSheet)).toEqual([]);
	act(() => element.props.onPress());

	expect(tree.root.findAllByType(SessionSheet)).toHaveLength(1);
	tree.unmount();
});

it("shows the chosen detail level and confirms it", async () => {
	const { tree } = mount();
	await flush();

	const [detail] = menuItems();
	const [section] = detail?.type === "submenu" ? detail.items : [];
	const full = section?.type === "submenu" ? section.items.find((item) => item.label === "Full") : undefined;
	if (full?.type !== "action") throw new Error("no Full level in the menu");
	act(() => full.onPress());

	expect(detailLevels("hub-1").get(ref)).toBe("full");
	expect(menuItems()[0]).toMatchObject({ label: "Detail level · Full" });
	expect(renderedText(tree)).toContain("Full: everything, including the agent's reasoning");
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
	const aside = { thread: { ...thread, id: "thread-2", name: "Side question", evener: { ...thread.evener, ref: "local:aside" } } };
	const { tree } = mount(withCapabilities({ forkFromTurn: true }), { "thread/fork": aside });
	await flush();

	act(() => menuAction("Ask aside…").onPress());
	await flush();

	expect(navigation.push).toHaveBeenCalledWith("Conversation", { hubId: "hub-1", ref: "local:aside", title: "Side question" });
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

	const [detail] = menuItems();
	const [section] = detail?.type === "submenu" ? detail.items : [];
	const chat = section?.type === "submenu" ? section.items.find((item) => item.label === "Chat") : undefined;
	if (chat?.type !== "action") throw new Error("no Chat level in the menu");
	act(() => chat.onPress());
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
