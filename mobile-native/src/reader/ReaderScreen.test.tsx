// The Reader (spec 10.2): a document from a session's folder, with what
// changed since you last read it, where you were, and its outline.
import { once } from "node:events";
import { mkdtemp, readFile, rm, rmdir, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { setTimeout as wait } from "node:timers/promises";
import type { ServerResponse } from "node:http";
import type { NativeStackHeaderItem, NativeStackNavigationOptions } from "@react-navigation/native-stack";
import type { ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cloneElement, type ReactElement } from "react";
import { FlatList } from "react-native";
import type { ReactTestInstance, ReactTestRenderer } from "react-test-renderer";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
	alertRequests,
	flatListCalls,
	pressable,
	render,
	render as renderElement,
	renderedText,
	settleMicrotasks,
	unmountMountedTrees,
} from "../renderNative.testkit";
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
	focused: true,
	markdowns: [] as string[],
}));
// What the Reader holds, whether and as what kind, and the count Back shows.
const alerts = vi.hoisted(() => ({ holds: [] as [boolean, string][], held: 0 }));
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
			return {
				remove: () => {
					harness.appState = harness.appState.filter((entry) => entry !== listener);
				},
			};
		},
	},
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-safe-area-context", () => ({
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
}));
vi.mock("react-native-enriched-markdown", async () => {
	const { createElement } = await import("react");
	return {
		EnrichedMarkdownText: (props: { markdown: string }) => {
			harness.markdowns.push(props.markdown);
			return createElement("EnrichedMarkdownText", props);
		},
	};
});
vi.mock("expo-clipboard", () => ({
	setStringAsync: vi.fn(async (text: string) => {
		harness.clipboard.push(text);
	}),
}));
vi.mock("@react-navigation/native", () => ({
	useNavigationState: <T,>(select: (state: typeof stack.state) => T) => select(stack.state),
	useNavigation: () => sheetNavigation,
	useIsFocused: () => harness.focused,
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
vi.mock("../alerts/alertsContext", () => ({
	useHoldAlerts: (active: boolean, kind: string) => alerts.holds.push([active, kind]),
	useHeldAlertCount: () => alerts.held,
}));
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
			cwd: "/work/a",
			status: { type: status },
			modelProvider: "glm",
			evener: { ref: "local:fix", instanceId: "instance-fix", capabilities: {}, queue: { revision: 1 } },
		},
	}) as ThreadReadResponse;

beforeEach(() => {
	harness.focused = true;
	harness.markdowns = [];
	alerts.holds = [];
	alerts.held = 0;
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
		const answer = served[(url.searchParams.get("path") ?? "").replace(/^\/work\/a\//, "")] ?? {
			status: 404,
			body: "not found",
		};
		return new Response(answer.body, {
			status: answer.status ?? 200,
			headers: { "Content-Type": "text/plain; charset=utf-8", ...answer.headers },
		});
	});
	client = new FakeClient("ready");
	client.on("thread/read", () => threadRead("idle"));
	harness.connection = {
		profiles: [{ id: "studio", name: "Studio", origin: "https://hub.test" }],
		activeProfile: { id: "studio" },
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
			{ key: "fix", name: "Conversation", params: { hubId: "studio", ref: "local:fix", title: "Fix race" } },
			{ key: "reader-1", name: "Reader" },
		],
	};
});
afterEach(() => {
	unmountMountedTrees();
	trees.length = 0;
	vi.restoreAllMocks();
	vi.useRealTimers();
});

/** Lets the reads land on microtasks alone, for a test that fakes setTimeout. */
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
		setParams: vi.fn(),
		navigate: vi.fn(),
		goBack: vi.fn(),
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
		reference: { path, cwd: "/work/a", readTarget: `/work/a/${path}`, provenance: "relative" },
		sessionTitle: "Fix race",
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
		await flush();
	};
	return { tree, navigation, rerender, params };
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

it("Reload in the actual Document actions recovers missing remote text from useful file bytes", async () => {
	fetchSpy.mockRestore();
	const actualFetch = globalThis.fetch;
	const errors = vi.spyOn(console, "error"); // Call through, never suppress diagnostics.
	const reads = Array.from({ length: 2 }, () => {
		let started!: (body: Promise<ArrayBuffer>) => void;
		const body = new Promise<ArrayBuffer>((resolve) => {
			started = resolve;
		});
		return { started, body };
	});
	let readIndex = 0;
	fetchSpy = vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
		const response = actualFetch(input, init);
		reads[readIndex++]?.started(response.then((value) => value.clone().arrayBuffer()));
		return response;
	});
	const directory = await mkdtemp(join(tmpdir(), "reader-reload-"));
	const file = join(directory, "recovered.md");
	const requests: { path: string | null; session: string | null; auth: string | undefined }[] = [];
	const server = createServer(async (request, response) => {
		const url = new URL(request.url ?? "", "http://fixture.test");
		requests.push({
			path: url.searchParams.get("path"),
			session: url.searchParams.get("session"),
			auth: request.headers.authorization,
		});
		try {
			const bytes = await readFile(file);
			response.writeHead(200, { "Content-Type": "text/plain; charset=utf-8" });
			response.end(bytes);
		} catch {
			response.writeHead(404);
			response.end("missing");
		}
	});
	server.listen(0, "127.0.0.1");
	await once(server, "listening");
	try {
		const address = server.address();
		if (!address || typeof address === "string") throw new Error("expected TCP fixture");
		harness.connection.profiles = [{ id: "studio", name: "Studio", origin: `http://127.0.0.1:${address.port}` }];
		harness.connection.activeProfile = { id: "studio" };
		client.on("thread/read", () => ({ thread: { ...threadRead("idle").thread, cwd: "/work/owner" } }));
		let mounted!: Awaited<ReturnType<typeof mount>>;
		const mounting = mount(
			"docs/recovered.md",
			{
				sessionRef: "h1:local:02wMz5Txv1C3Hut0M8GCeB",
				reference: {
					path: "docs/recovered.md",
					cwd: "/work/owner",
					readTarget: "/work/owner/docs/recovered.md",
					provenance: "relative",
				},
			},
			async () => {},
		);
		await act(async () => {
			mounted = await mounting;
			await reads[0]?.body;
		});
		const { tree, navigation } = mounted;
		expect(renderedText(tree)).toContain("isn't in this session's folder");
		await writeFile(file, "# Recovered bytes\n\nUseful file body.");
		await act(async () => {
			menuAction(navigation, "Reload").onPress?.();
			await reads[1]?.body;
		});
		expect(
			tree.root.findAll((node) => String(node.type) === "EnrichedMarkdownText").map((node) => node.props.markdown),
		).toContain("Useful file body.");
		expect(requests).toEqual([
			{ path: "/work/owner/docs/recovered.md", session: "h1:local:02wMz5Txv1C3Hut0M8GCeB", auth: "Bearer secret" },
			{ path: "/work/owner/docs/recovered.md", session: "h1:local:02wMz5Txv1C3Hut0M8GCeB", auth: "Bearer secret" },
		]);
		expect(errors.mock.calls.filter((args) => args.some((arg) => String(arg).includes("not wrapped in act")))).toEqual(
			[],
		);
	} finally {
		server.closeAllConnections();
		await new Promise<void>((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));
		await rm(file, { force: true });
		await rmdir(directory);
	}
});

it("offers a real Reload action even after a missing document", async () => {
	const { navigation } = await mount("docs/missing.md");
	expect(menuAction(navigation, "Reload").onPress).toBeTypeOf("function");
});

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
		"https://hub.test/doc/file?format=raw&session=local%3Afix&path=%2Fwork%2Fa%2Fdocs%2Fsuperpowers%2Fplans%2Fsettle.md",
		{ headers: { Authorization: "Bearer secret" } },
	);
});

// The same age as the document's chip: just now under a minute, even with a
// hub clock ahead of the phone's.
it.each([
	["seconds ago", -10_000],
	["ahead of the phone's clock", 5_000],
])("says updated just now for a document written %s", async (_name, offset) => {
	const { tree } = await mount(PATH, { updatedAt: new Date(Date.now() + offset).toISOString() });
	expect(renderedText(tree)).toContain("Plan · updated just now");
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
		sessionTitle: "Fix race",
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
		sessionTitle: "Fix race",
	});
	const { tree, rerender } = await mount(PATH, { sessionTitle: "Fix race" });
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
		sessionTitle: "Fix race",
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
		sessionTitle: "Fix race",
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
		sessionTitle: "Fix race",
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
		sessionTitle: "Fix race",
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

it("says why it can't show a binary, a missing file, or an unsupported host's document", async () => {
	served["out/data.bin"] = { body: "\u0000\u0001", headers: { "Content-Type": "application/octet-stream" } };
	const binary = await mount("out/data.bin");
	expect(renderedText(binary.tree)).toContain("data.bin isn't text, so it can't be shown here (2 bytes).");
	const missing = await mount("docs/gone.md");
	expect(renderedText(missing.tree)).toContain("gone.md isn't in this session's folder any more.");
	fetchSpy.mockClear();
	served[PATH] = { status: 501, body: "unsupported host" };
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
						reference: { path: PATH, cwd: "/work/a", readTarget: `/work/a/${PATH}`, provenance: "relative" },
						sessionTitle: "Fix",
					},
				} as never
			}
			navigation={navigation as never}
		/>,
	);
	trees.push(tree);
	await settle();
	expect(renderedText(tree)).toContain("does not support document reads");
	expect(fetchSpy).toHaveBeenCalledOnce();
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
	const url = new URL(image.props.source.uri);
	expect(url.origin).toBe("https://hub.test");
	expect(url.pathname).toBe("/doc/image");
	expect(url.searchParams.get("session")).toBe("local:fix");
	expect(url.searchParams.get("path")).toBe("/work/a/out/chart.png");
	expect(url.searchParams.get("read")).toBeTruthy();
	expect(image.props.source.headers).toEqual({ Authorization: "Bearer secret" });
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

it("holds banners while it's in front, and counts them on Back", async () => {
	alerts.held = 1;
	const { navigation, rerender } = await mount();
	expect(alerts.holds.at(-1)).toEqual([true, "quiet"]);
	const back = navigation.latest("headerLeft") as (props: never) => ReactElement;
	const button = renderElement(back({} as never));
	pressable(button, "Back, 1 new while you read")?.props.onPress();
	expect(navigation.goBack).toHaveBeenCalled();
	expect(renderedText(button)).toContain("1");

	// Leaving the stack ends the hold on its own, with no blur event: a fast
	// swipe can miss the focus change, and what waited must still drop in.
	stack.state = { index: 1, routes: stack.state.routes.slice(0, 2) };
	await rerender();
	expect(alerts.holds.at(-1)).toEqual([false, "quiet"]);
});

it("leaves Android its own back arrow", async () => {
	const { Platform } = (await import("react-native")) as unknown as { Platform: { OS: string } };
	Platform.OS = "android";
	try {
		const { navigation } = await mount();
		expect(navigation.latest("headerLeft")).toBeUndefined();
	} finally {
		Platform.OS = "ios";
	}
});

it("reads plain Back when nothing waits", async () => {
	const { navigation } = await mount();
	const back = navigation.latest("headerLeft") as (props: never) => ReactElement;
	const button = renderElement(back({} as never));
	expect(pressable(button, "Back")).toBeTruthy();
	expect(renderedText(button)).not.toContain("0");
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

async function until(check: () => void) {
	const deadline = performance.now() + 3000;
	for (;;) {
		try {
			await act(async () => {
				await wait(1);
			});
			check();
			return;
		} catch (error) {
			if (performance.now() > deadline) throw error;
		}
	}
}

it.each(["relative", "absolute"] as const)(
	"replaces %s cwd through the same real SessionLink, clears on the first render and rejects retired bytes",
	async (provenance) => {
		fetchSpy.mockRestore();
		let cwd = "/work/a";
		client.on("thread/read", () => ({ thread: { ...threadRead("idle").thread, cwd } }));
		const requests: string[] = [];
		let retired: ServerResponse | undefined;
		let refreshFails = false;
		const server = createServer((request, response) => {
			const target = new URL(request.url ?? "", "http://fixture.test").searchParams.get("path") ?? "";
			requests.push(target);
			if (requests.length === 2) {
				retired = response;
				return;
			}
			response.writeHead(requests.length === 3 || refreshFails ? 503 : 200, {
				"Content-Type": "text/plain; charset=utf-8",
			});
			response.end(
				requests.length === 1
					? "# Healthy A\n\nUseful A."
					: requests.length === 3 || refreshFails
						? "B offline"
						: "# Healthy B\n\nUseful B.",
			);
		});
		server.listen(0, "127.0.0.1");
		await once(server, "listening");
		const address = server.address();
		if (!address || typeof address === "string") throw new Error("expected TCP fixture");
		harness.connection.profiles = [{ id: "studio", name: "Studio", origin: `http://127.0.0.1:${address.port}` }];
		const reference = { path: "docs/cwd.md", cwd: "/work/a", readTarget: "/work/a/docs/cwd.md", provenance };
		const { tree, navigation } = await mount(reference.path, { reference });
		try {
			await until(() => expect(harness.markdowns).toContain("Useful A."));
			act(() => menuAction(navigation, "Reload").onPress());
			await until(() => expect(retired).toBeDefined());
			expect(harness.markdowns).toContain("Useful A.");
			// Unrelated publication cannot replace this Reader or start another lease.
			act(() =>
				client.emitNotification({
					method: "evener/thread/resync",
					params: { threadId: "other", ref: "local:other" },
				} as never),
			);
			expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
			cwd = "/work/b";
			harness.markdowns = [];
			act(() =>
				client.emitNotification({
					method: "evener/thread/resync",
					params: { threadId: "thread-fix", ref: "local:fix" },
				} as never),
			);
			const expected = {
				path: "docs/cwd.md",
				cwd: "/work/b",
				readTarget: provenance === "relative" ? "/work/b/docs/cwd.md" : "/work/a/docs/cwd.md",
				provenance,
			};
			await until(() => expect(navigation.setParams).toHaveBeenLastCalledWith({ reference: expected }));
			expect(harness.markdowns).not.toContain("Useful A.");
			expect(tree.root.findAll((node) => String(node.type) === "EnrichedMarkdownText")).toHaveLength(0);
			expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
			retired?.writeHead(200, { "Content-Type": "text/plain" });
			retired?.end("# Retired completion\n\nObsolete bytes.");
			await until(() => expect(renderedText(tree)).toContain("couldn't be loaded right now"));
			expect(harness.markdowns).not.toContain("Obsolete bytes.");
			expect(requests).toEqual(["/work/a/docs/cwd.md", "/work/a/docs/cwd.md", expected.readTarget]);
			act(() => menuAction(navigation, "Reload").onPress());
			await until(() => expect(harness.markdowns).toContain("Useful B."));
			expect(requests.at(-1)).toBe(expected.readTarget);
			refreshFails = true;
			act(() => menuAction(navigation, "Reload").onPress());
			await until(() => expect(renderedText(tree)).toContain("couldn't be loaded right now"));
			expect(
				tree.root.findAll((node) => String(node.type) === "EnrichedMarkdownText").map((node) => node.props.markdown),
			).toContain("Useful B.");
			expect(harness.connection.state).toBe("ready");
		} finally {
			act(() => tree.unmount());
			server.closeAllConnections();
			await new Promise<void>((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));
		}
		const reads = client.calls.filter((call) => call.method === "thread/read").length;
		act(() =>
			client.emitNotification({
				method: "evener/thread/resync",
				params: { threadId: "thread-fix", ref: "local:fix" },
			} as never),
		);
		expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(reads);
		expect(harness.appState).toEqual([]);
	},
);

it("settles actual native Image attempts, retries untyped errors, retains healthy refreshes and ignores retired callbacks", async () => {
	vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
	const { tree, navigation } = await mount("out/chart.png", {}, settleMicrotasks);
	await until(() => expect(tree.root.findAllByType("Image" as never)).toHaveLength(1));
	const first = tree.root.findByType("Image" as never).props;
	act(() => first.onError({ nativeEvent: { error: "untyped native failure" } }));
	await until(() => expect(renderedText(tree)).toContain("This image couldn't be loaded right now."));
	expect(vi.getTimerCount()).toBe(1);
	await act(async () => {
		await vi.advanceTimersByTimeAsync(999);
	});
	expect(tree.root.findByType("Image" as never).props.source.uri).toBe(first.source.uri);
	await act(async () => {
		await vi.advanceTimersByTimeAsync(1);
	});
	await until(() => expect(tree.root.findByType("Image" as never).props.source.uri).not.toBe(first.source.uri));
	const second = tree.root.findByType("Image" as never).props;
	act(() => first.onLoad({ nativeEvent: { source: { width: 9, height: 1 } } }));
	act(() => first.onError({ nativeEvent: { error: "late failure" } }));
	expect(vi.getTimerCount()).toBe(0);
	act(() => second.onLoad({ nativeEvent: { source: { width: 640, height: 320 } } }));
	await until(() => expect(tree.root.findByType("Image" as never).props.style.aspectRatio).toBe(2));
	expect(renderedText(tree)).not.toContain("couldn't be loaded");
	act(() => second.onError({ nativeEvent: { error: "duplicate after success" } }));
	expect(renderedText(tree)).not.toContain("couldn't be loaded");
	act(() => {
		menuAction(navigation, "Reload").onPress();
		menuAction(navigation, "Reload").onPress();
	});
	await until(() => expect(tree.root.findAllByType("Image" as never)).toHaveLength(2));
	const images = tree.root.findAllByType("Image" as never);
	expect(images[0]?.props.source.uri).toBe(second.source.uri);
	expect(images[0]?.props.style.aspectRatio).toBe(2);
	const pending = images[1]?.props;
	expect(pending.style.opacity).toBe(0);
	act(() => pending.onError({ nativeEvent: { error: "refresh failed" } }));
	await until(() => expect(renderedText(tree)).toContain("This image couldn't be loaded right now."));
	expect(tree.root.findAllByType("Image" as never)[0]?.props.source.uri).toBe(second.source.uri);
	act(() => tree.unmount());
	expect(vi.getTimerCount()).toBe(0);
	act(() => pending.onError({ nativeEvent: { error: "after unmount" } }));
	await act(async () => {
		await vi.advanceTimersByTimeAsync(60000);
	});
	expect(fetchSpy).not.toHaveBeenCalled();
	expect(harness.appState).toEqual([]);
});

it.each([
	["hidden", "load"],
	["hidden", "error"],
	["background", "load"],
	["background", "error"],
] as const)("never promotes a pending retired image after %s, with late %s first", async (reason, firstEvent) => {
	vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
	const { tree, rerender } = await mount("out/chart.png", {}, async () => {});
	await until(() => expect(tree.root.findAllByType("Image" as never)).toHaveLength(1));
	const old = tree.root.findByType("Image" as never).props;
	if (reason === "hidden") {
		stack.state.index = 1;
		await rerender();
	} else {
		act(() => {
			for (const listener of [...harness.appState]) listener("background");
		});
	}
	const lateLoad = () => old.onLoad({ nativeEvent: { source: { width: 9, height: 1 } } });
	const lateError = () => old.onError({ nativeEvent: { error: "retired pending image" } });
	act(() => {
		if (firstEvent === "load") {
			lateLoad();
			lateError();
		} else {
			lateError();
			lateLoad();
		}
	});
	const retired = tree.root.findByType("Image" as never).props;
	expect(retired.style.aspectRatio).toBe(4 / 3);
	expect(retired.onLoad).toBeTypeOf("function");
	expect(renderedText(tree)).not.toContain("couldn't be loaded");
	expect(vi.getTimerCount()).toBe(0);
	if (reason === "hidden") {
		stack.state.index = 2;
		await rerender();
	} else {
		act(() => {
			for (const listener of [...harness.appState]) listener("active");
		});
	}
	await until(() => expect(tree.root.findByType("Image" as never).props.source.uri).not.toBe(old.source.uri));
	const fresh = tree.root.findByType("Image" as never).props;
	expect(fresh.style.aspectRatio).toBe(4 / 3);
	act(() => {
		lateLoad();
		lateError();
	});
	expect(tree.root.findByType("Image" as never).props.source.uri).toBe(fresh.source.uri);
	act(() => fresh.onLoad({ nativeEvent: { source: { width: 640, height: 320 } } }));
	expect(tree.root.findByType("Image" as never).props.style.aspectRatio).toBe(2);
	expect(tree.root.findByType("Image" as never).props.source.uri).toBe(fresh.source.uri);
	act(() => fresh.onError({ nativeEvent: { error: "duplicate after success" } }));
	expect(renderedText(tree)).not.toContain("couldn't be loaded");
	if (reason === "hidden") {
		stack.state.index = 1;
		await rerender();
	} else {
		act(() => {
			for (const listener of [...harness.appState]) listener("background");
		});
	}
	expect(tree.root.findByType("Image" as never).props.source.uri).toBe(fresh.source.uri);
	expect(tree.root.findByType("Image" as never).props.style.aspectRatio).toBe(2);
	act(() => tree.unmount());
	expect(harness.appState).toEqual([]);
	expect(vi.getTimerCount()).toBe(0);
});

it("clears a healthy native image on origin replacement and ignores its retired load/error callbacks", async () => {
	vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
	const { tree, rerender } = await mount("out/chart.png", {}, settleMicrotasks);
	const old = tree.root.findByType("Image" as never).props;
	act(() => old.onLoad({ nativeEvent: { source: { width: 4, height: 1 } } }));
	expect(tree.root.findByType("Image" as never).props.style.aspectRatio).toBe(4);
	harness.connection.profiles = [{ id: "studio", name: "Studio", origin: "https://replacement.test" }];
	await rerender();
	const next = tree.root.findByType("Image" as never).props;
	expect(next.source.uri).toContain("https://replacement.test/doc/image?");
	expect(next.source.uri).not.toBe(old.source.uri);
	expect(next.style.aspectRatio).toBe(4 / 3);
	act(() => old.onLoad({ nativeEvent: { source: { width: 99, height: 1 } } }));
	act(() => old.onError({ nativeEvent: { error: "old origin" } }));
	expect(tree.root.findByType("Image" as never).props.source.uri).toBe(next.source.uri);
	expect(tree.root.findByType("Image" as never).props.style.aspectRatio).toBe(4 / 3);
	expect(vi.getTimerCount()).toBe(0);
	act(() => next.onLoad({ nativeEvent: { source: { width: 3, height: 1 } } }));
	expect(tree.root.findByType("Image" as never).props.style.aspectRatio).toBe(3);
	act(() => tree.unmount());
	expect(harness.appState).toEqual([]);
});
