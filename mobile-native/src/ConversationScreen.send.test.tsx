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
import { hasTestID, markers } from "./markerTestUtils";
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
import { answerFleetRead, type FleetShape, fleetSession } from "./session/fleetTestUtils";
import { FloatingStack } from "./session/FloatingStack";
import { Toast } from "./Toast";
import { DisplayPreferences } from "./display/displayPreferences";
import { DisplayProvider } from "./display/displayContext";
import {
	actionErrors,
	advanceFakeTimers,
	ghostStates,
	press,
	saysSteerFailed,
	toasts,
} from "./conversationScreenTestUtils";

const memoryStorage = () => {
	const values = new Map<string, string>();
	return {
		getItemSync: (key: string) => values.get(key) ?? null,
		setItemSync: (key: string, value: string) => {
			values.set(key, value);
		},
	};
};

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

/** Two turns where the unread one's agent side opens with a run before its
 * reply: the jump target (the run, row 3) sits above the list's last row (the
 * reply, row 4), which is what a restore that can land short (clamped) needs. */
function twoTurnsWithRun(ref: string): Thread {
	const served = thread(ref, "idle");
	(served as unknown as { turns: unknown[] }).turns = [
		askReplyTurn("turn_1"),
		{
			id: "turn_2",
			status: "completed",
			itemsView: "default",
			items: [
				{ id: "u-turn_2", turnId: "turn_2", type: "userMessage", status: "completed", text: "ask turn_2" },
				{
					id: "run-turn_2",
					turnId: "turn_2",
					type: "commandExecution",
					status: "completed",
					toolName: "npm test",
					callId: "call-run-2",
					argumentsJson: JSON.stringify({}),
				},
				{ id: "a-turn_2", turnId: "turn_2", type: "agentMessage", status: "completed", text: "reply turn_2" },
				{ id: "b-turn_2", turnId: "turn_2", type: "agentMessage", status: "completed", text: "reply again" },
			],
		},
	];
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

// Opening lands once (spec 7.3): the list stays hidden while it travels to
// where the session opens, and shows only once it is there, so no one sees it
// land, move and land again.
describe("opening a session", () => {
	const opacity = (tree: ReactTestRenderer) => transcriptList(tree).props.style?.opacity;
	const layOutViewport = (tree: ReactTestRenderer, contentHeight = 20_000) => {
		act(() =>
			transcriptList(tree).props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 600 } } }),
		);
		act(() => transcriptList(tree).props.onContentSizeChange(390, contentHeight));
	};
	/** The bottom bar lays out once per mount, so a view that remounts for a new
	 * session key needs it again before its opening cap can start. */
	const layOutBottomBar = (tree: ReactTestRenderer) => {
		const bar = tree.root.findAll(
			(node) => String(node.type) === "View" && node.props.testID === "session-bottom-bar",
		)[0];
		if (bar) act(() => bar.props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 0 } } }));
	};
	const savePosition = (ref: string, itemKey = "a-turn_2", turnsSeen = "turn_2") =>
		nativePlatform.kv.set(
			"evener.reader-positions",
			JSON.stringify({
				[`hub-1\u0000${ref}`]: {
					hubId: "hub-1",
					sessionRef: ref,
					itemKey,
					withinItemOffset: 0,
					touchedAt: 1,
					turnsSeen,
				},
			}),
		);

	it("shows the live end only once its last row is measured and the list is pinned there", async () => {
		const { tree } = await mount(twoTurns("ref-open-live"));
		layOutViewport(tree);
		await settle();
		// The list has laid out, but the rows at its end haven't: it would show
		// at the top and then jump.
		expect(opacity(tree)).toBe(0);
		layOutRow(tree, 0, 16);
		await settle();
		expect(opacity(tree)).toBe(0);
		flatListCalls.length = 0;
		layOutRow(tree, 3, 19_000);
		await settle();
		// Pinned to the end after the last row measured, then shown.
		expect(flatListCalls.map((call) => call.method)).toEqual(["scrollToEnd"]);
		expect(opacity(tree)).toBe(1);
	});

	// A ghost is the list's last row: showing before it measures would show
	// the end short of it and then move.
	it("shows the live end only once a queued message at its end has measured", async () => {
		const served = twoTurns("ref-open-ghost");
		(served as unknown as { evener: { queue: unknown } }).evener.queue = queueState(["check the logs"]);
		const { tree } = await mount(served);
		layOutViewport(tree);
		await settle();
		layOutRow(tree, 3, 19_000);
		await settle();
		expect(opacity(tree)).toBe(0);
		flatListCalls.length = 0;
		layOutRow(tree, 4, 19_150);
		await settle();
		expect(flatListCalls.map((call) => call.method)).toEqual(["scrollToEnd"]);
		expect(opacity(tree)).toBe(1);
	});

	it("shows an unread reply only once the exact restore to it has landed", async () => {
		savePosition("ref-open-anchor", "a-turn_2", "turn_1");
		flatListCalls.length = 0;
		const { tree } = await mount(twoTurns("ref-open-anchor"));
		layOutViewport(tree);
		await settle();
		// The approximate jump toward the unmeasured row happens out of sight.
		expect(flatListCalls.map((call) => call.method)).toContain("scrollToIndex");
		expect(flatListCalls.map((call) => call.method)).not.toContain("scrollToEnd");
		expect(opacity(tree)).toBe(0);
		layOutRow(tree, 3, 9_523);
		await settle();
		expect(flatListCalls.at(-1)).toEqual({ method: "scrollToOffset", args: { offset: 9_523, animated: false } });
		expect(opacity(tree)).toBe(1);
		// Later reflows restore in view: the opening has landed.
		layOutRow(tree, 3, 9_523);
		await settle();
		expect(opacity(tree)).toBe(1);
	});

	it("shows an unread reply restored before the viewport's first layout without moving it again", async () => {
		savePosition("ref-open-early", "a-turn_2", "turn_1");
		const { tree } = await mount(twoTurns("ref-open-early"));
		act(() => transcriptList(tree).props.onContentSizeChange(390, 20_000));
		// The row measures before the list's own layout: the restore lands, but
		// the list can't show until its viewport has laid out.
		layOutRow(tree, 3, 19_500);
		await settle();
		expect(flatListCalls.at(-1)).toEqual({ method: "scrollToOffset", args: { offset: 19_500, animated: false } });
		expect(opacity(tree)).toBe(0);
		flatListCalls.length = 0;
		act(() =>
			transcriptList(tree).props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 600 } } }),
		);
		await settle();
		// The viewport arriving shows the list where it landed, with no second restore.
		expect(flatListCalls).toEqual([]);
		expect(opacity(tree)).toBe(1);
	});

	it("shows the start of an unread reply only once the restore to it has landed", async () => {
		savePosition("ref-open-unread", "u-turn_1", "turn_1");
		const { tree } = await mount(twoTurns("ref-open-unread"));
		layOutViewport(tree);
		await settle();
		expect(opacity(tree)).toBe(0);
		// turn_2's reply, row 3, finished since you last read to the end.
		layOutRow(tree, 3, 9_523);
		await settle();
		expect(flatListCalls.at(-1)).toEqual({ method: "scrollToOffset", args: { offset: 9_523, animated: false } });
		expect(opacity(tree)).toBe(1);
	});

	// Jesse, 2026-10-18 ("make it a setting"): with Open at unread replies
	// off, the same saved history opens at the live end - no jump, no restore.
	it("opens at the live end with the unread-reply jump turned off", async () => {
		const prefs = new DisplayPreferences(memoryStorage());
		prefs.set({ openAtUnreadReplies: false });
		savePosition("ref-open-setting-off", "u-turn_1", "turn_1");
		flatListCalls.length = 0;
		const { tree } = await mount(twoTurns("ref-open-setting-off"), { display: prefs });
		layOutViewport(tree);
		await settle();
		const indexes = flatListCalls
			.filter((call) => call.method === "scrollToIndex")
			.map((call) => (call.args as { index: number }).index);
		// No jump to the reply: it pins the end like any other open.
		expect(indexes).not.toContain(3);
		layOutRow(tree, 3, 19_000);
		await settle();
		expect(flatListCalls.map((call) => call.method)).toContain("scrollToEnd");
		expect(opacity(tree)).toBe(1);
	});

	it("shows the list at once when you touch it while it opens", async () => {
		savePosition("ref-open-touch", "a-turn_2", "turn_1");
		const { tree } = await mount(twoTurns("ref-open-touch"));
		layOutViewport(tree);
		await settle();
		expect(opacity(tree)).toBe(0);
		act(() => transcriptList(tree).props.onScrollBeginDrag({ nativeEvent: { contentOffset: { y: 4_000 } } }));
		await settle();
		expect(opacity(tree)).toBe(1);
	});

	it("shows the list after two caps even when the opening never lands", async () => {
		savePosition("ref-open-cap", "a-turn_2", "turn_1");
		const { tree } = await mount(twoTurns("ref-open-cap"));
		vi.useFakeTimers();
		try {
			layOutViewport(tree);
			await advanceFakeTimers(999);
			// The anchor's row never measures: without a cap the list would
			// stay hidden.
			expect(opacity(tree)).toBe(0);
			await advanceFakeTimers(1);
			// Its row is loaded but still unmeasured: the first cap holds the
			// list hidden rather than show the approximate position, and the
			// second reveals it (never a permanent blank).
			expect(opacity(tree)).toBe(0);
			await advanceFakeTimers(1000);
			expect(opacity(tree)).toBe(1);
		} finally {
			vi.useRealTimers();
		}
	});

	// The cap must not show the list while the jump target's row is resolved
	// but unmeasured: the list sits at the approximate offset, far above the
	// row, and showing it there is the "way up" jump (Jesse, 2026-10-17). It
	// shows only once the exact restore lands, or at the end of a second cap
	// after that.
	it("does not show the list at the cap while the jump target's row is still unmeasured", async () => {
		savePosition("ref-open-cap-unmeasured", "a-turn_2", "turn_1");
		const { tree } = await mount(twoTurns("ref-open-cap-unmeasured"));
		vi.useFakeTimers();
		try {
			layOutViewport(tree);
			await advanceFakeTimers(999);
			expect(opacity(tree)).toBe(0);
			await advanceFakeTimers(1);
			// The anchor's row is loaded but never measured: the cap keeps the
			// list hidden rather than show the approximate position.
			expect(opacity(tree)).toBe(0);
			// The row finally measures: the exact restore lands and shows the
			// list at the saved position.
			layOutRow(tree, 3, 9_523);
			await advanceFakeTimers(0);
			expect(opacity(tree)).toBe(1);
		} finally {
			vi.useRealTimers();
		}
	});

	it("gives a session the screen switches to its own full second to land", async () => {
		savePosition("ref-open-first", "a-turn_2", "turn_1");
		const { tree, route } = await mount(twoTurns("ref-open-first"));
		const next = "ref-open-second";
		otherThreads.set(next, twoTurns(next));
		vi.useFakeTimers();
		try {
			layOutViewport(tree);
			await advanceFakeTimers(600);
			expect(opacity(tree)).toBe(0);
			const moved = { ...route, params: { ...route.params, ref: next } };
			navigationState.state = { index: 0, routes: [moved] };
			act(() => tree.update(<SessionScreen route={moved} navigation={navigation} />));
			// A new session key commits a fresh attachment and view body.
			await advanceFakeTimers(0);
			layOutBottomBar(tree);
			layOutViewport(tree);
			await advanceFakeTimers(0);
			// The first session's cap has 400ms left; the second's starts afresh.
			await advanceFakeTimers(999);
			expect(opacity(tree)).toBe(0);
			await advanceFakeTimers(1);
			expect(opacity(tree)).toBe(1);
		} finally {
			vi.useRealTimers();
		}
	});

	// A session whose saved row stays unmeasured consumes its first cap (the
	// extra round this change adds); the session the screen switches to next
	// must get its own extra round, not the leftovers of the one before.
	it("gives a session the screen switches to its own extra cap round", async () => {
		// The first session's jump target never measures: it burns its first cap
		// and reveals at its second.
		savePosition("ref-cap-first", "a-turn_2", "turn_1");
		const { tree, route } = await mount(twoTurns("ref-cap-first"));
		const next = "ref-cap-second";
		savePosition(next, "a-turn_2", "turn_1");
		otherThreads.set(next, twoTurns(next));
		vi.useFakeTimers();
		try {
			layOutViewport(tree);
			await advanceFakeTimers(1000);
			// The first cap consumed: the list stays hidden on the extra round.
			expect(opacity(tree)).toBe(0);
			await advanceFakeTimers(1000);
			expect(opacity(tree)).toBe(1);
			const moved = { ...route, params: { ...route.params, ref: next } };
			navigationState.state = { index: 0, routes: [moved] };
			act(() => tree.update(<SessionScreen route={moved} navigation={navigation} />));
			await advanceFakeTimers(0);
			layOutBottomBar(tree);
			layOutViewport(tree);
			await advanceFakeTimers(0);
			// The second session's saved row is loaded but unmeasured too: it
			// gets a fresh first cap, hidden through it.
			await advanceFakeTimers(999);
			expect(opacity(tree)).toBe(0);
			await advanceFakeTimers(1);
			// Revealed at its own second cap — never at the first, which the
			// previous session had already consumed.
			expect(opacity(tree)).toBe(0);
			await advanceFakeTimers(1000);
			expect(opacity(tree)).toBe(1);
		} finally {
			vi.useRealTimers();
		}
	});

	// Jesse, 2026-10-18: a saved spot never decides where a session opens, so
	// one on an older page is simply ignored - no wait for a page the opening
	// no longer needs, and no restore once it arrives.
	describe("when the saved spot sits on an older page", () => {
		const mountWaitingOnOlderPage = async (ref: string) => {
			savePosition(ref, "a-turn_0", "turn_0");
			let deliver = () => {};
			const olderPage = new Promise<void>((resolve) => {
				deliver = resolve;
			});
			const mounted = await mount(twoTurns(ref), {
				olderCursor: "cursor-1",
				olderTurns: [askReplyTurn("turn_0")],
				olderPage,
			});
			return { ...mounted, deliver };
		};

		it("opens at the live end without waiting for the older page", async () => {
			const { tree, deliver } = await mountWaitingOnOlderPage("ref-open-unloaded");
			vi.useFakeTimers();
			try {
				layOutViewport(tree);
				// It heads for the end at once, out of sight until the end's last row
				// measures: the saved spot never held the opening back.
				expect(flatListCalls.map((call) => call.method)).toContain("scrollToEnd");
				expect(opacity(tree)).toBe(0);
				layOutRow(tree, 3, 19_000);
				await advanceFakeTimers(0);
				expect(opacity(tree)).toBe(1);
				// The older page landing later doesn't pull the list back to the spot.
				flatListCalls.length = 0;
				deliver();
				await advanceFakeTimers(0);
				expect(flatListCalls.filter((call) => call.method !== "scrollToEnd")).toEqual([]);
			} finally {
				vi.useRealTimers();
			}
		});

		it("shows the live end at the cap even if its last row never measures", async () => {
			const { tree } = await mountWaitingOnOlderPage("ref-open-unloaded-cap");
			vi.useFakeTimers();
			try {
				layOutViewport(tree);
				await advanceFakeTimers(999);
				expect(opacity(tree)).toBe(0);
				await advanceFakeTimers(1);
				expect(opacity(tree)).toBe(1);
			} finally {
				vi.useRealTimers();
			}
		});
	});

	// The list's content grows in stages as it renders the rows below the jump
	// target, so the first restore can fall short of it (clamped). Showing the
	// list there would show it land short and then move.
	describe("when the list can't reach the jump target yet", () => {
		const offsets = () =>
			flatListCalls
				.filter((call) => call.method === "scrollToOffset")
				.map((call) => (call.args as { offset: number }).offset);

		it("shows it only once the restore reaches the row", async () => {
			savePosition("ref-open-clamped", "step:call-run-2", "turn_1");
			const { tree } = await mount(twoTurnsWithRun("ref-open-clamped"));
			flatListCalls.length = 0;
			layOutViewport(tree, 3_000);
			// The jump target, row 3, measures while the list reaches only 2_400.
			layOutRow(tree, 3, 9_523);
			await settle();
			// It moves as far as the list reaches, out of sight.
			expect(offsets().at(-1)).toBe(2_400);
			expect(opacity(tree)).toBe(0);
			act(() => transcriptList(tree).props.onContentSizeChange(390, 20_000));
			await settle();
			expect(offsets().at(-1)).toBe(9_523);
			expect(opacity(tree)).toBe(1);
		});

		it("shows it short of the row once the last row has measured", async () => {
			savePosition("ref-open-near-end", "step:call-run-2", "turn_1");
			const { tree } = await mount(twoTurnsWithRun("ref-open-near-end"));
			flatListCalls.length = 0;
			layOutViewport(tree, 3_000);
			layOutRow(tree, 3, 2_900);
			// The last row, row 5, measures: the content is whole, so as far as
			// the list reaches (2_400) is where it rests.
			layOutRow(tree, 5, 3_050);
			await settle();
			expect(offsets().at(-1)).toBe(2_400);
			expect(opacity(tree)).toBe(1);
		});

		it("keeps it hidden when the restore runs again short of the row, until the last row measures", async () => {
			savePosition("ref-open-clamped-again", "step:call-run-2", "turn_1");
			const { tree } = await mount(twoTurnsWithRun("ref-open-clamped-again"));
			layOutViewport(tree, 3_000);
			layOutRow(tree, 3, 9_523);
			await settle();
			expect(opacity(tree)).toBe(0);
			// A row between measures: the restore runs again, already as far as
			// the list reaches, so it neither moves the list nor shows it, since
			// the rows below are still estimates.
			flatListCalls.length = 0;
			layOutRow(tree, 4, 9_700);
			await settle();
			expect(flatListCalls).toEqual([]);
			expect(opacity(tree)).toBe(0);
			layOutRow(tree, 5, 9_900);
			await settle();
			expect(opacity(tree)).toBe(1);
		});

		it("shows it after a second when the last row never measures", async () => {
			savePosition("ref-open-clamped-cap", "step:call-run-2", "turn_1");
			const { tree } = await mount(twoTurnsWithRun("ref-open-clamped-cap"));
			vi.useFakeTimers();
			try {
				layOutViewport(tree, 3_000);
				layOutRow(tree, 3, 9_523);
				await advanceFakeTimers(0);
				// The restore fell short and the rows below never measure.
				expect(offsets().at(-1)).toBe(2_400);
				await advanceFakeTimers(999);
				expect(opacity(tree)).toBe(0);
				await advanceFakeTimers(1);
				expect(opacity(tree)).toBe(1);
			} finally {
				vi.useRealTimers();
			}
		});
	});

	it("shows a session with no rows at once", async () => {
		const served = thread("ref-open-empty", "idle");
		(served as unknown as { turns: unknown[] }).turns = [];
		const { tree } = await mount(served);
		layOutViewport(tree);
		await settle();
		expect(opacity(tree)).toBe(1);
	});
});

it("reads the session again on its own after a read fails", async () => {
	const { tree, hub } = await mount(twoTurns("ref-retry"), { failedReads: 1 });
	expect(hub.requests.filter((request) => request.method === "thread/read")).toHaveLength(2);
	expect(renderedText(tree)).toContain("ask turn_2");
	expect(hasTestID(tree, "session-read-retrying")).toBe(false);
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
		expect(hasTestID(tree, "session-read-retrying")).toBe(false);
		await advance(50);
		expect(reads()).toBe(2);
		await advance(1_050);
		expect(reads()).toBe(3);
		expect(hasTestID(tree, "session-read-retrying")).toBe(true);
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
	// each offset, and it ends at the last. Await its deferred history read's
	// publication before the case continues or starts teardown.
	async function drag(tree: ReactTestRenderer, ...offsets: number[]) {
		act(() => list(tree).props.onScrollBeginDrag(at(offsets[0] ?? 0)));
		for (const y of offsets) scrollTo(tree, y);
		await act(async () => list(tree).props.onScrollEndDrag(at(offsets.at(-1) ?? 0)));
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
		await drag(tree, 100);
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
		await drag(tree, 100);
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
		await drag(tree, 100);
		stream(hub, served, "s1");
		expect(pill(tree)?.props.accessibilityLabel).toBe("1 new below, scroll to the end");
		expect(contentGrows(tree, 4_200)).toBe(false);
	});

	it("follows again once a drag ends at the end, and not while the finger is still down", async () => {
		const served = working("ref-follow-again");
		const { tree, hub } = await mount(served);
		await drag(tree, 100);
		act(() => list(tree).props.onScrollBeginDrag(at(100)));
		scrollTo(tree, END);
		stream(hub, served, "s2");
		// A row landing mid-drag doesn't pull the list from under the finger.
		expect(contentGrows(tree, 4_200)).toBe(false);
		await act(async () => list(tree).props.onScrollEndDrag(at(END)));
		expect(pill(tree)).toBeUndefined();
		stream(hub, served, "s2b");
		expect(contentGrows(tree, 4_400)).toBe(true);
	});

	it("doesn't follow after a drag that reached the end and came back up", async () => {
		const served = working("ref-follow-back-up");
		const { tree } = await mount(served);
		await drag(tree, 100, END, 1_000);
		expect(contentGrows(tree, 4_200)).toBe(false);
	});

	it("follows again when a flick's momentum carries you to the end", async () => {
		const served = working("ref-follow-flick");
		const { tree, hub } = await mount(served);
		await drag(tree, 100);
		await drag(tree, 1_000);
		act(() => list(tree).props.onMomentumScrollBegin());
		scrollTo(tree, END);
		act(() => list(tree).props.onMomentumScrollEnd(at(END)));
		stream(hub, served, "s3");
		expect(contentGrows(tree, 4_200)).toBe(true);
	});

	it("doesn't scroll to the end while a flick released at the end coasts away", async () => {
		const served = working("ref-follow-coast");
		const { tree, hub } = await mount(served);
		await drag(tree, 100, END);
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
		await act(async () => list(tree).props.onScrollEndDrag(at(0, 300)));
		expect(contentGrows(tree, 320)).toBe(true);
	});

	it("stays unfollowed when the app itself scrolls to the end", async () => {
		const served = working("ref-follow-app-scroll");
		const { tree } = await mount(served);
		await drag(tree, 100);
		// No finger: a restore or the list settling lands at the end.
		scrollTo(tree, END);
		expect(contentGrows(tree, 4_200)).toBe(false);
	});

	it("counts what landed since you last left the end", async () => {
		const served = working("ref-follow-counts");
		const { tree, hub } = await mount(served);
		await drag(tree, 100);
		stream(hub, served, "c1");
		expect(pill(tree)?.props.accessibilityLabel).toBe("1 new below, scroll to the end");
		await drag(tree, END);
		expect(pill(tree)).toBeUndefined();
		await drag(tree, 100);
		stream(hub, served, "c2");
		stream(hub, served, "c3");
		expect(pill(tree)?.props.accessibilityLabel).toBe("2 new below, scroll to the end");
	});

	it("follows again from the pill, which then goes", async () => {
		const served = working("ref-follow-pill");
		const { tree, hub } = await mount(served);
		await drag(tree, 100);
		stream(hub, served, "p1");
		flatListCalls.length = 0;
		act(() => pill(tree)?.props.onPress());
		expect(flatListCalls.some((call) => call.method === "scrollToEnd")).toBe(true);
		expect(pill(tree)).toBeUndefined();
		stream(hub, served, "p2");
		expect(contentGrows(tree, 4_200)).toBe(true);
	});

	// The ghosts are the list's last rows (spec 8.5): the hub's queue frame
	// adds or takes one away below whatever you read.
	function queueNow(hub: { notify(notification: AnyNotification): void }, served: Thread, texts: string[]) {
		act(() =>
			hub.notify({
				method: "thread/queueChanged",
				params: { threadId: served.id, ref: served.evener.ref, queue: queueState(texts, texts.length + 1) },
			} as AnyNotification),
		);
	}

	it("keeps following at the live end when a queued message lands there", async () => {
		const served = working("ref-follow-ghost");
		const { tree, hub } = await mount(served);
		queueNow(hub, served, ["check the logs"]);
		expect(textOf(transcriptList(tree))).toContain("check the logs");
		expect(contentGrows(tree, 4_200)).toBe(true);
	});

	it("never moves the list or counts as new when a ghost comes or goes while you read above", async () => {
		const served = working("ref-follow-ghost-away");
		const { tree, hub } = await mount(served);
		await drag(tree, 100);
		flatListCalls.length = 0;
		queueNow(hub, served, ["check the logs"]);
		expect(textOf(transcriptList(tree))).toContain("check the logs");
		expect(pill(tree)).toBeUndefined();
		stream(hub, served, "g1");
		expect(pill(tree)?.props.accessibilityLabel).toBe("1 new below, scroll to the end");
		queueNow(hub, served, []);
		expect(textOf(transcriptList(tree))).not.toContain("check the logs");
		expect(pill(tree)?.props.accessibilityLabel).toBe("1 new below, scroll to the end");
		expect(flatListCalls).toEqual([]);
		expect(contentGrows(tree, 4_200)).toBe(false);
	});

	it("keeps the end in view when the viewport changes while following, as the keyboard does", async () => {
		const served = working("ref-follow-viewport");
		const { tree } = await mount(served);
		flatListCalls.length = 0;
		act(() => list(tree).props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 300 } } }));
		expect(flatListCalls.some((call) => call.method === "scrollToEnd")).toBe(true);
		await drag(tree, 100);
		flatListCalls.length = 0;
		act(() => list(tree).props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 600 } } }));
		expect(flatListCalls.some((call) => call.method === "scrollToEnd")).toBe(false);
	});

	it("keeps the newest message clear of all three floating controls, and never shrinks the room back (#2988)", async () => {
		// Stop can put the Stopped toast, Next and "↓ 1 new" on screen at once.
		// Three 44pt rows and their 8pt gaps stand 148pt tall over the 10pt
		// lift, while the end kept only 60pt.
		const next = fleetSession("local:fail", {
			title: "Fix retry loop",
			state: "errored",
			updated_at: new Date().toISOString(),
		});
		fleet.needsYou = [next];
		const served = working("ref-follow-stack");
		const { tree, hub } = await mount(served);
		await drag(tree, 100);
		stream(hub, served, "s-stack");
		expect(pill(tree)).toBeDefined();
		expect(pressable(tree, "Next, Fix retry loop")).toBeDefined();
		await press(tree, "Stop");
		expect(toasts(tree)).toEqual(["stop:stopped"]);
		const bottom = () => list(tree).props.contentContainerStyle.paddingBottom;
		expect(bottom()).toBe(60);
		const stack = tree.root.findByType(FloatingStack);
		const root = stack.findAll((node) => String(node.type) === "View")[0];
		act(() => root?.props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 148 } } }));
		expect(bottom()).toBe(158);
		// The toast's four seconds pass, leaving a shorter two-control stack:
		// the room stays where it was, so the list never moves back under a
		// reader.
		act(() => tree.root.findByType(Toast).props.dismiss());
		expect(tree.root.findAllByType(Toast)).toHaveLength(0);
		act(() => root?.props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 98 } } }));
		expect(bottom()).toBe(158);
	});
});

// A ghost can show before the first read lands (ghosts() lists this phone's
// own sends with no session yet); the quiet blocks still stand in for the
// conversation above it.
it("keeps the loading blocks above a ghost until the conversation first loads", async () => {
	const ref = "ref-skeleton-ghost";
	nativeDrafts().write({ hubId: "hub-1", sessionRef: ref }, { draft: "", unconfirmed: "lost send" });
	const { tree } = await mount(thread(ref, "idle"), { failedReads: 2 });
	await vi.waitFor(() => expect(markers(transcriptList(tree), "ghost")).toEqual(["unconfirmed"]));
	expect(
		transcriptList(tree).findAll((node) => node.props.accessibilityLabel === "Loading conversation").length,
	).toBeGreaterThan(0);
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
