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
import { act, type ReactTestRenderer } from "react-test-renderer";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import {
	type AnyNotification,
	type AppwireClientLike,
	type SessionDelegatesResponse,
	type Thread,
	WireError,
} from "@evener/appwire-client";
import { hasTestID } from "./markerTestUtils";
import { nativeDrafts } from "./nativeDrafts";
import { getNativeMutationRuntime, nativeMutationTargetKey } from "./nativeMutationRuntime";
import {
	keyboard,
	alertRequests,
	dropped as droppedConnection,
	type PanGestureMock,
	playedHaptics,
	pressable,
	render,
	renderedText,
	screenConnection,
	settle,
	systemGlass,
	unmountMountedTrees,
} from "./renderNative.testkit";
import { queueHosts } from "./QueueSheet";
import { ConversationScreen } from "./screens";
import { NativeConversationSessionsProvider } from "./session/NativeConversationSessionsProvider";
import { sheetKey } from "./sheet/sheetHosts";
import { SessionInfoSheet, sessionInfoHosts } from "./session/SessionInfoSheet";
import { SubagentPanel } from "./subagents/SubagentPanel";
import { ActionSheetIOS } from "react-native";
import type {
	NativeStackHeaderItemMenu,
	NativeStackHeaderItemMenuAction,
	NativeStackNavigationOptions,
} from "@react-navigation/native-stack";
import { answerFleetRead, type FleetShape, fleetSession } from "./session/fleetTestUtils";
import { FloatingStack } from "./session/FloatingStack";
import { SessionTitle } from "./session/SessionTitle";
import type { SessionStateLine } from "./session/sessionState";
import { Toast } from "./Toast";
import { forgetStopRequestsForHub, stopRequests } from "./subagents/nativeStopRequests";
import { forgetSubagentTrees } from "./subagents/subagentTree";
import { SubagentScreen } from "./subagents/SubagentScreen";
import { flattenSubagents } from "./subagents/subagentModel";
import { DisplayPreferences } from "./display/displayPreferences";
import { DisplayProvider } from "./display/displayContext";
import { advanceFakeTimers, ghostStates, press, toasts } from "./conversationScreenTestUtils";

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

/** Chooses an item in the ⋯ menu the screen set last. */
function chooseMenu(label: string) {
	const calls = vi.mocked(navigation.setOptions).mock.calls as [NativeStackNavigationOptions][];
	const options = calls.map(([options]) => options).findLast((options) => options.unstable_headerRightItems);
	const [menu] = (options?.unstable_headerRightItems?.({ canGoBack: true }) ?? []) as NativeStackHeaderItemMenu[];
	const found = menu?.menu.items.find((item) => item.label === label) as NativeStackHeaderItemMenuAction | undefined;
	if (!found) throw new Error(`no ${label} in the header menu`);
	act(() => found.onPress());
}

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

	it("keeps at least the 60pt floor at the transcript's end while controls come and go", async () => {
		// FloatingStack reports its laid-out height, and a taller stack raises
		// the room above this floor (the following-live-end test drives that).
		// Until it reports, the floor stands whatever shows.
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

	/** The coordinator's tree with no delegate row in it, as a read that has
	 * not landed or a tree that doesn't list this ref yet. */
	function emptyTree() {
		const tree = subagentTree();
		return { ...tree, root: { ...tree.root, entries: [] } };
	}

	/** The state line the screen's header set last, as SessionTitle receives
	 * it: structure, not its rendered words. */
	function headerLine(): SessionStateLine {
		const calls = vi.mocked(navigation.setOptions).mock.calls as [NativeStackNavigationOptions][];
		const options = calls.map(([options]) => options).findLast((options) => options.headerTitle);
		if (typeof options?.headerTitle !== "function") throw new Error("no headerTitle");
		const tree = render(<>{options.headerTitle({ children: "Fix race in tree settle" })}</>);
		return tree.root.findByType(SessionTitle).props.line as SessionStateLine;
	}

	/** The screen's own run time for this subagent, the number behind its
	 * line. */
	function subagentRunMs(now: number): number | null {
		return sessionInfoHosts.get(sheetKey("hub-1", "local:fix"))?.runMs(now) ?? null;
	}

	/** The opener's run start is RUNNING_SINCE (four minutes before the test
	 * file loaded), so the screen's number is that age within a second. */
	function expectRunMsFromOpener(now: number): void {
		expect(Math.abs((subagentRunMs(now) ?? 0) - (now - Date.parse(RUNNING_SINCE)))).toBeLessThan(5_000);
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
		{
			stopSubagent = false,
			jobs = subagentTree(),
			coordinatorId = COORDINATOR.threadId,
			runStartedAt,
		}: { stopSubagent?: boolean; jobs?: unknown; coordinatorId?: string; runStartedAt?: string } = {},
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
			params: {
				hubId: "hub-1",
				ref: "local:fix",
				title: "Fix race in tree settle",
				coordinator: COORDINATOR,
				...(runStartedAt === undefined ? {} : { runStartedAt }),
			},
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
		expect(headerLine().state).toBe("working");
		// Session info times it the same way.
		expectRunMsFromOpener(Date.now());
	});

	it("times its run from the row that opened it until the coordinator's tree reports one (#2988)", async () => {
		// The tree read has no row for this subagent yet, so the screen has only
		// the run start the Agents list or transcript row opened it with. Its
		// own turn started two minutes ago; the run started four.
		vi.mocked(navigation.setOptions).mockClear();
		const served = subagent(true);
		(served as unknown as { evener: Record<string, unknown> }).evener.activeTurnStartedAt = new Date(
			Date.now() - 2 * 60_000,
		).toISOString();
		await mountSubagent(served, { jobs: emptyTree(), runStartedAt: RUNNING_SINCE });
		expect(headerLine().state).toBe("working");
		expectRunMsFromOpener(Date.now());
	});

	it("keeps the opener's run start when the tree's running row has no timing (#2988)", async () => {
		// A running row with neither runStartedAt nor runningForMs cannot time
		// itself; the screen must keep the opener's fact rather than the turn's.
		vi.mocked(navigation.setOptions).mockClear();
		const served = subagent(true);
		(served as unknown as { evener: Record<string, unknown> }).evener.activeTurnStartedAt = new Date(
			Date.now() - 2 * 60_000,
		).toISOString();
		await mountSubagent(served, { jobs: subagentTree({ runStartedAt: undefined }), runStartedAt: RUNNING_SINCE });
		expect(headerLine().state).toBe("working");
		expectRunMsFromOpener(Date.now());
	});

	it("times a still-working subagent by its turn once its row has ended", async () => {
		// The row reads done a moment before the session itself stops: the row's
		// time since it ended is no Working time, and the opener's old run start
		// is no Working time either.
		vi.mocked(navigation.setOptions).mockClear();
		const served = subagent(true);
		(served as unknown as { evener: Record<string, unknown> }).evener.activeTurnStartedAt = Date.now() - 2 * 60_000;
		await mountSubagent(served, {
			jobs: subagentTree({ terminal: true, outcome: "completed", runEndedAt: new Date().toISOString() }),
			runStartedAt: RUNNING_SINCE,
		});
		expect(headerLine().state).toBe("working");
		// The screen does not time the ended run, so its runMs is null and the
		// state line falls back to the turn's own elapsed time.
		expect(subagentRunMs(Date.now())).toBeNull();
	});

	it("lets the panel's running row time the run over the opener's start (#2988)", async () => {
		// The tree's row is the live fact; the route's start only bridges until
		// it lands, so a changed run start in the tree wins.
		vi.mocked(navigation.setOptions).mockClear();
		const served = subagent(true);
		(served as unknown as { evener: Record<string, unknown> }).evener.activeTurnStartedAt = new Date(
			Date.now() - 2 * 60_000,
		).toISOString();
		await mountSubagent(served, { runStartedAt: new Date(Date.now() - 6 * 60_000).toISOString() });
		expect(headerLine().state).toBe("working");
		// The panel's RUNNING_SINCE beats the route's six-minute start.
		expectRunMsFromOpener(Date.now());
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
		// Its own session ref: the store keeps the previous test's conversation
		// under COORDINATOR.ref, including its finished delegate.
		const coordinatorRef = "local:coord-open";
		const served = thread(coordinatorRef, "active");
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
					type: "delegate",
					lifecycle: "running",
					phase: "running",
					status: "running",
					runStartedAt: RUNNING_SINCE,
					resumable: false,
					needsAttention: false,
					projectionRevision: 1,
				},
			],
		};
		const { tree } = await mount(served);
		await settle();
		const row = tree.root.findAll(
			(node) =>
				String(node.type) === "Pressable" &&
				String(node.props.accessibilityLabel).startsWith("Fix race in tree settle, "),
		)[0];
		if (!row) throw new Error("no subagent row");
		expect(row.props.accessibilityLabel).toContain("running");
		act(() => row.props.onPress());
		expect(navigation.push).toHaveBeenCalledWith("Subagent", {
			hubId: "hub-1",
			ref: "local:fix",
			title: "Fix race in tree settle",
			coordinator: { ref: coordinatorRef, threadId: `thread-${coordinatorRef}`, title: "Session" },
			runStartedAt: RUNNING_SINCE,
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
