// The Reader's comments (spec 10.2, rulings 14-15 and 26): the block menu,
// the comment and comments sheets, and the quote held for a reply.
import type { NativeStackNavigationOptions } from "@react-navigation/native-stack";
import type { ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cloneElement, type ReactElement } from "react";
import type { ReactTestInstance, ReactTestRenderer } from "react-test-renderer";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { alertRequests, flatListCalls, pressable, render, renderedText } from "../renderNative.testkit";
import { takeQuote } from "../session/pendingQuote";
import type { SyncStringStorage } from "../syncStringStorage";
import { documentBlocks } from "./documentBlocks";
import { DocumentMemory } from "./documentMemory";
import { CommentSheet } from "./CommentSheet";
import { CommentsSheet } from "./CommentsSheet";
import { ReaderScreen } from "./ReaderScreen";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
	memory: null as unknown,
	clipboard: [] as string[],
	appState: [] as ((state: string) => void)[],
	focused: true,
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

type Served = { status?: number; body: string; headers?: Record<string, string> };
let served: Record<string, Served> = {};
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
	harness.focused = true;
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
	vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
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
			{ key: "fix", name: "Conversation", params: { hubId: "studio", ref: "local:fix", title: "Fix race" } },
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
		await settle();
	};
	return { tree, navigation, rerender };
}

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
			sessionTitle: "Fix race",
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

	it("lets a drag of the comment sheet put the keyboard away", () => {
		const tree = commentSheet();
		expect(tree.root.findAllByType("ScrollView" as never)[0]?.props.keyboardDismissMode).toBe("on-drag");
	});

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
			sessionTitle: "Fix race",
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
		expect(takeQuote("studio", "local:fix")).toBe(goal.text);
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
	// was opened in (ruling 16), the one whose ref the Reader reads it from.
	const ownSession = { sessionTitle: "Fix race" };

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
