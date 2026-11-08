// The Review sheet (spec 10.2, rulings 16, 26 and 30): a verdict, an optional
// note and the document's comments, sent through the composer's one Send.
// The durable runtime is the real one, on the in-memory SQLite double, and
// the tests assert on what reaches the hub.
import type { ThreadCapabilities, ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { hasTestID, markers, withTestID } from "../markerTestUtils";
import { NativeMutationRuntime } from "../nativeMutationRuntime";
import {
	alertRequests,
	pressable,
	render,
	renderedText,
	settle,
	textOf,
	unmountMountedTrees,
} from "../renderNative.testkit";
import { openSqliteSyncDouble } from "../sqliteSync.testkit";
import type { SyncStringStorage } from "../syncStringStorage";
import { documentBlocks } from "./documentBlocks";
import { DocumentMemory } from "./documentMemory";
import { ReviewSheet } from "./ReviewSheet";
import { reviewMessage } from "./reviewMessage";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
	memory: null as unknown,
	runtime: null as unknown,
}));
const sheetNavigation = vi.hoisted(() => ({
	goBack: vi.fn(),
	dispatch: vi.fn(),
	navigate: vi.fn(),
	pop: vi.fn(),
	getState: () => ({ index: 0, routes: [] as { key: string; name: string; params?: object }[] }),
	prevented: [] as boolean[],
	onPrevent: null as null | ((event: { data: { action: unknown } }) => void),
}));

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	TextInput: "TextInput",
}));
vi.mock("@react-navigation/native", () => ({
	useNavigation: () => sheetNavigation,
	usePreventRemove: (prevent: boolean, onPrevent: (event: { data: { action: unknown } }) => void) => {
		sheetNavigation.prevented.push(prevent);
		sheetNavigation.onPrevent = onPrevent;
	},
}));
vi.mock("expo-sqlite", () => ({ openDatabaseSync: vi.fn() }));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => harness.connection }));
vi.mock("./nativeDocumentMemory", () => ({ documentMemory: () => harness.memory }));
// The app's one runtime is a module singleton; each test gets its own.
vi.mock("../nativeMutationRuntime", async (original) => ({
	...(await original<typeof import("../nativeMutationRuntime")>()),
	getNativeMutationRuntime: () => harness.runtime,
}));

const PLAN = "# Settle the race\n\nThe goal is a clean run.\n\n## Steps\n\n- Reproduce it.\n- Fix it.\n";
const PATH = "docs/superpowers/plans/settle-race.md";
const KEY = { sessionRef: "local:fix", path: PATH };
const PARAMS: {
	hubId: string;
	sessionRef: string;
	path: string;
	sessionTitle: string;
	root?: "artifacts";
	readTarget?: string;
} = {
	hubId: "studio",
	sessionRef: "local:fix",
	path: PATH,
	sessionTitle: "Get PR 2138 Test Clean",
};

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
let memory: DocumentMemory;
let runtime: NativeMutationRuntime;
let status = "idle";
let resumeRequired = false;

function storage(): SyncStringStorage {
	const data = new Map<string, string>();
	return {
		getItemSync: (key) => data.get(key) ?? null,
		setItemSync: (key, value) => void data.set(key, value),
		removeItemSync: (key) => void data.delete(key),
	};
}

const read = (): ThreadReadResponse => ({
	thread: wireThread("local:fix", {
		id: "thread-fix",
		status: { type: status },
		turns: [],
		evener: {
			ref: "local:fix",
			instanceId: "instance-fix",
			capabilities: capabilities(),
			queue: { revision: 1 },
			mutationStateAuthoritative: true,
			...(resumeRequired ? { resumeRequired: true } : {}),
		},
	}),
});

const applied = (params: unknown): never =>
	({
		receipt: {
			clientMutationId: (params as { clientMutationId: string }).clientMutationId,
			disposition: "applied",
			threadId: "thread-fix",
			turnId: "turn-1",
			projectionState: "pending",
		},
		turn: { id: "turn-1" },
	}) as never;

const blocks = documentBlocks(PLAN);
const goal = blocks[1];
const item = blocks[4];
if (!goal || !item) throw new Error("the plan's blocks moved");

beforeEach(async () => {
	alertRequests.length = 0;
	sheetNavigation.goBack.mockReset();
	sheetNavigation.navigate.mockReset();
	sheetNavigation.pop.mockReset();
	sheetNavigation.getState = () => ({
		index: 3,
		routes: [
			{ key: "board", name: "Sessions" },
			{
				key: "fix",
				name: "Conversation",
				params: { hubId: "studio", ref: "local:fix", title: "Get PR 2138 Test Clean" },
			},
			{ key: "reader", name: "Reader" },
			{ key: "review", name: "ReviewSheet" },
		],
	});
	sheetNavigation.prevented = [];
	sheetNavigation.onPrevent = null;
	status = "idle";
	resumeRequired = false;
	client = new FakeClient("ready");
	client.on("thread/read", read);
	// A resume clears the session's fence, so the read that follows it is the
	// one that releases a parked review.
	client.resumeThread = async () => {
		resumeRequired = false;
		return read();
	};
	client.on("turn/start", applied);
	client.on("turn/queue", applied);
	harness.connection = { state: "ready", client };
	memory = new DocumentMemory(storage(), "studio");
	memory.addComment(KEY, { blockIndex: 1, blockHash: goal.hash, quote: goal.text, text: "Say which run." });
	memory.addComment(KEY, { blockIndex: 4, blockHash: item.hash, quote: item.text, text: "Name the fix." });
	harness.memory = memory;
	runtime = new NativeMutationRuntime(openSqliteSyncDouble().port);
	runtime.registerTarget("studio", "local:fix", client);
	await runtime.start();
	harness.runtime = runtime;
});
afterEach(async () => {
	unmountMountedTrees();
	await runtime.stop();
	vi.restoreAllMocks();
});

async function mount(params: typeof PARAMS = PARAMS) {
	const tree = render(
		<ReviewSheet
			route={{ key: "review", name: "ReviewSheet", params } as never}
			navigation={sheetNavigation as never}
		/>,
	);
	await settle();
	return tree;
}

const sendButton = (tree: ReactTestRenderer) => pressable(tree, "Send review");
const sendDisabled = (tree: ReactTestRenderer) => sendButton(tree)?.props.accessibilityState?.disabled === true;
const mutations = () => client.calls.map((call) => call.method).filter((method) => method.startsWith("turn/"));

function choose(tree: ReactTestRenderer, verdict: string) {
	act(() => pressable(tree, verdict)?.props.onPress());
}

function typeNote(tree: ReactTestRenderer, text: string) {
	const field = tree.root.find((node) => String(node.type) === "TextInput");
	act(() => field.props.onChangeText(text));
}

async function send(tree: ReactTestRenderer) {
	await act(async () => sendButton(tree)?.props.onPress());
	await settle();
	await vi.waitFor(() => expect(mutations().length).toBeGreaterThan(0));
}

function sentText(): string {
	const call = client.calls.find((entry) => entry.method.startsWith("turn/"));
	return (call?.params as { input: { text: string }[] }).input[0]?.text ?? "";
}

it("names where the review goes, lists the comments, and waits for a verdict", async () => {
	const tree = await mount();
	const text = renderedText(tree);
	expect(text).toContain("Get PR 2138 Test Clean");
	expect(text).toContain("settle-race.md");
	expect(text).toContain("The goal is a clean run.");
	expect(text).toContain("Say which run.");
	expect(text).toContain("Name the fix.");
	expect(hasTestID(tree, "review-verdict-hint")).toBe(true);
	expect(sendDisabled(tree)).toBe(true);
	choose(tree, "Request changes");
	expect(hasTestID(tree, "review-verdict-hint")).toBe(false);
	expect(pressable(tree, "Request changes")?.props.accessibilityState).toMatchObject({ selected: true });
	expect(sendDisabled(tree)).toBe(false);
});

it("lets a drag of the sheet put the keyboard away", async () => {
	const tree = await mount();
	expect(tree.root.findAllByType("ScrollView" as never)[0]?.props.keyboardDismissMode).toBe("on-drag");
});

it("keeps a chosen verdict's words where they were, centred, at the same weight", async () => {
	// A bolder "Request changes" no longer fit its third of the row and wrapped
	// flush left (seen on the simulator, phase 4 PR 9).
	const tree = await mount();
	const label = () =>
		pressable(tree, "Request changes")?.find((node) => String(node.type) === "Text").props.style as {
			textAlign?: string;
			fontWeight?: string;
		};
	const before = label().fontWeight;
	choose(tree, "Request changes");
	expect(label().textAlign).toBe("center");
	expect(label().fontWeight).toBe(before);
});

it("says so when there are no comments", async () => {
	memory.clearComments(KEY);
	const tree = await mount();
	expect(hasTestID(tree, "review-no-comments")).toBe(true);
});

it("offers a note whose prompt follows the verdict", async () => {
	const tree = await mount();
	const placeholder = () => tree.root.find((node) => String(node.type) === "TextInput").props.placeholder;
	const unchosen = placeholder();
	expect(unchosen).toMatch(/\S/);
	choose(tree, "Approve");
	const approving = placeholder();
	expect(approving).toMatch(/\S/);
	expect(approving).not.toBe(unchosen);
	choose(tree, "Comment only");
	expect(placeholder()).toBe(unchosen);
});

it("sends the review's message to an idle session, clears the comments, and returns to the session", async () => {
	const tree = await mount();
	choose(tree, "Request changes");
	typeNote(tree, "Close. Fix the ordering and go.");
	const comments = memory.comments(KEY);
	await send(tree);
	expect(mutations()).toEqual(["turn/start"]);
	expect(sentText()).toBe(reviewMessage(PATH, "requestChanges", comments, "Close. Fix the ordering and go."));
	expect(memory.comments(KEY)).toEqual([]);
	expect(sheetNavigation.pop).toHaveBeenCalledWith(2);
	expect(sheetNavigation.goBack).not.toHaveBeenCalled();
	expect(hasTestID(tree, "review-send-failed")).toBe(false);
});

// An agent files a report in the session's retained artifacts, and the folder
// holds a report of the same name. The review must name the file that was read,
// and sending it must not clear the other report's unsent comments.
it("names an artifacts report by its full path and leaves a same-named folder report's drafts alone", async () => {
	const artifactsDir = "/state/sessions/local-fix/artifacts";
	const retainedKey = { sessionRef: "local:fix", path: "report.md", root: "artifacts" as const };
	memory.addComment(retainedKey, { blockIndex: 1, blockHash: goal.hash, quote: goal.text, text: "Keep this." });
	const tree = await mount({
		...PARAMS,
		path: "report.md",
		root: "artifacts",
		readTarget: `${artifactsDir}/report.md`,
	});
	choose(tree, "Approve");
	const drafts = memory.comments(retainedKey);
	await send(tree);
	expect(sentText()).toBe(reviewMessage(`${artifactsDir}/report.md`, "approve", drafts, ""));
	expect(memory.comments(retainedKey)).toEqual([]);
	expect(memory.comments(KEY).map((comment) => comment.text)).toEqual(["Say which run.", "Name the fix."]);
});

it("queues the review while the session works, and never steers or interrupts it", async () => {
	status = "active";
	const tree = await mount();
	choose(tree, "Approve");
	await send(tree);
	expect(mutations()).toEqual(["turn/queue"]);
});

it("queues the review for a session that started working after the sheet opened", async () => {
	const tree = await mount();
	choose(tree, "Approve");
	status = "active";
	await send(tree);
	expect(mutations()).toEqual(["turn/queue"]);
});

it("sends nothing to a session that stopped taking messages after the sheet opened, and says so", async () => {
	const tree = await mount();
	choose(tree, "Approve");
	status = "restartRequired";
	await act(async () => sendButton(tree)?.props.onPress());
	await settle();
	expect(mutations()).toEqual([]);
	expect(markers(tree, "review-held")).toEqual(["unavailable"]);
	expect(sheetNavigation.pop).not.toHaveBeenCalled();
});

it("forgets what Send did while the connection was down, until it reads again", async () => {
	const tree = await mount();
	choose(tree, "Approve");
	expect(sendDisabled(tree)).toBe(false);
	let answer: (value: ThreadReadResponse) => void = () => {};
	client.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (answer = resolve)));
	const again = () =>
		act(() =>
			tree.update(
				<ReviewSheet
					route={{ key: "review", name: "ReviewSheet", params: PARAMS } as never}
					navigation={sheetNavigation as never}
				/>,
			),
		);
	harness.connection = { state: "reconnecting", client };
	again();
	harness.connection = { state: "ready", client };
	again();
	await settle();
	expect(sendDisabled(tree)).toBe(true);
	await act(async () => answer(read()));
	await settle();
	expect(sendDisabled(tree)).toBe(false);
});

it("resumes a shut-down session with the review", async () => {
	status = "ended";
	const tree = await mount();
	choose(tree, "Comment only");
	await send(tree);
	expect(mutations()).toEqual(["turn/start"]);
});

it("can't send to a session that needs a restart, and says so", async () => {
	status = "restartRequired";
	const tree = await mount();
	choose(tree, "Approve");
	expect(sendDisabled(tree)).toBe(true);
	expect(markers(tree, "review-held")).toEqual(["unavailable"]);
});

// A recovery-fenced session takes the review the way it takes any message:
// Send parks it durably and the resume behind it releases the message.
it("sends a review to a paused session by resuming it", async () => {
	status = "idle";
	resumeRequired = true;
	const tree = await mount();
	choose(tree, "Approve");
	expect(sendDisabled(tree)).toBe(false);
	await send(tree);
	expect(mutations()).toEqual(["turn/start"]);
});

it("waits for the connection, and reads again when it returns", async () => {
	harness.connection = { state: "reconnecting", client };
	const tree = await mount();
	choose(tree, "Approve");
	expect(sendDisabled(tree)).toBe(true);
	expect(markers(tree, "review-held")).toEqual(["offline"]);
	expect(client.calls.filter((call) => call.method === "thread/read")).toEqual([]);
	harness.connection = { state: "ready", client };
	act(() =>
		tree.update(
			<ReviewSheet
				route={{ key: "review", name: "ReviewSheet", params: PARAMS } as never}
				navigation={sheetNavigation as never}
			/>,
		),
	);
	await settle();
	expect(markers(tree, "review-held")).toEqual([]);
	expect(sendDisabled(tree)).toBe(false);
});

it("keeps Send disabled until the session's read answers", async () => {
	let answer: (value: ThreadReadResponse) => void = () => {};
	client.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (answer = resolve)));
	const tree = await mount();
	choose(tree, "Approve");
	expect(sendDisabled(tree)).toBe(true);
	await act(async () => answer(read()));
	await settle();
	expect(sendDisabled(tree)).toBe(false);
});

it("queues the review behind this phone's own send that still waits", async () => {
	await runtime.submit({
		kind: "send",
		hubId: "studio",
		targetRef: "local:fix",
		threadId: "thread-fix",
		instanceId: "instance-fix",
		input: [{ type: "text", text: "first" }],
	});
	const tree = await mount();
	choose(tree, "Approve");
	await act(async () => sendButton(tree)?.props.onPress());
	await vi.waitFor(() => expect(mutations()).toEqual(["turn/start", "turn/queue"]));
	const texts = client.calls
		.filter((call) => call.method.startsWith("turn/"))
		.map((call) => (call.params as { input: { text: string }[] }).input[0]?.text);
	expect(texts[0]).toBe("first");
	expect(texts[1]).toContain("Review of docs/superpowers/plans/settle-race.md: approved.");
});

it("asks before losing a chosen verdict or a typed note", async () => {
	const tree = await mount();
	expect(sheetNavigation.prevented.at(-1)).toBe(false);
	choose(tree, "Approve");
	expect(sheetNavigation.prevented.at(-1)).toBe(true);
	const asked = alertRequests.length;
	act(() => sheetNavigation.onPrevent?.({ data: { action: { type: "GO_BACK" } } }));
	expect(alertRequests).toHaveLength(asked + 1);
	expect(alertRequests.at(-1)?.buttons?.map((button) => button.style)).toEqual(["cancel", "destructive"]);
	expect(memory.comments(KEY)).toHaveLength(2);
});

it("keeps everything when the send fails, and says why", async () => {
	vi.spyOn(runtime, "submit").mockRejectedValue(new Error("The mutations database is unavailable"));
	const tree = await mount();
	choose(tree, "Request changes");
	typeNote(tree, "Split it.");
	await act(async () => sendButton(tree)?.props.onPress());
	await settle();
	const [failed] = withTestID(tree, "review-send-failed");
	expect(failed && textOf(failed)).toContain("The mutations database is unavailable");
	expect(pressable(tree, "Request changes")?.props.accessibilityState).toMatchObject({ selected: true });
	expect(tree.root.find((node) => String(node.type) === "TextInput").props.value).toBe("Split it.");
	expect(memory.comments(KEY)).toHaveLength(2);
	expect(sheetNavigation.pop).not.toHaveBeenCalled();
	expect(sheetNavigation.goBack).not.toHaveBeenCalled();
	expect(sendDisabled(tree)).toBe(false);
});
