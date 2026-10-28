import { nativePlatform } from "./conversationPlatform.testkit";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { activityFixture } from "./subagents/sessionActivityTestUtils";
// The Session's one Send and the tray's Stop, on the real ConversationScreen:
// what a person presses, and which requests reach the hub through the durable
// runtime. Only native edges are mocked, as in
// ConversationScreen.recovery.test.tsx.
import type { ComponentProps } from "react";
import { act, type ReactTestInstance, type ReactTestRenderer } from "react-test-renderer";
import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { type AnyNotification, type SessionDelegatesResponse, type Thread } from "@evener/appwire-client";
import { hasTestID } from "./markerTestUtils";
import { nativeDrafts } from "./nativeDrafts";
import { getNativeMutationRuntime, nativeMutationTargetKey } from "./nativeMutationRuntime";
import {
	flatListCalls,
	keyboard,
	dockBody,
	playedHaptics,
	pressable,
	render,
	renderedText,
	screenConnection,
	settle,
	systemGlass,
	textOf,
	unmountMountedTrees,
	until,
} from "./renderNative.testkit";
import { ConversationScreen } from "./screens";
import type { NativeConversationSessions } from "./session/nativeConversationSessions";
import {
	NativeConversationSessionsProvider,
	useNativeConversationSessions,
} from "./session/NativeConversationSessionsProvider";
import { QuestionDock } from "./session/QuestionDock";
import { Platform } from "react-native";
import type {
	NativeStackHeaderItemMenu,
	NativeStackHeaderItemMenuAction,
	NativeStackNavigationOptions,
} from "@react-navigation/native-stack";
import { answerFleetRead, type FleetShape } from "./session/fleetTestUtils";
import { DisplayPreferences } from "./display/displayPreferences";
import { DisplayProvider } from "./display/displayContext";
import { actionErrors, ghostStates, press, saysSteerFailed, toasts } from "./conversationScreenTestUtils";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
}));

// The root stack the screen sits in, read by useScreenInFront and
// screenInFront (screens.tsx). Kept at the screen's own route on top, so the
// screen is in front the way a freshly opened conversation really is - this
// suite isn't exercising sheet coverage or a pushed screen, unlike
// ConversationScreen.sheets.test.tsx.
const navigationState = vi.hoisted(() => ({
	state: { index: 0, routes: [] as { key: string; name: string }[] },
}));

// Who alerted you most recently, and Next's word that it moved you on.
const alerts = vi.hoisted(() => ({ recent: [] as string[], nextUsed: vi.fn() }));

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

vi.mock("./ConnectionProvider", () => ({
	useConnection: () => harness.connection,
}));

vi.mock("./alerts/alertsContext", async (importOriginal) => ({
	...(await importOriginal<typeof import("./alerts/alertsContext")>()),
	useAlertedRecently: () => alerts.recent,
	useNextUsed: () => alerts.nextUsed,
}));

// The hub's transcript display setting, when a test sets one.
const displayPrefs = vi.hoisted(() => ({ hubId: null as string | null, config: null as unknown }));

vi.mock("./NativePreferencesProvider", () => ({
	useNativePreferences: () => ({
		hubId: displayPrefs.hubId,
		model: null,
		snapshot: null,
		config: displayPrefs.config,
		connected: false,
		offlineDraftUnreadable: false,
		offlineStorageUnavailable: false,
		discardUnreadableKeybindingsDraft: () => null,
	}),
}));

type ConversationScreenProps = ComponentProps<typeof ConversationScreen>;

/** Every screen mounts under the one real sessions provider, as the app's
 * navigator does; the registry is constructed by the provider itself. */
function SessionScreen(props: ConversationScreenProps) {
	return (
		<NativeConversationSessionsProvider>
			<ConversationScreen {...props} />
		</NativeConversationSessionsProvider>
	);
}

/** The provider's registry, for a test that drives two views of one session. */
function RegistryProbe({ seen }: { seen: { current: NativeConversationSessions | null } }) {
	seen.current = useNativeConversationSessions();
	return null;
}

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

// A coordinator's subagent tree (evener/jobs/list) and its direct stop
// (evener/delegate/stop), for the subagent screen's tests.
const coordinatorHub: {
	tree: unknown;
	delegates: SessionDelegatesResponse | null;
	stop: (params: Record<string, unknown>) => unknown;
	readFails: string | null;
} = {
	tree: null,
	delegates: null,
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

// Keep diagnostics visible and check the entire case, including afterEach teardown.
beforeEach(({ onTestFinished }) => {
	const errors = vi.spyOn(console, "error");
	const warnings = vi.spyOn(console, "warn");
	onTestFinished(() => {
		try {
			expect({ errors: errors.mock.calls, warnings: warnings.mock.calls }).toEqual({ errors: [], warnings: [] });
		} finally {
			errors.mockRestore();
			warnings.mockRestore();
		}
	});
});

afterEach(async () => {
	unmountMountedTrees();
	// The runtime is memoized for the whole file and arms a 2000ms MutationOutbox
	// discovery interval. Left running, a later case's mount re-registers a
	// target and the interval re-sends an earlier case's lost row through it
	// (#4901). Stop it with the trees that started it.
	await getNativeMutationRuntime().stop();
	displayPrefs.hubId = null;
	displayPrefs.config = null;
	catalogHub.fails = false;
	readHistory.live = false;
	keyboard.reset();
	systemGlass.reset();
	coordinatorHub.tree = null;
	coordinatorHub.delegates = null;
	coordinatorHub.stop = () => ({ outcome: "stopping" });
	coordinatorHub.readFails = null;
	otherThreads.clear();
	fleet.live = [];
	fleet.needsYou = [];
	fleet.sources = undefined;
	fleet.revision = undefined;
	vi.unstubAllGlobals();
});

afterAll(() => {
	for (const database of nativePlatform.sqliteDatabases) database.close();
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
	const client = Object.assign(new FakeClient("ready") as Pick<FakeClient, "state" | "onReady" | "onStateChange">, {
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
			if (method === "evener/thread/delegates/list" && coordinatorHub.delegates) return coordinatorHub.delegates;
			if (method.startsWith("evener/thread/") && coordinatorHub.tree) {
				const f = activityFixture(coordinatorHub.tree, {
					ref: String(params.ref),
					scope: params.scope as "session" | "subtree",
				});
				if (method === "evener/thread/activity/read") return f.summary;
				const page = {
					complete: !f.continuation,
					issues: f.issues,
					...(f.continuation ? { nextCursor: f.continuation } : {}),
				};
				if (method === "evener/thread/delegates/list")
					return { context: f.context, scope: f.scope, delegates: f.delegates, page };
				if (method === "evener/thread/jobs/list") return { context: f.context, scope: f.scope, jobs: f.jobs, page };
			}
			if (method === "evener/delegate/stop") return coordinatorHub.stop(params);
			if (method === "evener/session/seen/set")
				return { ok: true, changed: true, navigation: { generation_id: "generation-test", targets: [] } };
			return answerFleetRead(fleet, method, params) ?? {};
		},
	});
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
		// The display choices to mount under; absent reads the defaults, as a
		// tree without a provider does.
		display = undefined as DisplayPreferences | undefined,
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
	// Await mount-time accessibility and navigation reads, not the session
	// read's clock, before handing the live renderer back to the case.
	let tree!: ReactTestRenderer;
	await act(async () => {
		const screen = <SessionScreen route={route} navigation={navigation} />;
		tree = render(display ? <DisplayProvider value={display}>{screen}</DisplayProvider> : screen);
	});
	if (barLaysOut) {
		const bar = tree.root.findAll(
			(node) => String(node.type) === "View" && node.props.testID === "session-bottom-bar",
		)[0];
		if (bar) act(() => bar.props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 0 } } }));
	}
	if (settled) await settle();
	return { tree, hub, route };
}

/** One view under the real sessions provider, with the shared entry in hand so
 * a test can inspect the store the screen itself reads. */
async function mountTracked(served: Thread) {
	const hub = hubClient(served);
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
		params: { hubId: "hub-1", ref, title: "Session" },
	} as unknown as ConversationScreenProps["route"];
	navigationState.state = { index: 0, routes: [route] };
	const seen = { current: null as NativeConversationSessions | null };
	let tree!: ReactTestRenderer;
	await act(async () => {
		tree = render(
			<NativeConversationSessionsProvider>
				<RegistryProbe seen={seen} />
				<ConversationScreen route={route} navigation={navigation} />
			</NativeConversationSessionsProvider>,
		);
	});
	await settle();
	return { tree, hub, route, seen, entry: seen.current!.entryFor("hub-1", ref)! };
}

function field(tree: ReactTestRenderer) {
	return tree.root
		.findAll((node) => String(node.type) === "TextInput")
		.find((node) => node.props.accessibilityLabel === "Message");
}

/** Puts the composer's field in focus, which raises the keyboard for it. */
function typeInComposer(tree: ReactTestRenderer) {
	act(() => field(tree)?.props.onFocus());
	act(() => keyboard.show());
}

async function type(tree: ReactTestRenderer, text: string) {
	const input = field(tree);
	if (!input) throw new Error("no Message field");
	act(() => input.props.onChangeText(text));
	await settle();
}

it("queues a message while the agent works", async () => {
	const { tree, hub } = await mount(thread("ref-queue", "active"));
	await type(tree, "second");
	await press(tree, "Queue message");
	expect(hub.mutations()).toEqual(["turn/queue"]);
	expect(pressable(tree, "Steer")).toBeUndefined();
});

it("sends a message when the agent is at rest", async () => {
	const { tree, hub } = await mount(thread("ref-send", "idle"));
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

it("stops the running turn from the composer's slot and says so", async () => {
	const { tree, hub } = await mount(thread("ref-stop", "active"));
	await press(tree, "Stop");
	expect(hub.mutations()).toEqual(["turn/interrupt"]);
	expect(toasts(tree)).toEqual(["stop:stopped"]);
});

it("shows Stop in the composer's slot only while the turn runs with an empty field", async () => {
	// Running with an empty field: Stop, no Send.
	const running = await mount(thread("ref-slot", "active"));
	expect(pressable(running.tree, "Stop")).toBeDefined();
	// A draft takes the slot back for the queueing Send.
	await type(running.tree, "half a thought");
	expect(pressable(running.tree, "Stop")).toBeUndefined();
	expect(pressable(running.tree, "Queue message")).toBeDefined();
});

it("stops the running turn from the session menu while a draft waits", async () => {
	const { tree, hub } = await mount(thread("ref-slot-menu", "active"));
	await type(tree, "half a thought");
	// The slot is Send while typing; the ⋯ menu is where Stop stays reachable.
	expect(pressable(tree, "Stop")).toBeUndefined();
	chooseMenu("Stop");
	await settle();
	expect(hub.mutations()).toEqual(["turn/interrupt"]);
	// The draft survives the stop: stopping must not cost the typed message.
	expect(field(tree)?.props.value).toBe("half a thought");
});

it("keeps Send in the slot when the session is not running", async () => {
	const { tree } = await mount(thread("ref-slot-idle", "idle"));
	expect(pressable(tree, "Stop")).toBeUndefined();
	expect(pressable(tree, "Send")).toBeDefined();
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
	afterEach(() => {
		unmountMountedTrees();
		vi.unstubAllGlobals();
	});

	it("shows the dock in the composer's place", async () => {
		const { tree } = await mount(thread("ref-question", "awaiting", true));
		const text = renderedText(tree);
		expect(hasTestID(tree, "question-dock")).toBe(true);
		expect(text).toContain("Keep or drop the implied options?");
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
		typeInComposer(tree);
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
		typeInComposer(tree);
		const showOptions = pressable(tree, "Show options");
		expect(showOptions).toBeDefined();
		act(() => showOptions?.props.onPress());
		expect(pressable(tree, "Send answer")).toBeDefined();
		expect(pressable(tree, "Show options")).toBeUndefined();
	});

	it("sends what you typed as the answer while the keyboard is up", async () => {
		const { tree, hub } = await mount(thread("ref-question-typed-send", "awaiting", true));
		await press(tree, "Other answer…");
		typeInComposer(tree);
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
		expect(toasts(tree)).toEqual(["answer:sent"]);
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
		expect(toasts(tree)).toEqual(["answer:sent"]);
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
		const drafts = nativePlatform.sqlitePorts.get("evener-drafts.db") as { runSync: (...args: unknown[]) => unknown };
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
		const drafts = nativePlatform.sqlitePorts.get("evener-drafts.db") as {
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
				<SessionScreen route={route as unknown as ConversationScreenProps["route"]} navigation={navigation} />,
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
		expect(pressable(tree, "Answer the question")).toBeDefined();
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

// A typed /steer or /queue is the composer's Steer or Queue: the outbox
// keeps it, so a lost answer isn't a failure, and what the hub took reads as
// this phone's.
it.each([
	["/steer check the logs", "Steer", "turn/steer", "steering"],
	["/queue check the logs", "Queue", "turn/queue", "queued"],
])("sends %s through the outbox", async (text, label, method, ghost) => {
	const ref = `ref-typed-${method}`;
	const served = thread(ref, "active");
	const { tree, hub } = await mount(served);
	const client = hub.client as { request: (method: string, params: Record<string, unknown>) => Promise<unknown> };
	const request = client.request;
	client.request = async (requested, params) => {
		const answer = await request(requested, params);
		if (requested !== method) return answer;
		// The hub took it, and its answer was lost.
		const evener = (served as unknown as { evener: Record<string, unknown> }).evener;
		if (method === "turn/queue")
			evener.queue = { ...queueState(["check the logs"], 1), clientMutationIds: [params.clientMutationId] };
		else
			evener.pendingMutations = [
				{
					clientMutationId: params.clientMutationId,
					method,
					input: params.input,
					executionState: "accepted",
					projectionState: "pending",
				},
			];
		throw new Error("connection lost");
	};
	await type(tree, text);
	await press(tree, label);
	const sent = hub.requests.filter((entry) => entry.method === method);
	expect(sent.map((entry) => entry.params.input)).toEqual([[{ type: "text", text: "check the logs" }]]);
	act(() =>
		hub.notify({
			method: "evener/thread/resync",
			params: { ref, threadId: served.id },
		} as AnyNotification),
	);
	await settle();
	expect(field(tree)?.props.value).toBe("");
	expect(actionErrors(tree)).toEqual([]);
	expect(saysSteerFailed(tree)).toBe(false);
	expect(renderedText(tree)).toContain("check the logs");
	expect(ghostStates(tree)).toContain(ghost);
});

// A typed command the outbox couldn't take never left the phone: it says try
// again, and the text is still there to send.
it("asks to try a typed /queue again when the phone couldn't keep it", async () => {
	const { tree, hub } = await mount(thread("ref-typed-refused", "active"));
	vi.spyOn(getNativeMutationRuntime(), "submit").mockRejectedValueOnce(new Error("disk full"));
	await type(tree, "/queue check the logs");
	await press(tree, "Queue");
	expect(hub.mutations()).toEqual([]);
	expect(actionErrors(tree)).toEqual(["not-sent"]);
	// Nothing left the phone, so the command goes back to the composer.
	expect(field(tree)?.props.value).toBe("/queue check the logs");
	expect(ghostStates(tree)).toEqual([]);
});

// A typed /interrupt the phone couldn't keep says so, and stays to try again.
it("asks to try a typed /interrupt again when the phone couldn't keep it", async () => {
	const { tree, hub } = await mount(thread("ref-typed-interrupt-refused", "active"));
	vi.spyOn(getNativeMutationRuntime(), "submit").mockRejectedValueOnce(new Error("disk full"));
	await type(tree, "/interrupt");
	await press(tree, "Interrupt");
	expect(hub.mutations()).toEqual([]);
	expect(actionErrors(tree)).toEqual(["not-sent"]);
	expect(toasts(tree)).toEqual([]);
	expect(field(tree)?.props.value).toBe("/interrupt");
});

// A typed /interrupt is Stop: it holds the messages this phone hasn't sent
// yet, so they don't go out after it.
it("holds the phone's unsent messages on /interrupt, as Stop does", async () => {
	const { tree, hub } = await mount(thread("ref-typed-interrupt", "active"));
	const client = hub.client as { request: (method: string, params: Record<string, unknown>) => Promise<unknown> };
	const request = client.request;
	let answerFirst: () => void = () => undefined;
	const firstAnswered = new Promise<void>((resolve) => {
		answerFirst = resolve;
	});
	client.request = async (requested, params) => {
		const answer = request(requested, params);
		if (requested === "turn/queue" && hub.requests.filter((entry) => entry.method === "turn/queue").length === 1)
			await firstAnswered;
		return answer;
	};
	await type(tree, "first");
	await press(tree, "Queue message");
	await type(tree, "second");
	await press(tree, "Queue message");
	await type(tree, "/interrupt");
	await press(tree, "Interrupt");
	await act(async () => answerFirst());
	await settle();
	expect(hub.mutations()).toEqual(["turn/queue", "turn/interrupt"]);
	expect(ghostStates(tree)).toContain("held");
	expect(toasts(tree)).toEqual(["stop:stopped"]);
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
	await act(async () => tree.unmount());
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
	expect(toasts(tree)).toEqual([]);
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
	expect(ghostStates(tree)).toEqual(["held", "held"]);

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

/** Holds the production runtime's admission of the first press, so the store's
 * click-to-commit marker stays set while the test inspects the screen's guards.
 * The runtime underneath is the real one: release runs its real submit. */
function holdFirstAdmission() {
	const runtime = getNativeMutationRuntime();
	const submit = runtime.submit;
	let release!: () => void;
	const gate = new Promise<void>((resolve) => {
		release = resolve;
	});
	let first = true;
	runtime.submit = (request) => {
		if (!first) return submit.call(runtime, request);
		first = false;
		return gate.then(() => submit.call(runtime, request));
	};
	return {
		release: () => release(),
		restore: () => {
			runtime.submit = submit;
		},
	};
}

// Stage B: the store's click-to-commit marker is one any-mutation fact. Stop
// must refuse while a Send, a Queue, or Stop itself is still being admitted,
// and it must be allowed again as soon as that attempt settles.
describe("Stop refuses while any mutation is in its click-to-commit window", () => {
	it("holds Stop while a send is admitted, then lets it stop", async () => {
		const ref = "ref-stop-send-admission";
		const served = thread(ref, "idle");
		const { tree, hub } = await mount(served);
		const held = holdFirstAdmission();
		try {
			await type(tree, "first");
			act(() => pressable(tree, "Send")!.props.onPress());
			await settle();
			// A status frame reports the turn running before the send's admission
			// settles: Stop takes the slot, disabled by the any-mutation marker.
			act(() =>
				hub.notify({
					method: "thread/status/changed",
					params: { threadId: served.id, ref, status: { type: "active" } },
				} as AnyNotification),
			);
			await settle();
			const stop = pressable(tree, "Stop");
			expect(stop).toBeDefined();
			expect(stop?.props.accessibilityState).toEqual({ disabled: true });
			// A press that reaches the guard sends no interrupt.
			act(() => stop!.props.onPress());
			await settle();
			expect(hub.mutations()).toEqual([]);

			held.release();
			await until(() => expect(pressable(tree, "Stop")?.props.accessibilityState).toEqual({ disabled: false }));
			await press(tree, "Stop");
			await until(() => expect(hub.mutations()).toEqual(["turn/start", "turn/interrupt"]));
		} finally {
			held.release();
			await settle();
			held.restore();
		}
	});

	it("holds Stop while a queued message is admitted, then lets it stop", async () => {
		const ref = "ref-stop-queue-admission";
		const { tree, hub } = await mount(thread(ref, "active"));
		const held = holdFirstAdmission();
		try {
			await type(tree, "later");
			act(() => pressable(tree, "Queue message")!.props.onPress());
			await settle();
			const stop = pressable(tree, "Stop");
			expect(stop).toBeDefined();
			expect(stop?.props.accessibilityState).toEqual({ disabled: true });
			act(() => stop!.props.onPress());
			await settle();
			expect(hub.mutations()).toEqual([]);

			held.release();
			await until(() => expect(pressable(tree, "Stop")?.props.accessibilityState).toEqual({ disabled: false }));
			await press(tree, "Stop");
			await until(() => expect(hub.mutations()).toEqual(["turn/queue", "turn/interrupt"]));
		} finally {
			held.release();
			await settle();
			held.restore();
		}
	});

	it("holds a repeated Stop while the first interrupt is admitted, then lets it stop again", async () => {
		const ref = "ref-stop-interrupt-admission";
		const { tree, hub } = await mount(thread(ref, "active"));
		const held = holdFirstAdmission();
		try {
			act(() => pressable(tree, "Stop")!.props.onPress());
			await settle();
			const stop = pressable(tree, "Stop");
			expect(stop).toBeDefined();
			expect(stop?.props.accessibilityState).toEqual({ disabled: true });
			act(() => stop!.props.onPress());
			await settle();
			expect(hub.mutations()).toEqual([]);

			held.release();
			await until(() => expect(pressable(tree, "Stop")?.props.accessibilityState).toEqual({ disabled: false }));
			await press(tree, "Stop");
			await until(() => expect(hub.mutations()).toEqual(["turn/interrupt", "turn/interrupt"]));
		} finally {
			held.release();
			await settle();
			held.restore();
		}
	});
});

it("refuses answers while a mutation owns the click-to-commit marker", async () => {
	const ref = "ref-question-marker";
	const { tree, hub, entry } = await mountTracked(thread(ref, "awaiting", true));
	await press(tree, "Drop them");
	const sendAnswer = pressable(tree, "Send answer");
	expect(sendAnswer?.props.accessibilityState).toMatchObject({ disabled: false });
	// A send still in its click-to-commit window, with no legacy pending record:
	// the marker alone is what the answer path must read.
	act(() => entry.store.setState({ pendingSubmission: { kind: "send" } }));
	await settle();
	expect(pressable(tree, "Send answer")?.props.accessibilityState).toMatchObject({ disabled: true });
	act(() => sendAnswer!.props.onPress());
	await settle();
	expect(hub.mutations()).toEqual([]);
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
	nativePlatform.kv.set(
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
	// to chase every move, and the list ping-ponged. The anchor comes from the
	// open-at-unread-replies jump (turn_2's reply is the unread one), the one
	// opening that still lands through the restore machinery.
	seedReaderPosition("ref-restore-once", "a-turn_2", "turn_1");
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

// A frame's layout events arrive together: the rows' layouts, then the
// content size they add up to. The restore waits for all of them, so a row
// measured before the content size is known never lands the list short of it.
it("restores a reading position once the frame's layouts are all in", async () => {
	seedReaderPosition("ref-restore-frame", "a-turn_2", "turn_1");
	const { tree } = await mount(twoTurns("ref-restore-frame"));
	act(() => transcriptList(tree).props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 600 } } }));
	await settle();
	flatListCalls.length = 0;
	act(() => {
		layOutRow(tree, 3, 9_523);
		transcriptList(tree).props.onContentSizeChange(390, 20_000);
	});
	await settle();
	expect(flatListCalls.filter((call) => call.method === "scrollToOffset")).toEqual([
		{ method: "scrollToOffset", args: { offset: 9_523, animated: false } },
	]);
});

// The content size can come in a later batch than the rows: until it is
// known, where the list can reach isn't, so the restore waits for it.
it("waits for the content size before restoring a reading position", async () => {
	seedReaderPosition("ref-restore-size", "a-turn_2", "turn_1");
	const { tree } = await mount(twoTurns("ref-restore-size"));
	act(() => transcriptList(tree).props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 600 } } }));
	await settle();
	flatListCalls.length = 0;
	layOutRow(tree, 3, 9_523);
	await settle();
	expect(flatListCalls.filter((call) => call.method === "scrollToOffset")).toEqual([]);
	expect(transcriptList(tree).props.style?.opacity).toBe(0);
	act(() => transcriptList(tree).props.onContentSizeChange(390, 20_000));
	await settle();
	expect(flatListCalls.filter((call) => call.method === "scrollToOffset")).toEqual([
		{ method: "scrollToOffset", args: { offset: 9_523, animated: false } },
	]);
	expect(transcriptList(tree).props.style?.opacity).toBe(1);
});

it("waits for the bottom bar to lay out before restoring a reading position", async () => {
	seedReaderPosition("ref-restore-waits", "a-turn_2", "turn_1");
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

function transcriptList(tree: ReactTestRenderer) {
	return tree.root.findAll(
		(node) =>
			typeof node.type === "function" && Array.isArray(node.props.data) && typeof node.props.renderItem === "function",
	)[0];
}

// Saves a reading position on hub-1 for the session ref, at the top of the
// row keyed itemKey, with the turns through turnsSeen already seen.
function seedReaderPosition(sessionRef: string, itemKey: string, turnsSeen: string) {
	nativePlatform.kv.set(
		"evener.reader-positions",
		JSON.stringify({
			[`hub-1\u0000${sessionRef}`]: {
				hubId: "hub-1",
				sessionRef,
				itemKey,
				withinItemOffset: 0,
				touchedAt: 1,
				turnsSeen,
			},
		}),
	);
}

// Lays out the transcript's row at index at y. twoTurns' rows are ask/reply
// for turn_1, then turn_2.
function layOutRow(tree: ReactTestRenderer, index: number, y: number) {
	const cell = transcriptList(tree)
		.findAll((node) => String(node.type) === "Item")
		[index]?.findAll((node) => String(node.type) === "View" && node.props.onLayout)[0];
	if (!cell) throw new Error(`no cell at row ${index}`);
	act(() => cell.props.onLayout({ nativeEvent: { layout: { x: 0, y, width: 390, height: 150 } } }));
}

/** Chooses an item in the ⋯ menu the screen set last. */
function chooseMenu(label: string) {
	const calls = vi.mocked(navigation.setOptions).mock.calls as [NativeStackNavigationOptions][];
	const options = calls.map(([options]) => options).findLast((options) => options.unstable_headerRightItems);
	const [menu] = (options?.unstable_headerRightItems?.({ canGoBack: true }) ?? []) as NativeStackHeaderItemMenu[];
	const found = menu?.menu.items.find((item) => item.label === label) as NativeStackHeaderItemMenuAction | undefined;
	if (!found) throw new Error(`no ${label} in the header menu`);
	act(() => found.onPress());
}
