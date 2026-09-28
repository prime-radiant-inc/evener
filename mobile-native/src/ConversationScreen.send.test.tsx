// The Session's one Send and the tray's Stop, on the real ConversationScreen:
// what a person presses, and which requests reach the hub through the durable
// runtime. Only native edges are mocked, as in
// ConversationScreen.recovery.test.tsx.
import type { ComponentProps, ReactNode } from "react";
import { createElement } from "react";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AnyNotification, Thread } from "@evener/appwire-client";
import { flatListCalls, pressable, render, renderedText, screenConnection } from "./renderNative.testkit";
import { queueHosts } from "./QueueSheet";
import { ConversationScreen } from "./screens";
import { sheetKey } from "./sheet/sheetHosts";
import { holdQuote, takeQuote } from "./session/pendingQuote";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
	uuid: 0,
	// The device's key-value store, where reading positions are saved.
	kv: new Map<string, string>(),
}));

// The root stack the screen sits in, read by useScreenInFront and
// screenInFront (screens.tsx). Kept at the screen's own route on top, so the
// screen is in front the way a freshly opened conversation really is - this
// suite isn't exercising sheet coverage or a pushed screen, unlike
// ConversationScreen.sheets.test.tsx.
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
		Keyboard: { dismiss: vi.fn() },
		Linking: { openURL: vi.fn() },
		RefreshControl: "RefreshControl",
		StatusBar: "StatusBar",
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
		useNavigationState: <T,>(select: (state: typeof navigationState.state) => T) =>
			select(navigationState.state),
	};
});
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("expo-clipboard", () => ({
	setStringAsync: vi.fn(async () => {}),
	getStringAsync: vi.fn(async () => ""),
}));
// Every durable mutation needs its own id, so the uuid counts.
vi.mock("expo-crypto", () => ({
	randomUUID: () => {
		harness.uuid += 1;
		return `send-test-uuid-${harness.uuid}`;
	},
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
	Storage: {
		getItemSync: (key: string) => harness.kv.get(key) ?? null,
		setItemSync: (key: string, value: string) => void harness.kv.set(key, value),
		removeItemSync: (key: string) => void harness.kv.delete(key),
	},
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
	launchCameraAsync: vi.fn(async () => ({ canceled: true, assets: [] })),
	requestCameraPermissionsAsync: vi.fn(async () => ({ granted: true })),
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
	forkFromTurn: false,
	shutdown: false,
	changeModel: false,
	changeVisionModel: false,
	sharedNotes: false,
	goal: false,
	rename: false,
};

// An ask_user call the model holds as pending, shaped like
// questionAnswers.test.ts's fixture.
const QUESTION_TURN = {
	id: "t1",
	status: "completed",
	itemsView: "default",
	items: [
		{
			id: "ask-1",
			turnId: "t1",
			type: "commandExecution",
			toolName: "ask_user",
			status: "completed",
			argumentsJson: JSON.stringify({
				questions: [
					{
						header: "Choice",
						question: "Keep or drop the implied options?",
						options: [{ label: "Drop them", detail: "" }],
						multi_select: false,
					},
				],
			}),
		},
	],
};

// A thread hydrated like the store tests' makeConversation: a minimal wire
// Thread the hub answers thread/read with.
function thread(ref: string, status: "idle" | "active", question = false, queued: string[] = []): Thread {
	return {
		id: `thread-${ref}`,
		sessionId: `session-${ref}`,
		preview: "",
		ephemeral: false,
		modelProvider: "anthropic",
		createdAt: 0,
		updatedAt: 0,
		status: { type: status },
		cwd: "",
		cliVersion: "",
		source: "",
		turns: question ? [QUESTION_TURN] : [],
		evener: {
			ref,
			instanceId: "instance",
			capabilities: CAPABILITIES,
			queue: queueState(queued),
			...(question ? { askPending: true } : {}),
		},
	} as unknown as Thread;
}

function queueState(texts: string[], revision = 0) {
	return {
		revision,
		depth: texts.length,
		preview: texts,
		texts,
		ids: texts.map((_, index) => `queue_${index + 1}`),
	};
}

/** The hub: it answers thread/read with `served` and acknowledges every
 * mutation, recording each request in order. It sends a frame only when a
 * test calls notify(). */
const otherThreads = new Map<string, Thread>();
afterEach(() => {
	otherThreads.clear();
	vi.unstubAllGlobals();
});

function hubClient(served: Thread, failedReads = 0, readLatencyMs = 0, olderCursor?: string) {
	let readsToFail = failedReads;
	const requests: { method: string; params: Record<string, unknown> }[] = [];
	const listeners = new Set<(notification: AnyNotification) => void>();
	const client = {
		state: "ready",
		onStateChange: () => () => {},
		onNotification: (listener: (notification: AnyNotification) => void) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		resumeThread: async (ref: string) => {
			requests.push({ method: "resumeThread", params: { ref } });
		},
		request: async (method: string, params: Record<string, unknown>) => {
			requests.push({ method, params });
			if (method === "thread/read") {
				if (readLatencyMs > 0) await new Promise((resolve) => setTimeout(resolve, readLatencyMs));
				if (readsToFail > 0) {
					readsToFail -= 1;
					throw new Error("read failed");
				}
				// A second session this client can also read, by its ref.
				const thread = otherThreads.get(String(params.ref)) ?? served;
				return { thread, ...(olderCursor ? { olderCursor } : {}) };
			}
			if (method === "thread/turns/list") return { data: [] };
			if (method.startsWith("turn/"))
				return {
					receipt: {
						clientMutationId: params.clientMutationId,
						disposition: "applied",
						threadId: served.id,
						turnId: "turn-1",
						projectionState: "pending",
						instanceId: "instance",
						// A queue action's receipt names the entry it acted on.
						...(params.expectedEntryId ? { queueEntryIds: [params.expectedEntryId] } : {}),
					},
				};
			return {};
		},
	};
	return {
		client,
		mutations: () =>
			requests
				.filter((request) => request.method.startsWith("turn/"))
				.map((request) => request.method),
		requests,
		/** A frame the hub pushes to every live subscriber. */
		notify(notification: AnyNotification) {
			for (const listener of [...listeners]) listener(notification);
		},
	};
}

async function mount(
	served: Thread,
	{ failedReads = 0, readLatencyMs = 0, settled = true, olderCursor = undefined as string | undefined } = {},
) {
	const hub = hubClient(served, failedReads, readLatencyMs, olderCursor);
	harness.connection = {
		...screenConnection(hub.client, "ready"),
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
	if (settled) await settle();
	return { tree, hub };
}

function field(tree: ReactTestRenderer) {
	return tree.root
		.findAll((node) => String(node.type) === "TextInput")
		.find((node) => node.props.accessibilityLabel === "Message");
}

async function type(tree: ReactTestRenderer, text: string) {
	const input = field(tree);
	if (!input) throw new Error("no Message field");
	act(() => input.props.onChangeText(text));
	await flush();
}

async function press(tree: ReactTestRenderer, label: string) {
	const target = pressable(tree, label);
	if (!target) throw new Error(`no pressable labelled ${label}`);
	expect(target.props.accessibilityState).toMatchObject({ disabled: false });
	act(() => target.props.onPress());
	await settle();
}

it("queues a message while the agent works", async () => {
	const { tree, hub } = await mount(thread("ref-queue", "active"));
	expect(field(tree)?.props.placeholder).toBe("Tell the agent something…");
	await type(tree, "second");
	await press(tree, "Queue message");
	expect(hub.mutations()).toEqual(["turn/queue"]);
	expect(pressable(tree, "Steer")).toBeUndefined();
});

it("sends a message when the agent is at rest", async () => {
	const { tree, hub } = await mount(thread("ref-send", "idle"));
	expect(field(tree)?.props.placeholder).toBe("Message");
	expect(pressable(tree, "Stop")).toBeUndefined();
	await type(tree, "first");
	const send = pressable(tree, "Send");
	expect(send?.findByType("SymbolView" as never).props.name).toBe("paperplane.fill");
	await press(tree, "Send");
	expect(hub.mutations()).toEqual(["turn/start"]);
});

it("stops the running turn from the tray and says so", async () => {
	const { tree, hub } = await mount(thread("ref-stop", "active"));
	await press(tree, "Stop");
	expect(hub.mutations()).toEqual(["turn/interrupt"]);
	expect(renderedText(tree)).toContain("Stopped");
});

it("queues a second Send pressed before the first one's turn is seen", async () => {
	const { tree, hub } = await mount(thread("ref-twice", "idle"));
	await type(tree, "first");
	await press(tree, "Send");
	expect(hub.mutations()).toEqual(["turn/start"]);
	// No status frame has arrived: the thread still reads idle, but this
	// phone's own unreflected send means the next message waits its turn.
	await type(tree, "second");
	await press(tree, "Queue message");
	expect(hub.mutations()).toEqual(["turn/start", "turn/queue"]);
	const inputs = hub.requests
		.filter((request) => request.method.startsWith("turn/"))
		.map((request) => request.params.input);
	expect(inputs).toEqual([
		[{ type: "text", text: "first" }],
		[{ type: "text", text: "second" }],
	]);
});

it("hides the message field and Send while a question waits for an answer", async () => {
	const { tree } = await mount(thread("ref-question", "active", true));
	expect(renderedText(tree)).toContain("1 question to answer");
	expect(field(tree)).toBeUndefined();
	for (const label of ["Send", "Queue message", "Send answer"])
		expect(pressable(tree, label)).toBeUndefined();
});

it("gives Send a typed command's own label, so VoiceOver hears what it runs", async () => {
	const { tree, hub } = await mount(thread("ref-command", "idle"));
	await type(tree, "/compact");
	const send = pressable(tree, "Compact transcript");
	// This session cannot compact, so the command's Send is disabled as
	// today's command button was.
	expect(send?.props.accessibilityState).toMatchObject({ disabled: true });
	expect(pressable(tree, "Send")).toBeUndefined();
	await type(tree, "/steer now");
	expect(pressable(tree, "Steer")).toBeDefined();
	expect(hub.mutations()).toEqual([]);
});

it("does nothing when a Stop lands after the turn already ended", async () => {
	const served = thread("ref-stale-stop", "active");
	const { tree, hub } = await mount(served);
	const stalePress = pressable(tree, "Stop")?.props.onPress as () => void;
	expect(stalePress).toBeDefined();
	act(() =>
		hub.notify({
			method: "thread/status/changed",
			params: {
				threadId: served.id,
				ref: "ref-stale-stop",
				status: { type: "idle" },
			},
		} as AnyNotification),
	);
	await settle();
	// The frame reached the screen: the tray and its Stop are gone.
	expect(pressable(tree, "Stop")).toBeUndefined();
	const before = renderedText(tree);

	act(() => stalePress());
	await settle();

	expect(hub.mutations()).toEqual([]);
	expect(renderedText(tree)).not.toContain("Stopped");
	// No error text of any kind: the screen reads exactly as it did.
	expect(renderedText(tree)).toBe(before);
});

it("holds Stop while a queued message is handed to the outbox", async () => {
	const { tree, hub } = await mount(thread("ref-stop-wait", "active"));
	await type(tree, "later");
	const queue = pressable(tree, "Queue message");
	if (!queue) throw new Error("no Queue message");
	act(() => {
		queue.props.onPress();
	});
	expect(pressable(tree, "Stop")?.props.accessibilityState).toMatchObject({
		disabled: true,
	});
	await settle();
	expect(hub.mutations()).toEqual(["turn/queue"]);
	expect(pressable(tree, "Stop")?.props.accessibilityState).toMatchObject({
		disabled: false,
	});
});

// Two finished turns, each your message and the agent's reply.
function twoTurns(ref: string): Thread {
	const served = thread(ref, "idle");
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

it("opens at the start of a reply that finished since you last read to the end", async () => {
	harness.kv.set(
		"evener.reader-positions",
		JSON.stringify({
			"hub-1\u0000ref-opening": {
				hubId: "hub-1",
				sessionRef: "ref-opening",
				itemKey: "u-turn_1",
				withinItemOffset: 0,
				touchedAt: 1,
				turnsSeen: "turn_1",
			},
		}),
	);
	flatListCalls.length = 0;
	await mount(twoTurns("ref-opening"));
	// The rows are ask/reply for turn_1, then turn_2: its reply is row 3.
	const indexes = flatListCalls.filter((call) => call.method === "scrollToIndex").map((call) => (call.args as { index: number }).index);
	expect(indexes).toContain(3);
	expect(indexes).not.toContain(0);
});

it("reads the session again on its own after a read fails", async () => {
	const { tree, hub } = await mount(twoTurns("ref-retry"), { failedReads: 1 });
	expect(hub.requests.filter((request) => request.method === "thread/read")).toHaveLength(2);
	expect(renderedText(tree)).toContain("ask turn_2");
	expect(renderedText(tree)).not.toContain("Trying again");
});

it("keeps trying quietly, and says so from the third failure in a row", async () => {
	vi.useFakeTimers();
	try {
		// Reads take 50ms, so the screen renders "opening" between failures as it
		// does over a real network.
		const { tree, hub } = await mount(twoTurns("ref-failing"), { failedReads: 10, readLatencyMs: 50, settled: false });
		// Each failure schedules the next retry from an effect, so time moves in
		// small steps with the renders between them flushed.
		const advance = async (ms: number) => {
			for (let round = 0; round < 10; round += 1)
				await act(async () => {
					await vi.advanceTimersByTimeAsync(0);
				});
			await act(async () => {
				await vi.advanceTimersByTimeAsync(ms);
			});
			for (let round = 0; round < 10; round += 1)
				await act(async () => {
					await vi.advanceTimersByTimeAsync(0);
				});
		};
		await advance(50);
		// Until the conversation first loads, quiet blocks stand in for it.
		expect(tree.root.findAll((node) => node.props.accessibilityLabel === "Loading conversation").length).toBeGreaterThan(0);
		const reads = () => hub.requests.filter((request) => request.method === "thread/read").length;
		// The first failure retries at once, the second after a second.
		expect(reads()).toBe(2);
		expect(renderedText(tree)).not.toContain("Trying again");
		await advance(50);
		expect(reads()).toBe(2);
		await advance(1_050);
		expect(reads()).toBe(3);
		expect(renderedText(tree)).toContain("Couldn't load this session. Trying again on its own.");
		for (const words of ["Pull down", "Reconnect", "Load older messages", "Loading conversation"])
			expect(renderedText(tree)).not.toContain(words);
		act(() => tree.unmount());
	} finally {
		vi.useRealTimers();
	}
});

it("has no Latest button, no pull to refresh, and no Load older button", async () => {
	const { tree } = await mount(twoTurns("ref-calm"));
	expect(tree.root.findAll((node) => node.props.accessibilityLabel === "Latest")).toEqual([]);
	// The transcript list itself (the stub component, whose props the screen set).
	const list = tree.root.findAll(
		(node) => typeof node.type === "function" && Array.isArray(node.props.data) && typeof node.props.renderItem === "function",
	)[0];
	expect(list).toBeDefined();
	expect(list.props.onRefresh).toBeUndefined();
	expect(list.props.refreshing).toBeUndefined();
	expect(renderedText(tree)).not.toContain("Load older messages");
});

function transcriptList(tree: ReactTestRenderer) {
	return tree.root.findAll(
		(node) => typeof node.type === "function" && Array.isArray(node.props.data) && typeof node.props.renderItem === "function",
	)[0];
}

function scrollTo(tree: ReactTestRenderer, y: number) {
	act(() =>
		transcriptList(tree).props.onScroll({
			nativeEvent: { contentOffset: { y }, contentSize: { height: 4_000 }, layoutMeasurement: { height: 600 } },
		}),
	);
}

it("shows nothing for a loaded conversation with no rows: the composer invites", async () => {
	const { tree } = await mount(thread("ref-empty", "idle"));
	expect(tree.root.findAll((node) => node.props.accessibilityLabel === "Loading conversation")).toEqual([]);
	for (const words of ["No messages", "Loading", "Pull down"]) expect(renderedText(tree)).not.toContain(words);
});

it("loads older history as you scroll near the top", async () => {
	const { tree, hub } = await mount(twoTurns("ref-older"), { olderCursor: "cursor-1" });
	scrollTo(tree, 2_000);
	await settle();
	expect(hub.requests.filter((request) => request.method === "thread/turns/list")).toEqual([]);
	scrollTo(tree, 100);
	await settle();
	expect(hub.requests.filter((request) => request.method === "thread/turns/list").map((request) => request.params.cursor)).toEqual([
		"cursor-1",
	]);
});

it("doesn't page older history while the hub is away", async () => {
	const { tree, hub } = await mount(twoTurns("ref-older-away"), { olderCursor: "cursor-1" });
	harness.connection = { ...harness.connection, state: "connecting" };
	const route = { key: "conversation-ref-older-away", name: "Conversation", params: { hubId: "hub-1", ref: "ref-older-away", title: "Session" } };
	act(() => tree.update(<ConversationScreen route={route as unknown as ConversationScreenProps["route"]} navigation={navigation} />));
	await settle();
	scrollTo(tree, 100);
	await settle();
	expect(hub.requests.filter((request) => request.method === "thread/turns/list")).toEqual([]);
});

it("retries a failed turn with Jesse's sentence, and leaves your draft alone", async () => {
	const served = thread("ref-retry-turn", "idle");
	(served as unknown as { turns: unknown[] }).turns = [
		{
			id: "turn_1",
			status: "failed",
			itemsView: "default",
			error: { message: "go test exited 1" },
			items: [{ id: "u-1", turnId: "turn_1", type: "userMessage", status: "completed", text: "run the tests" }],
		},
	];
	const { tree, hub } = await mount(served);
	await type(tree, "keep this");
	await press(tree, "Retry");
	expect(hub.mutations()).toEqual(["turn/start"]);
	const start = hub.requests.find((request) => request.method === "turn/start");
	expect(start?.params.input).toEqual([{ type: "text", text: "Something went wrong. Please try again." }]);
	expect(field(tree)?.props.value).toBe("keep this");
});

it("opens a session switched to in place at its own newer reply, never the last session's rows", async () => {
	const other = twoTurns("ref-switched-to");
	// B's rows have their own ids, so an anchor worked out from A's rows can't
	// land on them.
	(other as unknown as { turns: { items: { id: string }[] }[] }).turns.forEach((turn) =>
		turn.items.forEach((item) => {
			item.id = `b-${item.id}`;
		}),
	);
	otherThreads.set("ref-switched-to", other);
	harness.kv.set(
		"evener.reader-positions",
		JSON.stringify({
			"hub-1\u0000ref-switched-to": {
				hubId: "hub-1",
				sessionRef: "ref-switched-to",
				itemKey: "b-u-turn_1",
				withinItemOffset: 0,
				touchedAt: 1,
				turnsSeen: "turn_1",
			},
		}),
	);
	const { tree } = await mount(twoTurns("ref-switched-from"));
	flatListCalls.length = 0;
	const route = {
		key: "conversation-ref-switched-from",
		name: "Conversation",
		params: { hubId: "hub-1", ref: "ref-switched-to", title: "Session" },
	};
	navigationState.state = { index: 0, routes: [route as unknown as { key: string; name: string }] };
	act(() => tree.update(<ConversationScreen route={route as unknown as ConversationScreenProps["route"]} navigation={navigation} />));
	await settle();
	expect(renderedText(tree)).toContain("ask turn_2");
	const indexes = flatListCalls.filter((call) => call.method === "scrollToIndex").map((call) => (call.args as { index: number }).index);
	expect(indexes).toContain(3);
});

// A session whose last turn failed, as the hub reads it.
function failedTurn(ref: string, message: string, resumeRequired = false): Thread {
	const served = thread(ref, "idle");
	(served as unknown as { turns: unknown[] }).turns = [
		{
			id: "turn_1",
			status: "failed",
			itemsView: "default",
			error: { message },
			items: [{ id: "u-1", turnId: "turn_1", type: "userMessage", status: "completed", text: "run the tests" }],
		},
	];
	if (resumeRequired) (served as unknown as { evener: Record<string, unknown> }).evener.resumeRequired = true;
	return served;
}

it("resumes a paused session from its error", async () => {
	const { tree, hub } = await mount(failedTurn("ref-error-resume", "go test exited 1", true));
	await press(tree, "Resume");
	expect(hub.requests.filter((request) => request.method === "resumeThread").map((request) => request.params.ref)).toEqual([
		"ref-error-resume",
	]);
});

it("opens sign-in from an error that says a sign-in failed", async () => {
	vi.mocked(navigation.navigate).mockClear();
	const { tree } = await mount(failedTurn("ref-error-sign-in", "401 Unauthorized"));
	await press(tree, "Sign in");
	expect(navigation.navigate).toHaveBeenCalledWith("Providers", { hubId: "hub-1" });
});


describe("queued messages above the composer (spec 8.5)", () => {
	it("steers with a queued message the agent hasn't reached yet", async () => {
		const { tree, hub } = await mount(thread("ref-steer-now", "active", false, ["check the logs"]));
		expect(renderedText(tree)).toContain("check the logs");
		expect(renderedText(tree)).toContain("Queued · sends when this turn ends");
		await press(tree, "Steer now");
		const promote = hub.requests.filter((request) => request.method === "turn/promoteQueuedAsSteer");
		expect(promote).toHaveLength(1);
		expect(promote[0]?.params).toMatchObject({ index: 0, expectedEntryId: "queue_1" });
		expect(renderedText(tree)).not.toContain("Couldn't steer");
	});

	it("sends nothing when the message left the queue before the press (Review Focus 2)", async () => {
		const served = thread("ref-steer-stale", "active", false, ["check the logs"]);
		const { tree, hub } = await mount(served);
		const stalePress = pressable(tree, "Steer now")?.props.onPress as () => void;
		expect(stalePress).toBeDefined();
		act(() =>
			hub.notify({
				method: "thread/queueChanged",
				params: { threadId: served.id, ref: "ref-steer-stale", queue: queueState([], 1) },
			} as AnyNotification),
		);
		act(() => stalePress());
		await settle();
		expect(hub.requests.filter((request) => request.method.startsWith("turn/"))).toEqual([]);
	});

	it("clears a cancelled message on the hub's own queue frame, even when the read after it fails", async () => {
		const served = thread("ref-cancel-frame", "idle", false, ["drop this"]);
		const { tree, hub } = await mount(served);
		// The read after the cancel fails; the hub still reports its queue.
		const client = hub.client as { request: (method: string, params: Record<string, unknown>) => Promise<unknown> };
		const request = client.request;
		client.request = async (method, params) => {
			if (method === "thread/read") throw new Error("read failed");
			const answer = await request(method, params);
			if (method !== "turn/cancelQueued") return answer;
			hub.notify({
				method: "thread/queueChanged",
				params: { threadId: served.id, ref: "ref-cancel-frame", queue: queueState([], 1) },
			} as AnyNotification);
			// A cancel's receipt names no turn and says the entry is removed,
			// and its answer echoes what it took out of the queue.
			const { turnId: _turnId, ...receipt } = (answer as { receipt: Record<string, unknown> }).receipt;
			return { receipt: { ...receipt, projectionState: "removed" }, removedText: "drop this" };
		};
		await press(tree, "Cancel");
		expect(hub.requests.map((entry) => entry.method)).toContain("turn/cancelQueued");
		expect(renderedText(tree)).not.toContain("drop this");
		expect(renderedText(tree)).not.toContain("Couldn't take this message out of the queue.");
	});

	it("holds a queue a Stop parked, and Send now releases it (Review Focus 3)", async () => {
		const { tree, hub } = await mount(thread("ref-held", "idle", false, ["after the stop"]));
		expect(renderedText(tree)).toContain("Held · you stopped this turn");
		await press(tree, "Send now");
		const promote = hub.requests.filter((request) => request.method === "turn/promoteQueuedAsSteer");
		expect(promote.map((request) => request.params)).toMatchObject([{ index: 0, expectedEntryId: "queue_1" }]);
	});

	it("offers Steer all now only while the hub is there to take it", async () => {
		const { tree } = await mount(thread("ref-steer-all", "active", false, ["one", "two"]));
		const host = () => queueHosts.get(sheetKey("hub-1", "ref-steer-all"));
		expect(host()?.steerAll).toBeDefined();
		harness.connection = { ...harness.connection, state: "connecting" };
		const route = { key: "conversation-ref-steer-all", name: "Conversation", params: { hubId: "hub-1", ref: "ref-steer-all", title: "Session" } };
		act(() => tree.update(<ConversationScreen route={route as unknown as ConversationScreenProps["route"]} navigation={navigation} />));
		await settle();
		expect(host()?.ghosts).toHaveLength(2);
		expect(host()?.steerAll).toBeUndefined();
	});

	it("shows what the Queue sheet hands back on the session, once the sheet has closed", async () => {
		const { tree } = await mount(thread("ref-sheet-toast", "active", false, ["one"]));
		const host = queueHosts.get(sheetKey("hub-1", "ref-sheet-toast"));
		act(() => host?.showOnSession({ text: "Moved to your message, but it's still queued." }));
		expect(renderedText(tree)).toContain("Moved to your message, but it's still queued.");
	});

	it("shows three queued messages and opens the rest in the Queue sheet", async () => {
		vi.mocked(navigation.navigate).mockClear();
		const { tree, hub } = await mount(thread("ref-four", "active", false, ["one", "two", "three", "four"]));
		const text = renderedText(tree);
		for (const shown of ["one", "two", "three"]) expect(text).toContain(shown);
		expect(text).not.toContain("four");
		act(() => pressable(tree, "1 more queued")?.props.onPress());
		expect(navigation.navigate).toHaveBeenCalledWith("QueueSheet", { hubId: "hub-1", ref: "ref-four" });
		const host = queueHosts.get(sheetKey("hub-1", "ref-four"));
		expect(host?.ghosts.map((ghost) => ghost.text)).toEqual(["one", "two", "three", "four"]);
		const fourth = host?.ghosts[3];
		if (!host || !fourth) throw new Error("no fourth queued message in the host");
		await act(async () => {
			await host.act(fourth, "cancel");
		});
		const cancel = hub.requests.filter((request) => request.method === "turn/cancelQueued");
		expect(cancel.map((request) => request.params)).toMatchObject([{ index: 3, expectedEntryId: "queue_4" }]);
	});
});

it("offers no Retry that couldn't send: a question still waits on the failed turn", async () => {
	const served = thread("ref-retry-question", "idle", true);
	const turn = (served as unknown as { turns: { status: string; error?: unknown }[] }).turns[0];
	turn.status = "failed";
	turn.error = { message: "go test exited 1" };
	const { tree } = await mount(served);
	expect(renderedText(tree)).toContain("go test exited 1");
	expect(pressable(tree, "Retry")).toBeUndefined();
});

it("puts a quote held for this session into the draft when it comes back to the front, once", async () => {
	// Quoting focuses the field on the next frame.
	vi.stubGlobal("requestAnimationFrame", (frame: () => void) => {
		frame();
		return 0;
	});
	const { tree } = await mount(thread("ref-quote", "idle"));
	await type(tree, "keep this");
	const [route] = navigationState.state.routes;
	if (!route) throw new Error("no route");
	const rerender = async () => {
		act(() => tree.update(<ConversationScreen route={route as never} navigation={navigation} />));
		await settle();
	};
	// The Reader over this session holds a quote, and another session's.
	navigationState.state = { index: 1, routes: [route, { key: "reader", name: "Reader" }] };
	await rerender();
	holdQuote("hub-1", "ref-other", "not for this session");
	holdQuote("hub-1", "ref-quote", "The goal is green.\nThen ship it.");
	navigationState.state = { index: 0, routes: [route] };
	await rerender();
	const quoted = "keep this\n\n> The goal is green.\n> Then ship it.\n\n";
	expect(field(tree)?.props.value).toBe(quoted);
	navigationState.state = { index: 1, routes: [route, { key: "reader", name: "Reader" }] };
	await rerender();
	navigationState.state = { index: 0, routes: [route] };
	await rerender();
	expect(field(tree)?.props.value).toBe(quoted);
	expect(takeQuote("hub-1", "ref-other")).toBe("not for this session");
});
