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
import {
	type AnyNotification,
	makeTranscriptDisplayConfig,
	type SessionDelegatesResponse,
	type Thread,
	WireError,
} from "@evener/appwire-client";
import { findBar, findMatches, findShows } from "./findBarTestUtils";
import { hasTestID, markers } from "./markerTestUtils";
import { getNativeMutationRuntime } from "./nativeMutationRuntime";
import {
	flatListCalls,
	flatListScrollFailures,
	keyboard,
	dockBody,
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
import { NativeConversationSessionsProvider } from "./session/NativeConversationSessionsProvider";
import { NotesSheet, notesHosts } from "./session/NotesSheet";
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

	it("starts a ghost row's edge band at the transcript's padded left edge, not the row's", async () => {
		const { tree } = await mount(thread("ref-queue-edge-band", "active", false, ["check the logs"]));
		const swipeables = tree.root.findAllByType("ReanimatedSwipeable" as never);
		expect(swipeables).toHaveLength(1);
		const [swipeable] = swipeables;
		const tracker = swipeable?.props.simultaneousWithExternalGesture as PanGestureMock | undefined;
		expect(swipeable?.props.hitSlop).toEqual({ left: -8 });
		expect(tracker?.config.hitSlop).toEqual({ left: -8 });
		expect(transcriptList(tree).props.contentContainerStyle.paddingHorizontal).toBe(16);
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
