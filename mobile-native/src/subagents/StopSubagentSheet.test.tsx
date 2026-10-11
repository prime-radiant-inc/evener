import { installActivityFixture } from "./sessionActivityTestUtils";
// Ask coordinator to stop it (spec 9, ruling 10): a prefilled message to the
// coordinator, sent with the one Send that steers. The durable runtime is the
// real one, on the in-memory SQLite double; the tests assert on the wire.
import type { ThreadCapabilities, ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { activityChangedNotification, wireThread } from "@evener/appwire-client/testing/notifications";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { NativeMutationRuntime } from "../nativeMutationRuntime";
import {
	alertRequests,
	pressable,
	render,
	renderedText,
	screenConnection,
	settle,
	unmountMountedTrees,
} from "../renderNative.testkit";
import { openSqliteSyncDouble } from "../sqliteSync.testkit";
import { forgetStopRequestsForHub, stopRequests } from "./nativeStopRequests";
import { StopSubagentSheet } from "./StopSubagentSheet";
import { forgetSubagentTrees } from "./subagentTree";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
	runtime: null as unknown,
	uuid: 0,
	kv: new Map<string, string>(),
}));
const sheetNavigation = vi.hoisted(() => ({
	goBack: vi.fn(),
	dispatch: vi.fn(),
	prevented: [] as boolean[],
	onPrevent: null as null | ((event: { data: { action: unknown } }) => void),
}));

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	TextInput: "TextInput",
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("@react-navigation/native", () => ({
	useNavigation: () => sheetNavigation,
	usePreventRemove: (prevent: boolean, onPrevent: (event: { data: { action: unknown } }) => void) => {
		sheetNavigation.prevented.push(prevent);
		sheetNavigation.onPrevent = onPrevent;
	},
}));
vi.mock("expo-sqlite", () => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: {
		getItemSync: (key: string) => harness.kv.get(key) ?? null,
		setItemSync: (key: string, value: string) => void harness.kv.set(key, value),
		removeItemSync: (key: string) => void harness.kv.delete(key),
	},
}));
vi.mock("expo-crypto", () => ({
	randomUUID: () => {
		harness.uuid += 1;
		return `stop-uuid-${harness.uuid}`;
	},
	getRandomValues: (array: Uint8Array) => array,
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => harness.connection }));
// The app's one runtime is a module singleton; each test gets its own.
vi.mock("../nativeMutationRuntime", async (original) => ({
	...(await original<typeof import("../nativeMutationRuntime")>()),
	getNativeMutationRuntime: () => harness.runtime,
}));

const COORDINATOR = { ref: "local:coord", threadId: "coord", title: "Get PR 2138 Test Clean" };
const PARAMS = { hubId: "hub-1", coordinator: COORDINATOR, ref: "local:fix" };

const capabilities = (over: Partial<ThreadCapabilities> = {}): ThreadCapabilities => ({
	send: true,
	steer: true,
	interrupt: true,
	compact: false,
	clear: false,
	forkFromTurn: false,
	shutdown: false,
	changeModel: false,
	changeVisionModel: false,
	queue: true,
	goal: false,
	sharedNotes: false,
	rename: false,
	...over,
});

let client: FakeClient;
let runtime: NativeMutationRuntime;
let status = "active";
let failed = false;

const read = (): ThreadReadResponse => ({
	thread: wireThread(COORDINATOR.ref, {
		id: COORDINATOR.threadId,
		status: { type: status },
		turns: [],
		evener: {
			ref: COORDINATOR.ref,
			instanceId: "instance-coord",
			capabilities: capabilities(),
			queue: { revision: 1 },
			mutationStateAuthoritative: true,
		},
	}),
});

const tree = () => ({
	revision: 1,
	root: {
		kind: "session",
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
					projectionRevision: 1,
					...(failed
						? {
								terminal: true,
								outcome: "failed",
								runEndedAt: new Date().toISOString(),
								// A failed subagent with work still running under it.
								child: {
									kind: "session",
									sessionId: "fix",
									ref: "local:fix",
									label: "Fix race in tree settle",
									aggregate: "working",
									counts: { active: 1, failed: 0, completed: 0, complete: true },
									branch: {},
									entries: [
										{
											kind: "shell",
											job: {
												jobId: "job-1",
												ownerSessionId: "fix",
												ownerRef: "local:fix",
												type: "shell",
												status: "running",
												terminal: false,
												background: true,
												hasOutput: false,
												description: "go test",
												startedAt: new Date().toISOString(),
												outputBytes: 0,
											},
										},
									],
								},
							}
						: { runStartedAt: new Date().toISOString() }),
				},
			},
		],
	},
});

const applied = (params: unknown): never =>
	({
		receipt: {
			clientMutationId: (params as { clientMutationId: string }).clientMutationId,
			disposition: "applied",
			threadId: COORDINATOR.threadId,
			turnId: "turn-1",
			projectionState: "pending",
		},
		turn: { id: "turn-1" },
	}) as never;

beforeEach(async () => {
	alertRequests.length = 0;
	sheetNavigation.goBack.mockReset();
	sheetNavigation.prevented = [];
	sheetNavigation.onPrevent = null;
	status = "active";
	failed = false;
	harness.kv.clear();
	forgetStopRequestsForHub("hub-1");
	forgetSubagentTrees("hub-1");
	client = new FakeClient("ready");
	client.on("thread/read", read);
	installActivityFixture(client, () => tree());
	client.on("turn/steer", applied);
	client.on("turn/start", applied);
	client.on("turn/queue", applied);
	harness.connection = screenConnection(client, "ready");
	runtime = new NativeMutationRuntime(openSqliteSyncDouble().port);
	runtime.registerTarget("hub-1", COORDINATOR.ref, client);
	await runtime.start();
	harness.runtime = runtime;
});
afterEach(async () => {
	unmountMountedTrees();
	await runtime.stop();
	vi.restoreAllMocks();
});

async function mount() {
	const mounted = render(
		<StopSubagentSheet
			route={{ key: "stop", name: "StopSubagentSheet", params: PARAMS } as never}
			navigation={sheetNavigation as never}
		/>,
	);
	await settle();
	return mounted;
}

const field = (mounted: ReactTestRenderer) => mounted.root.find((node) => String(node.type) === "TextInput");
const sendDisabled = (mounted: ReactTestRenderer) =>
	pressable(mounted, "Send stop request")?.props.accessibilityState?.disabled === true;
const mutations = () => client.calls.filter((call) => call.method.startsWith("turn/"));

async function send(mounted: ReactTestRenderer) {
	await act(async () => pressable(mounted, "Send stop request")?.props.onPress());
	await settle();
}

it("lets a drag of the sheet put the keyboard away", async () => {
	const mounted = await mount();
	expect(mounted.root.findAllByType("ScrollView" as never)[0]?.props.keyboardDismissMode).toBe("on-drag");
});

it("prefills the message for a running subagent, and says when it arrives", async () => {
	const mounted = await mount();
	expect(renderedText(mounted)).toContain("Stop subagent");
	expect(field(mounted).props.value).toBe("Stop subagent “Fix race in tree settle”: it's no longer needed.");
	expect(renderedText(mounted)).toContain("Arrives at the coordinator's next step");
});

it("prefills the message for a failed subagent", async () => {
	failed = true;
	const mounted = await mount();
	expect(field(mounted).props.value).toBe("Stop subagent “Fix race in tree settle”: it has failed.");
});

it("reads the coordinator without taking the connection's subscription", async () => {
	await mount();
	const reads = client.calls.filter((call) => call.method === "thread/read").map((call) => call.params);
	expect(reads.length).toBeGreaterThan(0);
	expect(reads.filter((params) => (params as { subscribe?: boolean }).subscribe === true)).toHaveLength(1);
	for (const params of reads.filter((params) => (params as { subscribe?: boolean }).subscribe === true))
		expect(params).toMatchObject({ replaceSubscription: false });
});

it("steers a working coordinator, records the request, and closes", async () => {
	const mounted = await mount();
	await send(mounted);
	await vi.waitFor(() => expect(mutations().map((call) => call.method)).toEqual(["turn/steer"]));
	expect(mutations()[0]?.params).toMatchObject({
		ref: COORDINATOR.ref,
		input: [{ type: "text", text: "Stop subagent “Fix race in tree settle”: it's no longer needed." }],
	});
	expect(
		stopRequests("hub-1").view({
			id: "d-fix",
			active: true,
			state: "running",
			delegate: { delegateId: "d-fix", childRef: "local:fix" },
		} as never),
	).toBe("requested");
	expect(sheetNavigation.goBack).toHaveBeenCalled();
});

it("sends to an idle coordinator", async () => {
	status = "idle";
	const mounted = await mount();
	await send(mounted);
	await vi.waitFor(() => expect(mutations().map((call) => call.method)).toEqual(["turn/start"]));
});

it("asks before losing an edited message, and not for the prefill", async () => {
	const mounted = await mount();
	expect(sheetNavigation.prevented.at(-1)).toBe(false);
	act(() => field(mounted).props.onChangeText("Stop it please."));
	expect(sheetNavigation.prevented.at(-1)).toBe(true);
	act(() => sheetNavigation.onPrevent?.({ data: { action: { type: "GO_BACK" } } }));
	expect(alertRequests.at(-1)?.title).toBe("Discard this message?");
});

it("waits for words, and for a coordinator that can take a message", async () => {
	const mounted = await mount();
	act(() => field(mounted).props.onChangeText("   "));
	expect(sendDisabled(mounted)).toBe(true);
	status = "restartRequired";
	const other = await mount();
	expect(sendDisabled(other)).toBe(true);
	expect(renderedText(other)).toContain("The coordinator can't take a message right now.");
});

it("keeps the sheet and the words when the send fails, and says why", async () => {
	vi.spyOn(runtime, "submit").mockRejectedValueOnce(new Error("The mutations database is unavailable"));
	const mounted = await mount();
	await send(mounted);
	expect(renderedText(mounted)).toContain("Couldn't send this: The mutations database is unavailable");
	expect(field(mounted).props.value).toBe("Stop subagent “Fix race in tree settle”: it's no longer needed.");
	expect(sheetNavigation.goBack).not.toHaveBeenCalled();
});

it("waits for a fresh read of the coordinator after the connection comes back", async () => {
	const mounted = await mount();
	expect(sendDisabled(mounted)).toBe(false);
	let answer: (value: ThreadReadResponse) => void = () => {};
	const reply = new Promise<ThreadReadResponse>((resolve) => {
		answer = resolve;
	});
	client.on("thread/read", () => reply);
	const again = () =>
		act(() =>
			mounted.update(
				<StopSubagentSheet
					route={{ key: "stop", name: "StopSubagentSheet", params: PARAMS } as never}
					navigation={sheetNavigation as never}
				/>,
			),
		);
	harness.connection = screenConnection(client, "reconnecting");
	again();
	harness.connection = screenConnection(client, "ready");
	again();
	await settle();
	expect(sendDisabled(mounted)).toBe(true);
	await act(async () => answer(read()));
	await settle();
	expect(sendDisabled(mounted)).toBe(false);
});

it("reads the tree under the coordinator's thread as it reads now, after a restart gave it a new one", async () => {
	// The sheet opened with the route's thread, but the coordinator has since
	// restarted under a new one: the tree it reads comes back under that new
	// thread, and the sheet still takes it as the coordinator's.
	client.on(
		"thread/read",
		() =>
			({
				thread: wireThread(COORDINATOR.ref, {
					id: "coord-restarted",
					status: { type: status },
					turns: [],
					evener: {
						ref: COORDINATOR.ref,
						instanceId: "instance-coord",
						capabilities: capabilities(),
						queue: { revision: 1 },
						mutationStateAuthoritative: true,
					},
				}),
			}) as ThreadReadResponse,
	);
	installActivityFixture(client, () => ({ ...tree(), root: { ...tree().root, sessionId: "coord-restarted" } }));
	const mounted = await mount();
	expect(field(mounted).props.value).toContain("Fix race in tree settle");
	expect(sendDisabled(mounted)).toBe(false);
	expect(sheetNavigation.goBack).not.toHaveBeenCalled();
});

it("reads and sends only through its own hub's connection", async () => {
	harness.connection = { ...screenConnection(client, "ready"), activeProfile: { id: "hub-2", name: "Other hub" } };
	const mounted = await mount();
	expect(client.calls.filter((call) => call.method === "thread/read")).toEqual([]);
	expect(sendDisabled(mounted)).toBe(true);
});

it("stays open while the tree is only partly listed", async () => {
	installActivityFixture(client, (cursor) =>
		cursor
			? Promise.reject(new Error("offline"))
			: { revision: 1, root: { ...tree().root, entries: [], branch: { truncated: true, continuation: "page-2" } } },
	);
	await mount();
	expect(sheetNavigation.goBack).not.toHaveBeenCalled();
});

it("sends as the coordinator is when you press Send, not as it was when the sheet opened", async () => {
	const mounted = await mount();
	status = "idle";
	await send(mounted);
	await vi.waitFor(() => expect(mutations().map((call) => call.method)).toEqual(["turn/start"]));
});

it("keeps an edited message open when the subagent leaves the tree, rather than dropping it", async () => {
	const mounted = await mount();
	act(() => field(mounted).props.onChangeText("Stop it please."));
	installActivityFixture(client, () => ({ ...tree(), revision: 2, root: { ...tree().root, entries: [] } }));
	act(() => client.emitNotification(activityChangedNotification(COORDINATOR)));
	await settle();
	expect(sheetNavigation.goBack).not.toHaveBeenCalled();
});

it("says it waits for the connection while offline, not that the message will arrive", async () => {
	harness.connection = screenConnection(client, "reconnecting");
	const mounted = await mount();
	expect(renderedText(mounted)).toContain("Send when you're back online.");
	expect(renderedText(mounted)).not.toContain("Arrives at the coordinator's next step");
});
