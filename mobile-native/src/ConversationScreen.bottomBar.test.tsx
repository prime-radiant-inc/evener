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
import { findBar, findMatches, findShows } from "./findBarTestUtils";
import { hasTestID } from "./markerTestUtils";
import { getNativeMutationRuntime } from "./nativeMutationRuntime";
import {
	flatListCalls,
	flatListScrollFailures,
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
} from "./renderNative.testkit";
import { ConversationScreen } from "./screens";
import { NativeConversationSessionsProvider } from "./session/NativeConversationSessionsProvider";
import { indicatorRoom } from "./design/BarFrame";
import { sheetKey } from "./sheet/sheetHosts";
import { holdQuote, takeQuote } from "./session/pendingQuote";
import { commandHosts } from "./session/CommandsSheet";
import { AccessibilityInfo, ActionSheetIOS, Platform } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import type {
	NativeStackHeaderItemMenu,
	NativeStackHeaderItemMenuAction,
	NativeStackNavigationOptions,
} from "@react-navigation/native-stack";
import { paletteFor } from "./design/tokens";
import { answerFleetRead, type FleetShape } from "./session/fleetTestUtils";
import { FloatingStack } from "./session/FloatingStack";
import { DisplayPreferences } from "./display/displayPreferences";
import { DisplayProvider } from "./display/displayContext";
import { press, toasts } from "./conversationScreenTestUtils";

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

function field(tree: ReactTestRenderer) {
	return tree.root
		.findAll((node) => String(node.type) === "TextInput")
		.find((node) => node.props.accessibilityLabel === "Message");
}

async function type(tree: ReactTestRenderer, text: string) {
	const input = field(tree);
	if (!input) throw new Error("no Message field");
	act(() => input.props.onChangeText(text));
	await settle();
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
