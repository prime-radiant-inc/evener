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
import { type AnyNotification, type SessionDelegatesResponse, type Thread, WireError } from "@evener/appwire-client";
import { drafts, nativeDrafts } from "./nativeDrafts";
import { getNativeMutationRuntime, nativeMutationTargetKey } from "./nativeMutationRuntime";
import {
	flatListCalls,
	keyboard,
	playedHaptics,
	pressable,
	render,
	renderedText,
	screenConnection,
	settle,
	systemGlass,
	unmountMountedTrees,
	until,
} from "./renderNative.testkit";
import { ConversationScreen } from "./screens";
import type { NativeConversationSessions } from "./session/nativeConversationSessions";
import {
	NativeConversationSessionsProvider,
	useNativeConversationSessions,
} from "./session/NativeConversationSessionsProvider";
import { nativeDisclosureStore, setDisclosureOpenAll } from "./nativeDisclosure";
import { rowDisclosureIds, sessionDisclosureScope } from "./session/disclosureKeys";
import { FindBar } from "./session/FindBar";
import { GlassHeaderPanel } from "./design/GlassHeaderPanel";
import { SessionHeader } from "./session/SessionHeader";
import { sheetKey } from "./sheet/sheetHosts";
import { modelHosts } from "./session/ModelSheet";
import { SessionInfoSheet } from "./session/SessionInfoSheet";
import type {
	NativeStackHeaderItemMenu,
	NativeStackHeaderItemMenuAction,
	NativeStackNavigationOptions,
} from "@react-navigation/native-stack";
import { answerFleetRead, type FleetShape, fleetSession } from "./session/fleetTestUtils";
import { DisplayPreferences } from "./display/displayPreferences";
import { DisplayProvider } from "./display/displayContext";
import { displayForLevel } from "./session/detailLevels";
import { detailLevels } from "./session/nativeDetailLevels";
import { ghostStates, press, toasts } from "./conversationScreenTestUtils";

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

function twoTurns(ref: string): Thread {
	const served = thread(ref, "idle");
	(served as unknown as { turns: unknown[] }).turns = [askReplyTurn("turn_1"), askReplyTurn("turn_2")];
	return served;
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

/** Chooses an item in the ⋯ menu the screen set last. */
function chooseMenu(label: string) {
	const calls = vi.mocked(navigation.setOptions).mock.calls as [NativeStackNavigationOptions][];
	const options = calls.map(([options]) => options).findLast((options) => options.unstable_headerRightItems);
	const [menu] = (options?.unstable_headerRightItems?.({ canGoBack: true }) ?? []) as NativeStackHeaderItemMenu[];
	const found = menu?.menu.items.find((item) => item.label === label) as NativeStackHeaderItemMenuAction | undefined;
	if (!found) throw new Error(`no ${label} in the header menu`);
	act(() => found.onPress());
}
