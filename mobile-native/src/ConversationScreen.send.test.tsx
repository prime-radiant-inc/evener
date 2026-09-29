// The Session's one Send and the tray's Stop, on the real ConversationScreen:
// what a person presses, and which requests reach the hub through the durable
// runtime. Only native edges are mocked, as in
// ConversationScreen.recovery.test.tsx.
import type { ComponentProps, ReactNode } from "react";
import { createElement } from "react";
import { act, type ReactTestInstance, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { type AnyNotification, type Thread, WireError } from "@evener/appwire-client";
import { nativeDrafts } from "./nativeDrafts";
import { getNativeMutationRuntime, nativeMutationTargetKey } from "./nativeMutationRuntime";
import {
	flatListCalls,
	flatListScrollFailures,
	keyboard,
	alertRequests,
	dockBody,
	dropped as droppedConnection,
	type PanGestureMock,
	playedHaptics,
	pressable,
	render,
	renderedText,
	screenConnection,
	systemGlass,
	textOf,
} from "./renderNative.testkit";
import { queueHosts } from "./QueueSheet";
import { ConversationScreen } from "./screens";
import { nativeDisclosureStore, setDisclosureOpenAll } from "./nativeDisclosure";
import { rowDisclosureIds, sessionDisclosureScope } from "./session/disclosureKeys";
import { NotesSheet, notesHosts } from "./session/NotesSheet";
import { QuestionDock } from "./session/QuestionDock";
import { FindBar } from "./session/FindBar";
import { GlassHeaderPanel } from "./design/GlassHeaderPanel";
import { SessionHeader } from "./session/SessionHeader";
import { sheetKey } from "./sheet/sheetHosts";
import { holdQuote, takeQuote } from "./session/pendingQuote";
import { modelHosts } from "./session/ModelSheet";
import { SessionInfoSheet, sessionInfoHosts } from "./session/SessionInfoSheet";
import { commandHosts } from "./session/CommandsSheet";
import { compactDuration } from "./session/format";
import { SubagentPanel } from "./subagents/SubagentPanel";
import { AccessibilityInfo, ActionSheetIOS, Platform } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import type {
	NativeStackHeaderItemMenu,
	NativeStackHeaderItemMenuAction,
	NativeStackNavigationOptions,
} from "@react-navigation/native-stack";
import { paletteFor } from "./design/tokens";
import { answerFleetRead, type FleetShape, fleetSession } from "./session/fleetTestUtils";
import { FloatingStack } from "./session/FloatingStack";
import { Toast } from "./Toast";
import { forgetStopRequestsForHub, stopRequests } from "./subagents/nativeStopRequests";
import { forgetSubagentTrees } from "./subagents/subagentTree";
import { SubagentScreen } from "./subagents/SubagentScreen";
import { flattenSubagents } from "./subagents/subagentModel";

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
// Who alerted you most recently, and Next's word that it moved you on.
const alerts = vi.hoisted(() => ({ recent: [] as string[], nextUsed: vi.fn() }));

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
		useFocusEffect: (effect: () => void | (() => void)) => useEffect(effect, []),
		useNavigationState: <T,>(select: (state: typeof navigationState.state) => T) => select(navigationState.state),
		// A sheet route rendered beside the screen (NotesSheet) reads these.
		useNavigation: () => navigation,
		usePreventRemove: () => {},
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
vi.mock("./alerts/alertsContext", async (importOriginal) => ({
	...(await importOriginal<typeof import("./alerts/alertsContext")>()),
	useAlertedRecently: () => alerts.recent,
	useNextUsed: () => alerts.nextUsed,
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
	replace: vi.fn(),
	pop: vi.fn(),
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
function thread(ref: string, status: "idle" | "active" | "awaiting", question = false, queued: string[] = []): Thread {
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
// The fleet the hub answers the screen's navigation reads with: nobody else
// needs you unless a test says so.
const fleet: FleetShape = { live: [], needsYou: [] };
// Every screen a test mounts. Each is unmounted after its test: a screen
// left mounted keeps answering late reads and setting header options on the
// shared navigation mock, so a later test reading the last header options
// could act on it instead of its own screen.
const mountedScreens: ReactTestRenderer[] = [];
// A coordinator's subagent tree (evener/jobs/list) and its direct stop
// (evener/delegate/stop), for the subagent screen's tests.
const coordinatorHub: { tree: unknown; stop: (params: Record<string, unknown>) => unknown; readFails: string | null } =
	{
		tree: null,
		stop: () => ({ outcome: "stopping" }),
		readFails: null,
	};
// Whether thread/read answers with a history identity, as a v6 hub does:
// only then does the store take live pushes (overlay streams among them).
const readHistory = { live: false };
const READ_HISTORY_IDENTITY = {
	bootGeneration: "1",
	epoch: 1,
	snapshot: { incarnation: "inc-1", length: 0 },
} as const;
// Whether model/list refuses, as a hub mid-restart does.
const catalogHub = { fails: false };
afterEach(() => {
	catalogHub.fails = false;
	readHistory.live = false;
	for (const tree of mountedScreens.splice(0)) if (tree.toJSON() !== null) act(() => tree.unmount());
	keyboard.reset();
	systemGlass.reset();
	coordinatorHub.tree = null;
	coordinatorHub.stop = () => ({ outcome: "stopping" });
	coordinatorHub.readFails = null;
	otherThreads.clear();
	fleet.live = [];
	fleet.needsYou = [];
	fleet.sources = undefined;
	fleet.revision = undefined;
	vi.unstubAllGlobals();
});

function hubClient(
	served: Thread,
	failedReads = 0,
	readLatencyMs = 0,
	olderCursor?: string,
	olderTurns: unknown[] = [],
	olderPage: Promise<void> = Promise.resolve(),
	compacting: Promise<void> = Promise.resolve(),
) {
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
		forceStop: async (ref: string) => {
			requests.push({ method: "forceStop", params: { ref } });
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
				if (params.ref === coordinatorHub.readFails) throw new Error("read failed");
				// A second session this client can also read, by its ref.
				const thread = otherThreads.get(String(params.ref)) ?? served;
				return {
					thread,
					...(olderCursor ? { olderCursor } : {}),
					...(readHistory.live ? READ_HISTORY_IDENTITY : {}),
				};
			}
			// The page before the first read: older turns, and the start of history.
			if (method === "thread/compact/start") await compacting;
			if (method === "thread/turns/list") {
				await olderPage;
				return { data: olderTurns };
			}
			if (method === "model/list" && catalogHub.fails) throw new Error("hub restarting");
			if (method === "model/list")
				return {
					data: [{ provider: "anthropic", model: "claude-sonnet-5", displayName: "Claude Sonnet 5" }],
				};
			if (method === "notes/human/set")
				return {
					note: params.note,
					receipt: {
						clientMutationId: params.clientMutationId,
						disposition: "applied",
						threadId: served.id,
						projectionState: "pending",
						instanceId: "instance",
					},
				};
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
			if (method === "evener/jobs/list" && coordinatorHub.tree) return { data: coordinatorHub.tree };
			if (method === "evener/delegate/stop") return coordinatorHub.stop(params);
			if (method === "evener/session/seen/set")
				return { ok: true, changed: true, navigation: { generation_id: "generation-test", targets: [] } };
			return answerFleetRead(fleet, method, params) ?? {};
		},
	};
	return {
		client,
		mutations: () => requests.filter((request) => request.method.startsWith("turn/")).map((request) => request.method),
		requests,
		/** A frame the hub pushes to every live subscriber. */
		notify(notification: AnyNotification) {
			for (const listener of [...listeners]) listener(notification);
		},
	};
}

async function mount(
	served: Thread,
	{
		failedReads = 0,
		readLatencyMs = 0,
		settled = true,
		olderCursor = undefined as string | undefined,
		olderTurns = [] as unknown[],
		// Holds the older page until the promise settles, for what happens
		// while one is on its way.
		olderPage = Promise.resolve(),
		// Holds a compact until the promise settles, so a session control stays
		// pending.
		compacting = Promise.resolve(),
		openedBy = undefined as "next" | undefined,
		// The bottom bar lays out as it would on a device (0pt here, so it
		// leaves the transcript's geometry as it was); a test of what waits
		// for that layout says false.
		barLaysOut = true,
	} = {},
) {
	const hub = hubClient(served, failedReads, readLatencyMs, olderCursor, olderTurns, olderPage, compacting);
	harness.connection = {
		...screenConnection(hub.client, "ready"),
		profiles: [{ id: "hub-1", name: "Work hub", origin: "https://hub.test" }],
		error: null,
		disconnect: () => {},
	};
	const ref = String((served as unknown as { evener: { ref: string } }).evener.ref);
	const route = {
		key: `conversation-${ref}`,
		name: "Conversation",
		params: { hubId: "hub-1", ref, title: "Session", ...(openedBy ? { openedBy } : {}) },
	} as unknown as ConversationScreenProps["route"];
	navigationState.state = { index: 0, routes: [route] };
	const tree = render(<ConversationScreen route={route} navigation={navigation} />);
	mountedScreens.push(tree);
	if (barLaysOut) {
		const bar = tree.root.findAll(
			(node) => String(node.type) === "View" && node.props.testID === "session-bottom-bar",
		)[0];
		if (bar) act(() => bar.props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 0 } } }));
	}
	if (settled) await settle();
	return { tree, hub, route };
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
	playedHaptics.length = 0;
	await press(tree, "Send");
	expect(hub.mutations()).toEqual(["turn/start"]);
	// Spec 16.6: a light impact on send, once the hub took it.
	expect(playedHaptics).toEqual(["impact:light"]);
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
	expect(inputs).toEqual([[{ type: "text", text: "first" }], [{ type: "text", text: "second" }]]);
});

// The composer's Send, told apart from the dock's own "Send answer" by its
// paper airplane.
function composerSend(tree: ReactTestRenderer, label: string) {
	return tree.root
		.findAll((node) => String(node.type) === "Pressable" && node.props.accessibilityLabel === label)
		.find((node) => node.findAll((child) => child.props.name === "paperplane.fill").length > 0);
}

// A node's style flattened, as React Native flattens a style array.
const flatStyle = (node: ReactTestInstance): Record<string, unknown> =>
	Object.assign({}, ...[node.props.style].flat(Number.POSITIVE_INFINITY));

// The views between `node` and the screen's capped bottom area (the view
// with a maxHeight), each with its flattened style.
function viewsUpToBottomArea(node: ReactTestInstance) {
	const views: Record<string, unknown>[] = [];
	for (let at = node.parent; at; at = at.parent) {
		if (String(at.type) !== "View") continue;
		const style = flatStyle(at);
		if (style.maxHeight !== undefined) return views;
		views.push(style);
	}
	throw new Error("no capped bottom area above this node");
}

// Style wiring only (the geometry is checked on a simulator): the dock's own
// slot gives up height inside the capped bottom area, so a dock taller than
// the room left scrolls its body there, and nothing else in that area
// shrinks, so the tray and the composer keep their height.
function expectOnlyTheDockSlotShrinks(tree: ReactTestRenderer, testID: string) {
	const slot = viewsUpToBottomArea(tree.root.findByProps({ testID }));
	expect(slot).toEqual([expect.objectContaining({ flexShrink: 1 })]);
	const input = tree.root.findAll((node) => node.props.accessibilityLabel === "Message" && node.props.multiline)[0];
	if (input) for (const style of viewsUpToBottomArea(input)) expect(style.flexShrink ?? 0).toBe(0);
}

// A question taller than any phone's bottom area: a long question, a long
// why, and five options with long details.
const LONG_QUESTION = "Fourteen tool descriptions mention options their tools don't accept. ".repeat(4).trim();
const LONG_WHY = "Models try them and fail, which costs a retry each time and sometimes derails a whole turn. "
	.repeat(6)
	.trim();
const LONG_QUESTION_TURN = {
	...QUESTION_TURN,
	items: [
		{
			...QUESTION_TURN.items[0],
			argumentsJson: JSON.stringify({
				questions: [
					{
						header: "Choice",
						question: LONG_QUESTION,
						why: LONG_WHY,
						options: ["Drop them", "Keep them", "Only three", "Ask per tool", "Leave them"].map((label) => ({
							label,
							detail: `${label}, and everything that follows from it across the web and the TUI help text.`,
						})),
						multi_select: false,
					},
				],
			}),
		},
	],
};

describe("a question waiting for an answer (spec 8.4)", () => {
	// "Other answer…" focuses the composer on the next frame; these tests run
	// that frame at once.
	beforeEach(() => {
		vi.stubGlobal("requestAnimationFrame", (frame: (time: number) => void) => {
			frame(0);
			return 0;
		});
	});
	afterEach(() => vi.unstubAllGlobals());

	it("shows the dock in the composer's place", async () => {
		const { tree } = await mount(thread("ref-question", "awaiting", true));
		const text = renderedText(tree);
		expect(text).toContain("Question");
		expect(text).toContain("Keep or drop the implied options?");
		expect(text).not.toContain("question to answer");
		expect(field(tree)).toBeUndefined();
		for (const label of ["Send", "Queue message"]) expect(composerSend(tree, label)).toBeUndefined();
		expect(composerSend(tree, "Send answer")).toBeUndefined();
	});

	it("puts a long question's text and options in the dock's scroller, and its answer controls outside it", async () => {
		const served = thread("ref-question-long", "awaiting", true);
		(served as unknown as { turns: unknown[] }).turns = [LONG_QUESTION_TURN];
		const { tree } = await mount(served);
		const body = dockBody(tree, "question-dock");
		const scrolled = textOf(body.scroller);
		expect(scrolled).toContain(LONG_QUESTION);
		expect(scrolled).toContain(LONG_WHY);
		expect(body.holds("Leave them")).toBe(true);
		for (const label of ["Other answer…", "Send answer", "Fold"]) expect(body.holds(label)).toBe(false);
	});

	// The bar's cap is measured, not a percentage (#3248): arriving by a push,
	// the screen first lays out at the full window, then shorter once the
	// header's inset lands, and a dock's content arriving in that same pass left
	// a percentage cap worked out against the old height until the next layout.
	it("caps the bottom bar at four fifths of its room's latest height, in points", async () => {
		const served = thread("ref-question-cap", "awaiting", true);
		(served as unknown as { turns: unknown[] }).turns = [LONG_QUESTION_TURN];
		const { tree } = await mount(served);
		const bar = tree.root.find((node) => String(node.type) === "View" && node.props.testID === "session-bottom-bar");
		const room = tree.root.find(
			(node) => String(node.type) === "View" && node.props.testID === "session-bottom-bar-room",
		);
		// Before the room is measured, the cap is the same share as a percentage.
		expect(flatStyle(bar).maxHeight).toBe("80%");
		const layout = (height: number) => ({ nativeEvent: { layout: { x: 0, y: 0, width: 375, height } } });
		act(() => room.props.onLayout(layout(667)));
		expect(flatStyle(bar).maxHeight).toBeCloseTo(533.6);
		// The header's inset lands: the room is shorter, and so is the cap.
		act(() => room.props.onLayout(layout(593)));
		expect(flatStyle(bar).maxHeight).toBeCloseTo(474.4);
	});

	it("wires only the dock's slot to shrink, with the dock alone and with the composer back", async () => {
		const { tree } = await mount(thread("ref-question-room", "awaiting", true));
		expectOnlyTheDockSlotShrinks(tree, "question-dock");
		await press(tree, "Other answer…");
		expect(field(tree)).toBeDefined();
		expectOnlyTheDockSlotShrinks(tree, "question-dock");
	});

	it("shows only the question while the keyboard is up for your own answer, and the options once it goes down", async () => {
		const { tree } = await mount(thread("ref-question-typing", "awaiting", true));
		const options = () =>
			tree.root.findAll((node) => String(node.type) === "Pressable" && node.props.accessibilityRole === "radio");
		await press(tree, "Other answer…");
		expect(options()).not.toHaveLength(0);
		act(() => keyboard.show());
		expect(renderedText(tree)).toContain("Keep or drop the implied options?");
		expect(options()).toHaveLength(0);
		expect(pressable(tree, "Send answer")).toBeUndefined();
		expect(pressable(tree, "Other answer…")).toBeUndefined();
		expect(field(tree)).toBeDefined();
		act(() => keyboard.hide());
		expect(options()).not.toHaveLength(0);
		expect(pressable(tree, "Send answer")).toBeDefined();
	});

	// The dock follows the same typing rule as the rest (useComposerTyping):
	// a keyboard up for the find bar's field isn't the composer's.
	it("keeps the options while the keyboard is up for the find bar", async () => {
		const { tree } = await mount(thread("ref-question-find", "awaiting", true));
		const options = () =>
			tree.root.findAll((node) => String(node.type) === "Pressable" && node.props.accessibilityRole === "radio");
		await press(tree, "Other answer…");
		chooseMenu("Find in session");
		act(() => keyboard.show());
		expect(options()).not.toHaveLength(0);
		act(() => keyboard.hide());
	});

	it("brings the options back from Show options, which lowers the keyboard", async () => {
		const { tree } = await mount(thread("ref-question-show-options", "awaiting", true));
		await press(tree, "Other answer…");
		act(() => keyboard.show());
		act(() => pressable(tree, "Show options")?.props.onPress());
		expect(pressable(tree, "Send answer")).toBeDefined();
		expect(pressable(tree, "Show options")).toBeUndefined();
	});

	it("sends what you typed as the answer while the keyboard is up", async () => {
		const { tree, hub } = await mount(thread("ref-question-typed-send", "awaiting", true));
		await press(tree, "Other answer…");
		act(() => keyboard.show());
		await type(tree, "Drop them");
		const send = composerSend(tree, "Send your answer");
		expect(send?.props.accessibilityState).toMatchObject({ disabled: false });
		act(() => send?.props.onPress());
		await settle();
		expect(
			hub.requests.filter((request) => request.method === "turn/start").map((request) => request.params.input),
		).toEqual([[{ type: "text", text: '[answers]\n1. [Choice] \u2192 free text: "Drop them"' }]]);
		act(() => keyboard.hide());
	});

	it("lets a drag lower the keyboard", async () => {
		const { tree } = await mount(thread("ref-question-drag", "awaiting", true));
		const [list] = tree.root.findAll(
			(node) => node.props.keyExtractor !== undefined && node.props.renderItem !== undefined,
		);
		expect(list?.props.keyboardDismissMode).toBe("interactive");
	});

	it("lets a drag lower the keyboard on Android, which has no interactive dismissal", async () => {
		const platform = Platform as { OS: string };
		platform.OS = "android";
		try {
			const { tree } = await mount(thread("ref-question-drag-android", "awaiting", true));
			const [list] = tree.root.findAll(
				(node) => node.props.keyExtractor !== undefined && node.props.renderItem !== undefined,
			);
			expect(list?.props.keyboardDismissMode).toBe("on-drag");
		} finally {
			platform.OS = "ios";
		}
	});

	it("brings the composer back for Other answer…, and sends your text as the answer", async () => {
		const { tree, hub } = await mount(thread("ref-question-other", "awaiting", true));
		await press(tree, "Other answer…");
		expect(field(tree)?.props.placeholder).toBe("Answer or ask…");
		await type(tree, "Drop them");
		// The dock's own "Send answer" stays above; the composer's Send says
		// it sends what you typed, so VoiceOver tells the two apart.
		expect(composerSend(tree, "Send answer")).toBeUndefined();
		expect(pressable(tree, "Send answer")).toBeDefined();
		const send = composerSend(tree, "Send your answer");
		expect(send?.props.accessibilityState).toMatchObject({ disabled: false });
		act(() => send?.props.onPress());
		await settle();
		const starts = hub.requests.filter((request) => request.method === "turn/start");
		expect(starts.map((request) => request.params.input)).toEqual([
			[{ type: "text", text: '[answers]\n1. [Choice] \u2192 free text: "Drop them"' }],
		]);
		expect(renderedText(tree)).toContain("Answer sent");
		expect(field(tree)?.props.value ?? "").toBe("");
	});

	it("sends the option chosen in the dock, and says so", async () => {
		const { tree, hub } = await mount(thread("ref-question-option", "awaiting", true));
		await press(tree, "Drop them");
		playedHaptics.length = 0;
		await press(tree, "Send answer");
		// Spec 16.6: success on answer sent.
		expect(playedHaptics).toEqual(["notification:success"]);
		const starts = hub.requests.filter((request) => request.method === "turn/start");
		expect(starts.map((request) => request.params.input)).toEqual([
			[{ type: "text", text: '[answers]\n1. [Choice] \u2192 "Drop them"' }],
		]);
		expect(renderedText(tree)).toContain("Answer sent");
	});

	it("keeps a refused message's failed Edit out of the dock, in the screen's error area", async () => {
		const ref = "ref-question-ghost";
		const { tree } = await mount(thread(ref, "awaiting", true));
		// A message the hub refused, as a ghost that offers Edit.
		const runtime = getNativeMutationRuntime();
		const targetKey = nativeMutationTargetKey("hub-1", ref);
		const record = await runtime.storage.enqueueIntent({
			targetRef: targetKey,
			method: "turn/queue",
			payload: { ref, input: [{ type: "text", text: "recover this message" }] },
			attachments: [],
			optimisticDisplay: { method: "turn/queue" },
		});
		await runtime.storage.transferToRecovery(record.clientMutationId, "rejected", "daemon refused");
		await act(async () => {
			await runtime.discardRecovery("no-such-row", targetKey);
		});
		await settle();
		// The device can't write the draft, so Edit can't bring the message back.
		const drafts = sqlite.ports.get("evener-drafts.db") as { runSync: (...args: unknown[]) => unknown };
		const runSync = drafts.runSync;
		drafts.runSync = () => {
			throw new Error("disk full");
		};
		try {
			await press(tree, "Edit");
		} finally {
			drafts.runSync = runSync;
		}
		const failure = "This message could not be restored to the draft.";
		expect(renderedText(tree)).toContain(failure);
		const dock = tree.root.findAll((node) => node.type === QuestionDock);
		expect(dock).toHaveLength(1);
		expect(textOf(dock[0])).toContain("Keep or drop the implied options?");
		expect(textOf(dock[0])).not.toContain(failure);
	});

	// The device's saved answers can't be read until the returned function
	// is called.
	function questionReadsFail(): () => void {
		nativeDrafts();
		const drafts = sqlite.ports.get("evener-drafts.db") as {
			getFirstSync: (sql: string, ...args: unknown[]) => unknown;
		};
		const getFirstSync = drafts.getFirstSync;
		drafts.getFirstSync = (sql, ...args) => {
			if (sql.includes("question_")) throw new Error("database is locked");
			return getFirstSync(sql, ...args);
		};
		return () => {
			drafts.getFirstSync = getFirstSync;
		};
	}

	function rerender(tree: ReactTestRenderer, ref: string) {
		const route = {
			key: `conversation-${ref}`,
			name: "Conversation",
			params: { hubId: "hub-1", ref, title: "Session" },
		};
		act(() =>
			tree.update(
				<ConversationScreen route={route as unknown as ConversationScreenProps["route"]} navigation={navigation} />,
			),
		);
	}

	it("waits to answer until saved answers load, and reads them again when the hub comes back", async () => {
		const ref = "ref-question-unloaded";
		const readable = questionReadsFail();
		const { tree } = await mount(thread(ref, "awaiting", true));
		expect(pressable(tree, "Drop them")?.props.accessibilityState).toMatchObject({ disabled: true });
		await press(tree, "Fold");
		await type(tree, "Drop them");
		expect(composerSend(tree, "Send answer")?.props.accessibilityState).toMatchObject({ disabled: true });
		readable();
		harness.connection = { ...harness.connection, state: "connecting" };
		rerender(tree, ref);
		await settle();
		harness.connection = { ...harness.connection, state: "ready" };
		rerender(tree, ref);
		await settle();
		expect(composerSend(tree, "Send answer")?.props.accessibilityState).toMatchObject({ disabled: false });
	});

	it("reads saved answers again when the session comes back to front", async () => {
		const ref = "ref-question-front";
		const readable = questionReadsFail();
		const { tree } = await mount(thread(ref, "awaiting", true));
		await press(tree, "Fold");
		await type(tree, "Drop them");
		expect(composerSend(tree, "Send answer")?.props.accessibilityState).toMatchObject({ disabled: true });
		readable();
		const own = navigationState.state.routes[0];
		navigationState.state = { index: 1, routes: [own, { key: "conversation-other", name: "Conversation" }] };
		rerender(tree, ref);
		await settle();
		navigationState.state = { index: 0, routes: [own] };
		rerender(tree, ref);
		await settle();
		expect(composerSend(tree, "Send answer")?.props.accessibilityState).toMatchObject({ disabled: false });
	});

	it("folds to a bar, and the composer comes back beneath it", async () => {
		const { tree } = await mount(thread("ref-question-fold", "awaiting", true));
		await press(tree, "Fold");
		expect(renderedText(tree)).toContain("Answer the question");
		expect(field(tree)?.props.placeholder).toBe("Answer or ask…");
		// Folded, the composer's Send is the only one.
		expect(composerSend(tree, "Send answer")).toBeDefined();
		await press(tree, "Answer the question");
		expect(field(tree)).toBeUndefined();
	});
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

it("keeps the session open when /shutdown is typed and completed (ruling 19)", async () => {
	const served = thread("ref-typed-shutdown", "idle");
	// thread() shares the module-level CAPABILITIES object; clone it so this
	// test's shutdown capability never leaks into a later test's fixture.
	served.evener = {
		...served.evener,
		capabilities: { ...served.evener.capabilities, shutdown: true },
	};
	const { tree, hub } = await mount(served);
	await type(tree, "/shutdown");
	const send = pressable(tree, "Shut down");
	expect(send?.props.accessibilityState).toMatchObject({ disabled: false });
	const reads = () => hub.requests.filter(({ method }) => method === "thread/read").length;
	const readsBefore = reads();

	await press(tree, "Shut down");

	expect(hub.requests.filter(({ method }) => method === "thread/shutdown")).toEqual([
		{ method: "thread/shutdown", params: { ref: "ref-typed-shutdown" } },
	]);
	// It rereads the session it stays on instead of closing it and leaving.
	expect(reads()).toBeGreaterThan(readsBefore);
	expect(navigation.pop).not.toHaveBeenCalled();
	expect(navigation.goBack).not.toHaveBeenCalled();
	expect(field(tree)).toBeDefined();
	tree.unmount();
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

it("brings back messages a Stop held before they left the phone: Cancel drops one, Send now sends one (phase 6)", async () => {
	const ref = "ref-held";
	const { tree, hub } = await mount(thread(ref, "idle"));
	const runtime = getNativeMutationRuntime();
	const targetKey = nativeMutationTargetKey("hub-1", ref);
	const yours = (text: string) =>
		runtime.storage.enqueueIntent({
			targetRef: targetKey,
			method: "turn/queue",
			payload: { ref, input: [{ type: "text", text }] },
			attachments: [],
			optimisticDisplay: { text },
		});
	const dropped = await yours("drop this one");
	const kept = await yours("send this one");
	// A Stop commits before either left the phone, and holds them both.
	await runtime.storage.enqueueInterruptAndCancel({
		targetRef: targetKey,
		method: "turn/interrupt",
		payload: { ref },
		attachments: [],
		optimisticDisplay: { method: "turn/interrupt" },
	});
	await act(async () => {
		await runtime.discardRecovery("no-such-row", targetKey);
	});
	await settle();
	const text = renderedText(tree);
	expect(text).toContain("drop this one");
	expect(text).toContain("send this one");
	expect(text).toContain("Held · you stopped this turn");

	// Cancel drops the first from the phone's outbox; it never reaches the hub.
	await press(tree, "Cancel");
	await settle();
	expect(await runtime.storage.getOutbox(dropped.clientMutationId)).toBeUndefined();
	expect(renderedText(tree)).not.toContain("drop this one");

	// Send now releases the second behind the Stop that held it.
	await press(tree, "Send now");
	await settle();
	await vi.waitFor(() => expect(hub.mutations()).toEqual(["turn/interrupt", "turn/queue"]));
	const sent = hub.requests.find((request) => request.method === "turn/queue");
	expect(sent?.params).toMatchObject({ clientMutationId: kept.clientMutationId });
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
/** A settled turn: your ask, then the agent's reply. */
function askReplyTurn(id: string, ask = `ask ${id}`, reply = `reply ${id}`) {
	return {
		id,
		status: "completed",
		itemsView: "default",
		items: [
			{ id: `u-${id}`, turnId: id, type: "userMessage", status: "completed", text: ask },
			{ id: `a-${id}`, turnId: id, type: "agentMessage", status: "completed", text: reply },
		],
	};
}

function twoTurns(ref: string): Thread {
	const served = thread(ref, "idle");
	(served as unknown as { turns: unknown[] }).turns = [askReplyTurn("turn_1"), askReplyTurn("turn_2")];
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
	const indexes = flatListCalls
		.filter((call) => call.method === "scrollToIndex")
		.map((call) => (call.args as { index: number }).index);
	expect(indexes).toContain(3);
	expect(indexes).not.toContain(0);
});

it("restores a reading position once, however its row's measured y moves after", async () => {
	// A virtualized list re-estimating rows it unmounted (older pages provoke
	// it) moves the anchor row's measured y back and forth; the restore used
	// to chase every move, and the list ping-ponged.
	harness.kv.set(
		"evener.reader-positions",
		JSON.stringify({
			"hub-1\u0000ref-restore-once": {
				hubId: "hub-1",
				sessionRef: "ref-restore-once",
				itemKey: "a-turn_2",
				withinItemOffset: 0,
				touchedAt: 1,
				turnsSeen: "turn_2",
			},
		}),
	);
	const { tree } = await mount(twoTurns("ref-restore-once"));
	const list = transcriptList(tree);
	act(() => list.props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 600 } } }));
	act(() => list.props.onContentSizeChange(390, 20_000));
	const items = () => transcriptList(tree).findAll((node) => String(node.type) === "Item");
	// The rows are ask/reply for turn_1, then turn_2: its reply is row 3.
	const reply = () => {
		const cell = items()[3]?.findAll((node) => String(node.type) === "View" && node.props.onLayout)[0];
		if (!cell) throw new Error("no reply cell");
		return cell;
	};
	const layOut = (y: number) =>
		act(() => reply().props.onLayout({ nativeEvent: { layout: { x: 0, y, width: 390, height: 150 } } }));
	flatListCalls.length = 0;
	layOut(9_523);
	await settle();
	layOut(9_684);
	await settle();
	layOut(9_523);
	await settle();
	expect(flatListCalls.filter((call) => call.method === "scrollToOffset")).toEqual([
		{ method: "scrollToOffset", args: { offset: 9_523, animated: false } },
	]);
});

it("waits for the bottom bar to lay out before restoring a reading position", async () => {
	harness.kv.set(
		"evener.reader-positions",
		JSON.stringify({
			"hub-1\u0000ref-restore-waits": {
				hubId: "hub-1",
				sessionRef: "ref-restore-waits",
				itemKey: "a-turn_2",
				withinItemOffset: 0,
				touchedAt: 1,
				turnsSeen: "turn_2",
			},
		}),
	);
	const { tree } = await mount(twoTurns("ref-restore-waits"), { barLaysOut: false });
	const list = transcriptList(tree);
	act(() => list.props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 600 } } }));
	act(() => list.props.onContentSizeChange(390, 20_000));
	const cell = transcriptList(tree)
		.findAll((node) => String(node.type) === "Item")[3]
		?.findAll((node) => String(node.type) === "View" && node.props.onLayout)[0];
	if (!cell) throw new Error("no reply cell");
	flatListCalls.length = 0;
	// 19,500 is past what the list reaches without the bar's inset (19,400).
	act(() => cell.props.onLayout({ nativeEvent: { layout: { x: 0, y: 19_500, width: 390, height: 150 } } }));
	await settle();
	expect(flatListCalls.filter((call) => call.method === "scrollToOffset")).toEqual([]);
	// The bar lays out: its inset lets the list reach the row.
	const bar = tree.root.find((node) => String(node.type) === "View" && node.props.testID === "session-bottom-bar");
	act(() => bar.props.onLayout({ nativeEvent: { layout: { x: 0, y: 450, width: 390, height: 150 } } }));
	await settle();
	expect(flatListCalls.filter((call) => call.method === "scrollToOffset")).toEqual([
		{ method: "scrollToOffset", args: { offset: 19_500, animated: false } },
	]);
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
		expect(
			tree.root.findAll((node) => node.props.accessibilityLabel === "Loading conversation").length,
		).toBeGreaterThan(0);
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
		(node) =>
			typeof node.type === "function" && Array.isArray(node.props.data) && typeof node.props.renderItem === "function",
	)[0];
	expect(list).toBeDefined();
	expect(list.props.onRefresh).toBeUndefined();
	expect(list.props.refreshing).toBeUndefined();
	expect(renderedText(tree)).not.toContain("Load older messages");
});

function transcriptList(tree: ReactTestRenderer) {
	return tree.root.findAll(
		(node) =>
			typeof node.type === "function" && Array.isArray(node.props.data) && typeof node.props.renderItem === "function",
	)[0];
}

function scrollTo(tree: ReactTestRenderer, y: number) {
	act(() =>
		transcriptList(tree).props.onScroll({
			nativeEvent: { contentOffset: { y }, contentSize: { height: 4_000 }, layoutMeasurement: { height: 600 } },
		}),
	);
}

describe("following the live end (spec 8.2)", () => {
	// scrollTo's list is 4,000pt tall in a 600pt viewport: 3,400 is its end.
	const END = 3_400;
	const at = (y: number, content = 4_000) => ({
		nativeEvent: { contentOffset: { y }, contentSize: { height: content }, layoutMeasurement: { height: 600 } },
	});
	const list = (tree: ReactTestRenderer) => transcriptList(tree);
	// A drag the way a finger makes one: it begins, the list scrolls through
	// each offset, and it ends at the last.
	function drag(tree: ReactTestRenderer, ...offsets: number[]) {
		act(() => list(tree).props.onScrollBeginDrag(at(offsets[0] ?? 0)));
		for (const y of offsets) scrollTo(tree, y);
		act(() => list(tree).props.onScrollEndDrag(at(offsets.at(-1) ?? 0)));
	}
	// A reply streaming into the session: a new row below whatever you read.
	function stream(hub: { notify(notification: AnyNotification): void }, served: Thread, id: string) {
		const key = `stream:${id}:agentMessage`;
		act(() =>
			hub.notify({
				method: "overlay/upserted",
				params: {
					threadId: served.id,
					ref: served.evener.ref,
					item: {
						key,
						kind: "stream",
						turnId: "turn_2",
						roundId: id,
						streamId: id,
						item: { id: key, type: "agentMessage", roundId: id, text: `streamed ${id}`, status: "inProgress" },
					},
				},
			} as unknown as AnyNotification),
		);
	}
	// The list growing, as it does when a row lands. Whether the screen
	// answered by scrolling to the end says whether it follows.
	function contentGrows(tree: ReactTestRenderer, height: number) {
		flatListCalls.length = 0;
		act(() => list(tree).props.onContentSizeChange(390, height));
		return flatListCalls.some((call) => call.method === "scrollToEnd");
	}
	beforeEach(() => {
		readHistory.live = true;
	});
	// Two turns, the second still running, so replies can stream into it.
	function working(ref: string): Thread {
		const served = thread(ref, "active");
		(served as unknown as { turns: unknown[] }).turns = [
			askReplyTurn("turn_1"),
			{ ...askReplyTurn("turn_2"), status: "inProgress" },
		];
		(served as unknown as { evener: Record<string, unknown> }).evener.activeTurnId = "turn_2";
		return served;
	}
	const pill = (tree: ReactTestRenderer) =>
		tree.root.findAll((node) =>
			String(node.props.accessibilityLabel ?? "").endsWith("new below, scroll to the end"),
		)[0];

	it("keeps a streamed reply's row, and doesn't count it as new, once history records it", async () => {
		// Recorded items carry their transcript positions, as a hub's do, so
		// the recorded reply lands after them.
		const served = working("ref-follow-recorded");
		const at = (turnId: string, entry: number) => ({
			turnId,
			status: "completed",
			transcriptKey: `${turnId}:${entry}:0`,
			position: { entry, item: 0 },
		});
		(served as unknown as { turns: unknown[] }).turns = [
			{
				id: "turn_1",
				status: "completed",
				itemsView: "default",
				items: [
					{ id: "u-turn_1", type: "userMessage", text: "ask turn_1", ...at("turn_1", 0) },
					{ id: "a-turn_1", type: "agentMessage", text: "reply turn_1", ...at("turn_1", 1) },
				],
			},
			{
				id: "turn_2",
				status: "inProgress",
				itemsView: "default",
				items: [{ id: "u-turn_2", type: "userMessage", text: "ask turn_2", ...at("turn_2", 2) }],
			},
		];
		const { tree, hub } = await mount(served);
		stream(hub, served, "s1");
		const keyOf = (row: unknown) => list(tree).props.keyExtractor(row) as string;
		const streamed = keyOf((list(tree).props.data as unknown[]).at(-1));
		drag(tree, 100);
		act(() =>
			hub.notify({
				method: "history/updated",
				params: {
					threadId: served.id,
					ref: served.evener.ref,
					...READ_HISTORY_IDENTITY,
					items: [
						{
							id: "item_assistant_3_0",
							turnId: "turn_2",
							type: "agentMessage",
							roundId: "s1",
							transcriptKey: "turn_2:3:0",
							position: { entry: 3, item: 0 },
							status: "completed",
							text: "streamed s1",
						},
					],
				},
			} as unknown as AnyNotification),
		);
		const rows = list(tree).props.data as { id: string }[];
		expect(rows.map((row) => row.id)).toContain("item_assistant_3_0");
		expect(rows.map((row) => row.id)).not.toContain("stream:s1:agentMessage");
		expect(keyOf(rows.at(-1))).toBe(streamed);
		expect(pill(tree)).toBeUndefined();
	});

	// Tapping "↓ new" follows again, but the cap trims the top only once the
	// list reaches the end: trimming while it still travels would move what it
	// passes.
	it("trims past the cap only once the pill's scroll reaches the end", async () => {
		const served = working("ref-follow-trim");
		const turns = (served as unknown as { turns: { items: unknown[] }[] }).turns;
		turns[0].items = Array.from({ length: 510 }, (_, i) => ({
			id: `u-${i}`,
			turnId: "turn_1",
			type: "userMessage",
			status: "completed",
			text: `message ${i}`,
			transcriptKey: `turn_1:${i}:0`,
			position: { entry: i, item: 0 },
		}));
		const evener = (served as unknown as { evener: { capabilities: Record<string, unknown> } }).evener;
		evener.capabilities = { ...evener.capabilities, pageBefore: true };
		const { tree, hub } = await mount(served);
		const rows = () => (list(tree).props.data as unknown[]).length;
		drag(tree, 100);
		// A reply lands below while you read above: nothing is trimmed, so the
		// session's rows past the cap are all there.
		stream(hub, served, "s-trim");
		const grown = rows();
		const tap = pill(tree);
		if (!tap) throw new Error("no new-below pill");
		act(() => tap.props.onPress());
		// The pill scrolls toward the end; nothing is trimmed on the way.
		expect(rows()).toBe(grown);
		scrollTo(tree, END);
		expect(rows()).toBeLessThan(grown);
	});

	it("stops following when you drag up, and says what landed below", async () => {
		const served = working("ref-follow-away");
		const { tree, hub } = await mount(served);
		drag(tree, 100);
		stream(hub, served, "s1");
		expect(pill(tree)?.props.accessibilityLabel).toBe("1 new below, scroll to the end");
		expect(contentGrows(tree, 4_200)).toBe(false);
	});

	it("follows again once a drag ends at the end, and not while the finger is still down", async () => {
		const served = working("ref-follow-again");
		const { tree, hub } = await mount(served);
		drag(tree, 100);
		act(() => list(tree).props.onScrollBeginDrag(at(100)));
		scrollTo(tree, END);
		stream(hub, served, "s2");
		// A row landing mid-drag doesn't pull the list from under the finger.
		expect(contentGrows(tree, 4_200)).toBe(false);
		act(() => list(tree).props.onScrollEndDrag(at(END)));
		expect(pill(tree)).toBeUndefined();
		stream(hub, served, "s2b");
		expect(contentGrows(tree, 4_400)).toBe(true);
	});

	it("doesn't follow after a drag that reached the end and came back up", async () => {
		const served = working("ref-follow-back-up");
		const { tree } = await mount(served);
		drag(tree, 100, END, 1_000);
		expect(contentGrows(tree, 4_200)).toBe(false);
	});

	it("follows again when a flick's momentum carries you to the end", async () => {
		const served = working("ref-follow-flick");
		const { tree, hub } = await mount(served);
		drag(tree, 100);
		drag(tree, 1_000);
		act(() => list(tree).props.onMomentumScrollBegin());
		scrollTo(tree, END);
		act(() => list(tree).props.onMomentumScrollEnd(at(END)));
		stream(hub, served, "s3");
		expect(contentGrows(tree, 4_200)).toBe(true);
	});

	it("doesn't scroll to the end while a flick released at the end coasts away", async () => {
		const served = working("ref-follow-coast");
		const { tree, hub } = await mount(served);
		drag(tree, 100, END);
		act(() => list(tree).props.onMomentumScrollBegin());
		scrollTo(tree, 2_000);
		stream(hub, served, "m1");
		expect(contentGrows(tree, 4_200)).toBe(false);
		act(() => list(tree).props.onMomentumScrollEnd(at(2_000)));
		expect(contentGrows(tree, 4_400)).toBe(false);
	});

	it("follows a transcript shorter than the screen once you let go", async () => {
		const served = working("ref-follow-short");
		const { tree } = await mount(served);
		act(() => list(tree).props.onScrollBeginDrag(at(0, 300)));
		act(() => list(tree).props.onScrollEndDrag(at(0, 300)));
		expect(contentGrows(tree, 320)).toBe(true);
	});

	it("stays unfollowed when the app itself scrolls to the end", async () => {
		const served = working("ref-follow-app-scroll");
		const { tree } = await mount(served);
		drag(tree, 100);
		// No finger: a restore or the list settling lands at the end.
		scrollTo(tree, END);
		expect(contentGrows(tree, 4_200)).toBe(false);
	});

	it("counts what landed since you last left the end", async () => {
		const served = working("ref-follow-counts");
		const { tree, hub } = await mount(served);
		drag(tree, 100);
		stream(hub, served, "c1");
		expect(pill(tree)?.props.accessibilityLabel).toBe("1 new below, scroll to the end");
		drag(tree, END);
		expect(pill(tree)).toBeUndefined();
		drag(tree, 100);
		stream(hub, served, "c2");
		stream(hub, served, "c3");
		expect(pill(tree)?.props.accessibilityLabel).toBe("2 new below, scroll to the end");
	});

	it("follows again from the pill, which then goes", async () => {
		const served = working("ref-follow-pill");
		const { tree, hub } = await mount(served);
		drag(tree, 100);
		stream(hub, served, "p1");
		flatListCalls.length = 0;
		act(() => pill(tree)?.props.onPress());
		expect(flatListCalls.some((call) => call.method === "scrollToEnd")).toBe(true);
		expect(pill(tree)).toBeUndefined();
		stream(hub, served, "p2");
		expect(contentGrows(tree, 4_200)).toBe(true);
	});

	it("keeps the end in view when the viewport changes while following, as the keyboard does", async () => {
		const served = working("ref-follow-viewport");
		const { tree } = await mount(served);
		flatListCalls.length = 0;
		act(() => list(tree).props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 300 } } }));
		expect(flatListCalls.some((call) => call.method === "scrollToEnd")).toBe(true);
		drag(tree, 100);
		flatListCalls.length = 0;
		act(() => list(tree).props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 600 } } }));
		expect(flatListCalls.some((call) => call.method === "scrollToEnd")).toBe(false);
	});
});

it("shows nothing for a loaded conversation with no rows: the composer invites", async () => {
	const { tree } = await mount(thread("ref-empty", "idle"));
	expect(tree.root.findAll((node) => node.props.accessibilityLabel === "Loading conversation")).toEqual([]);
	for (const words of ["No messages", "Loading", "Pull down"]) expect(renderedText(tree)).not.toContain(words);
});

// Rows' open state lives in one app-wide store, scoped by session. Leaving a
// session drops its scope, so the store stays bounded.
it("forgets a session's open rows when you leave it", async () => {
	const { tree } = await mount(twoTurns("ref-disclosure-scope"));
	const run = { kind: "run" as const, id: "run:x", turnId: "turn_1", steps: [] };
	act(() => setDisclosureOpenAll(rowDisclosureIds("hub-1", "ref-disclosure-scope", run), true));
	const inScope = () =>
		[...nativeDisclosureStore.getState().open.keys()].filter((id) =>
			id.startsWith(`${sessionDisclosureScope("hub-1", "ref-disclosure-scope")}\0`),
		);
	expect(inScope()).toHaveLength(1);
	act(() => tree.unmount());
	expect(inScope()).toEqual([]);
});

// Where the device has Liquid Glass (iOS 26 and later), the nav bar is the
// system's glass over the transcript (spec 16.3), and the header's glass runs
// on under the chips and note: the transcript runs under both, and its top
// starts below them. Elsewhere, and while Reduce Transparency is on, the bar
// is opaque and the screen starts below it.
describe("the nav bar's glass (spec 16.3)", () => {
	// The bar's options: transparent, and clear, since react-native-screens
	// draws a transparent bar's background only when its color is itself
	// clear; and no system edge effect where the header's own glass is drawn.
	const lastBar = () => {
		const options = (vi.mocked(navigation.setOptions).mock.calls as [NativeStackNavigationOptions][])
			.map(([options]) => options)
			.findLast((options) => "headerTransparent" in options);
		if (options?.headerTransparent === undefined) return undefined;
		const background = (options.headerStyle as { backgroundColor?: string } | undefined)?.backgroundColor;
		expect(background === "transparent").toBe(options.headerTransparent);
		return { transparent: options.headerTransparent, topEdge: options.scrollEdgeEffects?.top };
	};
	const header = (tree: ReactTestRenderer) => tree.root.findByType(SessionHeader);
	const panel = (tree: ReactTestRenderer) => header(tree).findByType(GlassHeaderPanel);
	const layout = (tree: ReactTestRenderer) => ({
		headerTop: Object.assign({}, ...[panel(tree).props.style].flat(Number.POSITIVE_INFINITY)).top,
		glassTop: header(tree).props.glassTop,
		listTop: transcriptList(tree).props.contentContainerStyle.paddingTop,
		keyboardOffset: tree.root.findAll((node) => String(node.type) === "KeyboardControllerAvoidingView")[0]?.props
			.keyboardVerticalOffset,
	});
	const measureHeader = (tree: ReactTestRenderer, height: number) =>
		act(() => panel(tree).props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height } } }));

	it("runs the transcript under one glass spanning the bar and the chips, its top below them", async () => {
		systemGlass.available = true;
		const { tree } = await mount(twoTurns("ref-glass"));
		await act(async () => {});
		expect(lastBar()).toEqual({ transparent: true, topEdge: "hidden" });
		// Before the header has measured, the list keeps the bar's room.
		expect(layout(tree)).toEqual({ headerTop: 0, glassTop: 64, listTop: 16 + 64, keyboardOffset: 0 });
		// The header's height covers the bar's room and the rows under it.
		measureHeader(tree, 64 + 48);
		expect(layout(tree).listTop).toBe(16 + 64 + 48);
		// The find bar, in the chips' place, draws clear on the glass too.
		chooseMenu("Find in session");
		expect(tree.root.findByType(FindBar).props.onGlass).toBe(true);
	});

	// Turning the bar glass or opaque moves the list's frame by the bar's
	// height and its top padding by the same, so the rows stay where they are
	// with no scroll of the list's own; only the rows block growing or
	// shrinking asks for one.
	it("keeps the rows where they are when Reduce Transparency flips while scrolled", async () => {
		systemGlass.available = true;
		const { tree } = await mount(twoTurns("ref-glass-flip"));
		await act(async () => {});
		measureHeader(tree, 64 + 48);
		scrollTo(tree, 500);
		flatListCalls.length = 0;
		act(() => systemGlass.setReduceTransparency(true));
		measureHeader(tree, 48);
		act(() => systemGlass.setReduceTransparency(false));
		measureHeader(tree, 64 + 48);
		expect(flatListCalls.filter((call) => call.method === "scrollToOffset")).toEqual([]);
		// The rows block itself growing (the connection line arriving) still does.
		measureHeader(tree, 64 + 48 + 24);
		expect(flatListCalls.filter((call) => call.method === "scrollToOffset")).toEqual([
			{ method: "scrollToOffset", args: { offset: 524, animated: false } },
		]);
	});

	it("keeps an opaque bar the screen starts below where there is no glass", async () => {
		const { tree } = await mount(twoTurns("ref-no-glass"));
		await act(async () => {});
		expect(lastBar()?.transparent).toBe(false);
		expect(layout(tree)).toEqual({ headerTop: 0, glassTop: undefined, listTop: 16, keyboardOffset: 64 });
	});

	it("keeps an opaque bar while Reduce Transparency is on, following the setting", async () => {
		systemGlass.available = true;
		systemGlass.setReduceTransparency(true);
		const { tree } = await mount(twoTurns("ref-reduce-transparency"));
		await act(async () => {});
		expect(lastBar()?.transparent).toBe(false);
		expect(layout(tree)).toEqual({ headerTop: 0, glassTop: undefined, listTop: 16, keyboardOffset: 64 });
		act(() => systemGlass.setReduceTransparency(false));
		expect(lastBar()).toEqual({ transparent: true, topEdge: "hidden" });
		expect(layout(tree)).toEqual({ headerTop: 0, glassTop: 64, listTop: 16 + 64, keyboardOffset: 0 });
	});
});

it("moves the Session with the keyboard through the keyboard controller", async () => {
	const { tree } = await mount(twoTurns("ref-keyboard-controller"));
	const avoiding = tree.root.findAll((node) => String(node.type) === "KeyboardControllerAvoidingView");
	expect(avoiding).toHaveLength(1);
	expect(avoiding[0]?.props).toMatchObject({ behavior: "padding", keyboardVerticalOffset: 64 });
});

// A short transcript rests just above the composer (spec 8.5), not at the top
// with the page's empty middle between it and the bar.
it("rests a short transcript's end just above the composer", async () => {
	const { tree } = await mount(twoTurns("ref-short-rests"), { barLaysOut: false });
	const opacity = () => transcriptList(tree).props.style?.opacity;
	// It shows once the viewport and the bar have both laid out.
	expect(opacity()).toBe(0);
	act(() => transcriptList(tree).props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 600 } } }));
	await settle();
	expect(opacity()).toBe(0);
	const bar = tree.root.find((node) => String(node.type) === "View" && node.props.testID === "session-bottom-bar");
	act(() => bar.props.onLayout({ nativeEvent: { layout: { x: 0, y: 450, width: 390, height: 150 } } }));
	await settle();
	expect(opacity()).toBe(1);
	expect(transcriptList(tree).props.contentContainerStyle).toMatchObject({
		minHeight: 450,
		justifyContent: "flex-end",
	});
});

it("loads older history as you drag near the top", async () => {
	const { tree, hub } = await mount(twoTurns("ref-older"), { olderCursor: "cursor-1" });
	const drag = (y: number) => {
		const at = {
			nativeEvent: { contentOffset: { y }, contentSize: { height: 4_000 }, layoutMeasurement: { height: 600 } },
		};
		act(() => transcriptList(tree).props.onScrollBeginDrag(at));
		scrollTo(tree, y);
		act(() => transcriptList(tree).props.onScrollEndDrag(at));
	};
	drag(2_000);
	await settle();
	expect(hub.requests.filter((request) => request.method === "thread/turns/list")).toEqual([]);
	drag(100);
	await settle();
	expect(
		hub.requests.filter((request) => request.method === "thread/turns/list").map((request) => request.params.cursor),
	).toEqual(["cursor-1"]);
});

// A long session watched from its start: its first read held it all, so
// there is no cursor, and the 500-row cap trimmed its top while you followed
// the live end. Scrolling up asks for the trimmed rows by the oldest one kept.
it("pages the rows the cap trimmed back as you drag near the top", async () => {
	const served = thread("ref-trimmed", "idle");
	const items = Array.from({ length: 600 }, (_, i) => ({
		id: `u-${i}`,
		turnId: "turn_1",
		type: "userMessage",
		status: "completed",
		text: `message ${i}`,
		transcriptKey: `turn_1:${i}:0`,
		position: { entry: i, item: 0 },
	}));
	(served as unknown as { turns: unknown[] }).turns = [
		{ id: "turn_1", status: "completed", itemsView: "default", items },
	];
	// The hub's read says it pages this thread from a before position.
	const evener = (served as unknown as { evener: { capabilities: Record<string, unknown> } }).evener;
	evener.capabilities = { ...evener.capabilities, pageBefore: true };
	const { tree, hub } = await mount(served);
	const at = {
		nativeEvent: { contentOffset: { y: 100 }, contentSize: { height: 40_000 }, layoutMeasurement: { height: 600 } },
	};
	act(() => transcriptList(tree).props.onScrollBeginDrag(at));
	scrollTo(tree, 100);
	act(() => transcriptList(tree).props.onScrollEndDrag(at));
	await settle();
	const pages = hub.requests.filter((request) => request.method === "thread/turns/list").map((r) => r.params);
	expect(pages).toHaveLength(1);
	expect(pages[0]).toMatchObject({ ref: "ref-trimmed", before: { entry: 100, item: 0 } });
	expect(pages[0]).not.toHaveProperty("cursor");
});

it("doesn't page older history at the live end of a short first page, or until you scroll", async () => {
	// Opening at the live end of a page shorter than the screen puts the list
	// near its top; paging there chained every older page in on open, each
	// prepend landing the list near the top again.
	const { tree, hub } = await mount(twoTurns("ref-older-live"), { olderCursor: "cursor-1" });
	scrollTo(tree, 100);
	await settle();
	expect(hub.requests.filter((request) => request.method === "thread/turns/list")).toEqual([]);
});

it("pages older history on a short first page you drag, whose end is its top", async () => {
	const { tree, hub } = await mount(twoTurns("ref-older-short"), { olderCursor: "cursor-1" });
	const short = {
		nativeEvent: { contentOffset: { y: 0 }, contentSize: { height: 300 }, layoutMeasurement: { height: 600 } },
	};
	act(() => transcriptList(tree).props.onScrollBeginDrag(short));
	act(() => transcriptList(tree).props.onScrollEndDrag(short));
	await settle();
	expect(hub.requests.filter((request) => request.method === "thread/turns/list")).toHaveLength(1);
});

it("pages older history on a flick near the top that reports no scroll mid-drag", async () => {
	const { tree, hub } = await mount(twoTurns("ref-older-flick"), { olderCursor: "cursor-1" });
	const at = (y: number) => ({
		nativeEvent: { contentOffset: { y }, contentSize: { height: 4_000 }, layoutMeasurement: { height: 600 } },
	});
	act(() => transcriptList(tree).props.onScrollBeginDrag(at(900)));
	act(() => transcriptList(tree).props.onScrollEndDrag(at(700)));
	await settle();
	expect(hub.requests.filter((request) => request.method === "thread/turns/list")).toHaveLength(1);
});

it("pages older history when the list moves near the top with no drag, as a VoiceOver scroll moves it", async () => {
	const { tree, hub } = await mount(twoTurns("ref-older-assistive"), { olderCursor: "cursor-1" });
	// Away from the live end with no finger: the app scrolls only to follow
	// the end or to restore (which suppresses capture), so this is the reader.
	act(() => transcriptList(tree).props.onScrollBeginDrag());
	act(() =>
		transcriptList(tree).props.onScrollEndDrag({
			nativeEvent: { contentOffset: { y: 2_000 }, contentSize: { height: 4_000 }, layoutMeasurement: { height: 600 } },
		}),
	);
	await settle();
	expect(hub.requests.filter((request) => request.method === "thread/turns/list")).toEqual([]);
	scrollTo(tree, 1_400);
	scrollTo(tree, 100);
	await settle();
	expect(hub.requests.filter((request) => request.method === "thread/turns/list")).toHaveLength(1);
});

it("doesn't page older history for a reading position restored near the top before you scroll", async () => {
	harness.kv.set(
		"evener.reader-positions",
		JSON.stringify({
			"hub-1\u0000ref-older-restored": {
				hubId: "hub-1",
				sessionRef: "ref-older-restored",
				itemKey: "u-turn_1",
				withinItemOffset: 0,
				touchedAt: 1,
				turnsSeen: "turn_2",
			},
		}),
	);
	const { tree, hub } = await mount(twoTurns("ref-older-restored"), { olderCursor: "cursor-1" });
	scrollTo(tree, 40);
	await settle();
	expect(hub.requests.filter((request) => request.method === "thread/turns/list")).toEqual([]);
});

it("keeps the transcript's first row when an older page lands above it", async () => {
	// Timed turns, the older one ending five minutes before the next starts,
	// so a whole history marks only the first.
	const minute = 60_000;
	const start = Date.parse("2026-09-28T12:00:00Z");
	const timed = (id: string, from: number) => ({ ...askReplyTurn(id), startedAt: from, completedAt: from + minute });
	const served = thread("ref-older-first-key", "idle");
	(served as unknown as { turns: unknown[] }).turns = [
		timed("turn_1", start + 6 * minute),
		timed("turn_2", start + 8 * minute),
	];
	const { tree } = await mount(served, { olderCursor: "cursor-1", olderTurns: [timed("turn_0", start)] });
	const ids = () => (transcriptList(tree).props.data as { id: string }[]).map((row) => row.id);
	const first = ids()[0];
	// No time marker leads while older history is still to load.
	expect(first).toBe("u-turn_1");
	const at = {
		nativeEvent: { contentOffset: { y: 100 }, contentSize: { height: 4_000 }, layoutMeasurement: { height: 600 } },
	};
	act(() => transcriptList(tree).props.onScrollBeginDrag(at));
	act(() => transcriptList(tree).props.onScrollEndDrag(at));
	await settle();
	expect(ids()).toContain("u-turn_0");
	expect(ids()).toContain(first);
});

it("doesn't page older history while the hub is away", async () => {
	const { tree, hub } = await mount(twoTurns("ref-older-away"), { olderCursor: "cursor-1" });
	harness.connection = { ...harness.connection, state: "connecting" };
	const route = {
		key: "conversation-ref-older-away",
		name: "Conversation",
		params: { hubId: "hub-1", ref: "ref-older-away", title: "Session" },
	};
	act(() =>
		tree.update(
			<ConversationScreen route={route as unknown as ConversationScreenProps["route"]} navigation={navigation} />,
		),
	);
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
	playedHaptics.length = 0;
	await press(tree, "Retry");
	expect(hub.mutations()).toEqual(["turn/start"]);
	// Spec 16.6: a light impact on send, a retry's included.
	expect(playedHaptics).toEqual(["impact:light"]);
	const start = hub.requests.find((request) => request.method === "turn/start");
	expect(start?.params.input).toEqual([{ type: "text", text: "Something went wrong. Please try again." }]);
	expect(field(tree)?.props.value).toBe("keep this");
});

// Retry shows only while a press would send, which a session control going
// pending (a compact) changes without changing the rows: the row still
// follows it.
it("hides Retry while a session control is pending, and brings it back once it settles", async () => {
	const served = thread("ref-retry-pending", "idle");
	served.evener = { ...served.evener, capabilities: { ...served.evener.capabilities, compact: true } };
	(served as unknown as { turns: unknown[] }).turns = [
		{
			id: "turn_1",
			status: "failed",
			itemsView: "default",
			error: { message: "go test exited 1" },
			items: [{ id: "u-1", turnId: "turn_1", type: "userMessage", status: "completed", text: "run the tests" }],
		},
	];
	let settleCompact = () => {};
	const compacting = new Promise<void>((resolve) => {
		settleCompact = resolve;
	});
	const { tree } = await mount(served, { compacting });
	expect(pressable(tree, "Retry")).toBeDefined();
	await type(tree, "/compact");
	await press(tree, "Compact transcript");
	expect(pressable(tree, "Retry")).toBeUndefined();
	await act(async () => settleCompact());
	await settle();
	expect(pressable(tree, "Retry")).toBeDefined();
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
	act(() =>
		tree.update(
			<ConversationScreen route={route as unknown as ConversationScreenProps["route"]} navigation={navigation} />,
		),
	);
	await settle();
	expect(renderedText(tree)).toContain("ask turn_2");
	const indexes = flatListCalls
		.filter((call) => call.method === "scrollToIndex")
		.map((call) => (call.args as { index: number }).index);
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
	expect(
		hub.requests.filter((request) => request.method === "resumeThread").map((request) => request.params.ref),
	).toEqual(["ref-error-resume"]);
});

it("names the model on the composer's chip, which opens the model sheet with the session's controls", async () => {
	vi.mocked(navigation.navigate).mockClear();
	const served = thread("ref-model", "idle");
	(served as unknown as { modelProvider: string }).modelProvider = "anthropic/claude-sonnet-5";
	(served as unknown as { evener: Record<string, unknown> }).evener.capabilities = {
		...CAPABILITIES,
		changeModel: true,
	};
	const { tree, hub } = await mount(served);
	// The screen loads the catalog once, so the chip can name the model.
	expect(hub.requests.filter((request) => request.method === "model/list")).toHaveLength(1);
	const chip = pressable(tree, "Model: Claude Sonnet 5. Change model or effort");
	if (!chip) throw new Error("no model chip");
	act(() => chip.props.onPress());
	expect(navigation.navigate).toHaveBeenCalledWith("ModelSheet", {
		hubId: "hub-1",
		ref: "ref-model",
		setting: "model",
	});
	const host = modelHosts.get(sheetKey("hub-1", "ref-model"));
	expect(host?.session.modelProvider).toBe("anthropic/claude-sonnet-5");
	expect(host?.controls?.getSnapshot().catalog?.data).toHaveLength(1);
});

it("names the model as the session's Board row does while the catalog can't be read (S17)", async () => {
	catalogHub.fails = true;
	fleet.live = [fleetSession("ref-model", { title: "Model", state: "idle", model_name: "Claude Sonnet 5" })];
	const served = thread("ref-model", "idle");
	(served as unknown as { modelProvider: string }).modelProvider = "anthropic/claude-sonnet-5";
	(served as unknown as { evener: Record<string, unknown> }).evener.capabilities = {
		...CAPABILITIES,
		changeModel: true,
	};
	const { tree } = await mount(served);
	await act(async () => {});
	expect(pressable(tree, "Model: Claude Sonnet 5. Change model or effort")).toBeDefined();
	expect(renderedText(tree)).not.toContain("claude-sonnet-5");
});

it("keeps the Session sheet and a half-typed name through a connection blip, and saves once the hub is back", async () => {
	vi.mocked(navigation.goBack).mockClear();
	const served = thread("ref-blip", "idle");
	(served as unknown as { evener: Record<string, unknown> }).evener.capabilities = { ...CAPABILITIES, rename: true };
	const { tree, hub } = await mount(served);
	const params = { hubId: "hub-1", ref: "ref-blip" };
	const sheet = render(
		<SessionInfoSheet
			route={
				{ key: "session-info", name: "SessionInfoSheet", params } as unknown as ComponentProps<
					typeof SessionInfoSheet
				>["route"]
			}
			navigation={navigation as unknown as ComponentProps<typeof SessionInfoSheet>["navigation"]}
		/>,
	);
	const title = sheet.root.findAll(
		(node) => String(node.type) === "Pressable" && String(node.props.accessibilityLabel).endsWith(", rename"),
	)[0];
	if (!title) throw new Error("no rename");
	act(() => title.props.onPress());
	const nameField = () => sheet.root.find((node) => String(node.type) === "TextInput");
	act(() => nameField().props.onChangeText("Settle race"));

	const screenAt = (state: string) => {
		harness.connection = { ...harness.connection, state };
		const route = { key: "conversation-ref-blip", name: "Conversation", params: { ...params, title: "Session" } };
		act(() =>
			tree.update(
				<ConversationScreen route={route as unknown as ConversationScreenProps["route"]} navigation={navigation} />,
			),
		);
	};
	screenAt("connecting");
	await settle();
	expect(navigation.goBack).not.toHaveBeenCalled();
	expect(nameField().props.value).toBe("Settle race");
	// Save waits for the hub.
	await act(async () => nameField().props.onSubmitEditing());
	expect(hub.requests.filter((request) => request.method === "evener/thread/name/set")).toEqual([]);
	expect(nameField().props.value).toBe("Settle race");

	screenAt("ready");
	await settle();
	await act(async () => nameField().props.onSubmitEditing());
	await settle();
	expect(
		hub.requests.filter((request) => request.method === "evener/thread/name/set").map((request) => request.params),
	).toEqual([{ ref: "ref-blip", name: "Settle race" }]);
	act(() => sheet.unmount());
});

describe("a session that can't take a message yet (ruling 20)", () => {
	it("asks for a restart in the composer's place, and restarts by stopping and resuming", async () => {
		const served = thread("ref-restart", "idle");
		(served as unknown as { status: unknown }).status = { type: "restartRequired" };
		const { tree, hub } = await mount(served);
		expect(renderedText(tree)).toContain("This session runs an older Evener. Restart it to pick up the hub's update.");
		expect(field(tree)).toBeUndefined();
		await press(tree, "Restart session");
		expect(
			hub.requests
				.filter((request) => request.method === "forceStop" || request.method === "resumeThread")
				.map((request) => [request.method, request.params.ref]),
		).toEqual([
			["forceStop", "ref-restart"],
			["resumeThread", "ref-restart"],
		]);
	});

	function restartNeeded(ref: string): Thread {
		const served = thread(ref, "idle");
		(served as unknown as { status: unknown }).status = { type: "restartRequired" };
		return served;
	}

	function rerender(tree: ReactTestRenderer, ref: string, state: string) {
		harness.connection = { ...harness.connection, state };
		const route = {
			key: `conversation-${ref}`,
			name: "Conversation",
			params: { hubId: "hub-1", ref, title: "Session" },
		};
		act(() =>
			tree.update(
				<ConversationScreen route={route as unknown as ConversationScreenProps["route"]} navigation={navigation} />,
			),
		);
	}

	it("still resumes after a restart whose controls were replaced while it stopped", async () => {
		const { tree, hub } = await mount(restartNeeded("ref-restart-swap"));
		let stopped!: () => void;
		hub.client.forceStop = (ref: string) => {
			hub.requests.push({ method: "forceStop", params: { ref } });
			return new Promise<void>((resolve) => {
				stopped = resolve;
			});
		};
		await press(tree, "Restart session");
		// A blip while the stop is on its way replaces the session's controls.
		rerender(tree, "ref-restart-swap", "connecting");
		await settle();
		rerender(tree, "ref-restart-swap", "ready");
		await settle();
		stopped();
		await settle();
		expect(
			hub.requests
				.filter((request) => request.method === "forceStop" || request.method === "resumeThread")
				.map((request) => request.method),
		).toEqual(["forceStop", "resumeThread"]);
		expect(renderedText(tree)).not.toContain("Restarting…");
	});

	it("says so in the notice when the session stopped but couldn't start again", async () => {
		const { tree, hub } = await mount(restartNeeded("ref-restart-fails"));
		hub.client.resumeThread = async (ref: string) => {
			hub.requests.push({ method: "resumeThread", params: { ref } });
			throw new Error("resume refused");
		};
		await press(tree, "Restart session");
		expect(renderedText(tree)).toContain("Stopped, but couldn't start it again.");
		expect(pressable(tree, "Restart session")?.props.disabled).toBe(false);
	});

	it("says so in the notice when the stop fails", async () => {
		const { tree, hub } = await mount(restartNeeded("ref-stop-fails"));
		hub.client.forceStop = async () => {
			throw new Error("stop refused");
		};
		await press(tree, "Restart session");
		expect(hub.requests.filter((request) => request.method === "resumeThread")).toEqual([]);
		expect(renderedText(tree)).toContain("Couldn't restart this session.");
	});

	it("offers Resume in the composer's place for a paused session", async () => {
		const served = thread("ref-paused", "idle");
		(served as unknown as { evener: Record<string, unknown> }).evener.resumeRequired = true;
		const { tree, hub } = await mount(served);
		expect(renderedText(tree)).toContain("This session is paused.");
		expect(field(tree)).toBeUndefined();
		await press(tree, "Resume");
		expect(
			hub.requests.filter((request) => request.method === "resumeThread").map((request) => request.params.ref),
		).toEqual(["ref-paused"]);
	});
});

it("opens sign-in from an error that says a sign-in failed", async () => {
	vi.mocked(navigation.navigate).mockClear();
	const { tree } = await mount(failedTurn("ref-error-sign-in", "401 Unauthorized"));
	await press(tree, "Sign in");
	// The error doesn't name the provider, so the Hub opens at Providers.
	expect(navigation.navigate).toHaveBeenCalledWith("Hub", {
		screen: "Providers",
		params: { hubId: "hub-1" },
		initial: false,
	});
});

it("previews your note in the notes bar, and the sheet it opens saves through the screen", async () => {
	vi.mocked(navigation.navigate).mockClear();
	vi.mocked(navigation.goBack).mockClear();
	const served = thread("ref-notes", "idle");
	const evener = (served as unknown as { evener: Record<string, unknown> }).evener;
	evener.capabilities = { ...CAPABILITIES, sharedNotes: true };
	evener.humanNote = "keep the tests";
	evener.sessionUrls = [
		{ id: "u1", url: "https://example.com/pr/1", label: "The PR" },
		{ id: "u2", url: "https://example.com/pr/2" },
	];
	(served as unknown as { cwd: string }).cwd = "/home/jesse/git/evener";
	const { tree, hub } = await mount(served);
	// The sheet opens a file link in the Reader with the session's folder and title.
	expect(notesHosts.get(sheetKey("hub-1", "ref-notes"))).toMatchObject({
		cwd: "/home/jesse/git/evener",
		title: "Session",
	});
	const bar = pressable(tree, "Your note: keep the tests, 2 links");
	if (!bar) throw new Error("no notes bar");
	act(() => bar.props.onPress());
	const params = { hubId: "hub-1", ref: "ref-notes", focusEditor: true };
	expect(navigation.navigate).toHaveBeenCalledWith("NotesSheet", params);

	// The sheet route renders beside the screen and reads the host it provides.
	const sheetRoute = { key: "notes-sheet", name: "NotesSheet", params };
	const sheet = render(
		<NotesSheet
			route={sheetRoute as unknown as ComponentProps<typeof NotesSheet>["route"]}
			navigation={navigation as unknown as ComponentProps<typeof NotesSheet>["navigation"]}
		/>,
	);
	const editor = sheet.root
		.findAll((node) => String(node.type) === "TextInput")
		.find((node) => node.props.accessibilityLabel === "Your note");
	if (!editor) throw new Error("no note editor");
	expect(editor.props.value).toBe("keep the tests");
	act(() => editor.props.onChangeText("keep the tests green"));
	act(() => pressable(sheet, "Done")?.props.onPress());
	expect(navigation.goBack).toHaveBeenCalledOnce();
	// The route leaves.
	act(() => sheet.unmount());
	await settle();

	expect(
		hub.requests.filter((request) => request.method === "notes/human/set").map((request) => request.params),
	).toEqual([
		expect.objectContaining({ ref: "ref-notes", expectedInstanceId: "instance", note: "keep the tests green" }),
	]);
	expect(renderedText(tree)).toContain("Note saved. The agent is reading it.");
});

it("shows no notes bar for a session with nothing shared", async () => {
	const served = thread("ref-no-notes", "idle");
	(served as unknown as { evener: Record<string, unknown> }).evener.capabilities = {
		...CAPABILITIES,
		sharedNotes: true,
	};
	const { tree } = await mount(served);
	expect(renderedText(tree)).not.toContain("Your note");
	expect(tree.root.findAll((node) => String(node.type) === "SymbolView" && node.props.name === "person")).toEqual([]);
});

it("retries a note that failed to save once, on its own, without needing a reconnect", async () => {
	const served = thread("ref-notes-retry", "idle");
	(served as unknown as { evener: Record<string, unknown> }).evener.capabilities = {
		...CAPABILITIES,
		sharedNotes: true,
	};
	const { hub } = await mount(served);
	const client = hub.client as { request: (method: string, params: Record<string, unknown>) => Promise<unknown> };
	const request = client.request;
	let attempts = 0;
	client.request = async (method, params) => {
		if (method === "notes/human/set") {
			attempts += 1;
			if (attempts === 1) throw new Error("offline");
		}
		return request(method, params);
	};
	const params = { hubId: "hub-1", ref: "ref-notes-retry" };
	const sheet = render(
		<NotesSheet
			route={
				{ key: "notes-sheet", name: "NotesSheet", params } as unknown as ComponentProps<typeof NotesSheet>["route"]
			}
			navigation={navigation as unknown as ComponentProps<typeof NotesSheet>["navigation"]}
		/>,
	);
	const editor = sheet.root
		.findAll((node) => String(node.type) === "TextInput")
		.find((node) => node.props.accessibilityLabel === "Your note");
	if (!editor) throw new Error("no note editor");
	act(() => editor.props.onChangeText("first try"));
	act(() => pressable(sheet, "Done")?.props.onPress());
	act(() => sheet.unmount());
	await settle();
	// The session stayed open, connected and in front the whole time: the
	// screen retries the failed save on its own rather than waiting for an
	// unrelated reconnect or remount to notice it.
	expect(attempts).toBe(2);
	expect(
		hub.requests.filter((request) => request.method === "notes/human/set").map((request) => request.params.note),
	).toEqual(["first try"]);
});

it("sends a note kept on this phone from a failed save once the session opens connected", async () => {
	harness.kv.set("evener.native.note-draft.hub-1", JSON.stringify({ "ref-kept-note": "kept from before" }));
	const served = thread("ref-kept-note", "idle");
	(served as unknown as { evener: Record<string, unknown> }).evener.capabilities = {
		...CAPABILITIES,
		sharedNotes: true,
	};
	const { hub } = await mount(served);
	expect(
		hub.requests.filter((request) => request.method === "notes/human/set").map((request) => request.params.note),
	).toEqual(["kept from before"]);
	expect(harness.kv.has("evener.native.note-draft.hub-1")).toBe(false);
});

it("keeps a note from a failed save on this phone while the session can't take notes", async () => {
	harness.kv.set("evener.native.note-draft.hub-1", JSON.stringify({ "ref-kept-ended": "kept from before" }));
	const served = thread("ref-kept-ended", "idle");
	(served as unknown as { status: { type: string } }).status = { type: "ended" };
	(served as unknown as { evener: Record<string, unknown> }).evener.capabilities = {
		...CAPABILITIES,
		sharedNotes: true,
	};
	const { hub } = await mount(served);
	expect(hub.requests.filter((request) => request.method === "notes/human/set")).toEqual([]);
	expect(harness.kv.has("evener.native.note-draft.hub-1")).toBe(true);
});

it("follows the hub's note in the bar and the open sheet when it changes", async () => {
	const served = thread("ref-notes-follow", "idle");
	const evener = (served as unknown as { evener: Record<string, unknown> }).evener;
	evener.capabilities = { ...CAPABILITIES, sharedNotes: true };
	evener.humanNote = "first";
	const { tree, hub } = await mount(served);
	const params = { hubId: "hub-1", ref: "ref-notes-follow", focusEditor: false };
	const sheet = render(
		<NotesSheet
			route={
				{ key: "notes-sheet", name: "NotesSheet", params } as unknown as ComponentProps<typeof NotesSheet>["route"]
			}
			navigation={navigation as unknown as ComponentProps<typeof NotesSheet>["navigation"]}
		/>,
	);
	act(() =>
		hub.notify({
			method: "evener/notes/updated",
			params: { threadId: served.id, ref: "ref-notes-follow", humanNote: "second", agentNote: "" },
		} as unknown as AnyNotification),
	);
	await settle();
	expect(renderedText(tree)).toContain("Your note: second");
	const editor = sheet.root
		.findAll((node) => String(node.type) === "TextInput")
		.find((node) => node.props.accessibilityLabel === "Your note");
	expect(editor?.props.value).toBe("second");
	act(() => sheet.unmount());
	await settle();
	expect(hub.requests.filter((request) => request.method === "notes/human/set")).toEqual([]);
});

describe("queued messages above the composer (spec 8.5)", () => {
	it("paints a swiped ghost what it sits on: the composer, or the page while the dock takes its place", async () => {
		const palette = paletteFor("light");
		const backdrop = (tree: ReactTestRenderer) =>
			tree.root.findByProps({ testID: "swipe-row-content" }).props.style.backgroundColor;
		const composing = (await mount(thread("ref-swipe-composer", "active", false, ["check the logs"]))).tree;
		expect(backdrop(composing)).toBe(palette.surface);
		const asking = (await mount(thread("ref-swipe-dock", "awaiting", true, ["check the logs"]))).tree;
		expect(field(asking)).toBeUndefined();
		expect(backdrop(asking)).toBe(palette.page);
	});

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

	// While you type in the composer the queue folds to one line: with one
	// message, its action; with several, their count, which opens them.
	it("folds one queued message while you type, steers from there, and shows it again when the keyboard lowers", async () => {
		const { tree, hub } = await mount(thread("ref-steer-typing", "active", false, ["check the logs"]));
		act(() => keyboard.show());
		expect(renderedText(tree)).not.toContain("check the logs");
		expect(pressable(tree, "1 queued")).toBeDefined();
		await press(tree, "Steer now, check the logs");
		expect(hub.requests.filter((request) => request.method === "turn/promoteQueuedAsSteer")).toHaveLength(1);
		act(() => keyboard.hide());
		expect(renderedText(tree)).toContain("check the logs");
	});

	it("folds several queued messages to their count while you type", async () => {
		const { tree } = await mount(thread("ref-typing-several", "active", false, ["check the logs", "then deploy"]));
		act(() => keyboard.show());
		expect(pressable(tree, "2 queued")).toBeDefined();
		expect(pressable(tree, "Steer now, check the logs")).toBeUndefined();
		act(() => keyboard.hide());
	});

	it("folds a held message to its count and Send now while you type", async () => {
		const { tree } = await mount(thread("ref-typing-held", "idle", false, ["check the logs"]));
		act(() => keyboard.show());
		expect(pressable(tree, "1 held")).toBeDefined();
		expect(pressable(tree, "Send now, check the logs")).toBeDefined();
		act(() => keyboard.hide());
	});

	// The keyboard is up for the find bar's field, not the composer.
	it("keeps the queue open while you type in the find bar", async () => {
		const { tree } = await mount(thread("ref-typing-find", "active", false, ["check the logs"]));
		chooseMenu("Find in session");
		act(() => keyboard.show());
		expect(renderedText(tree)).toContain("check the logs");
		expect(pressable(tree, "1 queued")).toBeUndefined();
		act(() => keyboard.hide());
	});

	// With the dock in the composer's place, a keyboard up isn't the
	// composer's, so the queue stays as it is.
	it("keeps the queue open when the keyboard is up while the dock takes the composer's place", async () => {
		const { tree } = await mount(thread("ref-typing-dock", "awaiting", true, ["check the logs"]));
		expect(field(tree)).toBeUndefined();
		act(() => keyboard.show());
		expect(renderedText(tree)).toContain("check the logs");
		expect(pressable(tree, "1 queued")).toBeUndefined();
		act(() => keyboard.hide());
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
		const route = {
			key: "conversation-ref-steer-all",
			name: "Conversation",
			params: { hubId: "hub-1", ref: "ref-steer-all", title: "Session" },
		};
		act(() =>
			tree.update(
				<ConversationScreen route={route as unknown as ConversationScreenProps["route"]} navigation={navigation} />,
			),
		);
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

describe("the bottom bar (spec 8.1, the prototype's .bottom)", () => {
	// The bar's own host view, not the BarFrame element that carries its props.
	const bar = (tree: ReactTestRenderer) =>
		tree.root.find((node) => String(node.type) === "View" && node.props.testID === "session-bottom-bar");
	const homeIndicator = () => useSafeAreaInsets().bottom;

	it("runs to the screen's bottom edge under a hairline, and keeps the composer above the home indicator", async () => {
		const { tree } = await mount(thread("ref-bar", "active"));
		// The screen leaves the bottom safe area to the bar, which paints it.
		const [screen] = tree.root.findAll((node) => String(node.type) === "SafeAreaView");
		expect(screen?.props.edges).not.toContain("bottom");
		expect(flatStyle(bar(tree))).toMatchObject({ borderTopWidth: 0.5, paddingBottom: homeIndicator() });
		// The tray and the composer ride in it, so the bar's padding lifts them
		// clear of the home indicator.
		expect(
			bar(tree).findAll((node) => node.props.accessibilityLabel === "Message" && node.props.multiline),
		).toHaveLength(1);
		expect(bar(tree).findAll((node) => node.props.accessibilityLabel === "Stop")).not.toHaveLength(0);
	});

	it("drops the home indicator's room while the keyboard is up, so the composer sits on the keyboard", async () => {
		const { tree } = await mount(thread("ref-bar-keyboard", "active"));
		act(() => keyboard.show());
		expect(flatStyle(bar(tree)).paddingBottom).toBe(0);
		act(() => keyboard.hide());
		expect(flatStyle(bar(tree)).paddingBottom).toBe(homeIndicator());
	});

	it("lays the bar over the transcript's end, so the transcript runs under it", async () => {
		const { tree } = await mount(thread("ref-bar-over", "active"));
		expect(flatStyle(bar(tree))).toMatchObject({ position: "absolute", left: 0, right: 0, bottom: 0 });
	});

	const barLaysOut = (tree: ReactTestRenderer, height: number) =>
		act(() => bar(tree).props.onLayout({ nativeEvent: { layout: { x: 0, y: 600, width: 390, height } } }));
	const transcript = (tree: ReactTestRenderer) =>
		tree.root.find((node) => node.props.maintainVisibleContentPosition && node.props.contentContainerStyle);
	const moves = () => flatListCalls.filter((call) => call.method !== "scrollToIndex");

	it("keeps the transcript's end clear of the bar as an inset, so its content size ignores the bar", async () => {
		const { tree } = await mount(thread("ref-bar-end", "active"));
		barLaysOut(tree, 180);
		// iOS: the bar is a content inset past the 60pt end room, which stays
		// the content's own padding.
		expect(transcript(tree).props.contentInset).toEqual({ bottom: 180 });
		expect(transcript(tree).props.contentContainerStyle.paddingBottom).toBe(60);
		expect(transcript(tree).props.scrollIndicatorInsets).toEqual({ bottom: 180 });
		// Next, a toast and "↓ new" float 10pt above the bar.
		expect(
			flatStyle(tree.root.findByType(FloatingStack).findAll((node) => String(node.type) === "View")[0]),
		).toMatchObject({ bottom: 180 + 10 });
	});

	it("keeps a follower at the end as the bar grows and shrinks, the keyboard's drop included", async () => {
		const { tree } = await mount(thread("ref-bar-follow", "active"));
		barLaysOut(tree, 180);
		flatListCalls.length = 0;
		barLaysOut(tree, 260);
		expect(moves().map((call) => call.method)).toEqual(["scrollToEnd"]);
		flatListCalls.length = 0;
		// Keyboard up: the bar drops the home indicator's room.
		act(() => keyboard.show());
		barLaysOut(tree, 226);
		expect(moves().map((call) => call.method)).toEqual(["scrollToEnd"]);
		act(() => keyboard.hide());
	});

	it("pads the transcript's end by the bar on Android, and keeps a follower at the end as the bar changes", async () => {
		const platform = Platform as { OS: string };
		platform.OS = "android";
		try {
			const { tree } = await mount(thread("ref-bar-follow-android", "active"));
			barLaysOut(tree, 180);
			expect(transcript(tree).props.contentInset).toBeUndefined();
			expect(transcript(tree).props.contentContainerStyle.paddingBottom).toBe(180 + 60);
			flatListCalls.length = 0;
			barLaysOut(tree, 260);
			expect(transcript(tree).props.contentContainerStyle.paddingBottom).toBe(260 + 60);
			expect(moves().map((call) => call.method)).toEqual(["scrollToEnd"]);
		} finally {
			platform.OS = "ios";
		}
	});

	it("never moves a reader mid-list, or one resting at the end without following, as the bar changes", async () => {
		const { tree } = await mount(thread("ref-bar-reader", "active"));
		barLaysOut(tree, 180);
		const at = (y: number) => ({
			nativeEvent: {
				contentOffset: { y },
				contentSize: { height: 4_000 },
				layoutMeasurement: { height: 600 },
				contentInset: { bottom: 180 },
			},
		});
		act(() => transcriptList(tree).props.onScrollBeginDrag(at(1_000)));
		act(() => transcriptList(tree).props.onScroll(at(1_000)));
		act(() => transcriptList(tree).props.onScrollEndDrag(at(1_000)));
		flatListCalls.length = 0;
		barLaysOut(tree, 260);
		barLaysOut(tree, 120);
		expect(moves()).toEqual([]);
		// The app lands the list at the end (a restore), with no finger: not following.
		act(() => transcriptList(tree).props.onScroll(at(4_000 - 600 + 180)));
		flatListCalls.length = 0;
		barLaysOut(tree, 260);
		expect(moves()).toEqual([]);
	});

	it("holds a question dock too", async () => {
		const { tree } = await mount(thread("ref-bar-question", "awaiting", true));
		expect(bar(tree).findAll((node) => node.props.testID === "question-dock")).toHaveLength(1);
	});
});

describe("an approval waiting for a decision (spec 8.4, ruling 38)", () => {
	function withApproval(ref: string): Thread {
		const served = thread(ref, "active");
		(served as unknown as { evener: Record<string, unknown> }).evener.pendingEscalations = [
			{
				threadId: served.id,
				ref,
				escalationId: "esc-1",
				mode: "workspace-write",
				tool: "write_file",
				kind: "file_tool",
				deniedPath: "/Users/jesse/sites/docs/index.html",
			},
		];
		return served;
	}

	it("says how many more wait, and the next takes the dock once the first is settled", async () => {
		const ref = "ref-approval-two";
		const served = withApproval(ref);
		const evener = (served as unknown as { evener: { pendingEscalations: Record<string, unknown>[] } }).evener;
		evener.pendingEscalations.push({
			...evener.pendingEscalations[0],
			escalationId: "esc-2",
			tool: "read_file",
			mode: "restricted",
			deniedPath: "/Users/jesse/notes/todo.md",
		});
		const { tree, hub } = await mount(served);
		expect(renderedText(tree)).toContain("Wants to write outside the workspace");
		expect(renderedText(tree)).toContain("1 more waiting");
		const firstBody = dockBody(tree, "approval-dock").scroller;
		await press(tree, "Allow this file only");
		act(() =>
			hub.notify({
				method: "evener/sandbox/escalation/resolved",
				params: { threadId: served.id, ref, escalationId: "esc-1" },
			} as unknown as AnyNotification),
		);
		await settle();
		const text = renderedText(tree).replaceAll("\u200b", "");
		expect(text).toContain("Wants to read outside the workspace");
		expect(text).toContain("read_file  /Users/jesse/notes/todo.md");
		expect(text).not.toContain("more waiting");
		expect(pressable(tree, "Allow this file only")?.props.accessibilityState).toMatchObject({ disabled: false });
		// The next approval gets a fresh dock (keyed by escalationId), so its
		// body opens at its top, whatever the last one was scrolled to.
		expect(dockBody(tree, "approval-dock").scroller).not.toBe(firstBody);
	});

	it("shows the dock in the tray's place, and never the composer", async () => {
		const { tree } = await mount(withApproval("ref-approval"));
		expect(renderedText(tree)).toContain("Wants to write outside the workspace");
		expect(pressable(tree, "Stop")).toBeUndefined();
		expect(field(tree)).toBeUndefined();
		expect(renderedText(tree)).not.toContain("approval needed");
	});

	it("puts a long approval's target in the dock's scroller, and Allow and Deny outside it", async () => {
		const served = withApproval("ref-approval-long");
		const deniedPath = `/home/jesse/sites/${"docs/reference/wire/".repeat(6)}index.html`;
		const [escalation] = (served as unknown as { evener: { pendingEscalations: Record<string, unknown>[] } }).evener
			.pendingEscalations;
		if (!escalation) throw new Error("no escalation");
		escalation.deniedPath = deniedPath;
		escalation.partiallyRan = true;
		const { tree } = await mount(served);
		const body = dockBody(tree, "approval-dock");
		expect(textOf(body.scroller).replaceAll("\u200b", "")).toContain(deniedPath);
		for (const label of ["Allow this file only", "Deny"]) expect(body.holds(label)).toBe(false);
	});

	it("keeps Allow and Deny while the keyboard is up", async () => {
		const { tree } = await mount(withApproval("ref-approval-keyboard"));
		act(() => keyboard.show());
		expect(pressable(tree, "Allow this file only")).toBeDefined();
		expect(pressable(tree, "Deny")).toBeDefined();
		expect(pressable(tree, "Show options")).toBeUndefined();
		act(() => keyboard.hide());
	});

	it("wires only the approval dock's slot to shrink", async () => {
		const { tree } = await mount(withApproval("ref-approval-room"));
		expectOnlyTheDockSlotShrinks(tree, "approval-dock");
	});

	it("keeps showing what waits while the hub is away, without Allow or Deny", async () => {
		const { tree } = await mount(withApproval("ref-approval-away"));
		harness.connection = { ...harness.connection, state: "connecting" };
		const route = {
			key: "conversation-ref-approval-away",
			name: "Conversation",
			params: { hubId: "hub-1", ref: "ref-approval-away", title: "Session" },
		};
		act(() =>
			tree.update(
				<ConversationScreen route={route as unknown as ConversationScreenProps["route"]} navigation={navigation} />,
			),
		);
		await settle();
		const text = renderedText(tree).replaceAll("\u200b", "");
		expect(text).toContain("Wants to write outside the workspace");
		expect(text).toContain("write_file  /Users/jesse/sites/docs/index.html");
		expect(pressable(tree, "Allow this file only")).toBeUndefined();
		expect(pressable(tree, "Deny")).toBeUndefined();
		expect(field(tree)).toBeUndefined();
	});

	it("allows the one file, and says so", async () => {
		const { tree, hub } = await mount(withApproval("ref-approval-allow"));
		playedHaptics.length = 0;
		await press(tree, "Allow this file only");
		// Spec 16.6: success on approval allowed.
		expect(playedHaptics).toEqual(["notification:success"]);
		const resolves = hub.requests.filter((request) => request.method === "evener/sandbox/escalation/resolve");
		expect(resolves.map((request) => request.params)).toEqual([
			{ ref: "ref-approval-allow", escalationId: "esc-1", approve: true },
		]);
		expect(renderedText(tree)).toContain("Allowed once");
	});

	it("denies without a haptic: spec 16.6 plays success only for allowed", async () => {
		const { tree } = await mount(withApproval("ref-approval-deny"));
		playedHaptics.length = 0;
		await press(tree, "Deny");
		expect(renderedText(tree)).toContain("Denied");
		expect(playedHaptics).toEqual([]);
	});
});

it("offers no Retry that couldn't send: a question still waits on the failed turn", async () => {
	const served = thread("ref-retry-question", "idle", true);
	// A per-test turn: spreading the shared QUESTION_TURN keeps its question
	// while this test owns the failed status, so it never mutates the fixture
	// the other question tests read.
	(served as unknown as { turns: unknown[] }).turns = [
		{ ...QUESTION_TURN, status: "failed", error: { message: "go test exited 1" } },
	];
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

it("leaves the shared question fixture as the other question tests expect it", () => {
	const turn = (
		thread("ref-fixture-intact", "idle", true) as unknown as { turns: { status: string; error?: unknown }[] }
	).turns[0];
	expect(turn.status).toBe("completed");
	expect(turn.error).toBeUndefined();
});

describe("Commands and skills (spec 8.5, ruling 15)", () => {
	// Choosing focuses the field on the next frame; these tests run it at once.
	beforeEach(() => {
		vi.stubGlobal("requestAnimationFrame", (frame: (time: number) => void) => {
			frame(0);
			return 0;
		});
		vi.mocked(navigation.navigate).mockClear();
	});
	afterEach(() => vi.unstubAllGlobals());

	it("opens the sheet for a slash typed into an empty draft, and keeps the slash out", async () => {
		const { tree } = await mount(thread("ref-slash", "idle"));
		await type(tree, "/");
		expect(navigation.navigate).toHaveBeenCalledWith("CommandsSheet", { hubId: "hub-1", ref: "ref-slash" });
		expect(field(tree)?.props.value).toBe("");
	});

	it("keeps a slash typed after other words", async () => {
		const { tree } = await mount(thread("ref-slash-later", "idle"));
		await type(tree, "a");
		await type(tree, "a/");
		expect(navigation.navigate).not.toHaveBeenCalledWith("CommandsSheet", expect.anything());
		expect(field(tree)?.props.value).toBe("a/");
	});

	it("opens the sheet from +", async () => {
		const { tree } = await mount(thread("ref-plus", "idle"));
		vi.mocked(ActionSheetIOS.showActionSheetWithOptions).mockClear();
		act(() => pressable(tree, "Add")?.props.onPress());
		const [options, choose] = vi.mocked(ActionSheetIOS.showActionSheetWithOptions).mock.calls[0] as [
			{ options: string[] },
			(index: number) => void,
		];
		act(() => choose(options.options.indexOf("Commands and skills")));
		expect(navigation.navigate).toHaveBeenCalledWith("CommandsSheet", { hubId: "hub-1", ref: "ref-plus" });
	});

	it("puts the chosen command at the start of the draft", async () => {
		const { tree } = await mount(thread("ref-choose", "idle"));
		await type(tree, "hello");
		const host = commandHosts.get(sheetKey("hub-1", "ref-choose"));
		expect(host?.session.capabilities).toMatchObject({ send: true });
		act(() => host?.choose("/goal"));
		await flush();
		expect(field(tree)?.props.value).toBe("/goal hello");
	});
});

/** Chooses an item in the ⋯ menu the screen set last. */
function chooseMenu(label: string) {
	const calls = vi.mocked(navigation.setOptions).mock.calls as [NativeStackNavigationOptions][];
	const options = calls.map(([options]) => options).findLast((options) => options.unstable_headerRightItems);
	const [menu] = (options?.unstable_headerRightItems?.({ canGoBack: true }) ?? []) as NativeStackHeaderItemMenu[];
	const found = menu?.menu.items.find((item) => item.label === label) as NativeStackHeaderItemMenuAction | undefined;
	if (!found) throw new Error(`no ${label} in the header menu`);
	act(() => found.onPress());
}

describe("Find in session (spec 8.7, ruling 29)", () => {
	const palette = paletteFor("light");

	function findTurns(ref: string): Thread {
		const served = thread(ref, "idle");
		(served as unknown as { turns: unknown[] }).turns = [
			askReplyTurn("turn_1", "Fix the flaky Settle test", "The race is in settle."),
			askReplyTurn("turn_2", "Anything else?", "Nothing else."),
		];
		return served;
	}

	function findField(tree: ReactTestRenderer) {
		return tree.root
			.findAll((node) => String(node.type) === "TextInput")
			.find((node) => node.props.accessibilityLabel === "Find in session");
	}

	async function search(tree: ReactTestRenderer, query: string) {
		const input = findField(tree);
		if (!input) throw new Error("no find field");
		act(() => input.props.onChangeText(query));
		await settle();
	}

	const findScrolls = () =>
		flatListCalls
			.filter((call) => call.method === "scrollToIndex")
			.map((call) => call.args as { index: number; viewPosition?: number })
			.filter((args) => args.viewPosition === 0.3)
			.map((args) => args.index);

	/** The words of the rows washed as the current match: your messages'
	 * text and the agent's markdown. */
	const washed = (tree: ReactTestRenderer) =>
		tree.root
			.findAll((node) => String(node.type) === "View" && node.props.style?.backgroundColor === palette.accentBg)
			.map((node) =>
				node
					.findAll((child) => String(child.type) === "Text" || String(child.type) === "EnrichedMarkdownText")
					.map((child) => (String(child.type) === "Text" ? textOf(child) : String(child.props.markdown)))
					.join(" "),
			);

	it("opens only the find bar, and no sheet over it", async () => {
		const { tree } = await mount(findTurns("ref-find-only"));
		vi.mocked(navigation.navigate).mockClear();
		chooseMenu("Find in session");
		await settle();
		expect(findField(tree)).toBeDefined();
		expect(navigation.navigate).not.toHaveBeenCalled();
	});

	it("puts the find bar where the chips were, and Done brings them back", async () => {
		const { tree } = await mount(findTurns("ref-find-open"));
		expect(findField(tree)).toBeUndefined();
		chooseMenu("Find in session");
		await settle();
		expect(findField(tree)?.props.autoFocus).toBe(true);
		act(() => pressable(tree, "Done")?.props.onPress());
		await settle();
		expect(findField(tree)).toBeUndefined();
	});

	it("shows the newest match first, then steps older and newer, washing the current row", async () => {
		const { tree } = await mount(findTurns("ref-find-step"));
		chooseMenu("Find in session");
		flatListCalls.length = 0;
		await search(tree, "settle");
		// Rows: ask 1, reply 1, ask 2, reply 2. Both matches are in turn 1.
		expect(renderedText(tree)).toContain("2 of 2");
		expect(findScrolls()).toEqual([1]);
		expect(washed(tree).join(" ")).toContain("The race is in settle.");
		await press(tree, "Older match");
		expect(renderedText(tree)).toContain("1 of 2");
		expect(findScrolls()).toEqual([1, 0]);
		expect(washed(tree).join(" ")).toContain("Fix the flaky Settle test");
		await press(tree, "Newer match");
		expect(renderedText(tree)).toContain("2 of 2");
		act(() => pressable(tree, "Done")?.props.onPress());
		await settle();
		expect(washed(tree)).toEqual([]);
	});

	describe("a match the list hasn't rendered yet", () => {
		// Each retry waits a frame. The frames queue here and run when a test
		// says, so a test can act between one try and the next.
		let frames = new Map<number, (time: number) => void>();
		let nextFrame = 0;
		beforeEach(() => {
			frames = new Map();
			vi.stubGlobal("requestAnimationFrame", (frame: (time: number) => void) => {
				nextFrame += 1;
				frames.set(nextFrame, frame);
				return nextFrame;
			});
			vi.stubGlobal("cancelAnimationFrame", (id: number) => void frames.delete(id));
		});
		afterEach(() => {
			flatListScrollFailures.remaining = 0;
			vi.unstubAllGlobals();
		});

		/** Runs the frames waiting now; any they ask for wait for the next call. */
		async function oneFrame() {
			const waiting = [...frames.values()];
			frames.clear();
			act(() => {
				for (const frame of waiting) frame(0);
			});
			await settle();
		}

		/** Runs the waiting frames, and any they ask for, until none wait. */
		async function runFrames() {
			while (frames.size > 0) await oneFrame();
		}

		/** Lays out the transcript cell at `index`, so the list has measured it. */
		function measureRow(tree: ReactTestRenderer, index: number) {
			const item = transcriptList(tree).findAll((node) => String(node.type) === "Item")[index];
			const cell = item?.findAll((node) => String(node.type) === "View" && node.props.onLayout)[0];
			if (!cell) throw new Error(`no cell at ${index}`);
			act(() => cell.props.onLayout({ nativeEvent: { layout: { x: 0, y: index * 80, width: 390, height: 80 } } }));
		}

		it("keeps moving toward it until the list reaches it", async () => {
			const { tree } = await mount(findTurns("ref-find-far"));
			chooseMenu("Find in session");
			flatListCalls.length = 0;
			flatListScrollFailures.remaining = 2;
			await search(tree, "race");
			await runFrames();
			// Two misses, each followed by a move near the row, then the jump lands.
			expect(findScrolls()).toEqual([1, 1, 1]);
			expect(flatListCalls.filter((call) => call.method === "scrollToOffset")).toHaveLength(2);
			expect(flatListScrollFailures.remaining).toBe(0);
		});

		it("stops after a few tries when the list never gets closer", async () => {
			const { tree } = await mount(findTurns("ref-find-stuck"));
			chooseMenu("Find in session");
			flatListCalls.length = 0;
			flatListScrollFailures.remaining = 10;
			await search(tree, "race");
			await runFrames();
			expect(findScrolls()).toEqual([1, 1, 1, 1]);
		});

		it("tries again for as long as each try measures rows closer to it", async () => {
			const { tree } = await mount(findTurns("ref-find-closer"));
			chooseMenu("Find in session");
			flatListCalls.length = 0;
			flatListScrollFailures.remaining = 10;
			// "Nothing else." is the last row, index 3.
			await search(tree, "nothing");
			await oneFrame();
			// A row before the match renders on the way: the budget starts over.
			measureRow(tree, 0);
			await runFrames();
			expect(findScrolls()).toEqual([3, 3, 3, 3, 3, 3]);
		});

		it("scrolls no further once Done closes find", async () => {
			const { tree } = await mount(findTurns("ref-find-done"));
			chooseMenu("Find in session");
			flatListCalls.length = 0;
			flatListScrollFailures.remaining = 1;
			await search(tree, "race");
			act(() => pressable(tree, "Done")?.props.onPress());
			await settle();
			await runFrames();
			expect(findScrolls()).toEqual([1]);
		});

		it("leaves a new search its own tries when the words change mid-way", async () => {
			const { tree } = await mount(findTurns("ref-find-requery"));
			chooseMenu("Find in session");
			flatListCalls.length = 0;
			flatListScrollFailures.remaining = 10;
			await search(tree, "race");
			// "Fix the flaky Settle test" is row 0.
			await search(tree, "flaky");
			await runFrames();
			expect(findScrolls()).toEqual([1, 0, 0, 0, 0]);
		});
	});

	it("tells VoiceOver the count a search settles on, and nothing on the way", async () => {
		const { tree } = await mount(findTurns("ref-find-announce"));
		chooseMenu("Find in session");
		const announce = vi.spyOn(AccessibilityInfo, "announceForAccessibility");
		try {
			await search(tree, "settle");
			await search(tree, "settle test");
			expect(announce.mock.calls).toEqual([["2 of 2"], ["1 of 1"]]);
		} finally {
			announce.mockRestore();
		}
	});

	it("says so when nothing matches", async () => {
		const { tree } = await mount(findTurns("ref-find-none"));
		chooseMenu("Find in session");
		await search(tree, "nowhere");
		expect(renderedText(tree)).toContain("No matches");
	});

	it("reaches back through older history for an older match", async () => {
		const { tree, hub } = await mount(findTurns("ref-find-older"), {
			olderCursor: "cursor-1",
			olderTurns: [askReplyTurn("turn_0", "Is settle flaky?", "Sometimes.")],
		});
		chooseMenu("Find in session");
		await search(tree, "settle");
		expect(renderedText(tree)).toContain("2 of 2");
		await press(tree, "Older match");
		await press(tree, "Older match");
		expect(
			hub.requests.filter((request) => request.method === "thread/turns/list").map((request) => request.params.cursor),
		).toEqual(["cursor-1"]);
		expect(washed(tree).join(" ")).toContain("Is settle flaky?");
		expect(renderedText(tree)).toContain("1 of 3");
	});

	describe("following the live end while find pages older history", () => {
		// Whether new content pulls the list to its end. The size keeps the
		// unmeasured test list (a 0pt viewport at offset 0) at its end.
		const followsTheEnd = (tree: ReactTestRenderer) => {
			flatListCalls.length = 0;
			act(() => transcriptList(tree).props.onContentSizeChange(390, 0));
			return flatListCalls.some((call) => call.method === "scrollToEnd");
		};
		const unrelated = [askReplyTurn("turn_0", "Unrelated?", "Yes.")];

		it("leaves the end while a page is on its way, and follows again when find closes", async () => {
			let release = () => {};
			const olderPage = new Promise<void>((resolve) => {
				release = resolve;
			});
			const { tree, hub } = await mount(findTurns("ref-find-close"), {
				olderCursor: "cursor-1",
				olderTurns: unrelated,
				olderPage,
			});
			chooseMenu("Find in session");
			await search(tree, "nowhere");
			expect(hub.requests.filter((request) => request.method === "thread/turns/list")).toHaveLength(1);
			expect(followsTheEnd(tree)).toBe(false);
			act(() => pressable(tree, "Done")?.props.onPress());
			await settle();
			expect(followsTheEnd(tree)).toBe(true);
			release();
			await settle();
		});

		it("stays off the end when find closes after the list moved away from it", async () => {
			let release = () => {};
			const olderPage = new Promise<void>((resolve) => {
				release = resolve;
			});
			const { tree } = await mount(findTurns("ref-find-moved"), {
				olderCursor: "cursor-1",
				olderTurns: unrelated,
				olderPage,
			});
			chooseMenu("Find in session");
			await search(tree, "nowhere");
			act(() =>
				transcriptList(tree).props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 600 } } }),
			);
			act(() => transcriptList(tree).props.onContentSizeChange(390, 4_000));
			scrollTo(tree, 1_000);
			act(() => pressable(tree, "Done")?.props.onPress());
			await settle();
			expect(followsTheEnd(tree)).toBe(false);
			release();
			await settle();
		});

		it("follows again when a search reaches the start of history with no match", async () => {
			const { tree, hub } = await mount(findTurns("ref-find-miss"), {
				olderCursor: "cursor-1",
				olderTurns: unrelated,
			});
			chooseMenu("Find in session");
			await search(tree, "nowhere");
			expect(hub.requests.filter((request) => request.method === "thread/turns/list")).toHaveLength(1);
			expect(followsTheEnd(tree)).toBe(true);
		});

		it("stays with a match it paged back to", async () => {
			const { tree, hub } = await mount(findTurns("ref-find-paged-match"), {
				olderCursor: "cursor-1",
				olderTurns: [askReplyTurn("turn_0", "Is it flaky?", "Sometimes.")],
			});
			chooseMenu("Find in session");
			flatListCalls.length = 0;
			await search(tree, "Is it flaky");
			expect(hub.requests.filter((request) => request.method === "thread/turns/list")).toHaveLength(1);
			expect(flatListCalls.filter((call) => call.method === "scrollToIndex")).not.toEqual([]);
			expect(followsTheEnd(tree)).toBe(false);
		});

		it("asks for no page when the match is already loaded", async () => {
			const { tree, hub } = await mount(findTurns("ref-find-loaded"), { olderCursor: "cursor-1" });
			chooseMenu("Find in session");
			await search(tree, "settle");
			expect(hub.requests.filter((request) => request.method === "thread/turns/list")).toEqual([]);
		});
	});

	it("says there are no older matches once history ends", async () => {
		const { tree, hub } = await mount(findTurns("ref-find-end"), { olderCursor: "cursor-1" });
		chooseMenu("Find in session");
		await search(tree, "settle");
		await press(tree, "Older match");
		await press(tree, "Older match");
		expect(hub.requests.filter((request) => request.method === "thread/turns/list")).toHaveLength(1);
		expect(renderedText(tree)).toContain("No older matches");
		expect(washed(tree).join(" ")).toContain("Fix the flaky Settle test");
	});
});

describe("document chips under the agent's messages (spec 8.2)", () => {
	const PLAN_PATH = "docs/superpowers/plans/settle-race.md";
	const WROTE_AT = "2026-09-26T11:39:00.000Z";

	function namedAfterWriting(ref: string): Thread {
		const served = thread(ref, "idle");
		(served as unknown as { cwd: string }).cwd = "/home/jesse/git/evener";
		(served as unknown as { turns: unknown[] }).turns = [
			{
				id: "t1",
				status: "completed",
				itemsView: "default",
				items: [
					{
						id: "write-1",
						turnId: "t1",
						type: "commandExecution",
						toolName: "write_file",
						status: "completed",
						// A wire Thread's times are epoch milliseconds; the reducer's
						// epochMsToISO hands documentReferences the ISO string.
						completedAt: Date.parse(WROTE_AT),
						argumentsJson: JSON.stringify({ file_path: `/home/jesse/git/evener/${PLAN_PATH}`, content: "# Plan" }),
					},
					{
						id: "said-1",
						turnId: "t1",
						type: "agentMessage",
						status: "completed",
						text: `The plan is in \`${PLAN_PATH}\`.`,
					},
				],
			},
		];
		return served;
	}

	it("puts a chip under a message that names a file the session wrote, and opens it in the Reader", async () => {
		vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("# Fix the settle race\n"));
		vi.mocked(navigation.navigate).mockClear();
		const { tree } = await mount(namedAfterWriting("ref-chips"));
		await settle();
		const chip = tree.root.findAll(
			(node) => String(node.type) === "Pressable" && String(node.props.accessibilityLabel).startsWith("Plan, "),
		)[0];
		if (!chip) throw new Error("no document chip");
		expect(renderedText(tree)).toContain("settle-race.md");
		act(() => chip.props.onPress());
		expect(navigation.navigate).toHaveBeenCalledWith("Reader", {
			hubId: "hub-1",
			sessionRef: "ref-chips",
			path: PLAN_PATH,
			sessionTitle: "Session",
			updatedAt: WROTE_AT,
		});
	});

	it("counts the session's documents on a Files chip, dotted while one is new, which opens Files & artifacts", async () => {
		vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("# Fix the settle race\n"));
		vi.mocked(navigation.navigate).mockClear();
		const { tree } = await mount(namedAfterWriting("ref-files"));
		const files = pressable(tree, "Files, 1, new or changed");
		if (!files) throw new Error("no Files chip");
		act(() => files.props.onPress());
		expect(navigation.navigate).toHaveBeenCalledWith("FilesSheet", {
			hubId: "hub-1",
			ref: "ref-files",
			title: "Session",
			documents: [{ path: PLAN_PATH, kind: "Plan", updatedAt: WROTE_AT }],
		});
	});
});

describe("moving between sessions (spec 8.3, 13.2)", () => {
	const at = (minute: number) => new Date(Date.UTC(2026, 8, 26, 12, minute)).toISOString();
	const failing = fleetSession("local:fail", {
		title: "Fix retry loop",
		state: "errored",
		updated_at: at(5),
		turn_ended_at: at(5),
	});
	const asking = fleetSession("local:ask", {
		title: "Pick a name",
		state: "awaiting",
		ask_pending: true,
		updated_at: at(3),
	});
	beforeEach(() => {
		fleet.live = [failing];
		fleet.needsYou = [failing, asking];
		vi.mocked(navigation.push).mockClear();
		vi.mocked(navigation.replace).mockClear();
		vi.mocked(navigation.goBack).mockClear();
		vi.mocked(ActionSheetIOS.showActionSheetWithOptions).mockClear();
		alerts.recent = [];
		alerts.nextUsed.mockClear();
	});

	/** The Back the screen set last, rendered as the header renders it. */
	function back() {
		const calls = vi.mocked(navigation.setOptions).mock.calls as [NativeStackNavigationOptions][];
		const options = calls.map(([options]) => options).findLast((options) => options.headerLeft);
		if (!options?.headerLeft) throw new Error("no headerLeft");
		const header = render(<>{options.headerLeft({ canGoBack: true })}</>);
		return header.root.findAll((node) => String(node.type) === "Pressable")[0];
	}

	const capsule = (tree: ReactTestRenderer) => pressable(tree, "Next, Fix retry loop");

	it("counts the others that need you on Back, which goes back", async () => {
		await mount(thread("ref-back", "idle"));
		const button = back();
		expect(button.props.accessibilityLabel).toBe("Back, 2 others need you");
		act(() => button.props.onPress());
		expect(navigation.goBack).toHaveBeenCalledTimes(1);
	});

	it("never counts this session, and shows no Next when nobody else needs you", async () => {
		fleet.live = [];
		fleet.needsYou = [fleetSession("ref-alone", { state: "errored", updated_at: at(1) })];
		const { tree } = await mount(thread("ref-alone", "idle"));
		expect(back().props.accessibilityLabel).toBe("Back");
		expect(renderedText(tree)).not.toContain("Next");
	});

	it("opens the first session that needs you from Next, marked seen", async () => {
		const { tree, hub } = await mount(thread("ref-next", "idle"));
		const next = capsule(tree);
		if (!next) throw new Error("no Next capsule");
		playedHaptics.length = 0;
		await act(async () => next.props.onPress());
		await settle();
		// Spec 16.6: a selection tick on a lateral session move.
		expect(playedHaptics).toEqual(["selection"]);
		expect(navigation.push).toHaveBeenCalledWith("Conversation", {
			hubId: "hub-1",
			ref: "local:fail",
			title: "Fix retry loop",
			openedBy: "next",
		});
		expect(navigation.replace).not.toHaveBeenCalled();
		expect(
			hub.requests.filter((request) => request.method === "evener/session/seen/set").map((request) => request.params),
		).toEqual([{ sessions: [{ ref: "local:fail", seenThrough: Date.parse(at(5)) }] }]);
	});

	it("replaces a session Next opened, so Back still lands where you started", async () => {
		const { tree } = await mount(thread("ref-next-again", "idle"), { openedBy: "next" });
		act(() => capsule(tree)?.props.onPress());
		expect(navigation.replace).toHaveBeenCalledWith("Conversation", {
			hubId: "hub-1",
			ref: "local:fail",
			title: "Fix retry loop",
			openedBy: "next",
		});
		expect(navigation.push).not.toHaveBeenCalled();
	});

	it("lists everyone who needs you on a hold, and opens the one you choose", async () => {
		const { tree } = await mount(thread("ref-hold", "idle"));
		act(() => capsule(tree)?.props.onLongPress());
		const [options, choose] = vi.mocked(ActionSheetIOS.showActionSheetWithOptions).mock.calls[0] as [
			{ options: string[]; cancelButtonIndex: number },
			(index: number) => void,
		];
		expect(options).toMatchObject({ options: ["Fix retry loop", "Pick a name", "Cancel"], cancelButtonIndex: 2 });
		act(() => choose(1));
		expect(navigation.push).toHaveBeenCalledWith("Conversation", {
			hubId: "hub-1",
			ref: "local:ask",
			title: "Pick a name",
			openedBy: "next",
		});
		act(() => choose(2));
		expect(navigation.push).toHaveBeenCalledTimes(1);
	});

	it("serves what alerted you first, on Next and on its list, and tells alerts it moved you on (spec 8.3)", async () => {
		alerts.recent = ["local:ask"];
		const { tree } = await mount(thread("ref-alerted", "idle"));
		const next = pressable(tree, "Next, Pick a name");
		if (!next) throw new Error("no Next capsule for the session that alerted");
		act(() => next.props.onLongPress());
		const [options, choose] = vi.mocked(ActionSheetIOS.showActionSheetWithOptions).mock.calls[0] as [
			{ options: string[] },
			(index: number) => void,
		];
		expect(options.options).toEqual(["Pick a name", "Fix retry loop", "Cancel"]);
		act(() => choose(2));
		expect(alerts.nextUsed).not.toHaveBeenCalled();
		act(() => choose(1));
		expect(alerts.nextUsed).toHaveBeenCalledTimes(1);
		await act(async () => next.props.onPress());
		expect(alerts.nextUsed).toHaveBeenCalledTimes(2);
		expect(navigation.push).toHaveBeenLastCalledWith("Conversation", {
			hubId: "hub-1",
			ref: "local:ask",
			title: "Pick a name",
			openedBy: "next",
		});
	});

	it("stacks a toast above Next, in the one column over the transcript's end, so neither covers the other", async () => {
		const { tree } = await mount(thread("ref-stacked", "active"));
		await press(tree, "Stop");
		expect(renderedText(tree)).toContain("Stopped");
		const next = capsule(tree);
		if (!next) throw new Error("no Next capsule");
		const stack = tree.root.findByType(FloatingStack);
		const toast = stack.findByType(Toast);
		expect(toast.props.toast).toMatchObject({ text: "Stopped" });
		expect(stack.props.next).toBeTruthy();
		// Nothing new arrived below, so there is no pill to stack.
		expect(stack.props.pill).toBeNull();
		expect(stack.findAll((node) => node === next)).toHaveLength(1);
		// The toast has no other place on the screen.
		expect(tree.root.findAllByType(Toast)).toHaveLength(1);
	});

	it("keeps a fixed 60pt of room at the transcript's end, whatever floats over it", async () => {
		const served = thread("ref-inset", "active");
		const { tree, hub } = await mount(served);
		const list = () =>
			tree.root.find((node) => node.props.maintainVisibleContentPosition && node.props.contentContainerStyle);
		const bottom = () => list().props.contentContainerStyle.paddingBottom;
		// Who needs you changes, as fleet polling finds: Next goes, then comes.
		let sequence = 0;
		async function needsYou(sessions: typeof fleet.needsYou) {
			fleet.needsYou = sessions;
			fleet.live = sessions;
			sequence += 1;
			fleet.revision = sequence + 1;
			act(() =>
				hub.notify({
					method: "evener/navigation/invalidated",
					params: {
						generationId: "generation-test",
						sequence,
						targets: [
							{ kind: "section", section: "needs_you", revision: sequence + 1 },
							{ kind: "section", section: "live", revision: sequence + 1 },
						],
					},
				} as AnyNotification),
			);
			await settle();
		}
		await needsYou([]);
		expect(capsule(tree)).toBeUndefined();
		expect(bottom()).toBe(60);
		await needsYou([failing]);
		expect(capsule(tree)).toBeDefined();
		expect(bottom()).toBe(60);
		// A toast joins it.
		await press(tree, "Stop");
		expect(renderedText(tree)).toContain("Stopped");
		expect(bottom()).toBe(60);
		// Away from the end, with rows the list didn't hold then, the pill joins too.
		act(() =>
			list().props.onScroll({
				nativeEvent: { contentOffset: { y: 100 }, contentSize: { height: 4_000 }, layoutMeasurement: { height: 600 } },
			}),
		);
		expect(bottom()).toBe(60);
	});

	it("shows no Next while this session asks you something", async () => {
		const { tree } = await mount(thread("ref-asks", "awaiting", true));
		expect(capsule(tree)).toBeUndefined();
		expect(back().props.accessibilityLabel).toBe("Back, 2 others need you");
	});

	it("steps Next aside while you type, and brings it back when the keyboard lowers", async () => {
		const { tree } = await mount(thread("ref-next-typing", "idle"));
		expect(capsule(tree)).toBeDefined();
		act(() => keyboard.show());
		expect(capsule(tree)).toBeUndefined();
		act(() => keyboard.hide());
		expect(capsule(tree)).toBeDefined();
	});

	it("shows no Next while the find bar is open", async () => {
		const { tree } = await mount(thread("ref-finding", "idle"));
		expect(capsule(tree)).toBeDefined();
		chooseMenu("Find in session");
		expect(capsule(tree)).toBeUndefined();
	});

	describe("swiping the title through Live order (spec 6, ruling 30)", () => {
		const working = fleetSession("ref-title", { title: "Session", state: "active", updated_at: at(4) });
		const quiet = fleetSession("local:quiet", {
			title: "Old spike",
			state: "idle",
			dormant: true,
			updated_at: at(1),
			turn_ended_at: at(1),
		});
		// Live order: failing, asking (Needs you), this one (Working), quiet
		// (Idle).
		beforeEach(() => {
			fleet.live = [failing, asking, working, quiet];
		});

		/** The title the screen set last, rendered as the header renders it. */
		function headerTitle() {
			const calls = vi.mocked(navigation.setOptions).mock.calls as [NativeStackNavigationOptions][];
			const options = calls.map(([options]) => options).findLast((options) => options.headerTitle);
			if (typeof options?.headerTitle !== "function") throw new Error("no headerTitle");
			return render(<>{options.headerTitle({ children: "Session" })}</>);
		}

		/** Pans the title the screen set last, as far and as fast as given. */
		function panTitle(translationX: number, velocityX = 0, success = true) {
			const title = headerTitle();
			const pan = title.root.findByType("GestureDetector" as never).props.gesture as PanGestureMock;
			act(() => pan.handlers.onEnd?.({ translationX, velocityX }, success));
		}

		it("replaces this session with the next one on a pan to the left, sliding in as a push does, marked seen", async () => {
			const { hub } = await mount(thread("ref-title", "active"));
			panTitle(-80);
			await settle();
			expect(navigation.replace).toHaveBeenCalledWith("Conversation", {
				hubId: "hub-1",
				ref: "local:quiet",
				title: "Old spike",
				slideFrom: "right",
			});
			expect(navigation.push).not.toHaveBeenCalled();
			expect(
				hub.requests.filter((request) => request.method === "evener/session/seen/set").map((request) => request.params),
			).toEqual([{ sessions: [{ ref: "local:quiet", seenThrough: Date.parse(at(1)) }] }]);
		});

		it("replaces it with the previous one on a pan to the right, sliding in from the left", async () => {
			await mount(thread("ref-title", "active"));
			panTitle(30, 900);
			expect(navigation.replace).toHaveBeenCalledWith("Conversation", {
				hubId: "hub-1",
				ref: "local:ask",
				title: "Pick a name",
				slideFrom: "left",
			});
		});

		it("keeps Next's mark, so Next from the new session still replaces it (ruling 2)", async () => {
			await mount(thread("ref-title", "active"), { openedBy: "next" });
			panTitle(-80);
			expect(navigation.replace).toHaveBeenCalledWith("Conversation", {
				hubId: "hub-1",
				ref: "local:quiet",
				title: "Old spike",
				openedBy: "next",
				slideFrom: "right",
			});
		});

		it("stays put past either end of Live order, and on a pan too short or slow", async () => {
			await mount(thread("ref-title", "active"));
			panTitle(30, 100);
			fleet.live = [working];
			fleet.needsYou = [];
			const { tree } = await mount(thread("ref-title", "active"));
			expect(renderedText(tree)).not.toContain("Next");
			panTitle(-80);
			panTitle(80);
			expect(navigation.replace).not.toHaveBeenCalled();
		});

		it("offers VoiceOver the sessions on either side, and only those there are", async () => {
			const actions = () =>
				headerTitle()
					.root.find((node) => node.props.accessibilityRole === "button")
					.props.accessibilityActions.map((action: { label: string }) => action.label);
			await mount(thread("ref-title", "active"));
			expect(actions()).toEqual(["Previous session", "Next session"]);
			fleet.live = [failing, asking, working];
			await mount(thread("ref-title", "active"));
			expect(actions()).toEqual(["Previous session"]);
		});

		it("goes nowhere on a pan the system cancelled", async () => {
			await mount(thread("ref-title", "active"));
			panTitle(-200, -2000, false);
			expect(navigation.replace).not.toHaveBeenCalled();
		});
	});

	it("marks a turn that ends while you watch seen through the hub's own turn end", async () => {
		fleet.live = [failing, fleetSession("ref-watching", { state: "working", updated_at: at(1), turn_ended_at: at(1) })];
		const served = thread("ref-watching", "active");
		const { hub } = await mount(served);
		const seenMarks = () =>
			hub.requests.filter((request) => request.method === "evener/session/seen/set").map((request) => request.params);
		expect(seenMarks()).toEqual([]);
		// The turn ends: the daemon says so, and the hub stamps the turn end
		// on the session's Live row and says Live changed.
		fleet.live = [
			failing,
			fleetSession("ref-watching", { state: "idle", updated_at: at(7), turn_ended_at: at(7), unseen: true }),
		];
		fleet.revision = 2;
		act(() => {
			hub.notify({
				method: "thread/status/changed",
				params: { threadId: served.id, ref: "ref-watching", status: { type: "idle" } },
			} as AnyNotification);
			hub.notify({
				method: "evener/navigation/invalidated",
				params: {
					generationId: "generation-test",
					sequence: 1,
					targets: [{ kind: "section", section: "live", revision: 2 }],
				},
			} as AnyNotification);
		});
		await settle();
		expect(seenMarks()).toEqual([{ sessions: [{ ref: "ref-watching", seenThrough: Date.parse(at(7)) }] }]);
	});

	it("names the session's host from the manifest in the Session sheet", async () => {
		fleet.sources = [{ id: "local", label: "Laptop" }];
		await mount(thread("ref-host", "idle"));
		const sheet = render(
			<SessionInfoSheet
				route={
					{
						key: "session-info",
						name: "SessionInfoSheet",
						params: { hubId: "hub-1", ref: "ref-host" },
					} as unknown as ComponentProps<typeof SessionInfoSheet>["route"]
				}
				navigation={navigation as unknown as ComponentProps<typeof SessionInfoSheet>["navigation"]}
			/>,
		);
		expect(renderedText(sheet)).toContain("Laptop");
		expect(renderedText(sheet)).not.toContain("Work hub");
	});
});

describe("a subagent's own session (spec 9, rulings 10 and 30)", () => {
	const COORDINATOR = { ref: "local:coord", threadId: "thread-local:coord", title: "Get PR 2138 Test Clean" };
	const RUNNING_SINCE = new Date(Date.now() - 4 * 60_000).toISOString();

	function subagentTree(over: Record<string, unknown> = {}, revision = 1) {
		return {
			revision,
			root: {
				kind: "session",
				// ActivityList checks the root is the coordinator's thread.
				sessionId: COORDINATOR.threadId,
				ref: COORDINATOR.ref,
				label: COORDINATOR.title,
				aggregate: "working",
				counts: { active: 1, failed: 0, completed: 0, complete: true },
				branch: {},
				entries: [
					{
						kind: "delegate",
						delegate: {
							delegateId: "d-fix",
							childSessionId: "fix",
							childRef: "local:fix",
							type: "delegate",
							description: "Fix race in tree settle",
							branch: {},
							runStartedAt: RUNNING_SINCE,
							// A delegate's update lands only with a newer projection revision.
							projectionRevision: revision,
							...over,
						},
					},
				],
			},
		};
	}

	/** The coordinator's thread, as the phone reads it without following it.
	 * `id` models a coordinator that restarted under a new thread. */
	function coordinator(stopSubagent: boolean, id = COORDINATOR.threadId) {
		const served = thread(COORDINATOR.ref, "active");
		(served as unknown as { id: string }).id = id;
		(served as unknown as { evener: { capabilities: Record<string, boolean> } }).evener.capabilities = {
			...CAPABILITIES,
			...(stopSubagent ? { stopSubagent: true } : {}),
		};
		return served;
	}

	/** The subagent's own thread: a running one is the hub's read-only alias,
	 * every capability false (the Go encoder writes each key); a finished one
	 * reads as a past session. */
	function subagent(running: boolean) {
		const served = thread("local:fix", running ? "active" : "idle");
		const evener = (served as unknown as { evener: Record<string, unknown> }).evener;
		evener.capabilities = running
			? Object.fromEntries(Object.keys(CAPABILITIES).map((key) => [key, false]))
			: { ...CAPABILITIES, steer: false, interrupt: false };
		return served;
	}

	async function mountSubagent(
		served: Thread,
		{ stopSubagent = false, jobs = subagentTree(), coordinatorId = COORDINATOR.threadId } = {},
	) {
		coordinatorHub.tree = jobs;
		otherThreads.set(COORDINATOR.ref, coordinator(stopSubagent, coordinatorId));
		const hub = hubClient(served);
		harness.connection = {
			...screenConnection(hub.client, "ready"),
			profiles: [{ id: "hub-1", name: "Work hub", origin: "https://hub.test" }],
			error: null,
			disconnect: () => {},
		};
		const route = {
			key: "subagent-local:fix",
			name: "Subagent",
			params: { hubId: "hub-1", ref: "local:fix", title: "Fix race in tree settle", coordinator: COORDINATOR },
		};
		navigationState.state = {
			index: 2,
			routes: [
				{ key: "board", name: "Sessions" },
				{
					key: "coord",
					name: "Conversation",
					params: { hubId: "hub-1", ref: COORDINATOR.ref, title: COORDINATOR.title },
				},
				route,
			] as never,
		};
		const tree = render(<SubagentScreen route={route as never} navigation={navigation as never} />);
		mountedScreens.push(tree);
		await settle();
		return { tree, hub };
	}

	const jobReads = (hub: ReturnType<typeof hubClient>) =>
		hub.requests.filter((request) => request.method === "evener/jobs/list").length;

	beforeEach(() => {
		vi.mocked(navigation.navigate).mockClear();
		vi.mocked(navigation.pop).mockClear();
		vi.mocked(navigation.push).mockClear();
		// The per-hub stop requests are one instance for the app's life.
		forgetStopRequestsForHub("hub-1");
	});

	it("times a running subagent's state line by its run, as its row does", async () => {
		// Its own turn started two minutes ago (a steer, say), but it has run
		// four: one number per subagent everywhere (ruling on the device pass).
		vi.mocked(navigation.setOptions).mockClear();
		const served = subagent(true);
		(served as unknown as { evener: Record<string, unknown> }).evener.activeTurnStartedAt = Date.now() - 2 * 60_000;
		await mountSubagent(served);
		const calls = vi.mocked(navigation.setOptions).mock.calls as [NativeStackNavigationOptions][];
		const options = calls.map(([options]) => options).findLast((options) => options.headerTitle);
		if (typeof options?.headerTitle !== "function") throw new Error("no headerTitle");
		const title = render(<>{options.headerTitle({ children: "Fix race in tree settle" })}</>);
		expect(renderedText(title)).toContain("Working · 4m");
		expect(renderedText(title)).not.toContain("Working · 2m");
		// Session info times it the same way.
		const runMs = sessionInfoHosts.get(sheetKey("hub-1", "local:fix"))?.runMs(Date.now()) ?? null;
		expect(runMs === null ? "" : compactDuration(runMs)).toBe("4m");
	});

	it("times a still-working subagent by its turn once its row has ended", async () => {
		// The row reads done a moment before the session itself stops: the row's
		// time since it ended is no Working time.
		vi.mocked(navigation.setOptions).mockClear();
		const served = subagent(true);
		(served as unknown as { evener: Record<string, unknown> }).evener.activeTurnStartedAt = Date.now() - 2 * 60_000;
		await mountSubagent(served, {
			jobs: subagentTree({ terminal: true, outcome: "completed", runEndedAt: new Date().toISOString() }),
		});
		const calls = vi.mocked(navigation.setOptions).mock.calls as [NativeStackNavigationOptions][];
		const options = calls.map(([options]) => options).findLast((options) => options.headerTitle);
		if (typeof options?.headerTitle !== "function") throw new Error("no headerTitle");
		const title = render(<>{options.headerTitle({ children: "Fix race in tree settle" })}</>);
		expect(renderedText(title)).toContain("Working · 2m");
	});

	it("takes back the row it reported when its panel goes away", async () => {
		// A screen that stops showing the panel shows no subagent's time.
		await mountSubagent(subagent(true));
		const reported: (string | null)[] = [];
		const panel = render(
			<SubagentPanel
				hubId="hub-1"
				ref="local:fix"
				coordinator={COORDINATOR}
				inFront
				barShown={false}
				showToast={() => {}}
				onRow={(row) => void reported.push(row?.ref ?? null)}
				navigation={navigation as never}
			/>,
		);
		await settle();
		expect(reported.at(-1)).toBe("local:fix");
		act(() => panel.unmount());
		expect(reported.at(-1)).toBeNull();
	});

	it("holds Ask coordinator to stop it where a running subagent's composer would be", async () => {
		const { tree } = await mountSubagent(subagent(true));
		expect(field(tree)).toBeUndefined();
		expect(pressable(tree, "Ask coordinator to stop it")).toBeDefined();
		expect(pressable(tree, "Open coordinator")).toBeDefined();
		expect(pressable(tree, "Stop")).toBeUndefined();
		// The bar says what you can do; the footer adds nothing.
		expect(renderedText(tree)).not.toContain("Sending is unavailable");
		for (const words of ["Retry", "Refresh", "Reconnect", "From the coordinator", "Talk to it through its coordinator"])
			expect(renderedText(tree)).not.toContain(words);
		act(() => pressable(tree, "Ask coordinator to stop it")?.props.onPress());
		expect(navigation.navigate).toHaveBeenCalledWith("StopSubagentSheet", {
			hubId: "hub-1",
			coordinator: COORDINATOR,
			ref: "local:fix",
		});
	});

	it("says Stop requested while a request you sent is pending", async () => {
		const { tree } = await mountSubagent(subagent(true));
		const [row] = flattenSubagents(subagentTree() as never);
		if (!row) throw new Error("no row");
		act(() => stopRequests("hub-1").request(COORDINATOR.ref, row, Date.now()));
		await settle();
		expect(renderedText(tree)).toContain("Stop requested");
		expect(pressable(tree, "Ask coordinator to stop it")).toBeUndefined();
		expect(pressable(tree, "Open coordinator")).toBeDefined();
	});

	it("offers Stop subagent itself when the coordinator can stop one directly (S6), after you confirm", async () => {
		const { tree, hub } = await mountSubagent(subagent(true), { stopSubagent: true });
		expect(pressable(tree, "Ask coordinator to stop it")).toBeUndefined();
		act(() => pressable(tree, "Stop subagent")?.props.onPress());
		const confirm = alertRequests.at(-1);
		expect(confirm?.title).toBe("Stop “Fix race in tree settle”?");
		expect(hub.requests.filter((request) => request.method === "evener/delegate/stop")).toEqual([]);
		await act(async () => confirm?.buttons?.find((button) => button.text === "Stop")?.onPress?.());
		await settle();
		expect(
			hub.requests.filter((request) => request.method === "evener/delegate/stop").map((request) => request.params),
		).toEqual([{ ref: COORDINATOR.ref, threadId: COORDINATOR.threadId, delegateId: "d-fix" }]);
		expect(renderedText(tree)).toContain("Stop requested");
		expect(stopRequests("hub-1").direct({ id: "d-fix" } as never)).toBe(true);
	});

	it("stops through the coordinator's thread as it reads now, after a restart gave it a new one", async () => {
		const { tree, hub } = await mountSubagent(subagent(true), { stopSubagent: true });
		// The coordinator restarts under a new thread while this screen is open.
		const restarted = coordinator(true);
		(restarted as unknown as { id: string }).id = "thread-restarted";
		otherThreads.set(COORDINATOR.ref, restarted);
		act(() => pressable(tree, "Stop subagent")?.props.onPress());
		await act(async () =>
			alertRequests
				.at(-1)
				?.buttons?.find((button) => button.text === "Stop")
				?.onPress?.(),
		);
		await settle();
		expect(
			hub.requests.filter((request) => request.method === "evener/delegate/stop").map((request) => request.params),
		).toEqual([{ ref: COORDINATOR.ref, threadId: "thread-restarted", delegateId: "d-fix" }]);
	});

	it("reads the tree under the coordinator's thread as it reads now, after a restart gave it a new one", async () => {
		// The screen opened with the route's thread, but the coordinator has
		// since restarted under a new one: its tree comes back under that new
		// thread, so the panel must ask for it by the thread it reads now.
		forgetSubagentTrees("hub-1");
		const restarted = { ...subagentTree(), root: { ...subagentTree().root, sessionId: "thread-restarted" } };
		const { tree } = await mountSubagent(subagent(true), { jobs: restarted, coordinatorId: "thread-restarted" });
		expect(pressable(tree, "Ask coordinator to stop it")).toBeDefined();
	});

	it("falls back to asking the coordinator when the hub doesn't know the direct stop", async () => {
		coordinatorHub.stop = () => {
			throw new WireError("method not found", -32601);
		};
		const { tree } = await mountSubagent(subagent(true), { stopSubagent: true });
		act(() => pressable(tree, "Stop subagent")?.props.onPress());
		await act(async () =>
			alertRequests
				.at(-1)
				?.buttons?.find((button) => button.text === "Stop")
				?.onPress?.(),
		);
		await settle();
		expect(pressable(tree, "Ask coordinator to stop it")).toBeDefined();
	});

	it("reads the tree again when the direct stop names a subagent the hub no longer has", async () => {
		coordinatorHub.stop = () => {
			throw new WireError("delegate not found", -32603, { evenerErrorInfo: "resourceNotFound" });
		};
		const { tree, hub } = await mountSubagent(subagent(true), { stopSubagent: true });
		const before = jobReads(hub);
		act(() => pressable(tree, "Stop subagent")?.props.onPress());
		await act(async () =>
			alertRequests
				.at(-1)
				?.buttons?.find((button) => button.text === "Stop")
				?.onPress?.(),
		);
		await settle();
		expect(jobReads(hub)).toBeGreaterThan(before);
		expect(renderedText(tree)).not.toContain("Stop requested");
	});

	it("reads the tree again when the direct stop finds it already finishing", async () => {
		coordinatorHub.stop = () => ({ outcome: "notRunning" });
		const { tree, hub } = await mountSubagent(subagent(true), { stopSubagent: true });
		const before = jobReads(hub);
		act(() => pressable(tree, "Stop subagent")?.props.onPress());
		await act(async () =>
			alertRequests
				.at(-1)
				?.buttons?.find((button) => button.text === "Stop")
				?.onPress?.(),
		);
		await settle();
		expect(jobReads(hub)).toBeGreaterThan(before);
		expect(renderedText(tree)).not.toContain("Stop requested");
	});

	it("offers no direct stop from a connection that has gone", async () => {
		const { tree } = await mountSubagent(subagent(true), { stopSubagent: true });
		expect(pressable(tree, "Stop subagent")).toBeDefined();
		harness.connection = { ...harness.connection, state: "reconnecting" };
		const route = {
			key: "subagent-local:fix",
			name: "Subagent",
			params: { hubId: "hub-1", ref: "local:fix", title: "Fix race in tree settle", coordinator: COORDINATOR },
		};
		act(() => tree.update(<SubagentScreen route={route as never} navigation={navigation as never} />));
		await settle();
		expect(pressable(tree, "Stop subagent")).toBeUndefined();
	});

	it("tries the direct stop again on a new connection after one hub didn't know it", async () => {
		coordinatorHub.stop = () => {
			throw new WireError("method not found", -32601);
		};
		const { tree } = await mountSubagent(subagent(true), { stopSubagent: true });
		act(() => pressable(tree, "Stop subagent")?.props.onPress());
		await act(async () =>
			alertRequests
				.at(-1)
				?.buttons?.find((button) => button.text === "Stop")
				?.onPress?.(),
		);
		await settle();
		expect(pressable(tree, "Stop subagent")).toBeUndefined();
		const route = {
			key: "subagent-local:fix",
			name: "Subagent",
			params: { hubId: "hub-1", ref: "local:fix", title: "Fix race in tree settle", coordinator: COORDINATOR },
		};
		const upgraded = hubClient(subagent(true));
		harness.connection = { ...harness.connection, client: upgraded.client };
		act(() => tree.update(<SubagentScreen route={route as never} navigation={navigation as never} />));
		await settle();
		expect(pressable(tree, "Stop subagent")).toBeDefined();
	});

	it("asks the coordinator when its thread can't be read to learn whether a direct stop works", async () => {
		coordinatorHub.readFails = COORDINATOR.ref;
		const { tree } = await mountSubagent(subagent(true), { stopSubagent: true });
		expect(pressable(tree, "Ask coordinator to stop it")).toBeDefined();
		expect(pressable(tree, "Stop subagent")).toBeUndefined();
	});

	it("gives a finished subagent the composer back, which sends to it", async () => {
		const { tree, hub } = await mountSubagent(subagent(false), {
			jobs: subagentTree({ terminal: true, outcome: "completed", runEndedAt: new Date().toISOString() }),
		});
		expect(pressable(tree, "Open coordinator")).toBeUndefined();
		await type(tree, "Try the other lock order");
		await press(tree, "Send");
		const sent = hub.requests.find((request) => request.method === "turn/start");
		expect(sent?.params).toMatchObject({
			ref: "local:fix",
			input: [{ type: "text", text: "Try the other lock order" }],
		});
	});

	it("goes back to the coordinator from Open coordinator", async () => {
		const { tree } = await mountSubagent(subagent(true));
		act(() => pressable(tree, "Open coordinator")?.props.onPress());
		expect(navigation.pop).toHaveBeenCalledWith(1);
	});

	it("reads the coordinator's tree again when this subagent's status changes", async () => {
		const { hub } = await mountSubagent(subagent(true));
		const before = jobReads(hub);
		act(() =>
			hub.notify({
				method: "thread/status/changed",
				params: { threadId: "thread-local:fix", ref: "local:fix", status: { type: "idle" } },
			} as AnyNotification),
		);
		await settle();
		expect(jobReads(hub)).toBeGreaterThan(before);
	});

	it("opens its coordinator's Subagents list from its own Subagents chip", async () => {
		const served = subagent(true);
		(served as unknown as { evener: Record<string, unknown> }).evener.diagnostics = {
			delegates: [
				{
					delegateId: "d-child",
					ownerSessionId: "fix",
					rootSessionId: "coord",
					childSessionId: "child",
					transcriptRef: "local:child",
					type: "subagent",
					lifecycle: "running",
					phase: "running",
					status: "running",
					resumable: false,
					needsAttention: false,
					projectionRevision: 1,
				},
			],
		};
		const { tree } = await mountSubagent(served);
		act(() => pressable(tree, "Subagents, 1")?.props.onPress());
		expect(navigation.navigate).toHaveBeenCalledWith("Subagents", { hubId: "hub-1", ...COORDINATOR });
	});

	it("opens a document it names in the Reader on its own ref, where its review goes (ruling 16)", async () => {
		vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("# Fix the settle race\n"));
		const served = subagent(false);
		(served as unknown as { cwd: string }).cwd = "/home/jesse/git/evener";
		(served as unknown as { turns: unknown[] }).turns = [
			{
				id: "t1",
				status: "completed",
				itemsView: "default",
				items: [
					{
						id: "said-1",
						turnId: "t1",
						type: "agentMessage",
						status: "completed",
						text: "The plan is in `docs/superpowers/plans/settle-race.md`.",
					},
				],
			},
		];
		const { tree } = await mountSubagent(served, {
			jobs: subagentTree({ terminal: true, outcome: "completed", runEndedAt: new Date().toISOString() }),
		});
		const chip = tree.root.findAll(
			(node) => String(node.type) === "Pressable" && String(node.props.accessibilityLabel).startsWith("Plan, "),
		)[0];
		if (!chip) throw new Error("no document chip");
		act(() => chip.props.onPress());
		expect(navigation.navigate).toHaveBeenCalledWith("Reader", {
			hubId: "hub-1",
			sessionRef: "local:fix",
			path: "docs/superpowers/plans/settle-race.md",
			sessionTitle: "Fix race in tree settle",
		});
	});

	it("opens a subagent row in a coordinator's transcript as that subagent's own session, under this one", async () => {
		const served = thread(COORDINATOR.ref, "active");
		(served as unknown as { turns: unknown[] }).turns = [
			{
				id: "t1",
				status: "inProgress",
				itemsView: "default",
				items: [
					{
						id: "call-d",
						turnId: "t1",
						type: "commandExecution",
						toolName: "delegate",
						status: "inProgress",
						argumentsJson: JSON.stringify({ description: "Fix race in tree settle" }),
					},
				],
			},
		];
		(served as unknown as { evener: Record<string, unknown> }).evener.diagnostics = {
			delegates: [
				{
					delegateId: "d-fix",
					ownerSessionId: "coord",
					rootSessionId: "coord",
					childSessionId: "fix",
					transcriptRef: "local:fix",
					originItemId: "call-d",
					description: "Fix race in tree settle",
					type: "subagent",
					lifecycle: "running",
					phase: "running",
					status: "running",
					resumable: false,
					needsAttention: false,
					projectionRevision: 1,
				},
			],
		};
		const { tree } = await mount(served);
		const row = tree.root.findAll(
			(node) =>
				String(node.type) === "Pressable" &&
				String(node.props.accessibilityLabel).startsWith("Fix race in tree settle, "),
		)[0];
		if (!row) throw new Error("no subagent row");
		act(() => row.props.onPress());
		expect(navigation.push).toHaveBeenCalledWith("Subagent", {
			hubId: "hub-1",
			ref: "local:fix",
			title: "Fix race in tree settle",
			coordinator: { ref: COORDINATOR.ref, threadId: COORDINATOR.threadId, title: "Session" },
		});
	});

	it("shows the toast once when the subagent stops at your request", async () => {
		const { tree, hub } = await mountSubagent(subagent(true));
		const [row] = flattenSubagents(subagentTree() as never);
		if (!row) throw new Error("no row");
		act(() => stopRequests("hub-1").request(COORDINATOR.ref, row, Date.now()));
		coordinatorHub.tree = subagentTree(
			{ terminal: true, outcome: "cancelled", runEndedAt: new Date().toISOString() },
			2,
		);
		act(() =>
			hub.notify({
				method: "thread/status/changed",
				params: { threadId: "thread-local:fix", ref: "local:fix", status: { type: "idle" } },
			} as AnyNotification),
		);
		await settle();
		const toasts = () => tree.root.findAllByType(Toast).map((toast) => toast.props.toast?.text);
		expect(toasts()).toContain("“Fix race in tree settle” stopped");
	});
});

describe("Send while offline (phase 6, spec 8.5)", () => {
	type Route = ConversationScreenProps["route"];
	/** The connection drops, or comes back with the same client. */
	async function reach(tree: ReactTestRenderer, route: Route, live: boolean) {
		harness.connection = live
			? { ...harness.connection, state: "ready", downSince: null }
			: { ...harness.connection, ...droppedConnection(harness.connection) };
		act(() => tree.update(<ConversationScreen route={route} navigation={navigation} />));
		await settle();
	}
	const outbox = (ref: string) => getNativeMutationRuntime().storage.listOutbox(nativeMutationTargetKey("hub-1", ref));

	it("keeps a message sent offline, and sends it exactly once when the connection returns", async () => {
		const ref = "ref-train";
		const { tree, hub, route } = await mount(thread(ref, "idle"));
		await reach(tree, route, false);
		await type(tree, "sent on the train");
		playedHaptics.length = 0;
		await press(tree, "Send when you're back online");
		expect(field(tree)?.props.value).toBe("");
		expect((await outbox(ref)).map((record) => [record.method, record.state])).toEqual([["turn/queue", "submitting"]]);
		expect(renderedText(tree)).toContain("Will send when you're back online");
		expect(playedHaptics).toEqual(["impact:light"]);
		expect(hub.mutations()).toEqual([]);

		await reach(tree, route, true);
		await vi.waitFor(() => expect(hub.mutations()).toEqual(["turn/queue"]));
		const sent = hub.requests.find((request) => request.method === "turn/queue");
		expect(sent?.params.input).toEqual([{ type: "text", text: "sent on the train" }]);
		await settle();
		expect(hub.mutations()).toEqual(["turn/queue"]);
	});

	it("keeps Send off, and the draft, for a session this phone hasn't read since launch", async () => {
		const read = await mount(thread("ref-read", "idle"));
		await reach(read.tree, read.route, false);
		const ref = "ref-never-read";
		const route = {
			key: `conversation-${ref}`,
			name: "Conversation",
			params: { hubId: "hub-1", ref, title: "Session" },
		} as unknown as Route;
		navigationState.state = { index: 0, routes: [route] };
		const tree = render(<ConversationScreen route={route} navigation={navigation} />);
		mountedScreens.push(tree);
		await settle();
		await type(tree, "not yet");
		expect(pressable(tree, "Send when you're back online")?.props.accessibilityState).toMatchObject({
			disabled: true,
		});
		expect(field(tree)?.props.value).toBe("not yet");
		expect(await outbox(ref)).toEqual([]);
	});

	it("answers a question offline, and sends the answer once when the connection returns", async () => {
		const ref = "ref-question-offline";
		const { tree, hub, route } = await mount(thread(ref, "awaiting", true));
		await reach(tree, route, false);
		await press(tree, "Drop them");
		await press(tree, "Send answer when you're back online");
		const records = await outbox(ref);
		expect(records).toHaveLength(1);
		expect((records[0]?.payload as { input: unknown }).input).toEqual([
			{ type: "text", text: '[answers]\n1. [Choice] \u2192 "Drop them"' },
		]);
		// The batch is answered: the dock gives way.
		expect(pressable(tree, "Send answer when you're back online")).toBeUndefined();

		await reach(tree, route, true);
		await vi.waitFor(() => expect(hub.mutations()).toHaveLength(1));
		await settle();
		// Queued, it runs as your next turn: the daemon runs queued input
		// ahead of everything else while awaiting, superseding the question
		// as an online answer's turn/start does.
		expect(hub.mutations()).toEqual(["turn/queue"]);
		expect(hub.requests.find((request) => request.method === "turn/queue")?.params.input).toEqual([
			{ type: "text", text: '[answers]\n1. [Choice] \u2192 "Drop them"' },
		]);
	});

	it("offers no Send offline where the session can't take it when it arrives", async () => {
		const ref = "ref-cannot-queue";
		const served = thread(ref, "awaiting", true);
		const evener = (served as unknown as { evener: { capabilities: Record<string, unknown> } }).evener;
		evener.capabilities = { ...evener.capabilities, queue: false };
		const { tree, route } = await mount(served);
		await reach(tree, route, false);
		// Nothing there can act, so nothing offers to (Calm).
		for (const label of ["Drop them", "Send answer when you're back online"])
			expect(pressable(tree, label)?.props.accessibilityState).toMatchObject({ disabled: true });
		expect(await outbox(ref)).toEqual([]);
	});

	it("shows a held message refused once a restarted session answers it, with Discard", async () => {
		const ref = "ref-restarted";
		const { tree, hub, route } = await mount(thread(ref, "idle"));
		await reach(tree, route, false);
		await type(tree, "for the old instance");
		await press(tree, "Send when you're back online");
		const request = hub.client.request;
		hub.client.request = async (method: string, params: Record<string, unknown>) => {
			if (method !== "turn/queue") return request(method, params);
			hub.requests.push({ method, params });
			// The daemon's answer to a stale fence (MutationNotAccepted).
			throw new WireError("thread instance is stale", -32013, {
				evenerErrorInfo: "conflict",
				clientMutationId: params.clientMutationId,
				mutationOutcome: "notAccepted",
				retryDisposition: "none",
			});
		};
		await reach(tree, route, true);
		await vi.waitFor(() => expect(renderedText(tree)).toContain("Couldn't send this · thread instance is stale"));
		expect(hub.mutations()).toEqual(["turn/queue"]);
		expect(pressable(tree, "Discard")).toBeDefined();
	});
});

it("clears the lost send's outbox row with one Discard on the draft ghost", async () => {
	const ref = "ref-draft-lost";
	const key = nativeMutationTargetKey("hub-1", ref);
	const runtime = getNativeMutationRuntime();
	// The draft kept a send the outbox also holds, and the outbox couldn't
	// confirm its outcome: ghosts() shows one ghost, the draft's, standing in for
	// the blockedUnknown row. Seed that exact pair before mounting.
	nativeDrafts().write({ hubId: "hub-1", sessionRef: ref }, { draft: "", unconfirmed: "lost send" });
	const { clientMutationId } = await runtime.storage.enqueueIntent({
		targetRef: key,
		method: "turn/start",
		payload: { ref, input: [{ type: "text", text: "lost send" }] },
		attachments: [],
		optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "lost send" }] },
	});
	await runtime.storage.markAttempted(clientMutationId);
	await runtime.storage.markUnknown(clientMutationId, "blockedUnknown", { onlyAttempted: true });

	const { tree } = await mount(thread(ref, "idle"));
	await vi.waitFor(() => expect(renderedText(tree)).toContain("Couldn't confirm this was sent"));
	await press(tree, "Discard");

	// One Discard clears both: the draft's uncertainty and the row it stood in
	// for, which otherwise returns as its own ghost.
	await vi.waitFor(async () => expect(await runtime.storage.listOutbox(key)).toEqual([]));
	await settle();
	expect(renderedText(tree)).not.toContain("Couldn't confirm this was sent");
});
