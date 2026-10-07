// The Notes & links sheet route, rendered with a host in notesHosts the way
// ConversationScreen provides one: a real NotesController over a fake client.
// Only native edges are mocked.
import type { NotesHumanSetResponse, SessionURL, ThreadCapabilities, ThreadModel } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { AccessibilityInfo, ActionSheetIOS, Platform } from "react-native";
import { act, type ReactTestInstance, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import {
	alertRequests,
	playedHaptics,
	pressable,
	render,
	renderedText,
	swipeRowFully,
	textOf,
} from "../renderNative.testkit";
import type { Routes } from "../screens";
import { sheetKey } from "../sheet/sheetHosts";
import type { SyncStringStorage } from "../syncStringStorage";
import { type NotesHost, NotesSheet, notesHosts } from "./NotesSheet";
import { canWriteHumanNote, NOTE_LIMIT, NotesController, type SaveOutcome } from "./sessionNotes";

const navigation = vi.hoisted(() => ({ goBack: vi.fn(), dispatch: vi.fn(), navigate: vi.fn() }));
const appState = vi.hoisted(() => ({ listeners: [] as ((state: string) => void)[] }));
const browser = vi.hoisted(() => ({ openBrowserAsync: vi.fn(async () => ({ type: "dismiss" })) }));
const clipboard = vi.hoisted(() => ({ setStringAsync: vi.fn(async () => true) }));

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	ActionSheetIOS: { showActionSheetWithOptions: vi.fn() },
	AppState: {
		currentState: "active",
		addEventListener: (_type: string, listener: (state: string) => void) => {
			appState.listeners.push(listener);
			return { remove: () => appState.listeners.splice(appState.listeners.indexOf(listener), 1) };
		},
	},
}));
vi.mock("@react-navigation/native", () => ({
	useNavigation: () => navigation,
	usePreventRemove: () => {},
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-gesture-handler/ReanimatedSwipeable", async () =>
	(await import("../renderNative.testkit")).gestureHandlerModuleMock(),
);
vi.mock("react-native-gesture-handler", async () =>
	(await import("../renderNative.testkit")).gestureDetectorModuleMock(),
);
vi.mock("expo-web-browser", () => browser);
vi.mock("expo-clipboard", () => clipboard);

const palette = paletteFor("light");
const HUB = "hub-1";
const REF = "ref-notes";
const CWD = "/home/jesse/git/evener";

type Session = NotesHost["session"];

function session(over: Partial<Session> = {}): Session {
	return {
		humanNote: "",
		agentNote: "",
		sessionUrls: [],
		status: { type: "idle" } as ThreadModel["status"],
		resumeRequired: false,
		capabilities: { sharedNotes: true } as ThreadCapabilities,
		...over,
	};
}

const web: SessionURL = { id: "u1", url: "https://example.com/pr/1", label: "The PR" };
const file: SessionURL = { id: "u2", url: "file:///Users/j/plan.md", label: "The plan" };

function memoryStorage(): SyncStringStorage {
	const values = new Map<string, string>();
	return {
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => void values.set(key, value),
		removeItemSync: (key) => void values.delete(key),
	};
}

let owner: object;
let controller: NotesController | null = null;

/** The screen's side: a controller over a hub that saves and removes, and the
 * host it provides under the session's key. */
function provide(current: Session, { removeThrows }: { removeThrows?: string } = {}) {
	const requests: { method: string; params: Record<string, unknown> }[] = [];
	const client = {
		request: async (method: string, params: Record<string, unknown>) => {
			requests.push({ method, params });
			if (method === "notes/human/set")
				return {
					note: params.note,
					receipt: { projectionState: "pending" },
				} as unknown as NotesHumanSetResponse;
			if (method === "urls/remove" && removeThrows) throw new Error(removeThrows);
			return {};
		},
	};
	const notes = new NotesController({
		client: client as never,
		hubId: HUB,
		ref: REF,
		instanceId: () => "instance",
		savedNote: () => current.humanNote,
		working: () => current.status.type === "active",
		writable: () => canWriteHumanNote(current),
		storage: memoryStorage(),
		uuid: () => "uuid",
	});
	controller = notes;
	const saved = vi.fn<(outcome: SaveOutcome) => void>();
	owner = {};
	notesHosts.provide(sheetKey(HUB, REF), owner, { session: current, notes, saved, cwd: CWD, title: "Fix race" });
	return { notes, saved, requests };
}

function sheet(focusEditor?: boolean): ReactTestRenderer {
	const props = {
		route: { key: "notes", name: "NotesSheet", params: { hubId: HUB, ref: REF, focusEditor } },
		navigation,
	} as unknown as NativeStackScreenProps<Routes, "NotesSheet">;
	const tree = render(<NotesSheet {...props} />);
	return tree;
}

const editor = (tree: ReactTestRenderer) =>
	tree.root
		.findAll((node) => String(node.type) === "TextInput")
		.find((node) => node.props.accessibilityLabel === "Your note");

function editorStyle(input: ReactTestInstance) {
	return [input.props.style].flat().reduce((all, style) => ({ ...all, ...style }), {});
}

const symbols = (tree: ReactTestRenderer) =>
	tree.root.findAll((node) => String(node.type) === "SymbolView").map((node) => node.props.name);

async function flush() {
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
}

const actionSheet = vi.mocked(ActionSheetIOS.showActionSheetWithOptions);

beforeEach(() => {
	navigation.goBack.mockClear();
	navigation.navigate.mockClear();
	browser.openBrowserAsync.mockClear();
	clipboard.setStringAsync.mockClear();
	actionSheet.mockClear();
});

afterEach(() => {
	notesHosts.release(sheetKey(HUB, REF), owner);
	controller?.dispose();
	controller = null;
});

describe("your note (spec 8.8)", () => {
	it("is a bordered serif editor with its placeholder and the 1,000-character limit, and typing edits the note", () => {
		const { notes } = provide(session({ humanNote: "keep the tests" }));
		const tree = sheet();
		const input = editor(tree);
		if (!input) throw new Error("no note editor");
		expect(input.props).toMatchObject({
			multiline: true,
			value: "keep the tests",
			placeholder: "Make a note…",
		});
		// No native maxLength: it counts UTF-16 units, not the daemon's runes,
		// which would cap an emoji-heavy note at roughly half its real
		// allowance. NotesController.edit() enforces NOTE_LIMIT by code point
		// on every change instead (RoboRev #2769 round 3).
		expect(input.props.maxLength).toBeUndefined();
		expect(NOTE_LIMIT).toBe(1000);
		expect(editorStyle(input)).toMatchObject({
			fontFamily: "SourceSerif4-Regular",
			fontSize: 17,
			lineHeight: 25,
			color: palette.prose,
			borderWidth: 1,
			borderColor: palette.edgeStrong,
			borderRadius: 12,
		});
		act(() => input.props.onChangeText("keep the tests green"));
		expect(notes.getSnapshot()).toEqual({ text: "keep the tests green", phase: "editing" });
		expect(editor(tree)?.props.value).toBe("keep the tests green");
	});

	it("lets a drag of the sheet put the keyboard away", () => {
		provide(session());
		const tree = sheet();
		expect(tree.root.findAllByType("ScrollView" as never)[0]?.props.keyboardDismissMode).toBe("on-drag");
	});

	it("draws the focus ring while focused, and leaving it schedules the save", () => {
		const { notes } = provide(session());
		const tree = sheet();
		act(() => editor(tree)?.props.onChangeText("draft"));
		act(() => editor(tree)?.props.onFocus());
		const focusedEditor = editor(tree);
		if (!focusedEditor) throw new Error("no note editor");
		expect(editorStyle(focusedEditor)).toMatchObject({ borderWidth: 2, borderColor: palette.accent });
		act(() => editor(tree)?.props.onBlur());
		expect(notes.getSnapshot().phase).toBe("scheduled");
		const blurred = editor(tree);
		if (!blurred) throw new Error("no note editor");
		expect(editorStyle(blurred)).toMatchObject({ borderWidth: 1, borderColor: palette.edgeStrong });
	});

	it("opens focused with the caret at the end when asked to", () => {
		provide(session({ humanNote: "keep the tests" }));
		const tree = sheet(true);
		expect(editor(tree)?.props).toMatchObject({ autoFocus: true, selection: { start: 14, end: 14 } });
	});

	it("opens unfocused otherwise", () => {
		provide(session({ humanNote: "keep the tests" }));
		const tree = sheet();
		expect(editor(tree)?.props.autoFocus).toBe(false);
		expect(editor(tree)?.props.selection).toBeUndefined();
	});

	it("says under the editor what saving will do, following the phase", () => {
		provide(session());
		const tree = sheet();
		expect(renderedText(tree)).toContain("Your note stays on this session. Saving it will wake the agent.");
		act(() => editor(tree)?.props.onChangeText("draft"));
		act(() => editor(tree)?.props.onBlur());
		expect(renderedText(tree)).toContain("Saves in 10 seconds, or when you close this.");
	});

	it("announces each change to the status line, since iOS ignores accessibilityLiveRegion (#2903)", () => {
		const announce = vi.mocked(AccessibilityInfo.announceForAccessibility);
		announce.mockClear();
		provide(session());
		const tree = sheet();
		// The standing explanation is the first render, not a change: quiet.
		expect(announce).not.toHaveBeenCalled();
		// Editing still shows the same explanation, so still nothing to say.
		act(() => editor(tree)?.props.onChangeText("draft"));
		expect(announce).not.toHaveBeenCalled();
		act(() => editor(tree)?.props.onBlur());
		expect(announce).toHaveBeenCalledWith("Saves in 10 seconds, or when you close this.");
	});

	it("says the agent is told when the agent is working", () => {
		provide(session({ status: { type: "active" } as ThreadModel["status"] }));
		expect(renderedText(sheet())).toContain("Your note stays on this session. The agent is told when it changes.");
	});

	it.each([
		["ended", session({ status: { type: "ended" } as ThreadModel["status"] })],
		["closed", session({ status: { type: "closed" } as ThreadModel["status"] })],
		["not loaded", session({ status: { type: "notLoaded" } as ThreadModel["status"] })],
		["restart required", session({ status: { type: "restartRequired" } as ThreadModel["status"] })],
		["fenced for resume", session({ resumeRequired: true })],
	])("shows a %s session's saved note read-only", (_name, base) => {
		provide({ ...base, humanNote: "keep the tests", agentNote: "on it" });
		const tree = sheet(true);
		expect(editor(tree)).toBeUndefined();
		expect(renderedText(tree)).toContain("keep the tests");
		expect(renderedText(tree)).not.toContain("Saving it will wake the agent");
	});

	it("says No shared notes, and nothing else, for a read-only session with nothing saved", () => {
		provide(session({ status: { type: "ended" } as ThreadModel["status"] }));
		const tree = sheet();
		expect(editor(tree)).toBeUndefined();
		expect(renderedText(tree)).toContain("No shared notes");
		for (const words of ["Agent", "No agent note yet", "No links yet"]) expect(renderedText(tree)).not.toContain(words);
	});

	it("omits the empty Your note group for a read-only session, when it has an agent note or links instead", () => {
		provide(session({ status: { type: "ended" } as ThreadModel["status"], agentNote: "on it" }));
		const tree = sheet();
		expect(editor(tree)).toBeUndefined();
		expect(renderedText(tree)).not.toContain("Your note");
		expect(renderedText(tree)).toContain("on it");
	});
});

describe("the agent's note", () => {
	it("shows the agent's note in the serif", () => {
		provide(session({ agentNote: "reading the router" }));
		const tree = sheet();
		const note = tree.root.find((node) => String(node.type) === "Text" && textOf(node) === "reading the router");
		expect(note.props.style).toMatchObject({ fontFamily: "SourceSerif4-Regular", color: palette.prose });
		expect(renderedText(tree)).toContain("Agent");
	});

	it("says No agent note yet when there is none", () => {
		provide(session());
		expect(renderedText(sheet())).toContain("No agent note yet");
	});
});

describe("links", () => {
	it("shows each link's label over its full URL, which may wrap after each slash, with its kind's glyph", () => {
		provide(session({ sessionUrls: [web, file] }));
		const tree = sheet();
		expect(symbols(tree)).toEqual(["globe", "doc.text"]);
		const text = renderedText(tree);
		expect(text).toContain("The PR");
		expect(text).toContain("https:/​/​example.com/​pr/​1");
		expect(text).toContain("file:/​/​/​Users/​j/​plan.md");
		const url = tree.root.find((node) => String(node.type) === "Text" && textOf(node).startsWith("https:"));
		expect(url.props.numberOfLines).toBeUndefined();
		expect(url.props.style).toMatchObject({ fontFamily: "Menlo", fontSize: 13, lineHeight: 18, color: palette.inkMid });
		expect(renderedText(tree)).toContain("The agent adds links as it works. Swipe left on one to remove it.");
	});

	it("says No links yet when there are none", () => {
		provide(session());
		expect(renderedText(sheet())).toContain("No links yet");
	});

	it("opens a web link in the in-app browser, and leaves a file link alone", () => {
		provide(session({ sessionUrls: [web, file] }));
		const tree = sheet();
		act(() => pressable(tree, "The PR, https://example.com/pr/1")?.props.onPress());
		expect(browser.openBrowserAsync).toHaveBeenCalledWith("https://example.com/pr/1", {
			dismissButtonStyle: "done",
			controlsColor: palette.accentInk,
		});
		expect(pressable(tree, "The plan, file:///Users/j/plan.md")?.props.onPress).toBeUndefined();
		expect(browser.openBrowserAsync).toHaveBeenCalledOnce();
	});

	it("opens a file link inside the session's folder in the Reader, after the sheet goes", () => {
		const plan: SessionURL = { id: "u3", url: `file://${CWD}/docs/plan.md`, label: "The plan" };
		provide(session({ sessionUrls: [plan] }));
		const tree = sheet();
		const order: string[] = [];
		navigation.goBack.mockImplementation(() => order.push("goBack"));
		navigation.navigate.mockImplementation((name: string) => order.push(name));
		const row = pressable(tree, `The plan, file://${CWD}/docs/plan.md`);
		expect(row?.props.accessibilityRole).toBe("link");
		act(() => row?.props.onPress());
		expect(order).toEqual(["goBack", "Reader"]);
		expect(navigation.navigate).toHaveBeenCalledWith("Reader", {
			hubId: HUB,
			sessionRef: REF,
			path: "docs/plan.md",
			reference: { path: "docs/plan.md", cwd: CWD, readTarget: `${CWD}/docs/plan.md`, provenance: "absolute" },
			sessionTitle: "Fix race",
		});
		expect(symbols(tree)).toEqual(["doc.text"]);
		expect(browser.openBrowserAsync).not.toHaveBeenCalled();
	});

	it.each([
		["another machine", "file://server/x.md"],
		["outside the session's folder", "file:///home/jesse/notes/todo.md"],
		["a malformed escape", "file:///tmp/bad%zz.md"],
	])("leaves a file link on %s untappable", (_name, url) => {
		provide(session({ sessionUrls: [{ id: "u4", url }] }));
		const tree = sheet();
		const row = pressable(tree, url);
		expect(row?.props.onPress).toBeUndefined();
		expect(row?.props.accessibilityRole).toBe("text");
		expect(symbols(tree)).toEqual(["doc.text"]);
		expect(navigation.navigate).not.toHaveBeenCalled();
		expect(navigation.goBack).not.toHaveBeenCalled();
	});

	it("offers Open, Copy link and Remove link on touch and hold, and says in the sheet that removing can't be undone", async () => {
		const { requests } = provide(session({ sessionUrls: [web] }));
		const tree = sheet();
		act(() => pressable(tree, "The PR, https://example.com/pr/1")?.props.onLongPress());
		const [options, choose] = actionSheet.mock.calls[0] ?? [];
		expect(options).toMatchObject({
			options: ["Open", "Copy link", "Remove link", "Cancel"],
			destructiveButtonIndex: 2,
			cancelButtonIndex: 3,
		});
		playedHaptics.length = 0;
		act(() => choose?.(1));
		expect(clipboard.setStringAsync).toHaveBeenCalledWith("https://example.com/pr/1");
		expect(playedHaptics).toEqual([]);
		act(() => choose?.(2));
		// Spec 16.6: rigid on a destructive choice, and only that one.
		expect(playedHaptics).toEqual(["impact:rigid"]);
		await flush();
		expect(requests.filter((request) => request.method === "urls/remove").map((request) => request.params.id)).toEqual([
			"u1",
		]);
		expect(renderedText(tree)).toContain("Link removed. Only the agent can add links.");
		expect(navigation.goBack).not.toHaveBeenCalled();
	});

	it("offers the same choices through Android's alert, since ActionSheetIOS doesn't exist there (RoboRev #2769 round 2)", () => {
		const os = Platform.OS;
		(Platform as { OS: string }).OS = "android";
		try {
			provide(session({ sessionUrls: [web] }));
			const tree = sheet();
			act(() => pressable(tree, "The PR, https://example.com/pr/1")?.props.onLongPress());
			const request = alertRequests.at(-1);
			expect(request?.title).toBe("The PR");
			expect(request?.buttons?.map((button) => button.text)).toEqual(["Open", "Copy link", "Remove link"]);
			expect(request?.options).toEqual({ cancelable: true });
			// Spec 16.6: rigid on the destructive choice here too.
			playedHaptics.length = 0;
			act(() => request?.buttons?.[2]?.onPress?.());
			expect(playedHaptics).toEqual(["impact:rigid"]);
		} finally {
			(Platform as { OS: string }).OS = os;
		}
	});

	it("says so in the sheet when a link couldn't be removed", async () => {
		provide(session({ sessionUrls: [web] }), { removeThrows: "hub unreachable" });
		const tree = sheet();
		act(() => pressable(tree, "The PR, https://example.com/pr/1")?.props.onLongPress());
		act(() => actionSheet.mock.calls[0]?.[1](2));
		await flush();
		expect(renderedText(tree)).toContain("Couldn't remove that link.");
	});

	it("drops a link the hub already removed, rather than leaving it listed (RoboRev #2769)", async () => {
		const { requests } = provide(session({ sessionUrls: [web] }), { removeThrows: 'no URL entry with id "u1"' });
		const tree = sheet();
		act(() => pressable(tree, "The PR, https://example.com/pr/1")?.props.onLongPress());
		act(() => actionSheet.mock.calls[0]?.[1](2));
		await flush();
		expect(requests.filter((request) => request.method === "urls/remove")).toHaveLength(1);
		expect(renderedText(tree)).toContain("Link removed. Only the agent can add links.");
		expect(renderedText(tree)).not.toContain("example.com");
		expect(symbols(tree)).toEqual([]);
	});

	it("stops hiding an id the session no longer lists, so a re-added link shows (RoboRev #2769)", async () => {
		provide(session({ sessionUrls: [web] }));
		const tree = sheet();
		act(() => pressable(tree, "The PR, https://example.com/pr/1")?.props.onLongPress());
		act(() => actionSheet.mock.calls[0]?.[1](2));
		await flush();
		expect(symbols(tree)).toEqual([]);
		const removedOwner = owner;
		// The hub re-reads with the row gone, then lists it again.
		act(() => {
			provide(session({ sessionUrls: [] }));
		});
		const emptyOwner = owner;
		act(() => {
			provide(session({ sessionUrls: [web] }));
		});
		expect(symbols(tree)).toEqual(["globe"]);
		notesHosts.release(sheetKey(HUB, REF), removedOwner);
		notesHosts.release(sheetKey(HUB, REF), emptyOwner);
	});

	it("removes a link on a full swipe left, with the same toast as its menu (spec 8.8)", async () => {
		const { requests } = provide(session({ sessionUrls: [web, file] }));
		const tree = sheet();
		const swipeables = tree.root.findAllByType("ReanimatedSwipeable" as never);
		expect(swipeables).toHaveLength(2);
		expect(renderedText(render(swipeables[0]?.props.renderRightActions()))).toBe("Remove");
		swipeRowFully(swipeables[0], "left");
		await flush();
		expect(requests.filter((request) => request.method === "urls/remove").map((request) => request.params.id)).toEqual([
			"u1",
		]);
		expect(renderedText(tree)).toContain("Link removed. Only the agent can add links.");
	});

	it("paints a swiped link the sheet's canvas, since the row itself has no fill", () => {
		provide(session({ sessionUrls: [web] }));
		const tree = sheet();
		expect(tree.root.findByProps({ testID: "swipe-row-content" }).props.style).toEqual({
			backgroundColor: palette.canvas,
		});
	});

	it("offers VoiceOver Remove link, the swipe's own remove, on a session whose notes you can change", async () => {
		const { requests } = provide(session({ sessionUrls: [web] }));
		const tree = sheet();
		const row = pressable(tree, "The PR, https://example.com/pr/1");
		expect(row?.props.accessibilityActions).toEqual([{ name: "remove", label: "Remove link" }]);
		act(() => row?.props.onAccessibilityAction({ nativeEvent: { actionName: "remove" } }));
		await flush();
		expect(requests.filter((request) => request.method === "urls/remove").map((request) => request.params.id)).toEqual([
			"u1",
		]);
		expect(renderedText(tree)).toContain("Link removed. Only the agent can add links.");
	});

	it("never removes a link from a swipe that began in the screen's left edge band", async () => {
		const { requests } = provide(session({ sessionUrls: [web] }));
		const tree = sheet();
		swipeRowFully(tree.root.findAllByType("ReanimatedSwipeable" as never)[0], "left", { pageX: 10 });
		await flush();
		expect(requests.filter((request) => request.method === "urls/remove")).toEqual([]);
	});

	it("offers no Remove link, and no Open for a file link, on a read-only session", () => {
		provide(session({ sessionUrls: [file], status: { type: "ended" } as ThreadModel["status"] }));
		const tree = sheet();
		act(() => pressable(tree, "The plan, file:///Users/j/plan.md")?.props.onLongPress());
		expect(actionSheet.mock.calls[0]?.[0]).toMatchObject({ options: ["Copy link", "Cancel"], cancelButtonIndex: 1 });
		expect(actionSheet.mock.calls[0]?.[0].destructiveButtonIndex).toBeUndefined();
		expect(renderedText(tree)).not.toContain("Swipe left on one to remove it.");
		expect(tree.root.findAllByType("ReanimatedSwipeable" as never)).toHaveLength(0);
		expect(pressable(tree, "The plan, file:///Users/j/plan.md")?.props.accessibilityActions).toBeUndefined();
	});
});

describe("closing the sheet", () => {
	it("is titled Notes & links, and Done goes back", () => {
		provide(session());
		const tree = sheet();
		expect(renderedText(tree)).toContain("Notes & links");
		act(() => pressable(tree, "Done")?.props.onPress());
		expect(navigation.goBack).toHaveBeenCalledOnce();
	});

	it("saves the note as the sheet leaves, and hands the screen the outcome", async () => {
		const { saved, requests } = provide(session());
		const tree = sheet();
		act(() => editor(tree)?.props.onChangeText("keep the tests"));
		act(() => tree.unmount());
		await flush();
		expect(
			requests.filter((request) => request.method === "notes/human/set").map((request) => request.params.note),
		).toEqual(["keep the tests"]);
		expect(saved).toHaveBeenCalledWith({ saved: true, woke: true });
	});

	it("saves when the app goes to the background with the sheet open", async () => {
		const { requests, saved } = provide(session());
		const tree = sheet();
		act(() => editor(tree)?.props.onChangeText("keep the tests"));
		act(() => {
			for (const listener of [...appState.listeners]) listener("background");
		});
		await flush();
		expect(requests.map((request) => request.method)).toEqual(["notes/human/set"]);
		expect(saved).not.toHaveBeenCalled();
		act(() => tree.unmount());
		expect(appState.listeners).toEqual([]);
	});

	it("leaves once its screen is gone", () => {
		provide(session());
		const tree = sheet();
		act(() => notesHosts.release(sheetKey(HUB, REF), owner));
		expect(navigation.goBack).toHaveBeenCalledOnce();
		expect(tree.toJSON()).toBeNull();
	});

	it("still saves an unsaved note when its own session screen goes away first (RoboRev #2769)", async () => {
		const { requests } = provide(session());
		const tree = sheet();
		act(() => editor(tree)?.props.onChangeText("keep the tests"));
		// The session screen (and its host) can go before the sheet's own
		// unmount runs: a real navigator's goBack() from useSheetHost's
		// finish() unmounts this component after that release, in that order.
		act(() => notesHosts.release(sheetKey(HUB, REF), owner));
		act(() => tree.unmount());
		await flush();
		expect(
			requests.filter((request) => request.method === "notes/human/set").map((request) => request.params.note),
		).toEqual(["keep the tests"]);
	});
});
