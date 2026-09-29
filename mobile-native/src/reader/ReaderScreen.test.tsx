// The Reader (spec 10.2): a document from a session's folder, with what
// changed since you last read it, where you were, and its outline.
import type { NativeStackHeaderItem, NativeStackNavigationOptions } from "@react-navigation/native-stack";
import type { ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cloneElement, type ReactElement } from "react";
import { FlatList } from "react-native";
import type { ReactTestInstance, ReactTestRenderer } from "react-test-renderer";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	alertRequests,
	flatListCalls,
	pressable,
	render,
	render as renderElement,
	renderedText,
} from "../renderNative.testkit";
import { takeQuote } from "../session/pendingQuote";
import { sheetKey } from "../sheet/sheetHosts";
import type { SyncStringStorage } from "../syncStringStorage";
import { documentBlocks } from "./documentBlocks";
import { DocumentMemory } from "./documentMemory";
import { CommentSheet } from "./CommentSheet";
import { CommentsSheet } from "./CommentsSheet";
import { ReaderScreen } from "./ReaderScreen";
import { readerHosts } from "./readerHosts";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
	memory: null as unknown,
	clipboard: [] as string[],
	appState: [] as ((state: string) => void)[],
}));
// The action sheets the Reader's blocks opened, newest last, with the
// callback that picks from one; and the sheet routes' navigation.
const menus = vi.hoisted(() => ({
	shown: [] as { options: { options: string[]; cancelButtonIndex: number }; pick: (index: number) => void }[],
}));
const sheetNavigation = vi.hoisted(() => ({
	goBack: vi.fn(),
	dispatch: vi.fn(),
	navigate: vi.fn(),
	prevented: [] as boolean[],
	onPrevent: null as null | ((event: { data: { action: unknown } }) => void),
}));
const stack = vi.hoisted(() => ({
	state: { index: 2, routes: [] as { key: string; name: string; params?: object }[] },
}));

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	Image: "Image",
	TextInput: "TextInput",
	ActionSheetIOS: {
		showActionSheetWithOptions: (
			options: { options: string[]; cancelButtonIndex: number },
			pick: (index: number) => void,
		) => {
			menus.shown.push({ options, pick });
		},
	},
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
	useNavigation: () => sheetNavigation,
	usePreventRemove: (prevent: boolean, onPrevent: (event: { data: { action: unknown } }) => void) => {
		sheetNavigation.prevented.push(prevent);
		sheetNavigation.onPrevent = onPrevent;
	},
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
	menus.shown = [];
	alertRequests.length = 0;
	sheetNavigation.goBack.mockReset();
	sheetNavigation.navigate.mockReset();
	sheetNavigation.prevented = [];
	sheetNavigation.onPrevent = null;
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

/** Lets the reads land on microtasks alone, for a test that fakes setTimeout. */
async function settleMicrotasks() {
	await act(async () => {
		for (let turn = 0; turn < 50; turn += 1) await Promise.resolve();
	});
}

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

async function mount(path = PATH, extra: Record<string, unknown> = {}, flush = settle) {
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
	await flush();
	// Renders again, as a change in the navigation state would.
	const rerender = async () => {
		// A fresh element: React skips an update given the identical one.
		act(() => tree.update(cloneElement(element)));
		await settle();
	};
	return { tree, navigation, rerender };
}

function documentList(tree: ReactTestRenderer): ReactTestInstance {
	return tree.root.findAllByType(FlatList as never)[0] as ReactTestInstance;
}

function ruled(tree: ReactTestRenderer): ReactTestInstance[] {
	return tree.root.findAll(
		(node) =>
			typeof node.type === "string" &&
			(node.props.style as { borderLeftWidth?: number } | undefined)?.borderLeftWidth === 3,
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

it("draws blocks in the document role with serif headings, spaced by the list alone", async () => {
	const { tree } = await mount();
	const style = tree.root.findAll((node) => String(node.type) === "EnrichedMarkdownText")[0]?.props.markdownStyle;
	expect(style.paragraph).toMatchObject({ fontFamily: "SourceSerif4-Regular", fontSize: 18, lineHeight: 28 });
	expect(style.h1).toMatchObject({ fontFamily: "SourceSerif4-SemiBold", fontSize: 24, fontWeight: "600" });
	expect(style.h2).toMatchObject({ fontSize: 20 });
	expect(style.h3).toMatchObject({ fontSize: 18 });
	for (const name of ["paragraph", "h1", "h2", "h3", "list", "blockquote", "table"])
		expect(style[name]).toMatchObject({ marginTop: 0, marginBottom: 0 });
});

it("leaves a table to the markdown view, which scrolls a wide one itself", async () => {
	// Wrapped in a horizontal ScrollView, the native view measured no width
	// and the table drew nothing (seen on the simulator, phase 4 PR 9).
	served[PATH] = { body: "| Work | Subagents |\n|---|---|\n| Fix the race | 1 |\n" };
	const { tree } = await mount();
	const table = tree.root.find(
		(node) => String(node.type) === "EnrichedMarkdownText" && String(node.props.markdown).includes("| Work |"),
	);
	expect(table.props.flavor).toBe("github");
	for (let node = table.parent; node; node = node.parent)
		expect(String(node.type) === "ScrollView" && node.props.horizontal).not.toBe(true);
});

it("puts the title in the nav bar once the first heading scrolls out", async () => {
	served[PATH] = { body: `Intro line.\n\n${PLAN}` };
	const { tree, navigation } = await mount(PATH, { updatedAt: new Date(Date.now() - 3 * MINUTE).toISOString() });
	const list = documentList(tree);
	const title = () => {
		const render = navigation.latest("headerTitle") as (() => ReactElement | null) | undefined;
		const element = render?.();
		return element ? renderedText(renderElement(element)) : "";
	};
	expect(title()).toBe("");
	act(() => list.props.onViewableItemsChanged({ viewableItems: [{ index: 0 }, { index: 1 }], changed: [] }));
	expect(title()).toBe("");
	// The intro has gone, but the first heading is still on screen.
	act(() => list.props.onViewableItemsChanged({ viewableItems: [{ index: 1 }, { index: 2 }], changed: [] }));
	expect(title()).toBe("");
	act(() => list.props.onViewableItemsChanged({ viewableItems: [{ index: 2 }, { index: 3 }], changed: [] }));
	expect(title()).toBe("Settle the race Plan · updated 3m ago");
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

it("keeps Send review on one line beside the change stepper", async () => {
	// Split into equal thirds, the bar left Send review about 81pt of the
	// iPhone's width, and it wrapped (seen on the simulator, phase 4 PR 9).
	// The stepper takes what the two ends leave instead.
	memory.left(KEY, {
		title: "Settle the race",
		blocks: documentBlocks(OLDER).map((block) => block.hash),
		position: null,
		reviewRef: "local:fix",
		reviewTitle: "Fix race",
	});
	const { tree, rerender } = await mount(PATH, { reviewRef: "local:fix", reviewTitle: "Fix race" });
	act(() =>
		client.emitNotification({
			method: "thread/status/changed",
			params: {
				threadId: "thread-fix",
				ref: "local:fix",
				status: { type: "idle" },
				capabilities: { send: true, queue: true } as never,
			},
		}),
	);
	await rerender();
	expect(pressable(tree, "Next change")).toBeDefined();
	const send = pressable(tree, "Send review");
	expect(send?.find((node) => String(node.type) === "Text").props.numberOfLines).toBe(1);
	// The View a control sits in.
	const holder = (node: ReactTestInstance | undefined) => {
		let parent = node?.parent;
		while (parent && String(parent.type) !== "View") parent = parent.parent;
		return (parent?.props.style ?? {}) as { flex?: number };
	};
	expect(holder(send).flex).toBeUndefined();
	expect(holder(pressable(tree, "Next change")).flex).toBe(1);
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

it("keeps your step when a re-read finds the same changes", async () => {
	memory.left(KEY, {
		title: "Settle the race",
		blocks: documentBlocks(OLDER).map((block) => block.hash),
		position: null,
		reviewRef: "local:coord",
		reviewTitle: "Coordinator",
	});
	const { tree } = await mount();
	act(() => pressable(tree, "Next change")?.props.onPress());
	act(() => pressable(tree, "Next change")?.props.onPress());
	expect(renderedText(tree)).toContain("Change 2 of 2");
	act(() => {
		for (const listener of harness.appState) listener("active");
	});
	await settle();
	expect(fetchSpy).toHaveBeenCalledTimes(2);
	expect(renderedText(tree)).toContain("Change 2 of 2");
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
		const { tree } = await mount(PATH, {}, settleMicrotasks);
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
	memory.left(KEY, {
		title: "Settle the race",
		blocks: older,
		position: null,
		reviewRef: "local:coord",
		reviewTitle: "Coordinator",
	});
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
	expect(flatListCalls).toContainEqual({
		method: "scrollToIndex",
		args: { index: 2, viewOffset: -0, animated: false },
	});
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
					params: {
						hubId: "studio",
						sessionRef: "laptop:fix",
						path: PATH,
						reviewRef: "laptop:fix",
						reviewTitle: "Fix",
					},
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
	expect(navigation.navigate).toHaveBeenCalledWith("OutlineSheet", {
		hubId: "studio",
		sessionRef: "local:fix",
		path: PATH,
	});
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

describe("comments (Task 16)", () => {
	const blocks = documentBlocks(PLAN);
	const goal = blocks[1];
	const secondItem = blocks[4];
	if (!goal || !secondItem) throw new Error("the plan's blocks moved");

	function block(tree: ReactTestRenderer, index: number): ReactTestInstance {
		return tree.root.find((node) => String(node.type) === "Pressable" && node.props.testID === `block-${index}`);
	}

	function openMenu(tree: ReactTestRenderer, index: number) {
		act(() => block(tree, index).props.onLongPress());
		const menu = menus.shown.at(-1);
		if (!menu) throw new Error("no menu");
		return menu;
	}

	function choose(menu: (typeof menus.shown)[number], label: string) {
		act(() => menu.pick(menu.options.options.indexOf(label)));
	}

	function marker(tree: ReactTestRenderer, index: number): string | null {
		const pills = block(tree, index).findAll(
			(node) => String(node.type) === "Pressable" && /^\d+ comments?$/.test(String(node.props.accessibilityLabel)),
		);
		return pills[0] ? String(pills[0].props.accessibilityLabel) : null;
	}

	const commentParams = (over: Record<string, unknown> = {}) => ({
		hubId: "studio",
		sessionRef: "local:fix",
		path: PATH,
		blockIndex: 1,
		blockHash: goal.hash,
		quote: goal.text,
		...over,
	});

	function mountSheet(element: ReactElement) {
		const tree = render(element);
		trees.push(tree);
		return tree;
	}

	function commentSheet(params = commentParams()) {
		return mountSheet(
			<CommentSheet
				route={{ key: "comment", name: "CommentSheet", params } as never}
				navigation={sheetNavigation as never}
			/>,
		);
	}

	function commentsSheet(over: Record<string, unknown> = {}) {
		const params = {
			hubId: "studio",
			sessionRef: "local:fix",
			path: PATH,
			reviewRef: "local:coord",
			reviewTitle: "Coordinator",
			...over,
		};
		return mountSheet(
			<CommentsSheet
				route={{ key: "comments", name: "CommentsSheet", params } as never}
				navigation={sheetNavigation as never}
			/>,
		);
	}

	function typeComment(tree: ReactTestRenderer, text: string) {
		const field = tree.root.find((node) => String(node.type) === "TextInput");
		act(() => field.props.onChangeText(text));
	}

	it("opens the block's four actions on a long press, and highlights that block while it's open", async () => {
		const { tree } = await mount();
		const menu = openMenu(tree, 1);
		expect(menu.options.options).toEqual(["Comment", "Quote in reply", "Copy", "Select text", "Cancel"]);
		const highlighted = () => (block(tree, 1).props.style as { backgroundColor?: string }).backgroundColor;
		expect(highlighted()).toBeTruthy();
		expect((block(tree, 0).props.style as { backgroundColor?: string }).backgroundColor).toBeUndefined();
		act(() => menu.pick(menu.options.cancelButtonIndex));
		expect(highlighted()).toBeUndefined();
	});

	it("adds a comment through the comment sheet, and marks its block", async () => {
		const { tree, navigation, rerender } = await mount();
		choose(openMenu(tree, 1), "Comment");
		expect(navigation.navigate).toHaveBeenCalledWith("CommentSheet", commentParams());
		const sheet = commentSheet();
		expect(renderedText(sheet)).toContain(goal.text);
		expect(renderedText(sheet)).toContain("Comments stay with this document until you send your review.");
		expect(pressable(sheet, "Add")?.props.accessibilityState).toMatchObject({ disabled: true });
		typeComment(sheet, "Say which run.");
		act(() => pressable(sheet, "Add")?.props.onPress());
		expect(memory.comments(KEY)).toMatchObject([
			{ blockIndex: 1, blockHash: goal.hash, quote: goal.text, text: "Say which run." },
		]);
		expect(sheetNavigation.goBack).toHaveBeenCalled();
		await rerender();
		expect(marker(tree, 1)).toBe("1 comment");
	});

	it("asks before losing a typed comment", async () => {
		const sheet = commentSheet();
		expect(sheetNavigation.prevented.at(-1)).toBe(false);
		typeComment(sheet, "Half a thought");
		expect(sheetNavigation.prevented.at(-1)).toBe(true);
		act(() => sheetNavigation.onPrevent?.({ data: { action: { type: "GO_BACK" } } }));
		expect(alertRequests.at(-1)?.title).toBe("Discard this comment?");
	});

	it("puts a list item's comments on that item alone", async () => {
		for (const text of ["one", "two"])
			memory.addComment(KEY, { blockIndex: 4, blockHash: secondItem.hash, quote: secondItem.text, text });
		const { tree } = await mount();
		expect(marker(tree, 4)).toBe("2 comments");
		expect(marker(tree, 3)).toBeNull();
	});

	it("keeps a block's words clear of its comment pill", async () => {
		// The pill sat on the item's last words (seen on the simulator, phase 4 PR 9).
		memory.addComment(KEY, { blockIndex: 4, blockHash: secondItem.hash, quote: secondItem.text, text: "one" });
		const { tree } = await mount();
		const inset = (index: number) => (block(tree, index).props.style as { paddingRight?: number }).paddingRight;
		expect(inset(4)).toBeGreaterThanOrEqual(44);
		expect(inset(3)).toBeUndefined();
	});

	it("shows the tip until the first comment, then the Comments button", async () => {
		const { tree, navigation, rerender } = await mount();
		expect(renderedText(tree)).toContain("Touch and hold a paragraph to comment on it");
		expect(pressable(tree, "Comments")).toBeUndefined();
		act(() => {
			memory.addComment(KEY, { blockIndex: 1, blockHash: goal.hash, quote: goal.text, text: "x" });
		});
		await rerender();
		expect(renderedText(tree)).not.toContain("Touch and hold a paragraph to comment on it");
		act(() => pressable(tree, "Comments")?.props.onPress());
		expect(navigation.navigate).toHaveBeenCalledWith("CommentsSheet", {
			hubId: "studio",
			sessionRef: "local:fix",
			path: PATH,
			reviewRef: "local:coord",
			reviewTitle: "Coordinator",
		});
	});

	it("lists the comments, shows one in the Reader, and deletes one", async () => {
		memory.addComment(KEY, {
			blockIndex: 4,
			blockHash: secondItem.hash,
			quote: secondItem.text,
			text: "Name the fix.",
		});
		await mount();
		const sheet = commentsSheet();
		expect(renderedText(sheet)).toContain("Comments · 1");
		expect(renderedText(sheet)).toContain("Fix it.");
		expect(renderedText(sheet)).toContain("Name the fix.");
		flatListCalls.length = 0;
		sheetNavigation.goBack.mockImplementation(() => flatListCalls.push({ method: "goBack" }));
		act(() => pressable(sheet, "Show")?.props.onPress());
		expect(flatListCalls).toEqual([
			{ method: "goBack" },
			{ method: "scrollToIndex", args: { index: 4, animated: true } },
		]);
		act(() => pressable(sheet, "Delete")?.props.onPress());
		expect(memory.comments(KEY)).toEqual([]);
		expect(renderedText(sheet)).toContain("No comments yet");
	});

	it("comments on the words you selected", async () => {
		const { tree, navigation } = await mount();
		choose(openMenu(tree, 1), "Select text");
		const text = block(tree, 1).find((node) => String(node.type) === "EnrichedMarkdownText");
		expect(text.props.selectable).toBe(true);
		const comment = (text.props.contextMenuItems as { text: string; onPress: (event: object) => void }[]).find(
			(item) => item.text === "Comment",
		);
		act(() => comment?.onPress({ text: "a clean run", selection: { start: 12, end: 23 } }));
		expect(navigation.navigate).toHaveBeenCalledWith("CommentSheet", commentParams({ quote: "a clean run" }));
		// Choosing Comment ends the selection; so does pressing another block.
		const selectable = (index: number) =>
			block(tree, index).find((node) => String(node.type) === "EnrichedMarkdownText").props.selectable;
		expect(selectable(1)).toBe(false);
		choose(openMenu(tree, 1), "Select text");
		expect(selectable(1)).toBe(true);
		openMenu(tree, 3);
		expect(selectable(1)).toBe(false);
	});

	// The library says nothing when its selection menu closes, so a tap ends
	// the selection: on another block, or on this one, where a tap is what
	// dismisses a selection.
	it("ends a selection when you tap a block", async () => {
		const { tree } = await mount();
		const selectable = (index: number) =>
			block(tree, index).find((node) => String(node.type) === "EnrichedMarkdownText").props.selectable;
		choose(openMenu(tree, 1), "Select text");
		expect(selectable(1)).toBe(true);
		act(() => block(tree, 3).props.onPress());
		expect(selectable(1)).toBe(false);
		choose(openMenu(tree, 1), "Select text");
		act(() => block(tree, 1).props.onPress());
		expect(selectable(1)).toBe(false);
	});

	it("keeps a comment whose paragraph changed, without a marker or a Show", async () => {
		const older = documentBlocks(OLDER)[1];
		if (!older) throw new Error("no block");
		memory.addComment(KEY, { blockIndex: 1, blockHash: older.hash, quote: older.text, text: "Too vague." });
		const { tree } = await mount();
		expect(marker(tree, 1)).toBeNull();
		const sheet = commentsSheet();
		expect(renderedText(sheet)).toContain("Too vague.");
		expect(pressable(sheet, "Show")).toBeUndefined();
	});

	it("quotes a block in a reply to its review session", async () => {
		const { tree, navigation } = await mount();
		choose(openMenu(tree, 1), "Quote in reply");
		expect(takeQuote("studio", "local:coord")).toBe(goal.text);
		expect(navigation.pop).toHaveBeenCalledWith(1);
	});

	it("copies a block's words", async () => {
		const { tree } = await mount();
		await act(async () => choose(openMenu(tree, 4), "Copy"));
		expect(harness.clipboard).toEqual(["Fix it."]);
	});

	// A running subagent's session read carries no capabilities (ruling 30),
	// so its document collects comments and offers Send review only once the
	// session can take a message. The review goes to the session the document
	// was opened in (ruling 16), so both refs name it.
	const ownSession = { reviewRef: "local:fix", reviewTitle: "Fix race" };

	it("offers Send review, in the bar and the Comments sheet, only while the review session can take a message", async () => {
		memory.addComment(KEY, { blockIndex: 1, blockHash: goal.hash, quote: goal.text, text: "Say which run." });
		const { tree, navigation, rerender } = await mount(PATH, ownSession);
		const sheet = commentsSheet(ownSession);
		expect(pressable(tree, "Send review")).toBeUndefined();
		expect(pressable(sheet, "Send review")).toBeUndefined();
		act(() =>
			client.emitNotification({
				method: "thread/status/changed",
				params: {
					threadId: "thread-fix",
					ref: "local:fix",
					status: { type: "idle" },
					capabilities: { send: true, queue: true } as never,
				},
			}),
		);
		await rerender();
		const params = { hubId: "studio", sessionRef: "local:fix", path: PATH, ...ownSession };
		act(() => pressable(tree, "Send review")?.props.onPress());
		expect(navigation.navigate).toHaveBeenCalledWith("ReviewSheet", params);
		act(() => pressable(sheet, "Send review")?.props.onPress());
		expect(sheetNavigation.navigate).toHaveBeenCalledWith("ReviewSheet", params);
		expect(sheetNavigation.goBack).not.toHaveBeenCalled();
	});

	it("offers no Send review from an empty Comments sheet", async () => {
		client.on("thread/read", () => ({
			...threadRead("idle"),
			thread: {
				...threadRead("idle").thread,
				evener: { ...threadRead("idle").thread.evener, capabilities: { send: true } as never },
			},
		}));
		const { tree } = await mount(PATH, ownSession);
		expect(pressable(tree, "Send review")).toBeDefined();
		expect(pressable(commentsSheet(ownSession), "Send review")).toBeUndefined();
	});
});
