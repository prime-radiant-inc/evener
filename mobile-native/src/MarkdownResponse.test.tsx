import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { Thread, SessionActivityReadParams } from "@evener/appwire-client";
import type { ComponentProps, ReactNode } from "react";
import { createElement } from "react";
import { act } from "react-test-renderer";
import { lexer } from "marked";
import { threadActivityFixture } from "./subagents/sessionActivityTestUtils";
import { ConversationScreen } from "./screens";
import { TimelineItem } from "./TimelineItem";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import * as Clipboard from "expo-clipboard";
import { DisplayProvider } from "./display/displayContext";
import { DisplayPreferences } from "./display/displayPreferences";
import { MarkdownResponse } from "./MarkdownResponse";
import { MermaidDiagram } from "./MermaidDiagram";
import { alertRequests, keyboard, render, screenConnection, unmountMountedTrees } from "./renderNative.testkit";

const mode = vi.hoisted(() => ({ scheme: "light" as "light" | "dark" }));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	AccessibilityInfo: {
		...(await import("./renderNative.testkit")).nativeModuleMock().AccessibilityInfo,
		announceForAccessibility: () => {},
	},
	ActionSheetIOS: { showActionSheetWithOptions: vi.fn() },
	AppState: { currentState: "active", addEventListener: () => ({ remove: () => {} }) },
	Image: "Image",
	RefreshControl: "RefreshControl",
	StatusBar: "StatusBar",
	Modal: (props: { visible?: boolean; children?: ReactNode }) =>
		props.visible ? createElement("Modal", null, props.children) : null,
	Linking: { openURL: vi.fn(async () => {}) },
	useColorScheme: () => mode.scheme,
}));
vi.mock("expo-clipboard", () => ({ setStringAsync: vi.fn(async () => {}) }));
vi.mock("react-native-enriched-markdown", () => ({ EnrichedMarkdownText: "EnrichedMarkdownText" }));
vi.mock("react-native-safe-area-context", () => ({
	SafeAreaView: "SafeAreaView",
	SafeAreaProvider: (props: { children?: ReactNode }) => props.children ?? null,
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
}));

function markdownStyle(markdown: string) {
	const tree = render(<MarkdownResponse markdown={markdown} />);
	return tree.root.findByType("EnrichedMarkdownText" as never).props.markdownStyle;
}

// MermaidDiagram is a React.memo component, so the test renderer reports its
// inner render function as the node type - a string lookup cannot find it.
// Match that function by identity, so a rename cannot quietly empty the finder.
const MERMAID_INNER = (MermaidDiagram as unknown as { type: unknown }).type;
function diagrams(tree: ReturnType<typeof render>) {
	return tree.root.findAll((node) => node.type === MERMAID_INNER);
}

it("picks light code colors in light mode", () => {
	mode.scheme = "light";
	expect(markdownStyle("`x`").codeBlock.syntaxColors).toMatchObject({ string: "#2e6443", number: "#785119" });
});

it("picks dark code colors in dark mode", () => {
	mode.scheme = "dark";
	expect(markdownStyle("`x`").codeBlock.syntaxColors).toMatchObject({ string: "#b8d8a3", number: "#ecc48d" });
});

it("fills a checked task box with accent-fill so its white checkmark stays legible", () => {
	mode.scheme = "dark";
	expect(markdownStyle("- [x] done").taskList.checkedColor).toBe("#0070E0");
});

it("sets agent prose in Source Serif 4 at 17/26 and headings in the system font", () => {
	mode.scheme = "light";
	const s = markdownStyle("Hello");
	expect(s.paragraph).toMatchObject({
		fontFamily: "SourceSerif4-Regular",
		fontSize: 17,
		lineHeight: 26,
		color: "#252521",
	});
	expect(s.list).toMatchObject({ fontFamily: "SourceSerif4-Regular", markerFontWeight: "normal" });
	expect(s.blockquote).toMatchObject({ fontFamily: "SourceSerif4-Regular" });
	expect(s.h1).toMatchObject({ fontSize: 20, fontWeight: "600" });
	expect(s.h1.fontFamily).toBeUndefined();
	expect(s.h2).toMatchObject({ fontSize: 17, fontWeight: "600" });
	expect(s.h3).toMatchObject({ fontSize: 15, fontWeight: "600" });
	expect(s.codeBlock).toMatchObject({ fontFamily: "Menlo" });
	expect(s.code).toMatchObject({ fontFamily: "Menlo" });
});

it("uses the dimmer prose ink in dark mode", () => {
	mode.scheme = "dark";
	expect(markdownStyle("Hello").paragraph).toMatchObject({ color: "#E0DED6" });
});

it("keeps tables in the system font and ink-hi, unlike the serif prose", () => {
	mode.scheme = "light";
	const light = markdownStyle("| a |\n|---|\n| 1 |").table;
	expect(light.fontFamily).toBeUndefined();
	expect(light.color).toBe("#252521");

	mode.scheme = "dark";
	expect(markdownStyle("| a |\n|---|\n| 1 |").table.color).toBe("#F2F1EB");
});

it("stays selectable with its Copy response item by default, for the task and subagent sheets", () => {
	const tree = render(<MarkdownResponse markdown="Hello" />);
	const text = tree.root.findByType("EnrichedMarkdownText" as never);
	expect(text.props.selectable).toBe(true);
	expect(text.props.contextMenuItems.map((item: { text: string }) => item.text)).toEqual(["Copy response"]);
});

it("drops native selection and its menu when the caller owns touch and hold", () => {
	const tree = render(<MarkdownResponse markdown="Hello" selectable={false} />);
	const text = tree.root.findByType("EnrichedMarkdownText" as never);
	expect(text.props.selectable).toBe(false);
	expect(text.props.contextMenuItems).toBeUndefined();
});

it("drops the serif for agent prose when the phone reads in Sans (spec 12)", () => {
	mode.scheme = "light";
	const values = new Map<string, string>();
	const prefs = new DisplayPreferences({
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => {
			values.set(key, value);
		},
	});
	prefs.set({ readingFont: "sans" });
	const tree = render(
		<DisplayProvider value={prefs}>
			<MarkdownResponse markdown="Hello" />
		</DisplayProvider>,
	);
	const style = tree.root.findByType("EnrichedMarkdownText" as never).props.markdownStyle;
	expect(style.paragraph.fontFamily).toBeUndefined();
	expect(style.paragraph).toMatchObject({ fontSize: 17, lineHeight: 26 });
	expect(style.codeBlock).toMatchObject({ fontFamily: "Menlo" });
});

it("renders a mermaid fence as a diagram between prose segments", () => {
	const tree = render(<MarkdownResponse markdown={"before\n\n```mermaid\ngraph TD; A-->B\n```\n\nafter"} />);
	const prose = tree.root.findAllByType("EnrichedMarkdownText" as never);
	expect(prose.map((node) => node.props.markdown)).toEqual(["before\n\n", "\n\nafter"]);
	expect(diagrams(tree)).toHaveLength(1);
});

it("keeps the whole message in every segment's Copy response item", () => {
	const markdown = "before\n\n```mermaid\ngraph TD; A-->B\n```\n\nafter";
	const tree = render(<MarkdownResponse markdown={markdown} />);
	for (const prose of tree.root.findAllByType("EnrichedMarkdownText" as never)) {
		const copy = prose.props.contextMenuItems.find((item: { text: string }) => item.text === "Copy response");
		copy.onPress();
	}
	expect(Clipboard.setStringAsync).toHaveBeenCalledWith(markdown);
});

it("hands the caller's accessibility actions to every prose segment and the diagram", () => {
	const actions = [{ name: "select", label: "Select text" }];
	const onAccessibilityAction = () => {};
	const tree = render(
		<MarkdownResponse
			markdown={"before\n\n```mermaid\ngraph TD; A-->B\n```\n\nafter"}
			accessibilityActions={actions}
			onAccessibilityAction={onAccessibilityAction}
		/>,
	);
	for (const prose of tree.root.findAllByType("EnrichedMarkdownText" as never)) {
		expect(prose.props.accessibilityActions).toBe(actions);
		expect(prose.props.onAccessibilityAction).toBe(onAccessibilityAction);
	}
	const diagram = diagrams(tree)[0];
	expect(diagram.props.accessibilityActions).toBe(actions);
	expect(diagram.props.onAccessibilityAction).toBe(onAccessibilityAction);
});

it("hands the caller's accessibility actions to the diagram for a diagram-only message", () => {
	const actions = [{ name: "select", label: "Select text" }];
	const tree = render(
		<MarkdownResponse
			markdown={"```mermaid\ngraph TD; A-->B\n```"}
			accessibilityActions={actions}
			onAccessibilityAction={() => {}}
		/>,
	);
	expect(diagrams(tree)[0]?.props.accessibilityActions).toBe(actions);
});

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
}));

// The root stack the screen sits in, read by useScreenInFront and
// screenInFront (screens.tsx), kept at the screen's own route on top.
const navigationState = vi.hoisted(() => ({
	state: { index: 0, routes: [] as { key: string; name: string }[] },
}));

const sqlite = vi.hoisted(() => ({ ports: new Map<string, unknown>() }));

vi.mock("@react-navigation/elements", () => ({ useHeaderHeight: () => 64 }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => void | (() => void)) => useEffect(effect, []),
		useNavigationState: <T,>(select: (state: typeof navigationState.state) => T) => select(navigationState.state),
	};
});
vi.mock("expo-web-browser", () => ({ openBrowserAsync: vi.fn(async () => ({ type: "dismiss" })) }));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-gesture-handler", async () =>
	(await import("./renderNative.testkit")).gestureDetectorModuleMock(),
);
vi.mock("react-native-gesture-handler/ReanimatedSwipeable", async () =>
	(await import("./renderNative.testkit")).gestureHandlerModuleMock(),
);

vi.mock("expo-crypto", () => ({
	randomUUID: () => "memoization-test-uuid",
	getRandomValues: (array: Uint8Array) => array,
}));
vi.mock("expo-sqlite", async () => {
	const { openSqliteSyncDouble } = await import("./sqliteSync.testkit");
	return {
		openDatabaseSync: (database: string) => {
			let port = sqlite.ports.get(database);
			if (!port) {
				port = openSqliteSyncDouble().port;
				sqlite.ports.set(database, port);
			}
			return port;
		},
	};
});
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: { getItemSync: () => null, setItemSync: () => {} },
}));
vi.mock("expo-file-system", () => ({
	File: class File {
		constructor(public uri: string) {}
	},
}));
vi.mock("expo-image-manipulator", () => ({}));
vi.mock("expo-image-picker", () => ({
	launchImageLibraryAsync: vi.fn(async () => ({ canceled: true, assets: [] })),
	UIImagePickerPreferredAssetRepresentationMode: { Current: "current" },
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: vi.fn(async () => null),
	setItemAsync: vi.fn(async () => {}),
	deleteItemAsync: vi.fn(async () => {}),
}));
vi.mock("./ConnectionProvider", () => ({
	useConnection: () => harness.connection,
}));
vi.mock("./NativePreferencesProvider", () => ({
	useNativePreferences: () => ({
		hubId: null,
		model: null,
		snapshot: null,
		config: null,
		connected: false,
		offlineDraftUnreadable: false,
		offlineStorageUnavailable: false,
		discardUnreadableKeybindingsDraft: () => null,
	}),
}));

type ConversationScreenProps = ComponentProps<typeof ConversationScreen>;

const navigation = {
	isFocused: () => true,
	getState: () => navigationState.state,
	navigate: vi.fn(),
	push: vi.fn(),
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
	// The fork affordance is on, so every row is handed the screen's
	// forkMessage callback and this suite can read its identity.
	forkFromTurn: true,
	shutdown: false,
	changeModel: false,
	changeVisionModel: false,
	sharedNotes: false,
	goal: false,
	rename: false,
};

function thread(ref: string): Thread {
	return {
		id: `thread-${ref}`,
		sessionId: `session-${ref}`,
		preview: "",
		ephemeral: false,
		modelProvider: "anthropic",
		createdAt: 0,
		updatedAt: 0,
		status: { type: "idle" },
		cwd: "",
		cliVersion: "",
		source: "",
		turns: [],
		evener: {
			ref,
			instanceId: "instance",
			capabilities: CAPABILITIES,
			queue: { revision: 0, depth: 0, preview: [], texts: [], ids: [] },
		},
	} as unknown as Thread;
}

import { Linking, ActionSheetIOS } from "react-native";
beforeEach(() => {
	vi.mocked(Linking.openURL).mockClear();
	alertRequests.length = 0;
});
afterEach(() => {
	unmountMountedTrees();
	keyboard.reset();
});
function emittedURLs(markdown: string): string[] {
	const urls: string[] = [];
	function visit(tokens: ReturnType<typeof lexer>) {
		for (const token of tokens) {
			if (token.type === "link") urls.push(token.href);
			if ("tokens" in token && token.tokens) visit(token.tokens as ReturnType<typeof lexer>);
		}
	}
	visit(lexer(markdown));
	return urls;
}
function prose(tree: ReturnType<typeof render>) {
	return tree.root.findByType("EnrichedMarkdownText" as never);
}
it("opens files through actual TimelineItem event props and preserves ordinary response holds", () => {
	const openFile = vi.fn();
	const original = "docs/a.md `README.md` [R](./docs/a%26b.md)";
	const tree = render(
		<TimelineItem
			item={{ kind: "assistant", id: "a", markdown: original, streaming: false }}
			hubId="source"
			sessionRef="host:source"
			fileContext={{ cwd: "/work/a", openFile }}
		/>,
	);
	const text = prose(tree);
	const urls = emittedURLs(text.props.markdown);
	expect(urls).toHaveLength(3);
	for (const url of urls) act(() => text.props.onLinkPress({ url }));
	expect(openFile.mock.calls.map(([ref]) => ref.readTarget)).toEqual([
		"/work/a/docs/a.md",
		"/work/a/README.md",
		"/work/a/docs/a&b.md",
	]);
	expect(Linking.openURL).not.toHaveBeenCalled();
	expect(text.props.selectable).toBe(false);
	expect(text.props.contextMenuItems).toBeUndefined();
	const hold = tree.root.findAll((node) => typeof node.props.onLongPress === "function")[0];
	act(() => hold.props.onLongPress());
	const [options, choose] = vi.mocked(ActionSheetIOS.showActionSheetWithOptions).mock.calls.at(-1)!;
	expect(options.options).toEqual(["Copy", "Select text", "Cancel"]);
	act(() => choose(options.options.indexOf("Copy")));
	expect(Clipboard.setStringAsync).toHaveBeenCalledWith(original);
});
it("keeps Copy response and external actions, but rejects unknown generated identifiers", () => {
	const openFile = vi.fn();
	const original = "docs/a.md [web](https://example.test/x)";
	const tree = render(<MarkdownResponse markdown={original} fileContext={{ cwd: "/work/a", openFile }} />);
	const text = prose(tree);
	act(() => text.props.contextMenuItems[0].onPress());
	expect(Clipboard.setStringAsync).toHaveBeenCalledWith(original);
	act(() => text.props.onLinkPress({ url: "https://example.test/x" }));
	expect(Linking.openURL).toHaveBeenCalledWith("https://example.test/x");
	act(() => text.props.onLinkLongPress({ url: "https://example.test/x" }));
	expect(alertRequests.at(-1)?.buttons?.map((b) => b.text)).toEqual(["Open in browser", "Copy destination", "Cancel"]);
	const count = alertRequests.length;
	act(() => text.props.onLinkPress({ url: "evener-file:unknown" }));
	act(() => text.props.onLinkLongPress({ url: "evener-file:unknown" }));
	expect(alertRequests).toHaveLength(count);
	expect(openFile).not.toHaveBeenCalled();
});
it("retires old render, event and menu callbacks after stream/cwd/context replacement and unmount", () => {
	const oldOpen = vi.fn(),
		nextOpen = vi.fn();
	const oldContext = { cwd: "/work/a", openFile: oldOpen };
	const tree = render(<MarkdownResponse markdown="docs/a.md" fileContext={oldContext} />);
	const old = prose(tree).props;
	const oldURL = emittedURLs(old.markdown)[0];
	expect(oldURL).toBeDefined();
	act(() => old.onLinkLongPress({ url: oldURL }));
	const menu = alertRequests.at(-1)!;
	expect(menu.message).toBe("docs/a.md");
	expect(menu.buttons?.map((b) => b.text)).toEqual(["Open file", "Copy path", "Cancel"]);
	act(() => menu.buttons?.[1]?.onPress?.());
	expect(Clipboard.setStringAsync).toHaveBeenCalledWith("docs/a.md");
	act(() => tree.update(<MarkdownResponse markdown="docs/a.md streaming docs/b.md" fileContext={oldContext} />));
	act(() => old.onLinkPress({ url: oldURL }));
	act(() => menu.buttons?.[0]?.onPress?.());
	expect(oldOpen).not.toHaveBeenCalled();
	const streaming = prose(tree).props;
	const streamURL = emittedURLs(streaming.markdown)[0];
	act(() =>
		tree.update(
			<MarkdownResponse
				markdown="docs/a.md streaming docs/b.md"
				fileContext={{ cwd: "/work/b", openFile: nextOpen }}
			/>,
		),
	);
	const next = prose(tree).props;
	act(() => streaming.onLinkPress({ url: streamURL }));
	act(() => next.onLinkPress({ url: oldURL }));
	act(() => next.onLinkPress({ url: emittedURLs(next.markdown)[0] }));
	expect(oldOpen).not.toHaveBeenCalled();
	expect(nextOpen).toHaveBeenCalledTimes(1);
	expect(nextOpen.mock.calls[0][0].readTarget).toBe("/work/b/docs/a.md");
	act(() => next.onLinkLongPress({ url: emittedURLs(next.markdown)[0] }));
	const currentMenu = alertRequests.at(-1)!;
	act(() => tree.unmount());
	act(() => next.onLinkPress({ url: emittedURLs(next.markdown)[0] }));
	act(() => currentMenu.buttons?.[0]?.onPress?.());
	expect(nextOpen).toHaveBeenCalledTimes(1);
});
it("does not recognize notice Markdown even when its TimelineItem receives a file context", () => {
	const tree = render(
		<TimelineItem
			item={{
				kind: "notice",
				id: "n",
				origin: "system",
				family: "informational",
				tone: "info",
				label: "Notice",
				text: "docs/a.md",
				rendersMarkdown: true,
			}}
			hubId="h"
			sessionRef="s"
			fileContext={{ cwd: "/work/a", openFile: vi.fn() }}
		/>,
	);
	act(() =>
		tree.root.find((n) => String(n.type) === "Pressable" && n.props.accessibilityLabel === "Notice").props.onPress(),
	);
	expect(tree.root.findAllByType("EnrichedMarkdownText" as never).map((t) => emittedURLs(t.props.markdown))).toEqual([
		[],
	]);
});
it("navigates Reader from actual owning ConversationScreen even when another conversation is selected", async () => {
	const served = thread("host:source-file-actions");
	served.cwd = "/work/source";
	(served as unknown as { turns: unknown[] }).turns = [
		{
			id: "file-turn",
			status: "completed",
			itemsView: "default",
			items: [
				{
					id: "file-message",
					turnId: "file-turn",
					type: "agentMessage",
					status: "completed",
					text: "docs/a.md `README.md` [R](./docs/a%26b.md)",
				},
			],
		},
	];
	const client = Object.assign(new FakeClient("ready"), {
		resumeThread: async () => {},
		request: async (method: string, params?: unknown) => {
			if (method === "thread/read") return { thread: served };
			if (method === "evener/thread/activity/read")
				return threadActivityFixture(served, params as SessionActivityReadParams).summary;
			if (method === "thread/turns/list") return { data: [] };
			return {};
		},
	});
	harness.connection = { ...screenConnection(client, "ready"), error: null, disconnect: () => {} };
	const route = {
		key: "source-files",
		name: "Conversation",
		params: { hubId: "hub-1", ref: "host:source-file-actions", title: "Source" },
	} as ConversationScreenProps["route"];
	navigationState.state = { index: 0, routes: [route] };
	vi.mocked(navigation.navigate).mockClear();
	const tree = render(<ConversationScreen route={route} navigation={navigation} />);
	try {
		await vi.waitFor(async () => {
			await act(async () => {
				await Promise.resolve();
			});
			expect(tree.root.findAll((n) => n.type === TimelineItem && n.props.item.kind === "assistant")).toHaveLength(1);
		});
		navigationState.state = { index: 1, routes: [route, { key: "selected-other", name: "Conversation" }] };
		const text = tree.root
			.find((n) => n.type === TimelineItem && n.props.item.kind === "assistant")
			.findByType("EnrichedMarkdownText" as never);
		const urls = emittedURLs(text.props.markdown);
		expect(urls).toHaveLength(3);
		for (const url of urls) act(() => text.props.onLinkPress({ url }));
		expect(vi.mocked(navigation.navigate).mock.calls).toEqual(
			["docs/a.md", "README.md", "docs/a&b.md"].map((path) => [
				"Reader",
				{
					hubId: "hub-1",
					sessionRef: "host:source-file-actions",
					sessionTitle: "Source",
					path,
					reference: { path, cwd: "/work/source", readTarget: `/work/source/${path}`, provenance: "relative" },
				},
			]),
		);
		expect(Linking.openURL).not.toHaveBeenCalled();
		const oldProps = text.props;
		const oldURL = urls[0];
		act(() => oldProps.onLinkLongPress({ url: oldURL }));
		const retiredMenu = alertRequests.at(-1)!;
		navigationState.state = { index: 0, routes: [route] };
		act(() => tree.update(<ConversationScreen route={route} navigation={navigation} />));
		served.cwd = "/work/replaced";
		await act(async () => {
			client.emitNotification({
				method: "evener/thread/resync",
				params: { ref: "host:source-file-actions", threadId: served.id },
			});
			await Promise.resolve();
		});
		await vi.waitFor(async () => {
			await act(async () => {
				await Promise.resolve();
			});
			expect(
				tree.root.find((n) => n.type === TimelineItem && n.props.item.kind === "assistant").props.fileContext.cwd,
			).toBe("/work/replaced");
		});
		vi.mocked(navigation.navigate).mockClear();
		act(() => oldProps.onLinkPress({ url: oldURL }));
		act(() => retiredMenu.buttons?.[0]?.onPress?.());
		expect(navigation.navigate).not.toHaveBeenCalled();
		const next = tree.root
			.find((n) => n.type === TimelineItem && n.props.item.kind === "assistant")
			.findByType("EnrichedMarkdownText" as never).props;
		act(() => next.onLinkPress({ url: emittedURLs(next.markdown)[0] }));
		expect(navigation.navigate).toHaveBeenCalledWith("Reader", {
			hubId: "hub-1",
			sessionRef: "host:source-file-actions",
			sessionTitle: "Source",
			path: "docs/a.md",
			reference: {
				path: "docs/a.md",
				cwd: "/work/replaced",
				readTarget: "/work/replaced/docs/a.md",
				provenance: "relative",
			},
		});
	} finally {
		act(() => tree.unmount());
	}
});
it("opens the emitted file hold action and copies normalized path without exposing its identifier", () => {
	const openFile = vi.fn();
	const tree = render(<MarkdownResponse markdown="./docs/a.md:12" fileContext={{ cwd: "/work/a", openFile }} />);
	const text = prose(tree);
	const url = emittedURLs(text.props.markdown)[0];
	act(() => text.props.onLinkLongPress({ url }));
	const menu = alertRequests.at(-1)!;
	expect(menu.message).toBe("docs/a.md");
	act(() => menu.buttons?.[0]?.onPress?.());
	expect(openFile).toHaveBeenCalledWith({
		path: "docs/a.md",
		cwd: "/work/a",
		readTarget: "/work/a/docs/a.md",
		provenance: "relative",
	});
	act(() => menu.buttons?.[1]?.onPress?.());
	expect(Clipboard.setStringAsync).toHaveBeenCalledWith("docs/a.md");
	expect(Linking.openURL).not.toHaveBeenCalled();
});
it("retires callbacks on source context replacement even when cwd and text are identical", () => {
	const first = vi.fn(),
		second = vi.fn();
	const markdown = "docs/a.md";
	const tree = render(<MarkdownResponse markdown={markdown} fileContext={{ cwd: "/work/a", openFile: first }} />);
	const old = prose(tree).props;
	const oldURL = emittedURLs(old.markdown)[0];
	act(() => tree.update(<MarkdownResponse markdown={markdown} fileContext={{ cwd: "/work/a", openFile: second }} />));
	act(() => old.onLinkPress({ url: oldURL }));
	expect(first).not.toHaveBeenCalled();
	const next = prose(tree).props;
	act(() => next.onLinkPress({ url: oldURL }));
	expect(second).not.toHaveBeenCalled();
	act(() => next.onLinkPress({ url: emittedURLs(next.markdown)[0] }));
	expect(second).toHaveBeenCalledTimes(1);
});
it("hydrates missing source/cwd without changing original text, and excludes code and Mermaid", () => {
	const original = "docs/a.md\n\n```mermaid\ngraph TD; A-->B\n```\n\n`README.md`\n\n    docs/code.md";
	const openFile = vi.fn();
	const tree = render(<MarkdownResponse markdown={original} />);
	const initial = tree.root.findAllByType("EnrichedMarkdownText" as never).map((t) => t.props);
	expect(initial.flatMap((t) => emittedURLs(t.markdown))).toEqual([]);
	act(() => tree.update(<MarkdownResponse markdown={original} fileContext={{ cwd: "", openFile }} />));
	expect(
		tree.root.findAllByType("EnrichedMarkdownText" as never).flatMap((t) => emittedURLs(t.props.markdown)),
	).toEqual([]);
	act(() => tree.update(<MarkdownResponse markdown={original} fileContext={{ cwd: "/work/a", openFile }} />));
	const current = tree.root.findAllByType("EnrichedMarkdownText" as never);
	expect(current.flatMap((t) => emittedURLs(t.props.markdown))).toHaveLength(2);
	expect(diagrams(tree)).toHaveLength(1);
	for (const text of current) act(() => text.props.contextMenuItems[0].onPress());
	expect(Clipboard.setStringAsync).toHaveBeenCalledWith(original);
});
