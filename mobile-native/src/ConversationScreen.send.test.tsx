import { nativePlatform } from "./conversationPlatform.testkit";
import { subagentOutcomesDelegatesResponse } from "@evener/appwire-client/testing/subagentWireFixtures";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { activityFixture } from "./subagents/sessionActivityTestUtils";
import { activityChangedNotification } from "@evener/appwire-client/testing/notifications";
// The Session's one Send and the tray's Stop, on the real ConversationScreen:
// what a person presses, and which requests reach the hub through the durable
// runtime. Only native edges are mocked, as in
// ConversationScreen.recovery.test.tsx.
import type { ComponentProps } from "react";
import { act, type ReactTestInstance, type ReactTestRenderer } from "react-test-renderer";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import {
	type AnyNotification,
	type AppwireClientLike,
	makeTranscriptDisplayConfig,
	type SessionDelegatesResponse,
	type Thread,
	WireError,
} from "@evener/appwire-client";
import { findBar, findMatches, findShows } from "./findBarTestUtils";
import { hasTestID, markers } from "./markerTestUtils";
import { drafts, nativeDrafts } from "./nativeDrafts";
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
	settle,
	systemGlass,
	textOf,
	unmountMountedTrees,
	until,
} from "./renderNative.testkit";
import { queueHosts } from "./QueueSheet";
import { ConversationScreen } from "./screens";
import type { NativeConversationSessions } from "./session/nativeConversationSessions";
import {
	NativeConversationSessionsProvider,
	useNativeConversationSessions,
} from "./session/NativeConversationSessionsProvider";
import { nativeDisclosureStore, setDisclosureOpenAll } from "./nativeDisclosure";
import { rowDisclosureIds, sessionDisclosureScope } from "./session/disclosureKeys";
import { NotesSheet, notesHosts } from "./session/NotesSheet";
import { QuestionDock } from "./session/QuestionDock";
import { FindBar } from "./session/FindBar";
import { GlassHeaderPanel } from "./design/GlassHeaderPanel";
import { indicatorRoom } from "./design/BarFrame";
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
import { DisplayPreferences } from "./display/displayPreferences";
import { DisplayProvider } from "./display/displayContext";
import { displayForLevel } from "./session/detailLevels";
import { detailLevels } from "./session/nativeDetailLevels";

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

async function advanceFakeTimers(ms: number) {
	await act(async () => {
		await vi.advanceTimersByTimeAsync(ms);
	});
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

async function press(tree: ReactTestRenderer, label: string) {
	const target = pressable(tree, label);
	if (!target) throw new Error(`no pressable labelled ${label}`);
	expect(target.props.accessibilityState).toMatchObject({ disabled: false });
	act(() => target.props.onPress());
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

/** What the session's toasts say happened, by kind. */
function toasts(tree: ReactTestRenderer): string[] {
	return markers(tree, "toast");
}

/** Why the composer's last action failed, by reason. */
function actionErrors(tree: ReactTestRenderer): string[] {
	return markers(tree, "action-error");
}

/** Every ghost on the screen, by state. */
function ghostStates(tree: ReactTestRenderer): string[] {
	return markers(tree, "ghost");
}

/** Whether the screen says a steer failed: its toast, or a refused ghost. */
function saysSteerFailed(tree: ReactTestRenderer): boolean {
	return toasts(tree).some((kind) => kind.startsWith("steer:")) || ghostStates(tree).includes("refused");
}

function transcriptList(tree: ReactTestRenderer) {
	return tree.root.findAll(
		(node) =>
			typeof node.type === "function" && Array.isArray(node.props.data) && typeof node.props.renderItem === "function",
	)[0];
}

/** Lays out a freshly mounted view's transcript and bottom bar, which a
 * session-key switch remounts. */
function layOutSessionView(tree: ReactTestRenderer) {
	act(() => transcriptList(tree).props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 600 } } }));
	act(() => transcriptList(tree).props.onContentSizeChange(390, 20_000));
	const bar = tree.root.findAll(
		(node) => String(node.type) === "View" && node.props.testID === "session-bottom-bar",
	)[0];
	if (bar) act(() => bar.props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 0 } } }));
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

it("shows nothing for a loaded conversation with no rows: the composer invites", async () => {
	const { tree } = await mount(thread("ref-empty", "idle"));
	expect(tree.root.findAll((node) => node.props.accessibilityLabel === "Loading conversation")).toEqual([]);
	for (const words of ["No messages", "Loading", "Pull down"]) expect(renderedText(tree)).not.toContain(words);
});

// Two mounted views of one session borrow one registry entry: the same
// canonical store, service and activity sink, and one live target
// registration. The shared code is the real native store/service/runtime.
describe("two mounted views of one session", () => {
	const routeFor = (key: string, ref: string) =>
		({
			key,
			name: "Conversation",
			params: { hubId: "hub-1", ref, title: "Session" },
		}) as unknown as ConversationScreenProps["route"];

	function mountTwo(ref: string, served: Thread) {
		const hub = hubClient(served);
		harness.connection = {
			...screenConnection(hub.client, "ready"),
			profiles: [{ id: "hub-1", name: "Work hub", origin: "https://hub.test" }],
			error: null,
			disconnect: () => {},
		};
		const a = routeFor("conversation-two-a", ref);
		const b = routeFor("conversation-two-b", ref);
		navigationState.state = { index: 1, routes: [a, b] };
		const seen = { current: null as NativeConversationSessions | null };
		let tree!: ReactTestRenderer;
		act(() => {
			tree = render(
				<NativeConversationSessionsProvider>
					<RegistryProbe seen={seen} />
					<ConversationScreen route={a} navigation={navigation} />
					<ConversationScreen route={b} navigation={navigation} />
				</NativeConversationSessionsProvider>,
			);
		});
		return { tree, hub, seen, a, b };
	}

	function lists(tree: ReactTestRenderer) {
		return tree.root.findAll(
			(node) =>
				typeof node.type === "function" &&
				Array.isArray(node.props.data) &&
				typeof node.props.renderItem === "function",
		);
	}

	function deferredRead() {
		let release!: () => void;
		const gate = new Promise<void>((resolve) => {
			release = resolve;
		});
		return { gate, release };
	}

	it("shares one canonical entry and both views see a newer publication", async () => {
		const ref = "ref-two-views";
		const served = twoTurns(ref);
		const { tree, seen } = mountTwo(ref, served);
		await settle();

		const registry = seen.current!;
		const entry = registry.entryFor("hub-1", ref)!;
		expect(entry).toBeDefined();
		expect(entry.attachmentCount).toBe(2);
		expect(entry.store.getState().status).toBe("open");

		(served as unknown as { turns: unknown[] }).turns.push({
			id: "turn_3",
			status: "completed",
			itemsView: "default",
			items: [
				{ id: "u-turn_3", turnId: "turn_3", type: "userMessage", status: "completed", text: "newest question" },
				{ id: "a-turn_3", turnId: "turn_3", type: "agentMessage", status: "completed", text: "newest reply" },
			],
		});
		await act(async () => {
			await entry.store.getState().ensureLatest();
		});
		await settle();

		expect(entry.store.getState().canonicalSession()?.turns.at(-1)?.id).toBe("turn_3");
		const views = lists(tree);
		expect(views.length).toBeGreaterThanOrEqual(2);
		for (const view of views) expect(JSON.stringify(view.props.data)).toContain("newest reply");
	});

	it("shares the session display level without recreating the binding", async () => {
		const ref = "ref-two-views-level";
		const { tree, seen } = mountTwo(ref, twoTurns(ref));
		await settle();
		const entry = seen.current!.entryFor("hub-1", ref)!;
		const store = entry.store;
		const service = entry.service;

		act(() => detailLevels("hub-1").set(ref, "chat"));
		await settle();

		expect(seen.current!.entryFor("hub-1", ref)!.store).toBe(store);
		expect(entry.service).toBe(service);
		expect(store.getState().displayConfig).toEqual(displayForLevel("chat", null).config);
		const views = lists(tree);
		expect(views.length).toBeGreaterThanOrEqual(2);
		const data = views.map((view) => JSON.stringify(view.props.data));
		expect(new Set(data).size).toBe(1);
	});

	it("lets a surviving view observe a read held when its sibling closes", async () => {
		const ref = "ref-two-views-held";
		const served = twoTurns(ref);
		const { tree, hub, seen, b } = mountTwo(ref, served);
		await settle();
		const registry = seen.current!;
		const entry = registry.entryFor("hub-1", ref)!;
		const original = hub.client.request.bind(hub.client);
		const held = deferredRead();
		let hold = true;
		hub.client.request = async (method: string, params: Record<string, unknown>) => {
			if (method === "thread/read" && hold) {
				hold = false;
				await held.gate;
			}
			return original(method, params);
		};
		let reading!: Promise<void>;
		await act(async () => {
			reading = entry.store.getState().ensureLatest();
		});

		act(() => {
			tree.update(
				<NativeConversationSessionsProvider>
					<RegistryProbe seen={seen} />
					<ConversationScreen route={b} navigation={navigation} />
				</NativeConversationSessionsProvider>,
			);
		});
		expect(entry.attachmentCount).toBe(1);
		expect(entry.retired).toBe(false);

		await act(async () => {
			held.release();
		});
		await act(async () => {
			await reading;
		});
		await settle();
		expect(entry.store.getState().status).toBe("open");
		expect(entry.store.getState().canonicalSession()).not.toBeNull();
		expect(lists(tree).length).toBeGreaterThanOrEqual(1);
	});

	it("retires the entry on the last release, and a reopen reads afresh", async () => {
		const ref = "ref-two-views-reopen";
		const first = mountTwo(ref, twoTurns(ref));
		await settle();
		const firstEntry = first.seen.current!.entryFor("hub-1", ref)!;
		expect(firstEntry.attachmentCount).toBe(2);

		act(() => first.tree.unmount());
		expect(firstEntry.retired).toBe(true);
		expect(first.seen.current!.entryFor("hub-1", ref)).toBeUndefined();

		const second = mountTwo(ref, twoTurns(ref));
		await settle();
		const nextEntry = second.seen.current!.entryFor("hub-1", ref)!;
		expect(nextEntry).not.toBe(firstEntry);
		expect(nextEntry.store).not.toBe(firstEntry.store);
		expect(nextEntry.store.getState().status).toBe("open");
		expect(nextEntry.store.getState().canonicalSession()).not.toBeNull();
	});

	it("offers one shared Retry answer across two views and submits nothing from a stale callback", async () => {
		const ref = "ref-two-views-retry";
		const served = failedTurn(ref, "go test exited 1");
		const { tree, hub, seen } = mountTwo(ref, served);
		await settle();
		const entry = seen.current!.entryFor("hub-1", ref)!;
		const offered = pressable(tree, "Retry");
		expect(offered).toBeDefined();
		expect(offered?.props.disabled).toBe(false);
		const stale = offered!.props.onPress;

		// Both views read the same canonical store: a newer completed turn hides
		// the historical failure's action in the one view that is in front.
		(served as unknown as { turns: unknown[] }).turns.push({
			id: "turn_2",
			status: "completed",
			itemsView: "default",
			items: [{ id: "u-turn_2", turnId: "turn_2", type: "userMessage", status: "completed", text: "next" }],
		});
		await act(async () => {
			await entry.store.getState().ensureLatest();
		});
		await settle();
		expect(pressable(tree, "Retry")).toBeUndefined();

		// The callback captured before the advance re-reads the shared store and
		// submits nothing.
		act(() => stale());
		await settle();
		expect(hub.mutations()).toEqual([]);
	});
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
	// And once its last row has measured, so it lands at its end once.
	expect(opacity()).toBe(0);
	layOutRow(tree, 3, 300);
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
	nativePlatform.kv.set(
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
		tree.update(<SessionScreen route={route as unknown as ConversationScreenProps["route"]} navigation={navigation} />),
	);
	await settle();
	scrollTo(tree, 100);
	await settle();
	expect(hub.requests.filter((request) => request.method === "thread/turns/list")).toEqual([]);
});

it.each(["idle"] as const)(
	"retries a failed turn with continuation while %s, leaving the draft alone",
	async (status) => {
		const ref = `ref-retry-turn-${status}`;
		const served = thread(ref, status);
		(served as unknown as { turns: unknown[] }).turns = [
			{
				id: "turn_1",
				status: "failed",
				itemsView: "default",
				error: { message: "go test exited 1" },
				items: [
					{ id: "u-1", turnId: "turn_1", type: "userMessage", status: "completed", text: "my original question" },
				],
			},
		];
		const { tree, hub } = await mount(served);
		await type(tree, "keep this");
		const destination = { hubId: "hub-1", sessionRef: ref };
		const document = drafts.open(destination);
		const draftWrites = vi.spyOn(nativeDrafts(), "write");
		act(() =>
			document.addImage({ id: "draft-image", marker: 1, mediaType: "image/png", name: "draft.png", data: "AQID" }),
		);
		const before = document.getSnapshot().record;
		const imagesBefore = nativeDrafts().imageInputs(destination, before.images ?? []);
		const method = status === "idle" ? "turn/start" : "turn/queue";
		const client = hub.client as { request: (method: string, params: Record<string, unknown>) => Promise<unknown> };
		const request = client.request;
		client.request = async (requested, params) => {
			const answer = await request(requested, params);
			if (requested === method) throw new Error("connection lost after admission");
			return answer;
		};
		playedHaptics.length = 0;
		await press(tree, "Retry");
		expect(hub.mutations()).toEqual([method]);
		const retryCapture = draftWrites.mock.calls.find(([, record]) => record.unconfirmed === "continue")?.[1];
		expect(retryCapture?.unconfirmedCaptureToken).toBeTruthy();
		const runtime = getNativeMutationRuntime();
		const targetKey = nativeMutationTargetKey("hub-1", ref);
		const admittedRetry = (await runtime.read(targetKey)).outbox.find(
			(row) => row.method === method && row.composerText === "continue",
		);
		expect(admittedRetry).toMatchObject({ draftCaptureToken: retryCapture?.unconfirmedCaptureToken });
		// Spec 16.6: a light impact on send, a retry's included.
		expect(playedHaptics).toEqual(["impact:light"]);
		const start = hub.requests.find((request) => request.method === method);
		expect(start?.params.input).toMatchInlineSnapshot(`
		[
		  {
		    "text": "continue",
		    "type": "text",
		  },
		]
	`);
		expect(field(tree)?.props.value).toBe(before.draft);
		expect(document.getSnapshot().record).toEqual(before);
		expect(nativeDrafts().imageInputs(destination, document.getSnapshot().record.images ?? [])).toEqual(imagesBefore);
	},
);

it("Retry continues without re-sending the failed turn's images", async () => {
	const served = thread("ref-retry-images", "idle");
	(served as unknown as { turns: unknown[] }).turns = [
		{
			id: "turn_1",
			status: "failed",
			itemsView: "default",
			error: { message: "go test exited 1" },
			items: [
				{
					id: "u-1",
					turnId: "turn_1",
					type: "userMessage",
					status: "completed",
					text: "(attached image 1: plot.png) my original question",
					images: [{ mediaType: "image/png", data: "image-bytes", name: "plot.png" }],
				},
			],
		},
	];
	const { tree, hub } = await mount(served);
	await press(tree, "Retry");
	expect(hub.mutations()).toEqual(["turn/start"]);
	const start = hub.requests.find((request) => request.method === "turn/start");
	expect(start?.params.input).toMatchInlineSnapshot(`
		[
		  {
		    "text": "continue",
		    "type": "text",
		  },
		]
	`);
});

it("Retry continues an image-only turn even when its bytes are unavailable", async () => {
	const served = thread("ref-retry-dropped", "idle");
	(served as unknown as { turns: unknown[] }).turns = [
		{
			id: "turn_1",
			status: "failed",
			itemsView: "default",
			error: { message: "go test exited 1" },
			items: [
				{
					id: "u-1",
					turnId: "turn_1",
					type: "userMessage",
					status: "completed",
					text: "(attached image 1: plot.png)",
					images: [{ src: "sha:abc123", name: "plot.png" }],
				},
			],
		},
	];
	const { tree, hub } = await mount(served);
	await press(tree, "Retry");
	expect(hub.mutations()).toEqual(["turn/start"]);
	const start = hub.requests.find((request) => request.method === "turn/start");
	expect(start?.params.input).toMatchInlineSnapshot(`
		[
		  {
		    "text": "continue",
		    "type": "text",
		  },
		]
	`);
	expect(toasts(tree)).toEqual([]);
});

it("Retry continues when the original input is outside loaded history", async () => {
	const served = thread("ref-retry-none", "idle");
	(served as unknown as { turns: unknown[] }).turns = [
		{
			id: "turn_1",
			status: "failed",
			itemsView: "default",
			error: { message: "go test exited 1" },
			// A reloaded failure entry: no user input anywhere to re-issue.
			items: [],
		},
	];
	const { tree, hub } = await mount(served);
	await press(tree, "Retry");
	expect(hub.mutations()).toEqual(["turn/start"]);
	expect(hub.requests.find((request) => request.method === "turn/start")?.params.input).toMatchInlineSnapshot(`
		[
		  {
		    "text": "continue",
		    "type": "text",
		  },
		]
	`);
});

// Retry shows only while a press would send, which a session control going
// pending (a compact) changes without changing the rows: the row still
// follows it.
it.each(["", "  ", "unsent work"])("a refused Retry keeps native draft %j and remains recoverable", async (text) => {
	const ref = `ref-retry-refusal-${text.length}`;
	const { tree, hub } = await mount(failedTurn(ref, "provider failure"));
	await type(tree, text);
	const document = drafts.open({ hubId: "hub-1", sessionRef: ref });
	const before = document.getSnapshot().record;
	const request = hub.client.request;
	let refuse = true;
	vi.spyOn(hub.client, "request").mockImplementation(async (method, params) => {
		if (method === "turn/start" && refuse) {
			hub.requests.push({ method, params });
			throw new WireError("refused", -32602, {
				clientMutationId: params.clientMutationId,
				mutationOutcome: "notAccepted",
				retryDisposition: "none",
			});
		}
		return request(method, params);
	});
	await press(tree, "Retry");
	expect(ghostStates(tree)).toContain("refused");
	expect(document.getSnapshot().record).toEqual(before);
	expect(field(tree)?.props.value).toBe(text);
	const runtime = getNativeMutationRuntime();
	const target = nativeMutationTargetKey("hub-1", ref);
	expect(await runtime.storage.listRecovery(target)).toHaveLength(1);
	if (text === "") {
		refuse = false;
		await press(tree, "Edit");
		await press(tree, "Send");
		await press(tree, "Discard");
		expect(await runtime.storage.listRecovery(target)).toHaveLength(0);
		expect(hub.mutations()).toEqual(["turn/start", "turn/start"]);
		expect(hub.requests.filter((entry) => entry.method === "turn/start")[1]?.params.input).toMatchInlineSnapshot(`
			[
			  {
			    "text": "continue",
			    "type": "text",
			  },
			]
		`);
	}
});

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
	nativePlatform.kv.set(
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
		tree.update(<SessionScreen route={route as unknown as ConversationScreenProps["route"]} navigation={navigation} />),
	);
	await settle();
	layOutSessionView(tree);
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

describe("Retry's one owner on the shared session binding", () => {
	function deferred() {
		let release!: () => void;
		const gate = new Promise<void>((resolve) => {
			release = resolve;
		});
		return { gate, release };
	}

	/** One view under the real sessions provider, with the shared entry in
	 * hand so a test can advance the canonical model or drive its pending seam. */
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

	function nextTurn(id: string, status: "completed" | "failed") {
		return {
			id,
			status,
			itemsView: "default",
			...(status === "failed" ? { error: { message: "second failure" } } : {}),
			items: [{ id: `u-${id}`, turnId: id, type: "userMessage", status: "completed", text: "next" }],
		};
	}

	it("offers Retry only while the shared canonical latest turn is the clicked failure", async () => {
		const ref = "ref-retry-canonical";
		const served = failedTurn(ref, "go test exited 1");
		const { tree, entry } = await mountTracked(served);
		expect(pressable(tree, "Retry")?.props.disabled).toBe(false);
		expect(pressable(tree, "Retry")?.props.accessibilityState).toEqual({ disabled: false });

		(served as unknown as { turns: unknown[] }).turns.push(nextTurn("turn_2", "completed"));
		await act(async () => {
			await entry.store.getState().ensureLatest();
		});
		await settle();
		expect(entry.store.getState().canonicalSession()?.turns.at(-1)?.id).toBe("turn_2");
		expect(pressable(tree, "Retry")).toBeUndefined();
	});

	it("disables Retry while this client's own durable submission is unresolved", async () => {
		const ref = "ref-retry-own-pending";
		const { tree, entry, hub } = await mountTracked(failedTurn(ref, "go test exited 1"));
		const held = deferred();
		const request = hub.client.request;
		let hold = true;
		hub.client.request = async (method, params) => {
			if (method === "turn/start" && hold) {
				hold = false;
				await held.gate;
			}
			return request(method, params);
		};
		act(() => pressable(tree, "Retry")!.props.onPress());
		await until(() => expect(entry.store.getState().pendingMutations ?? []).toHaveLength(1));
		const disabled = pressable(tree, "Retry");
		expect(disabled).toBeDefined();
		expect(disabled?.props.disabled).toBe(true);
		expect(disabled?.props.accessibilityState).toEqual({ disabled: true });
		// A second press on the disabled row submits nothing.
		act(() => disabled!.props.onPress());
		await settle();
		expect(hub.mutations()).toEqual([]);

		held.release();
		await until(() => expect(hub.mutations()).toEqual(["turn/start"]));
		// The accepted row is still this client's unresolved input until the
		// canonical read reflects it, so the row stays disabled.
		expect(pressable(tree, "Retry")?.props.disabled).toBe(true);
	});

	it("keeps Retry on the last displayed failure, disabled, while the shared store cannot certify readiness", async () => {
		const ref = "ref-retry-unknown";
		const { tree, entry } = await mountTracked(failedTurn(ref, "go test exited 1"));
		const runtime = getNativeMutationRuntime();
		const read = runtime.read.bind(runtime);
		const held = deferred();
		let hold = true;
		runtime.read = async (targetRef: string) => {
			if (hold) {
				hold = false;
				await held.gate;
			}
			return read(targetRef);
		};
		try {
			await act(async () => {
				await runtime.discardRecovery("no-such-row", nativeMutationTargetKey("hub-1", ref));
			});
			expect(entry.store.getState().pendingMutationsReady).toBe(false);
			expect(entry.store.getState().canonicalSession()).toBeNull();
			const disabled = pressable(tree, "Retry");
			expect(disabled).toBeDefined();
			expect(disabled?.props.disabled).toBe(true);
		} finally {
			runtime.read = read;
			held.release();
		}
		await until(() => expect(entry.store.getState().pendingMutationsReady).toBe(true));
		await act(async () => {
			await entry.store.getState().ensureLatest();
		});
		await settle();
		expect(pressable(tree, "Retry")?.props.disabled).toBe(false);
	});

	// The storage transition that drops canonical authority schedules no read of
	// its own (that read blocks the target's ordinary dispatch). The mounted
	// failure is the consumer that may demand it: without any explicit
	// ensureLatest, the view's own demand restores authority and enables Retry.
	it("demands one authoritative read for a displayed failure after a zero-deletion storage notification", async () => {
		const ref = "ref-retry-auto-read";
		const { tree, entry, hub } = await mountTracked(failedTurn(ref, "go test exited 1"));
		expect(pressable(tree, "Retry")?.props.disabled).toBe(false);
		const targetKey = nativeMutationTargetKey("hub-1", ref);
		const readsBefore = hub.requests.filter((request) => request.method === "thread/read").length;

		const runtime = getNativeMutationRuntime();
		const request = hub.client.request;
		const held = deferred();
		let hold = true;
		let heldReads = 0;
		hub.client.request = async (method, params) => {
			if (method === "thread/read" && hold) {
				hold = false;
				heldReads += 1;
				await held.gate;
			}
			return request(method, params);
		};
		try {
			await act(async () => {
				await runtime.discardRecovery("no-such-row", targetKey);
			});
			// The demand reaches the wire with no explicit call from the test, and
			// the row is disabled while that read is held.
			await until(() => expect(heldReads).toBe(1));
			const heldAction = pressable(tree, "Retry");
			expect(heldAction).toBeDefined();
			expect(heldAction?.props.disabled).toBe(true);
		} finally {
			await act(async () => held.release());
			hub.client.request = request;
		}

		await until(() => expect(entry.store.getState().latestReadState).toBe("ready"));
		await settle();
		expect(hub.requests.filter((request) => request.method === "thread/read").length).toBe(readsBefore + 1);
		expect(pressable(tree, "Retry")?.props.disabled).toBe(false);
		// The demand is a read, not a submission: one identity, nothing sent.
		expect(hub.mutations()).toEqual([]);
		expect(await runtime.storage.listOutbox(targetKey)).toEqual([]);
	});

	it("does not demand a read when the screen displays no failure", async () => {
		const ref = "ref-retry-no-failure";
		const { entry, hub } = await mountTracked(twoTurns(ref));
		await settle();
		const runtime = getNativeMutationRuntime();
		const targetKey = nativeMutationTargetKey("hub-1", ref);
		const readsBefore = hub.requests.filter((request) => request.method === "thread/read").length;

		await act(async () => {
			await runtime.discardRecovery("no-such-row", targetKey);
		});
		await until(() => expect(entry.store.getState().pendingMutationsReady).toBe(true));
		await settle();
		expect(entry.store.getState().latestReadState).toBe("unknown");
		expect(hub.requests.filter((request) => request.method === "thread/read").length).toBe(readsBefore);
	});

	it("stays disabled when the demanded read fails", async () => {
		const ref = "ref-retry-auto-read-fails";
		const { tree, entry, hub } = await mountTracked(failedTurn(ref, "go test exited 1"));
		await settle();
		const enabledAction = pressable(tree, "Retry");
		expect(enabledAction).toBeDefined();
		expect(enabledAction?.props.disabled).toBe(false);
		const enabledPress = enabledAction!.props.onPress;
		const runtime = getNativeMutationRuntime();
		const targetKey = nativeMutationTargetKey("hub-1", ref);
		const readsBefore = hub.requests.filter((request) => request.method === "thread/read").length;

		coordinatorHub.readFails = ref;
		try {
			await act(async () => {
				await runtime.discardRecovery("no-such-row", targetKey);
			});
			await until(() =>
				expect(hub.requests.filter((request) => request.method === "thread/read").length).toBeGreaterThan(readsBefore),
			);
			await until(() => expect(entry.store.getState().latestReadState).toBe("error"));
			await settle();
			// With the required read failed, the session is in error: the action is
			// never offered enabled, and the callback captured while it was enabled
			// re-reads the owners at activation and submits nothing.
			const afterFailure = pressable(tree, "Retry");
			expect(afterFailure === undefined || afterFailure.props.disabled === true).toBe(true);
			act(() => enabledPress());
			await settle();
			expect(hub.mutations()).toEqual([]);
		} finally {
			coordinatorHub.readFails = null;
		}
	});

	// A discarded reply is not an event that asks for another read. One demand
	// belongs to one binding generation and one displayed failure: when the
	// shorter same-incarnation replies are discarded, authority stays unavailable
	// and the token stays consumed. Recovery waits for an explicit demand (a
	// focus/reopen/reconnect in the field, called directly here) or a ready
	// publication.
	it("demands one authoritative read per binding while discarded replies leave authority unknown", async () => {
		const ref = "ref-retry-one-demand";
		readHistory.live = true;
		const { tree, entry, hub } = await mountTracked(failedTurn(ref, "go test exited 1"));
		await settle();
		// The retained history is longer than a shortened window's reply: same
		// incarnation with a shorter length, which applyReadModel discards.
		const held = entry.store.getState().conversation!;
		expect(held.history).toBeDefined();
		act(() =>
			entry.store.setState({
				conversation: { ...held, history: held.history && { ...held.history, length: 10 } },
			}),
		);
		expect(entry.store.getState().latestReadState).toBe("ready");
		expect(pressable(tree, "Retry")?.props.disabled).toBe(false);

		const runtime = getNativeMutationRuntime();
		const targetKey = nativeMutationTargetKey("hub-1", ref);
		const request = hub.client.request;
		let replyLength = 0;
		let reads = 0;
		const gates: (() => void)[] = [];
		hub.client.request = async (method, params) => {
			if (method !== "thread/read") return request(method, params);
			reads += 1;
			await new Promise<void>((resolve) => gates.push(resolve));
			const response = (await request(method, params)) as {
				snapshot?: { incarnation: string; length: number };
			};
			return { ...response, snapshot: { incarnation: "inc-1", length: replyLength } };
		};
		try {
			await act(async () => {
				await runtime.discardRecovery("no-such-row", targetKey);
			});
			await until(() => expect(reads).toBe(1));

			// Release the first three reads that appear with a shorter reply. The
			// fixed store demands only the first; a loop would consume gates 2-4.
			for (let index = 0; index < 3; index += 1) {
				await act(async () => gates[index]?.());
				await settle();
				expect(reads).toBe(1);
				await until(() => expect(entry.store.getState().latestReadState).toBe("unknown"));
			}
			expect(entry.store.getState().canonicalSession()).toBeNull();
			expect(hub.mutations()).toEqual([]);

			// The next explicit demand gets the matching length: authority returns
			// and the same failure's Retry is enabled again.
			replyLength = 10;
			let latest!: Promise<void>;
			act(() => {
				latest = entry.store.getState().ensureLatest();
			});
			await until(() => expect(reads).toBe(2));
			await act(async () => {
				gates[1]?.();
				await latest;
			});
			await settle();
			expect(entry.store.getState().latestReadState).toBe("ready");
			expect(pressable(tree, "Retry")?.props.disabled).toBe(false);
			expect(hub.mutations()).toEqual([]);
		} finally {
			hub.client.request = request;
		}
	});

	// A passive effect can miss a ready transition: an accepted read publishes
	// ready and a storage notification invalidates it in the same batch, before
	// effects flush. The demand token must reset on the store's ready
	// PUBLICATION, or it stays consumed and refuses the next read.
	it("resets the demand token when a ready publication is invalidated before effects flush", async () => {
		const ref = "ref-retry-ready-invalidated";
		readHistory.live = true;
		const { tree, entry, hub } = await mountTracked(failedTurn(ref, "go test exited 1"));
		await settle();
		const held = entry.store.getState().conversation!;
		expect(held.history).toBeDefined();
		act(() =>
			entry.store.setState({
				conversation: { ...held, history: held.history && { ...held.history, length: 10 } },
			}),
		);

		const runtime = getNativeMutationRuntime();
		const targetKey = nativeMutationTargetKey("hub-1", ref);
		const request = hub.client.request;
		let replyLength = 0;
		let reads = 0;
		const gates: (() => void)[] = [];
		hub.client.request = async (method, params) => {
			if (method !== "thread/read") return request(method, params);
			reads += 1;
			await new Promise<void>((resolve) => gates.push(resolve));
			const response = (await request(method, params)) as {
				snapshot?: { incarnation: string; length: number };
			};
			return { ...response, snapshot: { incarnation: "inc-1", length: replyLength } };
		};
		try {
			// Consume the token: a demanded read whose shorter reply is discarded.
			await act(async () => {
				await runtime.discardRecovery("no-such-row", targetKey);
			});
			await until(() => expect(reads).toBe(1));
			await act(async () => gates[0]?.());
			await until(() => expect(entry.store.getState().latestReadState).toBe("unknown"));
			expect(reads).toBe(1);

			// A real accepted read publishes ready, and a storage transition
			// invalidates it in the same publication turn — synchronously, before
			// any passive effect can observe the ready state.
			let invalidated = false;
			const invalidateBeforeEffects = entry.store.subscribe((state) => {
				if (invalidated || state.latestReadState !== "ready") return;
				invalidated = true;
				entry.store.setState({ latestReadState: "unknown" });
			});
			let accepted!: Promise<void>;
			replyLength = 10;
			try {
				act(() => {
					accepted = entry.store.getState().ensureLatest();
				});
				await until(() => expect(reads).toBe(2));
				await act(async () => {
					gates[1]?.();
					await accepted;
				});
			} finally {
				invalidateBeforeEffects();
			}
			expect(invalidated).toBe(true);

			// The invalidation re-runs the demand effect (reading -> unknown). The
			// token reset with the ready publication, so this demand reads exactly
			// once; a missed transition would leave it consumed and issue none.
			await until(() => expect(reads).toBe(3));
			replyLength = 10;
			await act(async () => gates[2]?.());
			await until(() => expect(entry.store.getState().latestReadState).toBe("ready"));
			await settle();
			expect(pressable(tree, "Retry")?.props.disabled).toBe(false);
			expect(hub.mutations()).toEqual([]);
			expect(reads).toBe(3);
		} finally {
			hub.client.request = request;
		}
	});

	it("submits nothing from a stale Retry callback after the canonical advances", async () => {
		const ref = "ref-retry-stale";
		const served = failedTurn(ref, "go test exited 1");
		const { tree, entry, hub } = await mountTracked(served);
		const stale = pressable(tree, "Retry")!.props.onPress;

		(served as unknown as { turns: unknown[] }).turns.push(nextTurn("turn_2", "completed"));
		await act(async () => {
			await entry.store.getState().ensureLatest();
		});
		await settle();
		expect(pressable(tree, "Retry")).toBeUndefined();
		act(() => stale());
		await settle();
		expect(hub.mutations()).toEqual([]);
	});

	it("submits a retained Retry through the live service after a reconnect, with no ghost", async () => {
		const ref = "ref-retry-reconnect";
		const served = failedTurn(ref, "go test exited 1");
		const { tree, hub, entry, route, seen } = await mountTracked(served);
		const document = drafts.open({ hubId: "hub-1", sessionRef: ref });
		const oldService = entry.service;
		const retained = pressable(tree, "Retry")!.props.onPress;

		// A rebuilt client resumes the same failed turn behind a fresh service.
		// Hold the new service's acceptance reply so the durable row is observable.
		const reconnected = hubClient(served);
		const held = deferred();
		let sent = false;
		const request = reconnected.client.request;
		reconnected.client.request = async (method, params) => {
			if (method === "turn/start") {
				sent = true;
				await held.gate;
			}
			return request(method, params);
		};
		harness.connection = { ...harness.connection, client: reconnected.client };
		act(() =>
			tree.update(
				<NativeConversationSessionsProvider>
					<RegistryProbe seen={seen} />
					<ConversationScreen route={route} navigation={navigation} />
				</NativeConversationSessionsProvider>,
			),
		);
		await settle();
		expect(entry.service).not.toBe(oldService);
		await until(() => expect(entry.store.getState().canonicalSession()).not.toBeNull());
		await until(() => expect(pressable(tree, "Retry")?.props.disabled).toBe(false));

		// The retained callback still names the same current failure. Activation
		// resolves the live service from the attachment instead of requiring the
		// render-captured one, so the enabled button is not a silent no-op.
		act(() => retained());
		await until(() => expect(sent).toBe(true));
		const runtime = getNativeMutationRuntime();
		const key = nativeMutationTargetKey("hub-1", ref);
		await act(async () => {
			await vi.waitFor(async () => expect(await runtime.storage.listOutbox(key)).toHaveLength(1));
		});
		const [row] = await runtime.storage.listOutbox(key);
		expect(row?.payload.input).toEqual([{ type: "text", text: "continue" }]);
		expect(hub.mutations()).toEqual([]);
		expect(document.getSnapshot().record.unconfirmed).toBeNull();
		await act(async () => held.release());
		await until(() => expect(reconnected.mutations()).toEqual(["turn/start"]));
	});

	it("submits nothing from a Retry callback after its attachment retires", async () => {
		const ref = "ref-retry-retired-callback";
		const { tree, hub, entry } = await mountTracked(failedTurn(ref, "go test exited 1"));
		const retained = pressable(tree, "Retry")!.props.onPress;
		act(() => tree.unmount());
		expect(entry.retired).toBe(true);
		act(() => retained());
		await settle();
		expect(hub.mutations()).toEqual([]);
		expect(await getNativeMutationRuntime().storage.listOutbox(nativeMutationTargetKey("hub-1", ref))).toEqual([]);
	});

	it("disables Retry while the live entry has no service, and submits once it rebinds", async () => {
		const ref = "ref-retry-no-service";
		const { tree, hub, entry, route, seen } = await mountTracked(failedTurn(ref, "go test exited 1"));
		const document = drafts.open({ hubId: "hub-1", sessionRef: ref });
		const runtime = getNativeMutationRuntime();
		const key = nativeMutationTargetKey("hub-1", ref);
		const rerender = () =>
			act(() =>
				tree.update(
					<NativeConversationSessionsProvider>
						<RegistryProbe seen={seen} />
						<ConversationScreen route={route} navigation={navigation} />
					</NativeConversationSessionsProvider>,
				),
			);
		const service = entry.service;
		expect(service).not.toBeNull();
		Object.defineProperty(entry, "service", { configurable: true, get: () => null });
		try {
			await rerender();
			await settle();
			// Readiness is otherwise ready; only the live service binding is absent.
			expect(entry.store.getState().latestReadState).toBe("ready");
			expect(entry.store.getState().pendingMutationsReady).toBe(true);
			expect(entry.store.getState().canonicalSession()).not.toBeNull();
			await until(() => expect(pressable(tree, "Retry")?.props.disabled).toBe(true));
			expect(pressable(tree, "Retry")?.props.accessibilityState).toEqual({ disabled: true });

			// A press in the null-service window fails closed and writes nothing.
			act(() => pressable(tree, "Retry")!.props.onPress());
			await settle();
			expect(hub.mutations()).toEqual([]);
			expect(await runtime.storage.listOutbox(key)).toEqual([]);
			expect(document.getSnapshot().record.unconfirmed).toBeNull();
		} finally {
			Reflect.deleteProperty(entry, "service");
		}

		// Once the service is live again the same action submits exactly once.
		await rerender();
		await until(() => expect(pressable(tree, "Retry")?.props.disabled).toBe(false));
		await press(tree, "Retry");
		await until(() => expect(hub.mutations()).toEqual(["turn/start"]));
		expect(document.getSnapshot().record.unconfirmed).toBeNull();
	});

	it("refuses a retained Retry callback after the same turn's error changes to Sign in", async () => {
		const ref = "ref-retry-error-signin";
		const served = failedTurn(ref, "go test exited 1");
		const { tree, entry, hub } = await mountTracked(served);
		const stale = pressable(tree, "Retry")!.props.onPress;

		// The authoritative refresh keeps the same turn but changes its error to
		// one that requires Sign in. The captured Retry callback must not submit.
		(served as unknown as { turns: { error?: unknown }[] }).turns[0]!.error = {
			message: "Sign-in expired",
		};
		await act(async () => {
			await entry.store.getState().ensureLatest();
		});
		await settle();
		expect(pressable(tree, "Retry")).toBeUndefined();
		act(() => stale());
		await settle();
		expect(hub.mutations()).toEqual([]);
		expect(hub.requests.filter((request) => request.method === "turn/start")).toEqual([]);
	});

	it("refuses a retained Retry callback after the same turn's error is removed", async () => {
		const ref = "ref-retry-error-removed";
		const served = failedTurn(ref, "go test exited 1");
		const { tree, entry, hub } = await mountTracked(served);
		const stale = pressable(tree, "Retry")!.props.onPress;

		const turn = (served as unknown as { turns: { status: string; error?: unknown }[] }).turns[0]!;
		turn.status = "completed";
		delete turn.error;
		await act(async () => {
			await entry.store.getState().ensureLatest();
		});
		await settle();
		expect(pressable(tree, "Retry")).toBeUndefined();
		act(() => stale());
		await settle();
		expect(hub.mutations()).toEqual([]);
	});

	it.each([
		["Sign in", { message: "Sign-in expired" }],
		["no error", undefined],
	] as const)(
		"refuses Retry at the runtime boundary when the same turn's error becomes %s while startup is held",
		async (name, nextError) => {
			const ref = `ref-retry-boundary-${name === "Sign in" ? "signin" : "removed"}`;
			const served = failedTurn(ref, "go test exited 1");
			const { tree, hub, entry } = await mountTracked(served);
			const document = drafts.open({ hubId: "hub-1", sessionRef: ref });
			const runtime = getNativeMutationRuntime();
			const targetKey = nativeMutationTargetKey("hub-1", ref);
			const start = runtime.start.bind(runtime);
			const held = deferred();
			let hold = true;
			runtime.start = async () => {
				if (hold) {
					hold = false;
					await held.gate;
				}
				return start();
			};
			try {
				act(() => pressable(tree, "Retry")!.props.onPress());
				await until(() => expect(document.getSnapshot().submitting).toBe(true));
				const turn = (served as unknown as { turns: { status: string; error?: unknown }[] }).turns[0]!;
				if (nextError === undefined) {
					turn.status = "completed";
					delete turn.error;
				} else {
					turn.error = nextError;
				}
				await act(async () => {
					await entry.store.getState().ensureLatest();
				});
				await act(async () => {
					held.release();
					await vi.waitFor(() => expect(document.getSnapshot().submitting).toBe(false));
				});
			} finally {
				runtime.start = start;
			}
			expect(hub.mutations()).toEqual([]);
			expect(hub.requests.filter((request) => request.method === "turn/start")).toEqual([]);
			expect(await runtime.storage.listOutbox(targetKey)).toEqual([]);
			// The proven not-sent refusal cleared only its uncertainty marker.
			expect(document.getSnapshot().record.unconfirmed).toBeNull();
		},
	);

	it("refuses Retry before commit when its view retires while startup is held, preserving the draft", async () => {
		const ref = "ref-retry-retire-before";
		const { tree, hub } = await mountTracked(failedTurn(ref, "go test exited 1"));
		const destination = { hubId: "hub-1", sessionRef: ref };
		const document = drafts.open(destination);
		await type(tree, "keep this");
		act(() =>
			document.addImage({ id: "draft-image", marker: 1, mediaType: "image/png", name: "draft.png", data: "AQID" }),
		);
		const before = document.getSnapshot().record;
		const runtime = getNativeMutationRuntime();
		const targetKey = nativeMutationTargetKey("hub-1", ref);
		const start = runtime.start.bind(runtime);
		const held = deferred();
		let hold = true;
		runtime.start = async () => {
			if (hold) {
				hold = false;
				await held.gate;
			}
			return start();
		};
		try {
			act(() => pressable(tree, "Retry")!.props.onPress());
			await until(() => expect(document.getSnapshot().submitting).toBe(true));
			// The only view goes away while the runtime is still starting.
			act(() => tree.unmount());
			await act(async () => {
				held.release();
				await vi.waitFor(() => expect(document.getSnapshot().submitting).toBe(false));
			});
		} finally {
			runtime.start = start;
		}
		expect(hub.mutations()).toEqual([]);
		expect(await runtime.storage.listOutbox(targetKey)).toEqual([]);
		expect(document.getSnapshot().record).toEqual(before);
	});

	it("leaves a committed Retry row owned by the runtime after its view retires", async () => {
		const ref = "ref-retry-retire-after";
		const { tree, hub } = await mountTracked(failedTurn(ref, "go test exited 1"));
		const runtime = getNativeMutationRuntime();
		const key = nativeMutationTargetKey("hub-1", ref);
		const held = deferred();
		const request = hub.client.request;
		let hold = true;
		hub.client.request = async (method, params) => {
			if (method === "turn/start" && hold) {
				hold = false;
				await held.gate;
			}
			return request(method, params);
		};
		act(() => pressable(tree, "Retry")!.props.onPress());
		await act(async () => {
			await vi.waitFor(async () => expect(await runtime.storage.listOutbox(key)).toHaveLength(1));
		});
		act(() => tree.unmount());
		// Committed before retirement: the runtime owns the row, not the view.
		expect(await runtime.storage.listOutbox(key)).toHaveLength(1);
		await act(async () => {
			held.release();
		});
		const [row] = await runtime.storage.listOutbox(key);
		if (row) await runtime.storage.discardUndelivered(row.clientMutationId, key);
	});

	it("creates one identity for repeated Retry presses while the precondition is pending", async () => {
		const ref = "ref-retry-repeat";
		const { tree, hub } = await mountTracked(failedTurn(ref, "go test exited 1"));
		const document = drafts.open({ hubId: "hub-1", sessionRef: ref });
		const runtime = getNativeMutationRuntime();
		const start = runtime.start.bind(runtime);
		const held = deferred();
		let hold = true;
		runtime.start = async () => {
			if (hold) {
				hold = false;
				await held.gate;
			}
			return start();
		};
		const button = pressable(tree, "Retry")!;
		try {
			act(() => button.props.onPress());
			await until(() => expect(document.getSnapshot().submitting).toBe(true));
			// A second press cannot enter the shared document while the first
			// operation owns it.
			act(() => button.props.onPress());
		} finally {
			await act(async () => {
				held.release();
				await vi.waitFor(() => expect(document.getSnapshot().submitting).toBe(false));
			});
			runtime.start = start;
		}
		await act(async () => {
			await vi.waitFor(() => expect(hub.mutations()).toEqual(["turn/start"]));
		});
		expect(hub.requests.filter((request) => request.method === "turn/start")).toHaveLength(1);
	});
});

// Send is the single recovery action: a fenced session's error row offers no
// Resume of its own, and the composer's Send carries that resume.
it("resumes a paused session from Send, not from its error row", async () => {
	const { tree, hub } = await mount(failedTurn("ref-error-resume", "go test exited 1", true));
	expect(pressable(tree, "Resume")).toBeUndefined();
	act(() => field(tree)?.props.onChangeText("carry on"));
	await press(tree, "Send and resume");
	expect(
		hub.requests.filter((request) => request.method === "resumeThread").map((request) => request.params.ref),
	).toEqual(["ref-error-resume"]);
});

it("resumes with the edited recovery context, consuming its source once", async () => {
	const ref = "ref-error-resume-recovery";
	const served = thread(ref, "idle");
	(served as unknown as { evener: Record<string, unknown> }).evener.resumeRequired = true;
	const { tree, hub } = await mount(served);
	const runtime = getNativeMutationRuntime();
	const targetKey = nativeMutationTargetKey("hub-1", ref);
	const text = "Run /review on [image 1]";
	const mention = { kind: "skill" as const, name: "review", offset: 4 };
	const source = await runtime.storage.enqueueIntent({
		targetRef: targetKey,
		threadId: served.id,
		instanceId: "instance",
		method: "turn/queue",
		payload: {
			ref,
			input: [
				{ type: "text", text, mentions: [mention] },
				{ type: "skill", name: "review" },
				{ type: "image", mediaType: "image/png", data: "AQID", name: "sample.png" },
			],
		},
		attachments: [{ presentationId: "recovery-image", marker: 1, name: "sample.png", mediaType: "image/png" }],
		composerText: text,
		composerMentions: [mention],
		optimisticDisplay: { method: "turn/queue" },
	});
	await act(async () => {
		const recovery = await runtime.editQueuedMessage(source.clientMutationId, targetKey);
		expect(recovery).toMatchObject({ recoveryKind: "editing", clientMutationId: source.clientMutationId });
	});
	await settle();

	const input = field(tree);
	if (!input) throw new Error("no Message field after queued recovery Edit");
	expect(nativeDrafts().read({ hubId: "hub-1", sessionRef: ref }).composer?.recoveryIds).toContain(
		source.clientMutationId,
	);
	act(() => input.props.onChangeText(`${text} now`));
	await settle();
	const observedReceipts: Array<Awaited<ReturnType<typeof runtime.storage.readRecoveryResend>>> = [];
	const readRecoveryResend = runtime.storage.readRecoveryResend.bind(runtime.storage);
	vi.spyOn(runtime.storage, "readRecoveryResend").mockImplementation(async (...args) => {
		const result = await readRecoveryResend(...args);
		if (result) observedReceipts.push(result);
		return result;
	});
	const submit = vi.spyOn(runtime, "submit");
	await press(tree, "Send and resume");

	expect(await runtime.storage.listRecovery(targetKey)).toEqual([]);
	expect(hub.requests.filter((request) => request.method === "resumeThread")).toHaveLength(1);
	const admitted = await runtime.read(targetKey);
	expect(admitted.outbox).toHaveLength(1);
	expect(admitted.outbox[0]?.payload).toMatchObject({
		input: expect.arrayContaining([
			expect.objectContaining({ type: "skill", name: "review" }),
			expect.objectContaining({ type: "image", mediaType: "image/png", data: "AQID" }),
		]),
	});
	expect(submit.mock.calls.at(-1)?.[0]).toMatchObject({
		recoverySourceIds: [source.clientMutationId],
		composer: {
			text: `${text} now`,
			mentions: [mention],
			skillNames: ["review"],
			attachments: [expect.objectContaining({ marker: 1, mediaType: "image/png", data: "AQID" })],
		},
	});
	expect(observedReceipts).toHaveLength(1);
	expect(observedReceipts[0]).toMatchObject({
		targetRef: targetKey,
		sourceIds: [source.clientMutationId],
		clientMutationId: expect.any(String),
	});
});
// A resume that fails leaves its message parked, which makes the next Send
// route to queue instead of resuming again. The queued message must still
// drive recovery: otherwise the session stays fenced, and every later message
// piles up behind one that can never go out.
it("still resumes from a later Send that queues behind a failed resume", async () => {
	const { tree } = await mount(failedTurn("ref-resume-retry", "go test exited 1", true));
	const runtime = getNativeMutationRuntime();
	// The runtime is a singleton shared by every test in this file, so a spy
	// keeps whatever calls earlier tests made. Scope this test's view to its own
	// target rather than trusting a call index.
	const settle = vi.spyOn(runtime, "settleTarget");
	settle.mockClear();
	const settlesFor = () => settle.mock.calls.filter(([, ref]) => ref === "ref-resume-retry");
	// The first resume cannot run, as when the daemon is still shutting down.
	settle.mockRejectedValueOnce(new Error("resume failed"));

	act(() => field(tree)?.props.onChangeText("first"));
	await press(tree, "Send and resume");
	expect(settlesFor()).toHaveLength(1);

	// The first message is still parked, so this Send queues behind it rather
	// than resuming. It must nevertheless try the resume again.
	act(() => field(tree)?.props.onChangeText("second"));
	await press(tree, "Queue message");
	expect(settlesFor()).toHaveLength(2);
	expect(settlesFor()[1]?.[3]).toMatchObject({ mayResumeFenced: expect.any(Function) });
	settle.mockRestore();
});

// The queued retry must follow an identity-changing resume, or the work it
// dispatches lands on a ref the mounted screen is not showing.
it("follows an identity-changing resume driven by a queued Send", async () => {
	vi.mocked(navigation.replace).mockClear();
	const { tree } = await mount(failedTurn("ref-queued-identity", "go test exited 1", true));
	const runtime = getNativeMutationRuntime();
	const settle = vi.spyOn(runtime, "settleTarget");
	settle.mockClear();
	const settlesFor = () => settle.mock.calls.filter(([, ref]) => ref === "ref-queued-identity");
	settle.mockRejectedValueOnce(new Error("resume failed"));

	act(() => field(tree)?.props.onChangeText("first"));
	await press(tree, "Send and resume");
	act(() => field(tree)?.props.onChangeText("second"));
	await press(tree, "Queue message");

	// The queued retry passes the same identity-following callback the resume
	// branch does, so a resolved ref moves the pane with the work.
	expect(settlesFor()).toHaveLength(2);
	const options = settlesFor()[1]?.[3] as { onResolvedIdentity?: (ref: string) => void } | undefined;
	if (!options?.onResolvedIdentity) throw new Error("queued recovery passed no identity callback");
	act(() => field(tree)?.props.onChangeText("handoff draft"));
	act(() => options.onResolvedIdentity?.("ref-queued-identity-resolved"));
	expect(navigation.replace).toHaveBeenCalledWith(
		"Conversation",
		expect.objectContaining({ ref: "ref-queued-identity-resolved" }),
	);
	expect(nativeDrafts().read({ hubId: "hub-1", sessionRef: "ref-queued-identity-resolved" }).draft).toBe(
		"handoff draft",
	);
	expect(nativeDrafts().read({ hubId: "hub-1", sessionRef: "ref-queued-identity" })).toEqual({
		draft: "",
		unconfirmed: null,
	});
	settle.mockRestore();
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
	// The chip steps aside while the composer is collapsed, so reach for it first.
	act(() => field(tree)?.props.onFocus());
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
	// The chip steps aside while the composer is collapsed, so reach for it first.
	act(() => field(tree)?.props.onFocus());
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
				<SessionScreen route={route as unknown as ConversationScreenProps["route"]} navigation={navigation} />,
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
		expect(markers(tree, "session-notice")).toEqual(["restartNeeded"]);
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
				<SessionScreen route={route as unknown as ConversationScreenProps["route"]} navigation={navigation} />,
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
		expect(pressable(tree, "Restart session")?.props.accessibilityState).toMatchObject({ busy: false });
	});

	it("says so in the notice when the session stopped but couldn't start again", async () => {
		const { tree, hub } = await mount(restartNeeded("ref-restart-fails"));
		hub.client.resumeThread = async (ref: string) => {
			hub.requests.push({ method: "resumeThread", params: { ref } });
			throw new Error("resume refused");
		};
		await press(tree, "Restart session");
		expect(hasTestID(tree, "session-notice-error")).toBe(true);
		expect(pressable(tree, "Restart session")?.props.disabled).toBe(false);
	});

	it("says so in the notice when the stop fails", async () => {
		const { tree, hub } = await mount(restartNeeded("ref-stop-fails"));
		hub.client.forceStop = async () => {
			throw new Error("stop refused");
		};
		await press(tree, "Restart session");
		expect(hub.requests.filter((request) => request.method === "resumeThread")).toEqual([]);
		expect(hasTestID(tree, "session-notice-error")).toBe(true);
	});

	it("keeps the composer for a paused session, so Send is how it resumes", async () => {
		const served = thread("ref-paused", "idle");
		(served as unknown as { evener: Record<string, unknown> }).evener.resumeRequired = true;
		const { tree, hub } = await mount(served);
		expect(markers(tree, "session-notice")).toEqual([]);
		expect(field(tree)).toBeDefined();
		act(() => field(tree)?.props.onChangeText("carry on"));
		await press(tree, "Send and resume");
		expect(
			hub.requests.filter((request) => request.method === "resumeThread").map((request) => request.params.ref),
		).toEqual(["ref-paused"]);
	});
});

it("Retry continues an image-only failed turn without reattaching it", async () => {
	const served = thread("ref-retry-image-only", "idle");
	(served as unknown as { turns: unknown[] }).turns = [
		{
			id: "turn_1",
			status: "failed",
			itemsView: "default",
			error: { message: "go test exited 1" },
			items: [
				{
					id: "u-1",
					turnId: "turn_1",
					type: "userMessage",
					status: "completed",
					text: "(attached image 1: plot.png)",
					images: [{ mediaType: "image/png", data: "image-bytes", name: "plot.png" }],
				},
			],
		},
	];
	const { tree, hub } = await mount(served);
	expect(pressable(tree, "Retry")).toBeDefined();
	await press(tree, "Retry");
	expect(hub.mutations()).toEqual(["turn/start"]);
	const start = hub.requests.find((request) => request.method === "turn/start");
	expect(start?.params.input).toMatchInlineSnapshot(`
		[
		  {
		    "text": "continue",
		    "type": "text",
		  },
		]
	`);
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
	expect(toasts(tree)).toEqual(["note:saved-reading"]);
});

it("shows no notes bar for a session with nothing shared", async () => {
	const served = thread("ref-no-notes", "idle");
	(served as unknown as { evener: Record<string, unknown> }).evener.capabilities = {
		...CAPABILITIES,
		sharedNotes: true,
	};
	const { tree } = await mount(served);
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
			if (attempts === 1) {
				// Fail a timer turn late, as a real dropped request does, so the
				// test can't count on the failure landing within a fixed turn.
				await new Promise((resolve) => setTimeout(resolve, 0));
				throw new Error("offline");
			}
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
	// The session stayed open, connected and in front the whole time: the
	// screen retries the failed save on its own rather than waiting for an
	// unrelated reconnect or remount to notice it. The retry runs a timer turn
	// after the failure lands, and the failure can itself land after any one
	// timer turn, so wait for the attempt rather than counting turns (#4119).
	await until(() => expect(attempts).toBe(2));
	expect(
		hub.requests.filter((request) => request.method === "notes/human/set").map((request) => request.params.note),
	).toEqual(["first try"]);
});

it("sends a note kept on this phone from a failed save once the session opens connected", async () => {
	nativePlatform.kv.set("evener.native.note-draft.hub-1", JSON.stringify({ "ref-kept-note": "kept from before" }));
	const served = thread("ref-kept-note", "idle");
	(served as unknown as { evener: Record<string, unknown> }).evener.capabilities = {
		...CAPABILITIES,
		sharedNotes: true,
	};
	const { hub } = await mount(served);
	expect(
		hub.requests.filter((request) => request.method === "notes/human/set").map((request) => request.params.note),
	).toEqual(["kept from before"]);
	expect(nativePlatform.kv.has("evener.native.note-draft.hub-1")).toBe(false);
});

it("keeps a note from a failed save on this phone while the session can't take notes", async () => {
	nativePlatform.kv.set("evener.native.note-draft.hub-1", JSON.stringify({ "ref-kept-ended": "kept from before" }));
	const served = thread("ref-kept-ended", "idle");
	(served as unknown as { status: { type: string } }).status = { type: "ended" };
	(served as unknown as { evener: Record<string, unknown> }).evener.capabilities = {
		...CAPABILITIES,
		sharedNotes: true,
	};
	const { hub } = await mount(served);
	expect(hub.requests.filter((request) => request.method === "notes/human/set")).toEqual([]);
	expect(nativePlatform.kv.has("evener.native.note-draft.hub-1")).toBe(true);
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

// Everything waiting to reach the agent is the transcript's last rows, just
// above the composer, and scrolls with it (spec 8.5; Jesse, 2026-10-03).
describe("queued messages at the transcript's end (spec 8.5)", () => {
	const bottomBar = (tree: ReactTestRenderer) =>
		tree.root.find((node) => String(node.type) === "View" && node.props.testID === "session-bottom-bar");
	const lastItem = (tree: ReactTestRenderer) => {
		const row = transcriptList(tree)
			.findAll((node) => String(node.type) === "Item")
			.at(-1);
		if (!row) throw new Error("no transcript rows");
		return row;
	};
	const lastRow = (tree: ReactTestRenderer) => textOf(lastItem(tree));
	/** The last row's ghost, by state. */
	const lastGhost = (tree: ReactTestRenderer) => markers(lastItem(tree), "ghost");

	it("shows a queued message as the transcript's last row, not in the bottom bar", async () => {
		const served = thread("ref-queue-row", "active", false, ["check the logs"]);
		(served as unknown as { turns: unknown[] }).turns = [askReplyTurn("turn_1")];
		const { tree } = await mount(served);
		expect(lastRow(tree)).toContain("check the logs");
		expect(lastGhost(tree)).toEqual(["queued"]);
		expect(textOf(transcriptList(tree))).toContain("ask turn_1");
		expect(textOf(bottomBar(tree))).not.toContain("check the logs");
	});

	// A message you queue shows once at every frame on its way: its ghost
	// while it sends and while it waits in the queue, then your message once
	// its turn starts, never beside its ghost.
	it("shows a queued message once at every frame until it lands as your message", async () => {
		readHistory.live = true;
		const served = thread("ref-queue-lands", "active");
		(served as unknown as { turns: unknown[] }).turns = [
			{
				id: "turn_1",
				status: "inProgress",
				itemsView: "default",
				items: [
					{
						id: "u-turn_1",
						turnId: "turn_1",
						type: "userMessage",
						status: "completed",
						text: "ask turn_1",
						transcriptKey: "turn_1:0:0",
						position: { entry: 0, item: 0 },
					},
				],
			},
		];
		const { tree, hub } = await mount(served);
		const shown = () => textOf(transcriptList(tree)).split("check the logs").length - 1;
		// A frame that names a mutation id settles its durable record, and that
		// settlement is async: await the same act block so its re-render lands
		// inside it.
		const frame = async (notification: Record<string, unknown>) =>
			act(async () => hub.notify(notification as unknown as AnyNotification));
		await type(tree, "check the logs");
		await press(tree, "Queue message");
		const clientMutationId = String(hub.requests.find((r) => r.method === "turn/queue")?.params.clientMutationId);
		expect(shown()).toBe(1);
		const queue = (texts: string[], revision: number) => ({
			...queueState(texts, revision),
			clientMutationIds: texts.map(() => clientMutationId),
		});
		await frame({
			method: "thread/queueChanged",
			params: { threadId: served.id, ref: "ref-queue-lands", queue: queue(["check the logs"], 1) },
		});
		expect(shown()).toBe(1);
		expect(lastGhost(tree)).toEqual(["queued"]);
		// The turn ends and the hub takes the message for the next one: the
		// queue frame comes first, then the message's own item.
		await frame({
			method: "thread/queueChanged",
			params: { threadId: served.id, ref: "ref-queue-lands", queue: queue([], 2) },
		});
		expect(shown()).toBe(1);
		await frame({
			method: "history/updated",
			params: {
				threadId: served.id,
				ref: "ref-queue-lands",
				...READ_HISTORY_IDENTITY,
				items: [
					{
						id: "u-turn_2",
						turnId: "turn_2",
						type: "userMessage",
						status: "completed",
						text: "check the logs",
						clientMutationId,
						transcriptKey: "turn_2:1:0",
						position: { entry: 1, item: 0 },
					},
				],
			},
		});
		expect(shown()).toBe(1);
		expect(markers(transcriptList(tree), "ghost")).toEqual([]);
	});

	// The usage lines (token counts, estimated cost) close the conversation;
	// the ghosts still come last, just above the composer.
	it("keeps the ghosts last, below the usage lines", async () => {
		displayPrefs.hubId = "hub-1";
		displayPrefs.config = makeTranscriptDisplayConfig({ kind: "preset", level: "intent" }, { tokenCounts: true });
		const served = thread("ref-usage-ghost", "active", false, ["check the logs"]);
		(served as unknown as { turns: unknown[] }).turns = [askReplyTurn("turn_1")];
		(served as unknown as { evener: Record<string, unknown> }).evener.usage = { inputTokens: 1200, outputTokens: 300 };
		const { tree } = await mount(served);
		const text = textOf(transcriptList(tree));
		expect(text).toContain("Input: 1,200");
		expect(lastRow(tree)).toContain("check the logs");
		expect(text.indexOf("Input: 1,200")).toBeLessThan(text.indexOf("check the logs"));
	});

	// With both settings off there are no usage lines, and no row stands in
	// for them between the conversation and the ghosts.
	it("adds no usage row when there are no usage lines to show", async () => {
		displayPrefs.hubId = "hub-1";
		displayPrefs.config = makeTranscriptDisplayConfig({ kind: "preset", level: "intent" });
		const served = thread("ref-usage-none", "active", false, ["check the logs"]);
		(served as unknown as { turns: unknown[] }).turns = [askReplyTurn("turn_1")];
		const { tree } = await mount(served);
		const rows = transcriptList(tree).props.data as { kind: string }[];
		expect(rows.map((row) => row.kind)).toEqual(["user", "assistant", "ghost"]);
	});

	it("paints a swiped ghost the page it sits on, and keeps it there while the dock is open", async () => {
		const palette = paletteFor("light");
		const backdrop = (tree: ReactTestRenderer) =>
			transcriptList(tree).findByProps({ testID: "swipe-row-content" }).props.style.backgroundColor;
		const composing = (await mount(thread("ref-swipe-composer", "active", false, ["check the logs"]))).tree;
		expect(backdrop(composing)).toBe(palette.page);
		const asking = (await mount(thread("ref-swipe-dock", "awaiting", true, ["check the logs"]))).tree;
		expect(field(asking)).toBeUndefined();
		expect(backdrop(asking)).toBe(palette.page);
	});

	it("steers with a queued message the agent hasn't reached yet", async () => {
		const { tree, hub } = await mount(thread("ref-steer-now", "active", false, ["check the logs"]));
		expect(renderedText(tree)).toContain("check the logs");
		expect(ghostStates(tree)).toEqual(["queued"]);
		await press(tree, "Steer now");
		const promote = hub.requests.filter((request) => request.method === "turn/promoteQueuedAsSteer");
		expect(promote).toHaveLength(1);
		expect(promote[0]?.params).toMatchObject({ index: 0, expectedEntryId: "queue_1" });
		expect(saysSteerFailed(tree)).toBe(false);
	});

	// Between the press and the agent's next step, the hub holds the steer as
	// a pending mutation; the phone shows it as its own (spec 8.5).
	// Send now on a queue a Stop held is a promote too: it starts the turn
	// that takes it.
	const STEERS_FROM_QUEUE = [
		["Steer now", "active", "turn/promoteQueuedAsSteer", ["check the logs"]],
		["Send now", "idle", "turn/promoteQueuedAsSteer", ["check the logs"]],
		["Steer all now", "active", "turn/drainAsSteer", ["check the logs", "and the metrics"]],
	] as const;
	// Starts the action, as a press does, and leaves it running. Steer all
	// now is the Queue sheet's, which shows what it hands back.
	async function steerFromQueue(tree: ReactTestRenderer, ref: string, label: string) {
		let handedBack: Promise<unknown> | undefined;
		if (label === "Steer all now")
			await act(async () => {
				handedBack = queueHosts.get(sheetKey("hub-1", ref))?.steerAll?.();
			});
		else await press(tree, label);
		return { handedBack };
	}
	it.each(STEERS_FROM_QUEUE)("shows %s as steering until the agent takes it", async (label, status, method, queued) => {
		const ref = `ref-steering-${label}`;
		const served = thread(ref, status, false, [...queued]);
		const { tree, hub } = await mount(served);
		const client = hub.client as { request: (method: string, params: Record<string, unknown>) => Promise<unknown> };
		const request = client.request;
		client.request = async (requested, params) => {
			const answer = await request(requested, params);
			if (requested === method) {
				// The next read: the queue is empty, and the hub holds the steer
				// for the agent's next step.
				const evener = (served as unknown as { evener: Record<string, unknown> }).evener;
				evener.queue = queueState([], 1);
				evener.pendingMutations = [
					{
						clientMutationId: params.clientMutationId,
						method,
						input: queued.map((text) => ({ type: "text", text })),
						executionState: "accepted",
						projectionState: "pending",
					},
				];
				// A drain's receipt names every entry it took.
				const { receipt } = answer as { receipt: Record<string, unknown> };
				return { receipt: { queueEntryIds: queued.map((_, index) => `queue_${index + 1}`), ...receipt } };
			}
			return answer;
		};
		await steerFromQueue(tree, ref, label);
		await settle();
		// The read settles the phone's own record of the steer, and the hub's
		// pending steer is still this phone's.
		act(() =>
			hub.notify({
				method: "evener/thread/resync",
				params: { ref, threadId: served.id },
			} as AnyNotification),
		);
		await settle();
		expect(hub.requests.filter((entry) => entry.method === "thread/read").length).toBeGreaterThan(1);
		expect(hub.requests.filter((entry) => entry.method === method)).toHaveLength(1);
		expect(lastRow(tree)).toContain("check the logs");
		expect(lastGhost(tree)).toEqual(["steering"]);
		expect(saysSteerFailed(tree)).toBe(false);
	});

	// The hub took the steer, but its answer never reached the phone: the
	// phone sends it again under the same id, the hub replays it, and it
	// shows as steering. Nothing says the steer failed.
	it.each(STEERS_FROM_QUEUE)(
		"shows %s as steering when the hub's answer was lost",
		async (label, status, method, queued) => {
			const ref = `ref-lost-${label}`;
			const served = thread(ref, status, false, [...queued]);
			const { tree, hub } = await mount(served);
			const client = hub.client as { request: (method: string, params: Record<string, unknown>) => Promise<unknown> };
			const request = client.request;
			let lost = false;
			client.request = async (requested, params) => {
				const answer = await request(requested, params);
				if (requested !== method || lost) return answer;
				lost = true;
				hub.notify({
					method: "thread/queueChanged",
					params: { threadId: served.id, ref, queue: queueState([], 1) },
				} as AnyNotification);
				throw new Error("connection lost");
			};
			const { handedBack } = await steerFromQueue(tree, ref, label);
			await settle();
			expect(saysSteerFailed(tree)).toBe(false);
			expect(await handedBack).toBeFalsy();
			await act(async () => {
				await getNativeMutationRuntime().connectionReady();
			});
			await settle();
			const sent = hub.requests.filter((entry) => entry.method === method);
			expect(sent).toHaveLength(2);
			expect(sent[1]?.params.clientMutationId).toBe(sent[0]?.params.clientMutationId);
			expect(lastRow(tree)).toContain("check the logs");
			expect(lastGhost(tree)).toEqual(["steering"]);
			expect(saysSteerFailed(tree)).toBe(false);
		},
	);

	// The hub's queue frame can reach the phone before its answer does: the
	// message leaves the queue, and the transcript's end still shows it on
	// its way.
	it.each(STEERS_FROM_QUEUE)(
		"keeps %s at the transcript's end while the hub's answer is on its way",
		async (label, status, method, queued) => {
			const ref = `ref-frames-first-${label}`;
			const served = thread(ref, status, false, [...queued]);
			const { tree, hub } = await mount(served);
			const client = hub.client as { request: (method: string, params: Record<string, unknown>) => Promise<unknown> };
			const request = client.request;
			let answer: () => void = () => undefined;
			const answered = new Promise<void>((resolve) => {
				answer = resolve;
			});
			client.request = async (requested, params) => {
				if (requested !== method) return request(requested, params);
				const reply = request(requested, params);
				hub.notify({
					method: "thread/queueChanged",
					params: { threadId: served.id, ref, queue: queueState([], 1) },
				} as AnyNotification);
				await answered;
				return reply;
			};
			await steerFromQueue(tree, ref, label);
			await settle();
			expect(hub.requests.filter((entry) => entry.method === method)).toHaveLength(1);
			expect(lastRow(tree)).toContain("check the logs");
			expect(lastGhost(tree)).not.toContain("queued");
			await act(async () => answer());
			await settle();
			expect(lastRow(tree)).toContain("check the logs");
			expect(lastGhost(tree)).toEqual(["steering"]);
		},
	);

	const rowsWith = (tree: ReactTestRenderer, text: string) =>
		transcriptList(tree)
			.findAll((node) => String(node.type) === "Item")
			.map(textOf)
			.filter((row) => row.includes(text));
	/** The text of each transcript row whose ghost is `state`. */
	const rowsWithGhost = (tree: ReactTestRenderer, state: string) =>
		transcriptList(tree)
			.findAll((node) => String(node.type) === "Item")
			.filter((row) => markers(row, "ghost").includes(state))
			.map(textOf);

	// Until the hub answers, the message shows once, on its way, and there is
	// no second Steer now (or Steer all now) to press for it.
	it.each(STEERS_FROM_QUEUE)(
		"shows a message once, with nothing to press again, while its %s waits for the hub",
		async (label, status, method, queued) => {
			const ref = `ref-pressed-once-${label}`;
			const { tree, hub } = await mount(thread(ref, status, false, [...queued]));
			const client = hub.client as { request: (method: string, params: Record<string, unknown>) => Promise<unknown> };
			const request = client.request;
			client.request = async (requested, params) => {
				if (requested !== method) return request(requested, params);
				// The hub never answers: record the request and leave it there.
				hub.requests.push({ method: requested, params });
				return new Promise(() => undefined);
			};
			await steerFromQueue(tree, ref, label);
			await settle();
			expect(hub.requests.filter((entry) => entry.method === method)).toHaveLength(1);
			expect(rowsWith(tree, "check the logs")).toHaveLength(1);
			expect(pressable(tree, "Steer now")).toBeUndefined();
			expect(pressable(tree, "Send now")).toBeUndefined();
			expect(queueHosts.get(sheetKey("hub-1", ref))?.steerAll).toBeUndefined();
		},
	);

	// Steer all now would take the message Steer now is already steering with.
	it("withholds Steer all now while a Steer now waits for the hub", async () => {
		const ref = "ref-steer-all-after-promote";
		const { tree, hub } = await mount(thread(ref, "active", false, ["check the logs", "one", "two"]));
		expect(queueHosts.get(sheetKey("hub-1", ref))?.steerAll).toBeDefined();
		const client = hub.client as { request: (method: string, params: Record<string, unknown>) => Promise<unknown> };
		const request = client.request;
		client.request = async (requested, params) => {
			if (requested !== "turn/promoteQueuedAsSteer") return request(requested, params);
			// The hub never answers: record the request and leave it there.
			hub.requests.push({ method: requested, params });
			return new Promise(() => undefined);
		};
		const [steerNow] = tree.root.findAll(
			(node) => node.props.accessibilityLabel === "Steer now" && typeof node.props.onPress === "function",
		);
		act(() => steerNow?.props.onPress());
		await settle();
		expect(rowsWithGhost(tree, "queued")).toHaveLength(2);
		expect(queueHosts.get(sheetKey("hub-1", ref))?.steerAll).toBeUndefined();
		// Typed, it waits too, and says why.
		await type(tree, "/drain-as-steer");
		await press(tree, "Drain queue");
		expect(hub.requests.filter((entry) => entry.method === "turn/drainAsSteer")).toEqual([]);
		expect(actionErrors(tree)).toEqual(["refused"]);
		// Nothing was tried, so the command stays in the composer.
		expect(field(tree)?.props.value).toBe("/drain-as-steer");
	});

	// /drain-as-steer is Steer all now typed: the same outbox, so a lost answer
	// isn't a failure, and the hub's pending steer reads as this phone's.
	it("sends /drain-as-steer through the outbox, as Steer all now", async () => {
		const ref = "ref-drain-command";
		const queued = ["check the logs", "and the metrics"];
		const served = thread(ref, "active", false, queued);
		const { tree, hub } = await mount(served);
		const client = hub.client as { request: (method: string, params: Record<string, unknown>) => Promise<unknown> };
		const request = client.request;
		client.request = async (requested, params) => {
			const answer = await request(requested, params);
			if (requested !== "turn/drainAsSteer") return answer;
			// The hub took the drain, and its answer was lost.
			const evener = (served as unknown as { evener: Record<string, unknown> }).evener;
			evener.queue = queueState([], 1);
			evener.pendingMutations = [
				{
					clientMutationId: params.clientMutationId,
					method: requested,
					input: queued.map((text) => ({ type: "text", text })),
					executionState: "accepted",
					projectionState: "pending",
				},
			];
			throw new Error("connection lost");
		};
		await type(tree, "/drain-as-steer");
		await press(tree, "Drain queue");
		const sent = hub.requests.filter((entry) => entry.method === "turn/drainAsSteer");
		expect(sent.map((entry) => entry.params)).toMatchObject([{ expectedQueueRevision: 0, input: [] }]);
		act(() =>
			hub.notify({
				method: "evener/thread/resync",
				params: { ref, threadId: served.id },
			} as AnyNotification),
		);
		await settle();
		expect(lastRow(tree)).toContain("check the logs");
		expect(lastGhost(tree)).toEqual(["steering"]);
		expect(field(tree)?.props.value).toBe("");
		expect(actionErrors(tree)).toEqual([]);
		expect(saysSteerFailed(tree)).toBe(false);
	});

	// A drain takes the queue it saw; a message queued after the hub took it
	// is queued again, with its own Steer now.
	it("shows a message queued after Steer all now took the queue", async () => {
		const ref = "ref-queued-after-drain";
		const served = thread(ref, "active", false, ["check the logs", "and the metrics"]);
		const { tree, hub } = await mount(served);
		const client = hub.client as { request: (method: string, params: Record<string, unknown>) => Promise<unknown> };
		const request = client.request;
		client.request = async (requested, params) => {
			const answer = await request(requested, params);
			if (requested === "turn/drainAsSteer")
				hub.notify({
					method: "thread/queueChanged",
					params: { threadId: served.id, ref, queue: queueState([], 1) },
				} as AnyNotification);
			return answer;
		};
		await steerFromQueue(tree, ref, "Steer all now");
		await settle();
		expect(lastGhost(tree)).toEqual(["steering"]);
		act(() =>
			hub.notify({
				method: "thread/queueChanged",
				params: { threadId: served.id, ref, queue: { ...queueState(["one more thing"], 2), ids: ["queue_9"] } },
			} as AnyNotification),
		);
		await settle();
		expect(rowsWithGhost(tree, "steering")[0]).toContain("check the logs");
		expect(lastRow(tree)).toContain("one more thing");
		expect(lastGhost(tree)).toEqual(["queued"]);
		expect(pressable(tree, "Steer now")).toBeDefined();
	});

	// A message with no text (an image) steers under its preview, so the
	// ghost of Steer all now still names every message it took.
	it("names an image-only message by its preview in Steer all now's ghost", async () => {
		const ref = "ref-drain-preview";
		const served = thread(ref, "active", false, ["check the logs", ""]);
		const evener = (served as unknown as { evener: { queue: ReturnType<typeof queueState> } }).evener;
		evener.queue = { ...evener.queue, preview: ["check the logs", "[image]"] };
		const { tree, hub } = await mount(served);
		const client = hub.client as { request: (method: string, params: Record<string, unknown>) => Promise<unknown> };
		const request = client.request;
		client.request = async (requested, params) => {
			if (requested !== "turn/drainAsSteer") return request(requested, params);
			// The hub never answers: record the request and leave it there.
			hub.requests.push({ method: requested, params });
			return new Promise(() => undefined);
		};
		await steerFromQueue(tree, ref, "Steer all now");
		await settle();
		const [ghost] = rowsWithGhost(tree, "sending");
		expect(ghost).toContain("check the logs");
		expect(ghost).toContain("[image]");
	});

	// A steer the hub refuses names the message it was for, says it was a
	// steer, and offers only Discard: the message may still be queued.
	it.each(STEERS_FROM_QUEUE)("names the message when the hub refuses %s", async (label, status, method, queued) => {
		const ref = `ref-refused-${label}`;
		const { tree, hub } = await mount(thread(ref, status, false, [...queued]));
		const client = hub.client as { request: (method: string, params: Record<string, unknown>) => Promise<unknown> };
		const request = client.request;
		client.request = async (requested, params) => {
			if (requested !== method) return request(requested, params);
			hub.requests.push({ method: requested, params });
			throw new WireError("queue entry changed", -32013, {
				evenerErrorInfo: "conflict",
				clientMutationId: params.clientMutationId,
				mutationOutcome: "notAccepted",
				retryDisposition: "none",
			});
		};
		await steerFromQueue(tree, ref, label);
		await settle();
		const refused = transcriptList(tree)
			.findAll((node) => String(node.type) === "Item")
			.find((row) => markers(row, "ghost").includes("refused"));
		if (!refused) throw new Error("no refused ghost");
		// The hub's reason, beside the message it was for.
		expect(textOf(refused)).toContain("queue entry changed");
		expect(textOf(refused)).toContain("check the logs");
		const offered = refused
			.findAll((node) => String(node.type) === "Pressable" && node.props.accessibilityRole === "button")
			.map((node) => node.props.accessibilityLabel);
		expect(offered).toContain("Discard");
		expect(offered).not.toContain("Edit");
	});

	// They take no room from the transcript, so nothing folds while you type.
	it("keeps a queued message and its Steer now while you type", async () => {
		const { tree, hub } = await mount(thread("ref-steer-typing", "active", false, ["check the logs"]));
		typeInComposer(tree);
		expect(lastRow(tree)).toContain("check the logs");
		expect(pressable(tree, "1 queued")).toBeUndefined();
		await press(tree, "Steer now");
		expect(hub.requests.filter((request) => request.method === "turn/promoteQueuedAsSteer")).toHaveLength(1);
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
		expect(toasts(tree)).toEqual([]);
	});

	it("holds a queue a Stop parked, and Send now releases it (Review Focus 3)", async () => {
		const { tree, hub } = await mount(thread("ref-held", "idle", false, ["after the stop"]));
		expect(ghostStates(tree)).toEqual(["held"]);
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
				<SessionScreen route={route as unknown as ConversationScreenProps["route"]} navigation={navigation} />,
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
		expect(lastRow(tree)).toBe("1 more queued");
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
	/** The room the bar reserves: the home indicator less its trim, so the
	 * composer sits close to the bottom edge (BarFrame INDICATOR_TRIM). */
	const room = () => indicatorRoom(homeIndicator());

	it("runs to the screen's bottom edge under a hairline, and keeps the composer above the home indicator", async () => {
		const { tree } = await mount(thread("ref-bar", "active"));
		// The screen leaves the bottom safe area to the bar, which paints it.
		const [screen] = tree.root.findAll((node) => String(node.type) === "SafeAreaView");
		expect(screen?.props.edges).not.toContain("bottom");
		expect(flatStyle(bar(tree))).toMatchObject({ borderTopWidth: 0.5, paddingBottom: room() });
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
		expect(flatStyle(bar(tree)).paddingBottom).toBe(room());
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
		expect(hasTestID(tree, "approval-dock")).toBe(true);
		const waiting = tree.root.findAllByProps({ testID: "approval-more-waiting" });
		expect(waiting.map((node) => textOf(node))).toEqual([expect.stringMatching(/\b1\b/)]);
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
		expect(text).toContain("read_file  /Users/jesse/notes/todo.md");
		expect(hasTestID(tree, "approval-more-waiting")).toBe(false);
		expect(pressable(tree, "Allow this file only")?.props.accessibilityState).toMatchObject({ disabled: false });
		// The next approval gets a fresh dock (keyed by escalationId), so its
		// body opens at its top, whatever the last one was scrolled to.
		expect(dockBody(tree, "approval-dock").scroller).not.toBe(firstBody);
	});

	it("shows the dock in the tray's place, and never the composer", async () => {
		const { tree } = await mount(withApproval("ref-approval"));
		expect(hasTestID(tree, "approval-dock")).toBe(true);
		expect(pressable(tree, "Stop")).toBeUndefined();
		expect(field(tree)).toBeUndefined();
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
				<SessionScreen route={route as unknown as ConversationScreenProps["route"]} navigation={navigation} />,
			),
		);
		await settle();
		const text = renderedText(tree).replaceAll("\u200b", "");
		expect(hasTestID(tree, "approval-dock")).toBe(true);
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
		expect(toasts(tree)).toEqual(["approval:allowed"]);
	});

	it("denies without a haptic: spec 16.6 plays success only for allowed", async () => {
		const { tree } = await mount(withApproval("ref-approval-deny"));
		playedHaptics.length = 0;
		await press(tree, "Deny");
		expect(toasts(tree)).toEqual(["approval:denied"]);
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
		act(() => tree.update(<SessionScreen route={route as never} navigation={navigation} />));
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
	afterEach(() => {
		unmountMountedTrees();
		vi.unstubAllGlobals();
	});

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
		await settle();
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
		expect(findMatches(tree)).toEqual({ position: 2, count: 2 });
		expect(findScrolls()).toEqual([1]);
		expect(washed(tree).join(" ")).toContain("The race is in settle.");
		await press(tree, "Older match");
		expect(findMatches(tree)).toEqual({ position: 1, count: 2 });
		expect(findScrolls()).toEqual([1, 0]);
		expect(washed(tree).join(" ")).toContain("Fix the flaky Settle test");
		await press(tree, "Newer match");
		expect(findMatches(tree)).toEqual({ position: 2, count: 2 });
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
			unmountMountedTrees();
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
		// Where the bar says you are at each announcement.
		const announced: ReturnType<typeof findMatches>[] = [];
		const announce = vi.spyOn(AccessibilityInfo, "announceForAccessibility").mockImplementation(() => {
			announced.push(findMatches(tree));
		});
		try {
			await search(tree, "settle");
			await search(tree, "settle test");
			expect(announced).toEqual([
				{ position: 2, count: 2 },
				{ position: 1, count: 1 },
			]);
		} finally {
			announce.mockRestore();
		}
	});

	it("says so when nothing matches", async () => {
		const { tree } = await mount(findTurns("ref-find-none"));
		chooseMenu("Find in session");
		await search(tree, "nowhere");
		expect(findShows(tree, 0, 0)).toBe(true);
	});

	it("reaches back through older history for an older match", async () => {
		const { tree, hub } = await mount(findTurns("ref-find-older"), {
			olderCursor: "cursor-1",
			olderTurns: [askReplyTurn("turn_0", "Is settle flaky?", "Sometimes.")],
		});
		chooseMenu("Find in session");
		await search(tree, "settle");
		expect(findMatches(tree)).toEqual({ position: 2, count: 2 });
		await press(tree, "Older match");
		await press(tree, "Older match");
		expect(
			hub.requests.filter((request) => request.method === "thread/turns/list").map((request) => request.params.cursor),
		).toEqual(["cursor-1"]);
		expect(washed(tree).join(" ")).toContain("Is settle flaky?");
		expect(findMatches(tree)).toEqual({ position: 1, count: 3 });
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
		expect(hasTestID(tree, "find-bar:exhausted")).toBe(false);
		await press(tree, "Older match");
		await press(tree, "Older match");
		expect(hub.requests.filter((request) => request.method === "thread/turns/list")).toHaveLength(1);
		// History has ended: the bar says so in place of a match.
		expect(hasTestID(tree, "find-bar:exhausted")).toBe(true);
		expect(findBar(tree).props.searchingOlder).toBe(false);
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
			reference: {
				cwd: "/home/jesse/git/evener",
				path: "docs/superpowers/plans/settle-race.md",
				provenance: "relative",
				readTarget: "/home/jesse/git/evener/docs/superpowers/plans/settle-race.md",
			},
			sessionTitle: "Session",
			updatedAt: WROTE_AT,
		});
	});

	it("counts the session's documents on a Files chip, dotted while one is new, which opens Files", async () => {
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
			cwd: "/home/jesse/git/evener",
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
		// No count of others: nobody else needs you.
		expect(back().props.accessibilityLabel).not.toMatch(/\d/);
		expect(tree.root.findByType(FloatingStack).props.next).toBeFalsy();
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
		expect(toasts(tree)).toEqual(["stop:stopped"]);
		const next = capsule(tree);
		if (!next) throw new Error("no Next capsule");
		const stack = tree.root.findByType(FloatingStack);
		const toast = stack.findByType(Toast);
		expect(toast.props.toast).toMatchObject({ kind: "stop:stopped" });
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
		expect(toasts(tree)).toEqual(["stop:stopped"]);
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
		typeInComposer(tree);
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
			expect(tree.root.findByType(FloatingStack).props.next).toBeFalsy();
			panTitle(-80);
			panTitle(80);
			expect(navigation.replace).not.toHaveBeenCalled();
		});

		it("offers VoiceOver the sessions on either side, and only those there are", async () => {
			const actions = () =>
				headerTitle()
					.root.find((node) => node.props.accessibilityRole === "button" && node.props.accessibilityActions)
					.props.accessibilityActions.map((action: { name: string }) => action.name);
			await mount(thread("ref-title", "active"));
			expect(actions()).toEqual(["previous", "next"]);
			fleet.live = [failing, asking, working];
			await mount(thread("ref-title", "active"));
			expect(actions()).toEqual(["previous"]);
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

	// The hub calls its own machine "this host" in the manifest
	// (cmd/evener-hub/web_api_tree.go); the Session sheet names it for the
	// hub, as Hub > Hosts does, with its connection dot (spec 8.6, audit N6).
	it("names the session's own-machine host for the hub in the Session sheet, with its dot", async () => {
		fleet.sources = [{ id: "local", label: "this host" }];
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
		expect(renderedText(sheet)).toContain("Work hub");
		expect(renderedText(sheet)).not.toContain("this host");
		expect(sheet.root.findAll((node) => node.props.accessibilityLabel === "Work hub, connected")).not.toEqual([]);
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
				// The activity context names the coordinator's thread.
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
		const tree = render(
			<NativeConversationSessionsProvider>
				<SubagentScreen route={route as never} navigation={navigation as never} />
			</NativeConversationSessionsProvider>,
		);
		await settle();
		return { tree, hub };
	}

	const jobReads = (hub: ReturnType<typeof hubClient>) =>
		hub.requests.filter((request) => request.method === "evener/thread/delegates/list").length;

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
		expect(hasTestID(tree, "sending-unavailable")).toBe(false);
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
		expect(hasTestID(tree, "subagent-stop-requested")).toBe(true);
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
		expect(hasTestID(tree, "subagent-stop-requested")).toBe(true);
		const [stoppedRow] = flattenSubagents(subagentTree() as never);
		if (!stoppedRow) throw new Error("no row");
		expect(stopRequests("hub-1").direct(stoppedRow)).toBe(true);
	});

	it("keeps another root's colliding stop evidence separate from the actual direct stop target", async () => {
		const [current] = flattenSubagents(subagentTree() as never);
		if (!current) throw new Error("no row");
		const other = {
			...current,
			ref: "remote:other-child",
			delegate: {
				...current.delegate,
				childRef: "remote:other-child",
				rootRef: "remote:other-root",
			},
		};
		stopRequests("hub-1").request("remote:other-root", other, 1000, { direct: true });
		const { tree, hub } = await mountSubagent(subagent(true), { stopSubagent: true });
		expect(pressable(tree, "Stop subagent")).toBeDefined();
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
		).toEqual([{ ref: COORDINATOR.ref, threadId: COORDINATOR.threadId, delegateId: "d-fix" }]);
		expect(stopRequests("hub-1").view(other)).toBe("requested");
		expect(stopRequests("hub-1").direct(current)).toBe(true);
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

	it("tells its screen the coordinator's thread it reads now, for the transcript's finished rows (#3326 review)", async () => {
		// The screen holds the same tree for its transcript's finished subagent
		// rows, and a tree asked for under the route's old thread is refused.
		forgetSubagentTrees("hub-1");
		const restarted = { ...subagentTree(), root: { ...subagentTree().root, sessionId: "thread-restarted" } };
		await mountSubagent(subagent(true), { jobs: restarted, coordinatorId: "thread-restarted" });
		const reported: string[] = [];
		const panel = render(
			<SubagentPanel
				hubId="hub-1"
				ref="local:fix"
				coordinator={COORDINATOR}
				inFront
				barShown={false}
				showToast={() => {}}
				onTreeThread={(threadId) => void reported.push(threadId)}
				navigation={navigation as never}
			/>,
		);
		await settle();
		expect(reported.at(-1)).toBe("thread-restarted");
		act(() => panel.unmount());
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
		expect(hasTestID(tree, "subagent-stop-requested")).toBe(false);
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
		expect(hasTestID(tree, "subagent-stop-requested")).toBe(false);
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
		act(() =>
			tree.update(
				<NativeConversationSessionsProvider>
					<SubagentScreen route={route as never} navigation={navigation as never} />
				</NativeConversationSessionsProvider>,
			),
		);
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
		act(() =>
			tree.update(
				<NativeConversationSessionsProvider>
					<SubagentScreen route={route as never} navigation={navigation as never} />
				</NativeConversationSessionsProvider>,
			),
		);
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
		act(() => hub.notify(activityChangedNotification({ ...COORDINATOR, sessionId: "fix" })));
		await settle();
		expect(jobReads(hub)).toBeGreaterThan(before);
	});

	it("opens its coordinator's Agents list from its own Agents chip", async () => {
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
		act(() => pressable(tree, "Agents, 1")?.props.onPress());
		expect(navigation.navigate).toHaveBeenCalledWith("Activity", { hubId: "hub-1", ...COORDINATOR, kind: "agents" });
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
			reference: {
				cwd: "/home/jesse/git/evener",
				path: "docs/superpowers/plans/settle-race.md",
				provenance: "relative",
				readTarget: "/home/jesse/git/evener/docs/superpowers/plans/settle-race.md",
			},
			sessionTitle: "Fix race in tree settle",
		});
	});

	// G13: a finished subagent's row shows its report's opening line, read
	// from the coordinator's tree, which the screen holds and the list's rows
	// read. The tree lands after the rows first render, so the rows must
	// render again when it does.
	it("shows a finished subagent's report once the coordinator's tree arrives", async () => {
		forgetSubagentTrees("hub-1");
		const actual = subagentOutcomesDelegatesResponse();
		const reported = actual.delegates.find((row) => row.delegateId === "dlg_reported");
		if (!reported) throw new Error("missing real producer report");
		coordinatorHub.delegates = {
			...actual,
			context: { ...actual.context, ref: COORDINATOR.ref, rootRef: COORDINATOR.ref, sessionId: COORDINATOR.threadId },
			delegates: [{ ...reported, ownerRef: COORDINATOR.ref, rootRef: COORDINATOR.ref, childRef: "local:fix" }],
		};
		const endedAt = reported.runEndedAt;
		coordinatorHub.tree = subagentTree({
			terminal: true,
			outcome: "completed",
			packetKind: "reported",
			message: "Fixed the race: settle now waits for the drain.",
			runEndedAt: endedAt,
		});
		const served = thread(COORDINATOR.ref, "idle");
		(served as unknown as { turns: unknown[] }).turns = [
			{
				id: "t1",
				status: "completed",
				itemsView: "default",
				items: [
					{
						id: "call-d",
						turnId: "t1",
						type: "commandExecution",
						toolName: "delegate",
						status: "completed",
						argumentsJson: JSON.stringify({ description: "Fix race in tree settle" }),
					},
				],
			},
		];
		(served as unknown as { evener: Record<string, unknown> }).evener.diagnostics = {
			delegates: [
				{
					delegateId: reported.delegateId,
					runGeneration: reported.runGeneration,
					ownerSessionId: "coord",
					rootSessionId: "coord",
					childSessionId: "fix",
					transcriptRef: "local:fix",
					originItemId: "call-d",
					description: "Fix race in tree settle",
					type: "subagent",
					lifecycle: "idle",
					phase: "idle",
					status: "idle",
					outcome: "completed",
					terminal: true,
					runEndedAt: endedAt,
					resumable: true,
					needsAttention: false,
					projectionRevision: 1,
				},
			],
		};
		const { tree } = await mount(served);
		await settle();
		expect(renderedText(tree)).toContain("Fixed the race: settle now waits for the drain.");
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

	it("opens a subagent's own subagent under the coordinator's thread as it reads now (#3326 RoboRev)", async () => {
		// The coordinator restarted since this screen opened: the screen it
		// pushes asks for the coordinator's tree under the thread it runs now.
		forgetSubagentTrees("hub-1");
		const served = subagent(false);
		(served as unknown as { turns: unknown[] }).turns = [
			{
				id: "t1",
				status: "completed",
				itemsView: "default",
				items: [
					{
						id: "call-n",
						turnId: "t1",
						type: "commandExecution",
						toolName: "delegate",
						status: "completed",
						argumentsJson: JSON.stringify({ description: "Check drain ordering" }),
					},
				],
			},
		];
		(served as unknown as { evener: Record<string, unknown> }).evener.diagnostics = {
			delegates: [
				{
					delegateId: "d-nested",
					ownerSessionId: "fix",
					rootSessionId: "coord",
					childSessionId: "nested",
					transcriptRef: "local:nested",
					originItemId: "call-n",
					description: "Check drain ordering",
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
		const restarted = { ...subagentTree(), root: { ...subagentTree().root, sessionId: "thread-restarted" } };
		const { tree } = await mountSubagent(served, { jobs: restarted, coordinatorId: "thread-restarted" });
		const row = tree.root.findAll(
			(node) =>
				String(node.type) === "Pressable" && String(node.props.accessibilityLabel).startsWith("Check drain ordering, "),
		)[0];
		if (!row) throw new Error("no subagent row");
		act(() => row.props.onPress());
		expect(navigation.push).toHaveBeenCalledWith("Subagent", {
			hubId: "hub-1",
			ref: "local:nested",
			title: "Check drain ordering",
			coordinator: { ...COORDINATOR, threadId: "thread-restarted" },
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
		act(() => hub.notify(activityChangedNotification({ ...COORDINATOR, sessionId: "fix" })));
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
		act(() => tree.update(<SessionScreen route={route} navigation={navigation} />));
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
		expect(ghostStates(tree)).toEqual(["sending"]);
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
		const tree = render(<SessionScreen route={route} navigation={navigation} />);
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

	it("answer enqueue carries its draft capture through later Edit and Remove", async () => {
		async function answerWithFailedConfirmation(ref: string) {
			const { tree, route } = await mount(thread(ref, "awaiting", true));
			vi.stubGlobal("requestAnimationFrame", (frame: (time: number) => void) => {
				frame(0);
				return 0;
			});
			await reach(tree, route, false);
			await press(tree, "Drop them");
			nativeDrafts();
			const database = nativePlatform.sqlitePorts.get("evener-drafts.db") as {
				runSync: (sql: string, ...args: unknown[]) => unknown;
			};
			const runSync = database.runSync;
			let draftWrites = 0;
			let confirmationFailed = false;
			database.runSync = (sql, ...args) => {
				if (sql.includes("INSERT INTO drafts")) draftWrites++;
				if (sql.includes("DELETE FROM drafts") && draftWrites === 1) {
					confirmationFailed = true;
					throw new Error("draft confirmation failed");
				}
				return runSync(sql, ...args);
			};
			try {
				await press(tree, "Send answer when you're back online");
			} finally {
				database.runSync = runSync;
			}
			expect(confirmationFailed).toBe(true);
			const record = nativeDrafts().read({ hubId: "hub-1", sessionRef: ref });
			expect(record.unconfirmed).toBe('[answers]\n1. [Choice] → "Drop them"');
			expect(record.unconfirmedCaptureToken).toBeTruthy();
			const outbox = await getNativeMutationRuntime().storage.listOutbox(nativeMutationTargetKey("hub-1", ref));
			expect(outbox).toHaveLength(1);
			expect(outbox[0]?.draftCaptureToken).toBe(record.unconfirmedCaptureToken);
			return { tree, ref, outbox: outbox[0] };
		}

		const edited = await answerWithFailedConfirmation("ref-answer-capture-edit");
		await press(edited.tree, "Retry saving");
		const editHost = queueHosts.get(sheetKey("hub-1", edited.ref));
		const editGhost = editHost?.ghosts.find(
			(candidate) =>
				candidate.origin.kind === "pending" && candidate.origin.clientMutationId === edited.outbox?.clientMutationId,
		);
		if (!editHost || !editGhost) throw new Error("missing queued answer ghost for Edit");
		expect(editGhost.menu).toContain("edit");
		let editResult: unknown;
		await act(async () => {
			editResult = await editHost.act(editGhost, "edit");
		});
		expect(editResult).toBeNull();
		expect(nativeDrafts().read({ hubId: "hub-1", sessionRef: edited.ref }).unconfirmed).toBeNull();
		expect(nativeDrafts().read({ hubId: "hub-1", sessionRef: edited.ref }).draft).toContain("[answers]");

		const removed = await answerWithFailedConfirmation("ref-answer-capture-remove");
		await press(removed.tree, "Retry saving");
		const host = queueHosts.get(sheetKey("hub-1", removed.ref));
		const ghost = host?.ghosts.find(
			(candidate) =>
				candidate.origin.kind === "pending" && candidate.origin.clientMutationId === removed.outbox?.clientMutationId,
		);
		if (!host || !ghost) throw new Error("missing queued answer ghost");
		await act(async () => {
			await host.act(ghost, "cancel");
		});
		expect(nativeDrafts().read({ hubId: "hub-1", sessionRef: removed.ref }).unconfirmed).toBeNull();
		expect(await getNativeMutationRuntime().storage.listOutbox(nativeMutationTargetKey("hub-1", removed.ref))).toEqual(
			[],
		);
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
	await vi.waitFor(() => expect(ghostStates(tree)).toEqual(["unconfirmed"]));
	await press(tree, "Discard");

	// One Discard clears both: the draft's uncertainty and the row it stood in
	// for, which otherwise returns as its own ghost.
	await vi.waitFor(async () => expect(await runtime.storage.listOutbox(key)).toEqual([]));
	await settle();
	expect(ghostStates(tree)).toEqual([]);
});

// #4901: the runtime is memoized for the whole file and arms a 2000ms
// MutationOutbox discovery interval. Stopping it between cases is what keeps a
// lost admission row from being re-sent under a later case's client. These two
// cases exercise the file's own teardown rather than a hand-written one, so
// deleting the afterEach stop above fails the second case.
describe("the mutation runtime's discovery interval across cases (issue #4901)", () => {
	const ref = "ref-retry-discovery-isolation";
	const target = nativeMutationTargetKey("hub-1", ref);

	// The runtime already exists from earlier cases, started on real timers.
	// Stop it, then fake only the interval so the next start is test-owned;
	// settle() and the screen's deferred work keep running on real timers.
	beforeAll(async () => {
		await getNativeMutationRuntime().stop();
		vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
	});
	afterAll(() => {
		vi.useRealTimers();
	});

	it("leaves a lost turn/start admission as one request with one clientMutationId", async () => {
		const served = thread(ref, "idle");
		(served as unknown as { turns: unknown[] }).turns = [
			{
				id: "turn_1",
				status: "failed",
				itemsView: "default",
				error: { message: "go test exited 1" },
				items: [
					{ id: "u-1", turnId: "turn_1", type: "userMessage", status: "completed", text: "my original question" },
				],
			},
		];
		const { tree, hub } = await mount(served);
		const client = hub.client as { request: (method: string, params: Record<string, unknown>) => Promise<unknown> };
		const request = client.request;
		client.request = async (method, params) => {
			const answer = await request(method, params);
			if (method === "turn/start") throw new Error("connection lost after admission");
			return answer;
		};
		await press(tree, "Retry");
		const turnStarts = hub.requests.filter((entry) => entry.method === "turn/start");
		expect(turnStarts).toHaveLength(1);
		expect(new Set(turnStarts.map((entry) => entry.params.clientMutationId)).size).toBe(1);
		const rows = await getNativeMutationRuntime().storage.listOutbox(target);
		expect(rows).toHaveLength(1);
		expect(rows[0]?.clientMutationId).toBe(turnStarts[0]?.params.clientMutationId);
	});

	it("does not re-send a durable turn/start after the file's teardown", async () => {
		// The case above ran, and its teardown must have stopped the runtime:
		// no discovery interval survives into this case.
		const leakedIntervals = vi.getTimerCount();
		const runtime = getNativeMutationRuntime();
		// Running this case alone starts with no lost admission, so seed the
		// same durable shape the case above leaves instead of passing vacuously.
		if ((await runtime.storage.listOutbox(target)).length === 0) {
			const { clientMutationId } = await runtime.storage.enqueueIntent({
				targetRef: target,
				method: "turn/start",
				payload: { ref, input: [{ type: "text", text: "continue" }] },
				attachments: [],
				optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "continue" }] },
			});
			await runtime.storage.markAttempted(clientMutationId);
		}
		const rows = await runtime.storage.listOutbox(target);
		expect(rows).toHaveLength(1);
		const clientMutationId = rows[0]?.clientMutationId;
		expect(clientMutationId).toBeTruthy();

		// A later case's client: if the interval leaked, its scan dispatches the
		// earlier row through this client. The authoritative snapshot keeps the
		// submitting row in place instead of parking it blockedUnknown.
		const later = hubClient(thread(ref, "idle"));
		const laterClient = later.client as unknown as AppwireClientLike;
		const unregister = runtime.registerTarget("hub-1", ref, laterClient);
		const lease = runtime.beginAuthoritativeRead("hub-1", ref, laterClient);
		if (!lease) throw new Error("no authoritative read lease");
		const authoritative = thread(ref, "idle");
		(authoritative as unknown as { evener: Record<string, unknown> }).evener.mutationStateAuthoritative = true;
		await runtime.reconcileAuthoritativeRead(lease, { thread: authoritative });
		await settle();
		await advanceFakeTimers(2_000);
		await settle();
		unregister();

		expect({ requests: later.mutations(), leakedIntervals }).toEqual({ requests: [], leakedIntervals: 0 });
		expect((await runtime.storage.listOutbox(target)).map((row) => row.clientMutationId)).toEqual([clientMutationId]);
	});
});
