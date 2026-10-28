import { nativePlatform } from "./conversationPlatform.testkit";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { activityFixture } from "./subagents/sessionActivityTestUtils";
// The Session's one Send and the tray's Stop, on the real ConversationScreen:
// what a person presses, and which requests reach the hub through the durable
// runtime. Only native edges are mocked, as in
// ConversationScreen.recovery.test.tsx.
import type { ComponentProps } from "react";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { type AnyNotification, type SessionDelegatesResponse, type Thread } from "@evener/appwire-client";
import { hasTestID, markers } from "./markerTestUtils";
import { nativeDrafts } from "./nativeDrafts";
import { getNativeMutationRuntime } from "./nativeMutationRuntime";
import {
	flatListCalls,
	keyboard,
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
import { answerFleetRead, type FleetShape, fleetSession } from "./session/fleetTestUtils";
import { FloatingStack } from "./session/FloatingStack";
import { Toast } from "./Toast";
import { DisplayPreferences } from "./display/displayPreferences";
import { DisplayProvider } from "./display/displayContext";
import { advanceFakeTimers, press, toasts } from "./conversationScreenTestUtils";

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
} // Two finished turns, each your message and the agent's reply.
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
