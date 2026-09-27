// The option rows a question timeline item renders key on their POSITION in
// the ask (timeline.ts's questionOptionKey), never on the label: the store's
// publish bounds every label the timeline carries (projectedRows.ts's
// truncateItem through boundQuestion, at MAX_ITEM_BYTES), so two options
// whose labels share a prefix past the bound cut to the same string. Keyed
// on that label — the pre-fix expression `${question.key}:${option.label}`
// — both rows answered to ONE React key, and React reported the duplicate
// on every render. This mounts the real TimelineItem with two such options
// (bounded exactly the way the store publishes them) and pins the absence
// of that report; the pure key's contract is pinned separately in
// timeline.test.ts.
import { createElement, type ReactNode } from "react";
import { act, type ReactTestInstance } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AskQuestionRef } from "@evener/appwire-client";
import {
	boundQuestion,
	MAX_ITEM_BYTES,
	truncateText,
	type MobileTimelineItem,
} from "./projectedRows";
import { TimelineItem } from "./TimelineItem";
import { Platform } from "react-native";
import { alertRequests, render, renderedText } from "./renderNative.testkit";
import type { TimelineRow } from "./timeline";

const mode = vi.hoisted(() => ({ scheme: "light" as "light" | "dark" }));
const native = vi.hoisted(() => ({
	showActionSheetWithOptions: vi.fn(),
	announceForAccessibility: vi.fn(),
	setStringAsync: vi.fn(async (_text: string) => true),
}));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	AccessibilityInfo: { announceForAccessibility: native.announceForAccessibility },
	ActionSheetIOS: { showActionSheetWithOptions: native.showActionSheetWithOptions },
	Linking: { openURL: async () => {} },
	// The real Modal renders its children only while visible.
	Modal: (props: { visible?: boolean; children?: ReactNode }) =>
		props.visible ? createElement("Modal", null, props.children) : null,
	useColorScheme: () => mode.scheme,
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("react-native-enriched-markdown", () => ({ EnrichedMarkdownText: "EnrichedMarkdownText" }));
vi.mock("expo-clipboard", () => ({ setStringAsync: native.setStringAsync }));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
// Attachments are not under test here, and the image leaf drags in the
// connection stack (expo-secure-store and the rest).
vi.mock("./TranscriptImages", () => ({ TranscriptImages: () => null }));

const ask: AskQuestionRef = {
	key: "call:0",
	callId: "call",
	header: "Choose",
	question: "Pick one",
	multiSelect: false,
	options: [],
};

// The store's bounded publish of one ask whose two options share a prefix
// past the display bound, so both labels cut to the same string.
function boundedAskRow(first: string, second: string): MobileTimelineItem {
	const questions = [
		boundQuestion(
			{
				...ask,
				options: [
					{ label: first, detail: "" },
					{ label: second, detail: "" },
				],
			},
			(text) => truncateText(text, MAX_ITEM_BYTES),
		),
	];
	return { kind: "question", id: "ask-1", questions };
}

it("renders an ask's option rows without a duplicate-key report when the bounded labels collide", () => {
	const prefix = "x".repeat(MAX_ITEM_BYTES * 2);
	const row = boundedAskRow(`${prefix}-first-tail`, `${prefix}-second-tail`);
	// The collision is real: the store's publish cuts both labels to the
	// same copy, so the pre-fix label-based key answered for both rows.
	const bounded = row.kind === "question" ? row.questions[0] : undefined;
	expect(bounded?.options[0].label).toBe(bounded?.options[1].label);

	const errors: string[] = [];
	const spy = vi.spyOn(console, "error").mockImplementation((...args) => {
		errors.push(args.map(String).join(" "));
	});
	try {
		render(<TimelineItem item={row} hubId="hub" sessionRef="session" />);
	} finally {
		spy.mockRestore();
	}
	expect(errors.filter((line) => /same key/.test(line))).toEqual([]);
});

function renderUserRow() {
	const row: MobileTimelineItem = { kind: "user", id: "u-1", text: "Ship it" };
	return render(<TimelineItem item={row} hubId="hub" sessionRef="session" />).root;
}

// The bubble is the node that fills with the bubble color.
function userBubbleStyle() {
	const [bubble] = renderUserRow().findAll(
		(node) => node.props.style?.backgroundColor !== undefined,
	);
	return bubble.props.style;
}

function userMessageTextProps() {
	return renderUserRow().findAllByType("Text" as never)[0].props;
}

it("fills your message bubble with the accent tint in both themes", () => {
	mode.scheme = "light";
	expect(userBubbleStyle()).toMatchObject({ backgroundColor: "#DDEBFC" });

	mode.scheme = "dark";
	expect(userBubbleStyle()).toMatchObject({ backgroundColor: "#2A343D" });
});

it("sets your message text in Source Serif 4 at 17/25 with the prose ink, keeping the text and label", () => {
	mode.scheme = "light";
	const light = userMessageTextProps();
	expect(light.style).toMatchObject({
		fontFamily: "SourceSerif4-Regular",
		fontSize: 17,
		lineHeight: 25,
		color: "#252521",
	});
	expect(light.children).toBe("Ship it");
	expect(light.accessibilityLabel).toBe("You: Ship it");

	mode.scheme = "dark";
	const dark = userMessageTextProps();
	expect(dark.style).toMatchObject({ color: "#E0DED6" });
});

const INK_LOW = "#6D6D64";

beforeEach(() => {
	mode.scheme = "light";
	native.showActionSheetWithOptions.mockReset();
	native.announceForAccessibility.mockReset();
	native.setStringAsync.mockClear();
});

function textOf(node: ReactTestInstance): string {
	return node.children.map((child) => (typeof child === "string" ? child : textOf(child))).join("");
}

/** Long-presses the row's pressable and returns the menu it opened: its
 * options, and a way to pick one the way ActionSheetIOS calls back. */
function longPress(root: ReactTestInstance) {
	const [target] = root.findAll((node) => typeof node.props.onLongPress === "function");
	act(() => target.props.onLongPress());
	const [options, pick] = native.showActionSheetWithOptions.mock.calls.at(-1) as [
		{ options: string[]; cancelButtonIndex: number },
		(index: number) => void,
	];
	return {
		options: options.options,
		cancelButtonIndex: options.cancelButtonIndex,
		choose: (label: string) => act(() => pick(options.options.indexOf(label))),
	};
}

function accessibilityActionsOf(root: ReactTestInstance) {
	const [node] = root.findAll((candidate) => Array.isArray(candidate.props.accessibilityActions));
	return {
		labels: (node.props.accessibilityActions as { label: string }[]).map((action) => action.label),
		run: (label: string) => {
			const action = (node.props.accessibilityActions as { name: string; label: string }[]).find(
				(candidate) => candidate.label === label,
			);
			act(() => node.props.onAccessibilityAction({ nativeEvent: { actionName: action?.name } }));
		},
	};
}

describe("your message", () => {
	const user = (over: Partial<Extract<MobileTimelineItem, { kind: "user" }>> = {}): MobileTimelineItem => ({
		kind: "user",
		id: "u-1",
		text: "Ship it",
		transcriptEntryIndex: 4,
		...over,
	});

	it("sits right-aligned in a bubble at most 85% wide with an 18pt continuous radius", () => {
		expect(userBubbleStyle()).toMatchObject({
			maxWidth: "85%",
			borderRadius: 18,
			borderCurve: "continuous",
			alignSelf: "flex-end",
		});
	});

	it("says a steered message was steered in mid-turn, and a plain one says nothing", () => {
		const steered = render(<TimelineItem item={user({ origin: "steered" })} hubId="hub" sessionRef="s" />);
		const caption = steered.root.findAll(
			(node) => String(node.type) === "Text" && textOf(node) === "Steered in mid-turn",
		);
		expect(caption).toHaveLength(1);
		expect(caption[0].props.style).toMatchObject({ fontSize: 12, lineHeight: 16, color: INK_LOW });

		const plain = render(<TimelineItem item={user()} hubId="hub" sessionRef="s" />);
		expect(renderedText(plain)).not.toContain("Steered in mid-turn");
	});

	it("carries no actions button beside the bubble", () => {
		const tree = render(<TimelineItem item={user()} hubId="hub" sessionRef="s" fork={() => {}} />);
		expect(tree.root.findAll((node) => /^Message actions/.test(node.props.accessibilityLabel ?? ""))).toEqual([]);
	});

	it("keeps its text out of native selection so touch and hold opens the menu", () => {
		const tree = render(<TimelineItem item={user()} hubId="hub" sessionRef="s" />);
		expect(tree.root.findAllByType("Text" as never)[0].props.selectable).toBe(false);
	});

	it("offers Copy, Fork from here and Quote on touch and hold, and Quote quotes the text", () => {
		const quote = vi.fn();
		const fork = vi.fn();
		const tree = render(
			<TimelineItem item={user()} hubId="hub" sessionRef="s" fork={fork} quote={quote} />,
		);
		const menu = longPress(tree.root);
		expect(menu.options).toEqual(["Copy", "Fork from here", "Quote", "Cancel"]);
		expect(menu.cancelButtonIndex).toBe(3);

		menu.choose("Quote");
		expect(quote).toHaveBeenCalledWith("Ship it");

		longPress(tree.root).choose("Fork from here");
		expect(fork).toHaveBeenCalledWith(4, "Ship it");
	});

	it("copies the text to the clipboard and announces it", async () => {
		const tree = render(<TimelineItem item={user()} hubId="hub" sessionRef="s" />);
		longPress(tree.root).choose("Copy");
		await vi.waitFor(() => expect(native.announceForAccessibility).toHaveBeenCalledWith("Copied"));
		expect(native.setStringAsync).toHaveBeenCalledWith("Ship it");
	});

	it.each([
		["no fork", { fork: undefined }],
		["a fork that can't run now", { forkDisabled: true }],
		["the transcript's first entry", { entry: 0 }],
		["no transcript entry", { entry: undefined }],
	])("leaves Fork from here out of the menu with %s", (_name, over: { fork?: undefined; forkDisabled?: boolean; entry?: number }) => {
		const tree = render(
			<TimelineItem
				item={user({ transcriptEntryIndex: "entry" in over ? over.entry : 4 })}
				hubId="hub"
				sessionRef="s"
				fork={"fork" in over ? over.fork : () => {}}
				forkDisabled={over.forkDisabled ?? false}
				quote={() => {}}
			/>,
		);
		expect(longPress(tree.root).options).toEqual(["Copy", "Quote", "Cancel"]);
	});

	it("leaves Quote out where nothing can take the quote", () => {
		const tree = render(<TimelineItem item={user()} hubId="hub" sessionRef="s" />);
		expect(longPress(tree.root).options).toEqual(["Copy", "Cancel"]);
	});

	it("fits every item in Android's three-button alert, cancelled by a tap outside", () => {
		const os = Platform.OS;
		(Platform as { OS: string }).OS = "android";
		try {
			const tree = render(
				<TimelineItem item={user({ text: "Ship   it\nnow" })} hubId="hub" sessionRef="s" fork={() => {}} quote={() => {}} />,
			);
			const [target] = tree.root.findAll((node) => typeof node.props.onLongPress === "function");
			act(() => target.props.onLongPress());
			const request = alertRequests.at(-1);
			expect(request?.message).toBe("Ship it now");
			expect(request?.buttons?.map((button) => button.text)).toEqual(["Copy", "Fork from here", "Quote"]);
			expect(request?.options).toEqual({ cancelable: true });
		} finally {
			(Platform as { OS: string }).OS = os;
		}
	});

	it("gives VoiceOver the menu's items as actions", () => {
		const quote = vi.fn();
		const tree = render(<TimelineItem item={user()} hubId="hub" sessionRef="s" fork={() => {}} quote={quote} />);
		const actions = accessibilityActionsOf(tree.root);
		expect(actions.labels).toEqual(["Copy", "Fork from here", "Quote"]);
		actions.run("Quote");
		expect(quote).toHaveBeenCalledWith("Ship it");
	});
});

describe("the agent's message", () => {
	const reply = (over: Partial<Extract<MobileTimelineItem, { kind: "assistant" }>> = {}): MobileTimelineItem => ({
		kind: "assistant",
		id: "a-1",
		markdown: "Done. **All** tests pass.\nNext: ship.",
		streaming: false,
		...over,
	});

	it("offers Copy, Quote in reply and Select text on touch and hold, in place of the selection menu", () => {
		const quote = vi.fn();
		const tree = render(<TimelineItem item={reply()} hubId="hub" sessionRef="s" quote={quote} />);
		const markdown = tree.root.findByType("EnrichedMarkdownText" as never);
		expect(markdown.props.selectable).toBe(false);
		expect(markdown.props.contextMenuItems).toBeUndefined();

		const menu = longPress(tree.root);
		expect(menu.options).toEqual(["Copy", "Quote in reply", "Select text", "Cancel"]);
		expect(menu.cancelButtonIndex).toBe(3);
		menu.choose("Quote in reply");
		expect(quote).toHaveBeenCalledWith("Done. **All** tests pass.\nNext: ship.");
	});

	it("copies the markdown source", async () => {
		const tree = render(<TimelineItem item={reply()} hubId="hub" sessionRef="s" />);
		longPress(tree.root).choose("Copy");
		await vi.waitFor(() => expect(native.announceForAccessibility).toHaveBeenCalledWith("Copied"));
		expect(native.setStringAsync).toHaveBeenCalledWith("Done. **All** tests pass.\nNext: ship.");
	});

	it("opens the message as selectable serif text in a full-screen view, closed by Done", () => {
		const tree = render(<TimelineItem item={reply()} hubId="hub" sessionRef="s" />);
		expect(tree.root.findAllByType("Modal" as never)).toEqual([]);
		longPress(tree.root).choose("Select text");

		const modal = tree.root.findByType("Modal" as never);
		const text = modal.findAll(
			(node) => String(node.type) === "Text" && node.props.children === "Done. **All** tests pass.\nNext: ship.",
		);
		expect(text).toHaveLength(1);
		expect(text[0].props.selectable).toBe(true);
		expect(text[0].props.style).toMatchObject({ fontFamily: "SourceSerif4-Regular", fontSize: 17, lineHeight: 26 });

		const done = modal.findAll((node) => node.props.accessibilityLabel === "Done" && typeof node.props.onPress === "function");
		act(() => done[0].props.onPress());
		expect(tree.root.findAllByType("Modal" as never)).toEqual([]);
	});

	it("gives VoiceOver the menu's items as actions on the message itself", () => {
		const quote = vi.fn();
		const tree = render(<TimelineItem item={reply()} hubId="hub" sessionRef="s" quote={quote} />);
		const markdown = tree.root.findByType("EnrichedMarkdownText" as never);
		expect((markdown.props.accessibilityActions as { label: string }[]).map((action) => action.label)).toEqual([
			"Copy",
			"Quote in reply",
			"Select text",
		]);
		accessibilityActionsOf(tree.root).run("Quote in reply");
		expect(quote).toHaveBeenCalledWith("Done. **All** tests pass.\nNext: ship.");
	});

	it("says nothing about writing while it streams: the tray says it", () => {
		const tree = render(<TimelineItem item={reply({ streaming: true })} hubId="hub" sessionRef="s" />);
		expect(renderedText(tree)).not.toContain("Writing");
	});
});

describe("a time marker", () => {
	// Local wall-clock times, so the test reads the same in every host zone.
	const at = new Date(2026, 8, 26, 14, 14).getTime();
	const row: TimelineRow = { kind: "time", id: "time:t1", turnId: "t1", at };

	beforeEach(() => {
		vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	function marker(tree: ReturnType<typeof render>) {
		return tree.root.findAll((node) => String(node.type) === "Text" && /2:14 PM/.test(textOf(node)))[0];
	}

	it("reads the day and time as a centered ink-low caption with tabular figures", () => {
		vi.setSystemTime(new Date(2026, 8, 26, 18, 0));
		const tree = render(<TimelineItem item={row} hubId="hub" sessionRef="s" />);
		const caption = marker(tree);
		expect(textOf(caption)).toBe("Today 2:14 PM");
		expect(caption.props.style).toMatchObject({
			fontSize: 12,
			lineHeight: 16,
			color: INK_LOW,
			textAlign: "center",
			fontVariant: ["tabular-nums"],
		});
		expect(tree.root.findAll((node) => node.props.style?.paddingTop === 16).length).toBeGreaterThan(0);
		act(() => tree.unmount());
	});

	it("turns to Yesterday when the clock passes midnight, with no new rows", () => {
		vi.setSystemTime(new Date(2026, 8, 26, 23, 59, 30));
		const tree = render(<TimelineItem item={row} hubId="hub" sessionRef="s" />);
		expect(textOf(marker(tree))).toBe("Today 2:14 PM");
		act(() => {
			vi.advanceTimersByTime(60_000);
		});
		expect(textOf(marker(tree))).toBe("Yesterday 2:14 PM");
		act(() => tree.unmount());
	});
});

describe("a run in the transcript", () => {
	const run: TimelineRow = {
		kind: "run",
		id: "run:a",
		turnId: "t1",
		steps: [
			{
				kind: "activity",
				id: "a",
				label: "read_file",
				family: "tool",
				state: "completed",
				detail: { arguments: '{"file_path":"agent/session.go"}' },
			},
		],
	};
	const header = (root: ReactTestInstance) =>
		root.findAll((node) => /^1 step · read 1 file, (collapsed|expanded)$/.test(node.props.accessibilityLabel ?? ""))[0];

	it("folds by default and remembers the reader's expansion", () => {
		const tree = render(<TimelineItem item={run} hubId="hub" sessionRef="run-folds" />);
		expect(header(tree.root).props.accessibilityLabel).toBe("1 step · read 1 file, collapsed");
		act(() => header(tree.root).props.onPress());
		expect(header(tree.root).props.accessibilityLabel).toBe("1 step · read 1 file, expanded");
		expect(renderedText(tree)).toContain("agent/session.go");
	});

	it("opens by default at the levels that expand by default", () => {
		const tree = render(<TimelineItem item={run} hubId="hub" sessionRef="run-opens" expandByDefault />);
		expect(header(tree.root).props.accessibilityLabel).toBe("1 step · read 1 file, expanded");
	});

	it("never folds while it is live", () => {
		const tree = render(<TimelineItem item={run} hubId="hub" sessionRef="run-live" live />);
		expect(header(tree.root)).toBeUndefined();
		expect(renderedText(tree)).toContain("agent/session.go");
	});
});
