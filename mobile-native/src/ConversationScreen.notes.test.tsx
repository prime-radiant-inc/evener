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
import {
	type AnyNotification,
	makeTranscriptDisplayConfig,
	type SessionDelegatesResponse,
	type Thread,
	WireError,
} from "@evener/appwire-client";
import { hasTestID, markers } from "./markerTestUtils";
import { getNativeMutationRuntime } from "./nativeMutationRuntime";
import {
	keyboard,
	type PanGestureMock,
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
import { sheetKey } from "./sheet/sheetHosts";
import { paletteFor } from "./design/tokens";
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
