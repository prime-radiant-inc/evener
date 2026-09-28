// The Reader (spec 10.2): a document from a session's folder, with what
// changed since you last read it, where you were, and its outline.
import type { NativeStackHeaderItem, NativeStackNavigationOptions } from "@react-navigation/native-stack";
import type { ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cloneElement } from "react";
import { FlatList } from "react-native";
import type { ReactTestInstance, ReactTestRenderer } from "react-test-renderer";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { flatListCalls, pressable, render, renderedText } from "../renderNative.testkit";
import { sheetKey } from "../sheet/sheetHosts";
import type { SyncStringStorage } from "../syncStringStorage";
import { documentBlocks } from "./documentBlocks";
import { DocumentMemory } from "./documentMemory";
import { ReaderScreen } from "./ReaderScreen";
import { readerHosts } from "./readerHosts";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
	memory: null as unknown,
	clipboard: [] as string[],
	appState: [] as ((state: string) => void)[],
}));
const stack = vi.hoisted(() => ({
	state: { index: 2, routes: [] as { key: string; name: string; params?: object }[] },
}));

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	Image: "Image",
	AppState: {
		currentState: "active",
		addEventListener: (_event: string, listener: (state: string) => void) => {
			harness.appState.push(listener);
			return { remove: () => {} };
		},
	},
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-safe-area-context", () => ({
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
}));
vi.mock("react-native-enriched-markdown", () => ({ EnrichedMarkdownText: "EnrichedMarkdownText" }));
vi.mock("expo-clipboard", () => ({
	setStringAsync: vi.fn(async (text: string) => {
		harness.clipboard.push(text);
	}),
}));
vi.mock("@react-navigation/native", () => ({
	useNavigationState: <T,>(select: (state: typeof stack.state) => T) => select(stack.state),
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: vi.fn(async (key: string) =>
		key === "evener.hub.studio"
			? JSON.stringify({ id: "studio", name: "Studio", origin: "https://hub.test", token: "secret" })
			: null,
	),
	setItemAsync: vi.fn(async () => {}),
	deleteItemAsync: vi.fn(async () => {}),
}));
// SessionLink's module also holds the durable outbox, which opens sqlite.
vi.mock("expo-sqlite", () => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "test-uuid", getRandomValues: (array: Uint8Array) => array }));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => harness.connection }));
vi.mock("./nativeDocumentMemory", () => ({ documentMemory: () => harness.memory }));

const PLAN = "# Settle the race\n\nThe goal is a clean run.\n\n## Steps\n\n- Reproduce it.\n- Fix it.\n";
const OLDER = "# Settle the race\n\nThe goal is green.\n\n## Steps\n\n- Reproduce it.\n- Patch it.\n";
const PATH = "docs/superpowers/plans/settle.md";
const KEY = { sessionRef: "local:fix", path: PATH };
const MINUTE = 60_000;

type Served = { status?: number; body: string; headers?: Record<string, string> };
let served: Record<string, Served> = {};
let fetchSpy: ReturnType<typeof vi.spyOn>;
let client: FakeClient;
let memory: DocumentMemory;
// The memory's clock: the phone's, unless a test moves it.
let clockOffset = 0;
const trees: ReactTestRenderer[] = [];

function storage(): SyncStringStorage {
	const data = new Map<string, string>();
	return {
		getItemSync: (key) => data.get(key) ?? null,
		setItemSync: (key, value) => void data.set(key, value),
		removeItemSync: (key) => void data.delete(key),
	};
}

const threadRead = (status: string): ThreadReadResponse =>
	({
		thread: {
			id: "thread-fix",
			status: { type: status },
			modelProvider: "glm",
			evener: { ref: "local:fix", instanceId: "instance-fix", capabilities: {}, queue: { revision: 1 } },
		},
	}) as ThreadReadResponse;

beforeEach(() => {
	flatListCalls.length = 0;
	harness.clipboard = [];
	harness.appState = [];
	served = { [PATH]: { body: PLAN } };
	fetchSpy = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
		const url = new URL(String(input));
		const answer = served[url.searchParams.get("path") ?? ""] ?? { status: 404, body: "not found" };
		return new Response(answer.body, {
			status: answer.status ?? 200,
			headers: { "Content-Type": "text/plain; charset=utf-8", ...answer.headers },
		});
	});
	client = new FakeClient("ready");
	client.on("thread/read", () => threadRead("idle"));
	harness.connection = {
		profiles: [{ id: "studio", name: "Studio", origin: "https://hub.test" }],
		state: "ready",
		client,
	};
	clockOffset = 0;
	memory = new DocumentMemory(storage(), "studio", () => Date.now() + clockOffset);
	harness.memory = memory;
	stack.state = {
		index: 2,
		routes: [
			{ key: "board", name: "Sessions" },
			{ key: "coord", name: "Conversation", params: { hubId: "studio", ref: "local:coord", title: "Coordinator" } },
			{ key: "reader-1", name: "Reader" },
		],
	};
});
afterEach(() => {
	for (const tree of trees.splice(0)) act(() => tree.unmount());
	vi.restoreAllMocks();
});

async function settle() {
	await act(async () => {
		for (let turn = 0; turn < 10; turn += 1) await Promise.resolve();
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
}

function navigationDouble() {
	const options: NativeStackNavigationOptions[] = [];
	return {
		options,
		setOptions: vi.fn((next: NativeStackNavigationOptions) => options.push(next)),
		navigate: vi.fn(),
		pop: vi.fn(),
		getState: () => stack.state,
		latest: <Key extends keyof NativeStackNavigationOptions>(key: Key) =>
			options.findLast((entry) => key in entry)?.[key],
	};
}

async function mount(path = PATH, extra: Record<string, unknown> = {}) {
	const navigation = navigationDouble();
	const params = {
		hubId: "studio",
		sessionRef: "local:fix",
		path,
		reviewRef: "local:coord",
		reviewTitle: "Coordinator",
		...extra,
	};
	const element = (
		<ReaderScreen route={{ key: "reader-1", name: "Reader", params } as never} navigation={navigation as never} />
	);
	const tree = render(element);
	trees.push(tree);
	await settle();
	// Renders again, as a change in the navigation state would.
	const rerender = async () => {
		// A fresh element: React skips an update given the identical one.
		act(() => tree.update(cloneElement(element)));
		await settle();
	};
	return { tree, navigation, rerender };
}

/** mount, settling on microtasks alone, for a test that fakes setTimeout. */
async function mountWithFakeTimers() {
	const navigation = navigationDouble();
	const params = { hubId: "studio", sessionRef: "local:fix", path: PATH, reviewRef: "local:coord", reviewTitle: "Coordinator" };
	const tree = render(<ReaderScreen route={{ key: "reader-1", name: "Reader", params } as never} navigation={navigation as never} />);
	trees.push(tree);
	await act(async () => {
		for (let turn = 0; turn < 50; turn += 1) await Promise.resolve();
	});
	return { tree, navigation };
}

function documentList(tree: ReactTestRenderer): ReactTestInstance {
	return tree.root.findAllByType(FlatList as never)[0] as ReactTestInstance;
}

function ruled(tree: ReactTestRenderer): ReactTestInstance[] {
	return tree.root.findAll(
		(node) => String(node.type) === "View" && (node.props.style as { borderLeftWidth?: number } | undefined)?.borderLeftWidth === 3,
	);
}

function headerItems(navigation: ReturnType<typeof navigationDouble>): NativeStackHeaderItem[] {
	return navigation.latest("unstable_headerRightItems")?.({ canGoBack: true } as never) ?? [];
}

function menuAction(navigation: ReturnType<typeof navigationDouble>, label: string) {
	const menu = headerItems(navigation).find((item) => item.type === "menu");
	if (menu?.type !== "menu") throw new Error("no ⋯ menu");
	const action = menu.menu.items.find((item) => item.type === "action" && item.label === label);
	if (action?.type !== "action") throw new Error(`no ${label}`);
	return action;
}

it("renders a plan's blocks under its caption", async () => {
	const updatedAt = new Date(Date.now() - 3 * MINUTE).toISOString();
	const { tree } = await mount(PATH, { updatedAt });
	const text = renderedText(tree);
	expect(text).toContain("Plan · updated 3m ago");
	const blocks = tree.root.findAll((node) => String(node.type) === "EnrichedMarkdownText");
	expect(blocks.map((block) => block.props.markdown)).toEqual([
		"# Settle the race",
		"The goal is a clean run.",
		"## Steps",
		"- Reproduce it.",
		"- Fix it.",
	]);
	expect(fetchSpy).toHaveBeenCalledWith(
		"https://hub.test/doc/file?format=raw&session=local%3Afix&path=docs%2Fsuperpowers%2Fplans%2Fsettle.md",
		{ headers: { Authorization: "Bearer secret" } },
	);
});

it("keeps its update time current while it's open", async () => {
	vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] });
	try {
		const updatedAt = new Date(Date.now() - 3 * MINUTE).toISOString();
		const { tree } = await mount(PATH, { updatedAt });
		expect(renderedText(tree)).toContain("updated 3m ago");
		act(() => {
			vi.advanceTimersByTime(2 * MINUTE);
		});
		expect(renderedText(tree)).toContain("updated 5m ago");
	} finally {
		vi.useRealTimers();
	}
});

it("leaves out an update time that doesn't parse", async () => {
	const { tree } = await mount(PATH, { updatedAt: "not a time" });
	expect(renderedText(tree)).toContain("Plan");
	expect(renderedText(tree)).not.toContain("updated");
});

it("marks what changed since your last read, and steps through the changes", async () => {
	clockOffset = -24 * 60 * MINUTE;
	memory.left(KEY, {
		title: "Settle the race",
		blocks: documentBlocks(OLDER).map((block) => block.hash),
		position: null,
		reviewRef: "local:coord",
		reviewTitle: "Coordinator",
	});
	clockOffset = 0;
	const { tree } = await mount();
	expect(renderedText(tree)).toContain("2 changes since you read it yesterday");
	expect(ruled(tree)).toHaveLength(2);
	expect(renderedText(tree)).toContain("2 changes");
	flatListCalls.length = 0;
	act(() => pressable(tree, "Next change")?.props.onPress());
	expect(renderedText(tree)).toContain("Change 1 of 2");
	expect(flatListCalls).toContainEqual({ method: "scrollToIndex", args: { index: 1, animated: true } });
	act(() => pressable(tree, "Next change")?.props.onPress());
	expect(renderedText(tree)).toContain("Change 2 of 2");
	act(() => pressable(tree, "Next change")?.props.onPress());
	expect(renderedText(tree)).toContain("Change 1 of 2");
	act(() => pressable(tree, "Previous change")?.props.onPress());
	expect(renderedText(tree)).toContain("Change 2 of 2");
	expect(flatListCalls.at(-1)).toEqual({ method: "scrollToIndex", args: { index: 4, animated: true } });
});

it("starts the steps over when a re-read changes what changed", async () => {
	memory.left(KEY, {
		title: "Settle the race",
		blocks: documentBlocks(OLDER).map((block) => block.hash),
		position: null,
		reviewRef: "local:coord",
		reviewTitle: "Coordinator",
	});
	const { tree } = await mount();
	act(() => pressable(tree, "Previous change")?.props.onPress());
	expect(renderedText(tree)).toContain("Change 2 of 2");
	served[PATH] = { body: OLDER.replace("Patch it.", "Fix it.") };
	act(() => {
		for (const listener of harness.appState) listener("active");
	});
	await settle();
	expect(renderedText(tree)).not.toContain("Change 2 of 1");
	expect(renderedText(tree)).toContain("1 change");
});

it("shows no changes and no bar on a first read", async () => {
	const { tree } = await mount();
	expect(ruled(tree)).toHaveLength(0);
	expect(pressable(tree, "Next change")).toBeUndefined();
	expect(renderedText(tree)).not.toContain("since you read it");
});

it("reopens at the remembered block", async () => {
	const blocks = documentBlocks(PLAN);
	memory.savePosition(KEY, { blockIndex: 3, blockHash: blocks[3]?.hash ?? "", offset: 12, progress: 0.5 });
	await mount();
	expect(flatListCalls).toContainEqual({
		method: "scrollToIndex",
		args: { index: 3, viewOffset: -12, animated: false },
	});
});

it("tries a scroll again once the list has measured its row, unless you've jumped elsewhere since", async () => {
	vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
	try {
		const { tree } = await mountWithFakeTimers();
		const list = documentList(tree);
		const host = readerHosts.get(sheetKey("studio", "local:fix", PATH));
		flatListCalls.length = 0;
		act(() => host?.jumpTo(4));
		act(() => list.props.onScrollToIndexFailed({ index: 4, averageItemLength: 50, highestMeasuredFrameIndex: 2 }));
		expect(flatListCalls.at(-1)).toEqual({ method: "scrollToOffset", args: { offset: 200, animated: false } });
		act(() => {
			vi.advanceTimersByTime(50);
		});
		expect(flatListCalls.at(-1)).toEqual({ method: "scrollToIndex", args: { index: 4, animated: true } });
		act(() => list.props.onScrollToIndexFailed({ index: 4, averageItemLength: 50, highestMeasuredFrameIndex: 2 }));
		act(() => host?.jumpTo(1));
		flatListCalls.length = 0;
		act(() => {
			vi.advanceTimersByTime(200);
		});
		expect(flatListCalls).toEqual([]);
	} finally {
		vi.useRealTimers();
	}
});

it("remembers where you were and what you read when you leave, and leaves the way back", async () => {
	const { tree } = await mount();
	const list = documentList(tree);
	const blocks = documentBlocks(PLAN);
	act(() => list.props.onViewableItemsChanged({ viewableItems: [{ index: 1 }, { index: 2 }], changed: [] }));
	const scroll = {
		nativeEvent: { contentOffset: { y: 100 }, layoutMeasurement: { height: 400 }, contentSize: { height: 1000 } },
	};
	act(() => {
		list.props.onScroll(scroll);
		list.props.onMomentumScrollEnd(scroll);
	});
	expect(memory.position(KEY)).toEqual({ blockIndex: 1, blockHash: blocks[1]?.hash, offset: 0, progress: 0.5 });
	const tree0 = trees.pop();
	act(() => tree0?.unmount());
	expect(memory.lastRead(KEY)?.blocks).toEqual(blocks.map((block) => block.hash));
	expect(memory.continueReading()).toMatchObject({
		sessionRef: "local:fix",
		path: PATH,
		title: "Settle the race",
		reviewRef: "local:coord",
		reviewTitle: "Coordinator",
		progress: 0.5,
	});
});

it("leaves no way back once you read to the end", async () => {
	const { tree } = await mount();
	const list = documentList(tree);
	act(() => list.props.onViewableItemsChanged({ viewableItems: [{ index: 4 }], changed: [] }));
	const scroll = {
		nativeEvent: { contentOffset: { y: 600 }, layoutMeasurement: { height: 400 }, contentSize: { height: 1000 } },
	};
	act(() => list.props.onScrollEndDrag(scroll));
	const tree0 = trees.pop();
	act(() => tree0?.unmount());
	expect(memory.continueReading()).toBeNull();
});

it("keeps the version you last read when you leave a document that didn't load", async () => {
	const older = documentBlocks(OLDER).map((block) => block.hash);
	memory.left(KEY, { title: "Settle the race", blocks: older, position: null, reviewRef: "local:coord", reviewTitle: "Coordinator" });
	served[PATH] = { status: 500, body: "boom" };
	await mount();
	const tree0 = trees.pop();
	act(() => tree0?.unmount());
	expect(memory.lastRead(KEY)?.blocks).toEqual(older);
});

it("remembers where you were in a code file, and reopens there", async () => {
	served["src/race.go"] = { body: "package race\n\nfunc Settle() {}\n" };
	const code = { sessionRef: "local:fix", path: "src/race.go" };
	const { tree } = await mount("src/race.go");
	const list = documentList(tree);
	act(() => list.props.onViewableItemsChanged({ viewableItems: [{ index: 2 }], changed: [] }));
	const scroll = {
		nativeEvent: { contentOffset: { y: 100 }, layoutMeasurement: { height: 400 }, contentSize: { height: 1000 } },
	};
	act(() => list.props.onScrollEndDrag(scroll));
	const tree0 = trees.pop();
	act(() => tree0?.unmount());
	expect(memory.position(code)).toMatchObject({ blockIndex: 2, offset: 0, progress: 0.5 });
	expect(memory.continueReading()).toMatchObject({ path: "src/race.go", progress: 0.5 });
	flatListCalls.length = 0;
	await mount("src/race.go");
	expect(flatListCalls).toContainEqual({ method: "scrollToIndex", args: { index: 2, viewOffset: -0, animated: false } });
});

it("says a document was cut short", async () => {
	served[PATH] = { body: PLAN, headers: { "X-Doc-Truncated": "true", "X-Doc-Total-Size": String(3 * 1024 * 1024) } };
	const { tree } = await mount();
	expect(renderedText(tree)).toContain(`Showing the first ${PLAN.length} bytes of 3 MB`);
});

it("says why it can't show a binary, a missing file, or another host's document", async () => {
	served["out/data.bin"] = { body: "\u0000\u0001", headers: { "Content-Type": "application/octet-stream" } };
	const binary = await mount("out/data.bin");
	expect(renderedText(binary.tree)).toContain("data.bin isn't text, so it can't be shown here (2 bytes).");
	const missing = await mount("docs/gone.md");
	expect(renderedText(missing.tree)).toContain("gone.md isn't in this session's folder any more.");
	fetchSpy.mockClear();
	const navigation = navigationDouble();
	const tree = render(
		<ReaderScreen
			route={
				{
					key: "reader-1",
					name: "Reader",
					params: { hubId: "studio", sessionRef: "laptop:fix", path: PATH, reviewRef: "laptop:fix", reviewTitle: "Fix" },
				} as never
			}
			navigation={navigation as never}
		/>,
	);
	trees.push(tree);
	await settle();
	expect(renderedText(tree)).toContain("This document is on laptop. Open it on the host to read it.");
	expect(fetchSpy).not.toHaveBeenCalled();
});

it("numbers a code file's lines", async () => {
	served["src/race.go"] = { body: "package race\n\nfunc Settle() {}\n" };
	const { tree } = await mount("src/race.go");
	const text = renderedText(tree);
	expect(text).toContain("Code");
	for (const line of ["1", "package race", "2", "3", "func Settle() {}"]) expect(text).toContain(line);
});

it("shows an image through the hub with the bearer header", async () => {
	const { tree } = await mount("out/chart.png");
	await settle();
	const image = tree.root.findByType("Image" as never);
	expect(image.props.source).toEqual({
		uri: "https://hub.test/doc/image?session=local%3Afix&path=out%2Fchart.png",
		headers: { Authorization: "Bearer secret" },
	});
	expect(fetchSpy).not.toHaveBeenCalled();
});

it("opens its outline, whose host lists the headings and jumps to one", async () => {
	const { navigation } = await mount();
	const button = headerItems(navigation).find((item) => item.type === "button");
	if (button?.type !== "button") throw new Error("no outline button");
	expect(button.icon).toEqual({ type: "sfSymbol", name: "list.bullet.indent" });
	button.onPress();
	expect(navigation.navigate).toHaveBeenCalledWith("OutlineSheet", { hubId: "studio", sessionRef: "local:fix", path: PATH });
	const host = readerHosts.get(sheetKey("studio", "local:fix", PATH));
	expect(host?.outline.map((entry) => entry.title)).toEqual(["Settle the race", "Steps"]);
	flatListCalls.length = 0;
	act(() => host?.jumpTo(2));
	expect(flatListCalls).toEqual([{ method: "scrollToIndex", args: { index: 2, animated: true } }]);
});

it("has no outline button with fewer than two headings", async () => {
	served["notes.md"] = { body: "# Notes\n\nOne line.\n" };
	const { navigation } = await mount("notes.md");
	expect(headerItems(navigation).some((item) => item.type === "button")).toBe(false);
});

it("goes back to its session and copies its path from ⋯", async () => {
	const { navigation } = await mount();
	await act(async () => menuAction(navigation, "Copy path").onPress());
	expect(harness.clipboard).toEqual([PATH]);
	await act(async () => menuAction(navigation, "Copy text").onPress());
	expect(harness.clipboard.at(-1)).toBe(PLAN);
	menuAction(navigation, "Open session").onPress();
	expect(navigation.pop).toHaveBeenCalledWith(1);
});

it("reads again when its session's turn ends while it's in front", async () => {
	client.on("thread/read", () => threadRead("active"));
	await mount();
	expect(fetchSpy).toHaveBeenCalledTimes(1);
	expect(client.calls.find((call) => call.method === "thread/read")?.params).toMatchObject({
		ref: "local:fix",
		subscribe: true,
	});
	act(() =>
		client.emitNotification({
			method: "thread/status/changed",
			params: { threadId: "thread-fix", ref: "local:fix", status: { type: "idle" } },
		}),
	);
	await settle();
	expect(fetchSpy).toHaveBeenCalledTimes(2);
});

it("reads again when it comes back to the front, and not for its own sheets", async () => {
	const under = stack.state.routes;
	const { rerender } = await mount();
	expect(fetchSpy).toHaveBeenCalledTimes(1);
	stack.state = { index: 3, routes: [...under, { key: "o", name: "OutlineSheet" }] };
	await rerender();
	stack.state = { index: 2, routes: under };
	await rerender();
	expect(fetchSpy).toHaveBeenCalledTimes(1);
	stack.state = { index: 3, routes: [...under, { key: "c2", name: "Conversation" }] };
	await rerender();
	stack.state = { index: 2, routes: under };
	await rerender();
	expect(fetchSpy).toHaveBeenCalledTimes(2);
});

it("records the read when a screen covers it, and not when its own sheet does", async () => {
	const under = stack.state.routes;
	const { rerender } = await mount();
	stack.state = { index: 3, routes: [...under, { key: "o", name: "OutlineSheet" }] };
	await rerender();
	expect(memory.lastRead(KEY)).toBeNull();
	stack.state = { index: 3, routes: [...under, { key: "c2", name: "Conversation" }] };
	await rerender();
	expect(memory.lastRead(KEY)?.blocks).toEqual(documentBlocks(PLAN).map((block) => block.hash));
});

it("never offers Retry, Refresh, Reconnect or a Next capsule", async () => {
	served[PATH] = { status: 500, body: "boom" };
	const { tree } = await mount();
	const text = renderedText(tree);
	for (const word of ["Retry", "Refresh", "Reconnect", "Next"]) expect(text).not.toContain(word);
	expect(text).toContain("settle.md couldn't be loaded right now.");
});
