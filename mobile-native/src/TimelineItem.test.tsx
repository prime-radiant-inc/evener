import { createElement, type ReactNode } from "react";
import { act, type ReactTestInstance } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { paletteFor } from "./design/tokens";
import type { MobileTimelineItem } from "./projectedRows";
import { errorAction } from "./session/errorAction";
import { TimelineItem } from "./TimelineItem";
import { Platform, Text } from "react-native";
import { alertRequests, render, renderedText, textOf } from "./renderNative.testkit";
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

function renderUserRow() {
	const row: MobileTimelineItem = { kind: "user", id: "u-1", text: "Ship it" };
	return render(<TimelineItem item={row} hubId="hub" sessionRef="session" />).root;
}

// The bubble is the node that fills with the bubble color.
function userBubbleStyle() {
	const [bubble] = renderUserRow().findAll((node) => node.props.style?.backgroundColor !== undefined);
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
const INK_MID = "#5F5F57";
const EDGE_STRONG = "#B7B6AC";

beforeEach(() => {
	mode.scheme = "light";
	native.showActionSheetWithOptions.mockReset();
	native.announceForAccessibility.mockReset();
	native.setStringAsync.mockClear();
});

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
		const tree = render(<TimelineItem item={user()} hubId="hub" sessionRef="s" fork={fork} quote={quote} />);
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
	])(
		"leaves Fork from here out of the menu with %s",
		(_name, over: { fork?: undefined; forkDisabled?: boolean; entry?: number }) => {
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
		},
	);

	it("leaves Quote out where nothing can take the quote", () => {
		const tree = render(<TimelineItem item={user()} hubId="hub" sessionRef="s" />);
		expect(longPress(tree.root).options).toEqual(["Copy", "Cancel"]);
	});

	it("fits every item in Android's three-button alert, cancelled by a tap outside", () => {
		const os = Platform.OS;
		(Platform as { OS: string }).OS = "android";
		try {
			const tree = render(
				<TimelineItem
					item={user({ text: "Ship   it\nnow" })}
					hubId="hub"
					sessionRef="s"
					fork={() => {}}
					quote={() => {}}
				/>,
			);
			const [target] = tree.root.findAll((node) => typeof node.props.onLongPress === "function");
			act(() => target.props.onLongPress());
			const request = alertRequests.at(-1);
			// The preview is the alert's title now, as NotesSheet's link menu
			// shows it, rather than a "Message" heading over the words.
			expect(request?.title).toBe("Ship it now");
			expect(request?.message).toBeUndefined();
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

		const done = modal.findAll(
			(node) => node.props.accessibilityLabel === "Done" && typeof node.props.onPress === "function",
		);
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

	// Every visible row re-renders on every publish, so a settled message must
	// leave the markdown view's memo intact: an unchanged message renders it
	// once, not once per streaming frame.
	it("leaves the markdown view alone when the list re-renders an unchanged message", () => {
		const quote = vi.fn();
		const message = () => <TimelineItem item={reply()} hubId="hub" sessionRef="s" quote={quote} />;
		const tree = render(message());
		const before = tree.root.findByType("EnrichedMarkdownText" as never).props;
		act(() => tree.update(message()));
		const after = tree.root.findByType("EnrichedMarkdownText" as never).props;
		expect(after).toBe(before);
	});

	it("draws the chips its screen gives it under the message, and a user's message asks for none", () => {
		const documentChips = vi.fn(() => <Text>chip for plan.md</Text>);
		const tree = render(
			<TimelineItem item={reply({ streaming: true })} hubId="hub" sessionRef="s" documentChips={documentChips} />,
		);
		expect(documentChips).toHaveBeenCalledWith({
			id: "a-1",
			markdown: "Done. **All** tests pass.\nNext: ship.",
			streaming: true,
		});
		const text = renderedText(tree);
		expect(text.indexOf("chip for plan.md")).toBeGreaterThan(text.indexOf("ship."));
		documentChips.mockClear();
		render(
			<TimelineItem
				item={{ kind: "user", id: "u-1", text: "Read `docs/plan.md`" }}
				hubId="hub"
				sessionRef="s"
				documentChips={documentChips}
			/>,
		);
		expect(documentChips).not.toHaveBeenCalled();
	});

	it("says nothing about writing while it streams: the tray says it", () => {
		const tree = render(<TimelineItem item={reply({ streaming: true })} hubId="hub" sessionRef="s" />);
		expect(renderedText(tree)).not.toContain("Writing");
	});
});

describe("a saved note (spec 8.2, 8.8)", () => {
	function render_(text: string) {
		const row: MobileTimelineItem = { kind: "note", id: "note:1", text };
		return render(<TimelineItem item={row} hubId="hub" sessionRef="s" />);
	}

	function caption(tree: ReturnType<typeof render>) {
		return tree.root.findAll((node) => String(node.type) === "Text" && /your note/i.test(textOf(node)))[0];
	}

	it('reads "You updated your note" over the note, in the serif prose ink, behind a left rule', () => {
		mode.scheme = "light";
		const tree = render_("Fix causes");
		expect(textOf(caption(tree))).toBe("You updated your note");
		expect(caption(tree).props.style).toMatchObject({ fontSize: 13, lineHeight: 18, color: INK_MID });
		const [body] = tree.root.findAll((node) => String(node.type) === "Text" && textOf(node) === "Fix causes");
		expect(body.props.style).toMatchObject({
			fontFamily: "SourceSerif4-Regular",
			fontSize: 17,
			lineHeight: 25,
			color: "#252521",
		});
		const [rule] = tree.root.findAll((node) => node.props.style?.borderLeftWidth === 2);
		expect(rule.props.style).toMatchObject({ borderLeftColor: EDGE_STRONG });
		act(() => tree.unmount());
	});

	it('reads "You cleared your note" with no text beneath, for an emptied note', () => {
		const tree = render_("");
		expect(textOf(caption(tree))).toBe("You cleared your note");
		expect(tree.root.findAll((node) => String(node.type) === "Text").length).toBe(1);
		act(() => tree.unmount());
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
				// Its words, as projectedRows builds them from the whole step.
				detail: { arguments: '{"file_path":"agent/session.go"}', summary: "Read agent/session.go" },
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
		const tree = render(<TimelineItem item={run} hubId="hub" sessionRef="run-live" live liveRunsOpen />);
		expect(header(tree.root)).toBeUndefined();
		expect(renderedText(tree)).toContain("agent/session.go");
	});

	// Nothing collapses on its own (Jesse's ruling on S7): a run held open
	// while live stays open once the next run starts or the turn ends.
	it("stays open after it stops being live, until you fold it", () => {
		const tree = render(<TimelineItem item={run} hubId="hub" sessionRef="run-stays-open" live liveRunsOpen />);
		act(() => tree.update(<TimelineItem item={run} hubId="hub" sessionRef="run-stays-open" liveRunsOpen />));
		expect(header(tree.root).props.accessibilityLabel).toBe("1 step · read 1 file, expanded");
		expect(renderedText(tree)).toContain("agent/session.go");
		act(() => header(tree.root).props.onPress());
		expect(header(tree.root).props.accessibilityLabel).toBe("1 step · read 1 file, collapsed");
	});

	it("stays open when you switch to a level that doesn't open live runs", () => {
		const tree = render(<TimelineItem item={run} hubId="hub" sessionRef="run-level-drop" live liveRunsOpen />);
		act(() => tree.update(<TimelineItem item={run} hubId="hub" sessionRef="run-level-drop" />));
		expect(header(tree.root).props.accessibilityLabel).toBe("1 step · read 1 file, expanded");
	});

	// Two parallel calls: the one that settles last joins the front of the run
	// in the same update that ends the turn. The run is the same run.
	it("stays open when a parallel call settles ahead of it as the turn ends", () => {
		const step = run.kind === "run" ? run.steps[0] : undefined;
		if (!step) throw new Error("run fixture has no step");
		const b = { ...step, id: "b", detail: { ...step.detail, callId: "call-b" } };
		const a = { ...step, id: "a", detail: { ...step.detail, callId: "call-a" } };
		const live: TimelineRow = { kind: "run", id: "run:b", turnId: "t1", steps: [b] };
		const settled: TimelineRow = { kind: "run", id: "run:a", turnId: "t1", steps: [a, b] };
		const tree = render(<TimelineItem item={live} hubId="hub" sessionRef="run-parallel" live liveRunsOpen />);
		act(() => tree.update(<TimelineItem item={settled} hubId="hub" sessionRef="run-parallel" liveRunsOpen />));
		const fold = tree.root.findAll((node) =>
			/^2 steps .*, (collapsed|expanded)$/.test(node.props.accessibilityLabel ?? ""),
		)[0];
		expect(fold?.props.accessibilityLabel).toMatch(/, expanded$/);
	});

	// An older page can bring earlier items of the first loaded turn: a run
	// ahead of this one, or steps that join its front. Either way it is the
	// same run, and the run ahead of it stays folded.
	it("stays open when an older page brings an earlier run or earlier steps of its turn", () => {
		const step = run.kind === "run" ? run.steps[0] : undefined;
		if (!step) throw new Error("run fixture has no step");
		const call = (id: string) => ({ ...step, id, detail: { ...step.detail, callId: `call-${id}` } });
		const runOf = (...ids: string[]): TimelineRow => ({
			kind: "run",
			id: `run:${ids[0]}`,
			turnId: "t1",
			steps: ids.map(call),
		});
		const tree = render(<TimelineItem item={runOf("b")} hubId="hub" sessionRef="run-prepend" live liveRunsOpen />);
		act(() => tree.update(<TimelineItem item={runOf("b")} hubId="hub" sessionRef="run-prepend" liveRunsOpen />));
		const earlier = render(<TimelineItem item={runOf("z")} hubId="hub" sessionRef="run-prepend" liveRunsOpen />);
		expect(header(earlier.root).props.accessibilityLabel).toBe("1 step · read 1 file, collapsed");
		act(() => tree.update(<TimelineItem item={runOf("y", "b")} hubId="hub" sessionRef="run-prepend" liveRunsOpen />));
		const fold = tree.root.findAll((node) =>
			/^2 steps .*, (collapsed|expanded)$/.test(node.props.accessibilityLabel ?? ""),
		)[0];
		expect(fold?.props.accessibilityLabel).toMatch(/, expanded$/);
	});

	// At Activity and Full every run opens by default, so a run held open
	// there must still be pinned, or a switch to Intent would fold it.
	it("stays open after a switch down from a level that opens every run", () => {
		const tree = render(
			<TimelineItem item={run} hubId="hub" sessionRef="run-full-drop" live liveRunsOpen expandByDefault />,
		);
		act(() => tree.update(<TimelineItem item={run} hubId="hub" sessionRef="run-full-drop" />));
		expect(header(tree.root).props.accessibilityLabel).toBe("1 step · read 1 file, expanded");
	});

	// A tool shown from the overlay carries the overlay's id until history
	// records its call; the call id is the same on both.
	it("stays open when its first step's id changes as history records the call", () => {
		const step = run.kind === "run" ? run.steps[0] : undefined;
		if (!step) throw new Error("run fixture has no step");
		const withStep = (id: string): TimelineRow => ({
			kind: "run",
			id: `run:${id}`,
			turnId: "t1",
			steps: [{ ...step, id, detail: { ...step.detail, callId: "call-1" } }],
		});
		const tree = render(
			<TimelineItem item={withStep("tool:call:call-1")} hubId="hub" sessionRef="run-renamed" live liveRunsOpen />,
		);
		act(() =>
			tree.update(<TimelineItem item={withStep("item_tool_1_0")} hubId="hub" sessionRef="run-renamed" liveRunsOpen />),
		);
		expect(header(tree.root).props.accessibilityLabel).toBe("1 step · read 1 file, expanded");
	});

	// At Intent the tray shows the live step, so the run's line is enough.
	it("doesn't open while live where the tray shows the live step", () => {
		const tree = render(<TimelineItem item={run} hubId="hub" sessionRef="run-live-intent" live />);
		expect(header(tree.root).props.accessibilityLabel).toBe("1 step · read 1 file, collapsed");
		act(() => tree.update(<TimelineItem item={run} hubId="hub" sessionRef="run-live-intent" />));
		expect(header(tree.root).props.accessibilityLabel).toBe("1 step · read 1 file, collapsed");
	});
});

function texts(root: ReactTestInstance) {
	return root.findAll((node) => String(node.type) === "Text");
}

describe("a settled thought", () => {
	const thought = (durationMs?: number): TimelineRow => ({
		kind: "activity",
		id: "r-1",
		label: "Reasoning",
		family: "reasoning",
		state: "completed",
		detail: { output: "Weigh the options first.", ...(durationMs === undefined ? {} : { durationMs }) },
	});

	it("reads how long it took, folded, and opens to the thought", () => {
		const tree = render(<TimelineItem item={thought(12_000)} hubId="hub" sessionRef="thought-open" />);
		const line = tree.root.findAll((node) => node.props.accessibilityRole === "button")[0];
		expect(textOf(line)).toBe("Thought for 12s ›");
		expect(renderedText(tree)).not.toContain("Weigh the options first.");
		act(() => line.props.onPress());
		expect(renderedText(tree)).toContain("Weigh the options first.");
	});

	it("says only that it thought when the length is unknown", () => {
		const tree = render(<TimelineItem item={thought()} hubId="hub" sessionRef="thought-plain" />);
		expect(textOf(tree.root.findAll((node) => node.props.accessibilityRole === "button")[0])).toBe("Thought ›");
	});

	it("reads a thought it can't show as one quiet line, with no rule", () => {
		const row: TimelineRow = { kind: "failure", id: "r-2", title: "Thought not shown", detail: "", thought: true };
		const tree = render(<TimelineItem item={row} hubId="hub" sessionRef="thought-hidden" />);
		expect(renderedText(tree)).toContain("Thought not shown");
		expect(tree.root.findAll((node) => node.props.style?.borderLeftWidth !== undefined)).toEqual([]);
		expect(texts(tree.root).find((node) => textOf(node) === "Thought not shown")?.props.style).toMatchObject({
			color: INK_LOW,
		});
	});
});

describe("a subagent", () => {
	const ALIVE = "#189A4D";
	const DANGER = "#E3474C";
	const EDGE_STRONG = "#B7B6AC";
	const row = (state: "running" | "failed" | "completed"): TimelineRow => ({
		kind: "activity",
		id: "item-1",
		label: "delegate",
		family: "tool",
		state,
		detail: { callId: "call-1", description: "Audit the store" },
	});
	const delegate = {
		delegateId: "d1",
		ownerSessionId: "s0",
		rootSessionId: "s0",
		childSessionId: "s1",
		transcriptRef: "local:child-1",
		type: "delegate",
		lifecycle: "running",
		phase: "running",
		status: "running",
		resumable: false,
		needsAttention: false,
		projectionRevision: 1,
		description: "Audit the store",
		originItemId: "item-1",
		runningForMs: 60_000,
	};
	const rail = (root: ReactTestInstance) =>
		root.findAll((node) => node.props.style?.borderLeftWidth === 2)[0]?.props.style.borderLeftColor;

	it("rails its row in its state's hue", () => {
		expect(
			rail(render(<TimelineItem item={row("running")} hubId="hub" sessionRef="s" delegates={[delegate]} />).root),
		).toBe(ALIVE);
		expect(rail(render(<TimelineItem item={row("failed")} hubId="hub" sessionRef="s" />).root)).toBe(DANGER);
		expect(rail(render(<TimelineItem item={row("completed")} hubId="hub" sessionRef="s" />).root)).toBe(EDGE_STRONG);
	});

	it("is a full 44pt target even with no activity line", () => {
		const done = { ...delegate, status: "completed", terminal: true };
		const tree = render(
			<TimelineItem item={row("completed")} hubId="hub" sessionRef="s" delegates={[done]} openSubagent={() => {}} />,
		);
		expect(tree.root.findAll((node) => node.props.accessibilityRole === "button")[0].props.style).toMatchObject({
			minHeight: 44,
		});
	});

	it("opens the subagent's own transcript when pressed", () => {
		const openSubagent = vi.fn();
		const tree = render(
			<TimelineItem
				item={row("running")}
				hubId="hub"
				sessionRef="s"
				delegates={[delegate]}
				openSubagent={openSubagent}
			/>,
		);
		expect(renderedText(tree)).toContain("running · 1m");
		tree.root.findAll((node) => node.props.accessibilityRole === "button")[0].props.onPress();
		expect(openSubagent).toHaveBeenCalledWith("local:child-1", "Audit the store");
	});
});

describe("a question you answered", () => {
	const row: TimelineRow = {
		kind: "activity",
		id: "ask-1",
		label: "ask_user",
		family: "tool",
		state: "completed",
		detail: {
			arguments: JSON.stringify({
				questions: [
					{
						header: "Choice",
						question: "Keep or drop the implied options?",
						options: [{ label: "Drop them", detail: "" }],
					},
				],
			}),
		},
	};

	it("shows the question with your answer beneath", () => {
		const tree = render(<TimelineItem item={row} hubId="hub" sessionRef="s" answerFor={() => "Drop them"} />);
		expect(renderedText(tree)).toContain("Keep or drop the implied options?");
		expect(renderedText(tree)).toContain("You answered: Drop them");
	});

	it("shows the question alone while it has no answer", () => {
		const tree = render(<TimelineItem item={row} hubId="hub" sessionRef="s" answerFor={() => undefined} />);
		expect(renderedText(tree)).toContain("Keep or drop the implied options?");
		expect(renderedText(tree)).not.toContain("You answered");
	});
});

describe("a system event", () => {
	const notice = (over: Partial<Extract<TimelineRow, { kind: "notice" }>> = {}): TimelineRow => ({
		kind: "notice",
		id: "n-1",
		origin: "system",
		family: "lifecycle",
		tone: "info",
		text: "Context compacted · 412K → 38K tokens",
		...over,
	});

	it("reads quietly beside a diamond", () => {
		const tree = render(<TimelineItem item={notice()} hubId="hub" sessionRef="event" />);
		const diamond = tree.root.findAllByType("SymbolView" as never)[0];
		expect([diamond?.props.name, diamond?.props.tintColor]).toEqual(["diamond", INK_LOW]);
		expect(
			texts(tree.root).find((node) => textOf(node) === "Context compacted · 412K → 38K tokens")?.props.style,
		).toMatchObject({
			fontSize: 13,
			lineHeight: 18,
			color: INK_LOW,
		});
	});

	it("is a full 44pt target to open", () => {
		const tree = render(<TimelineItem item={notice()} hubId="hub" sessionRef="event-target" />);
		const target = tree.root.findAll((node) => node.props.accessibilityRole === "button")[0];
		expect(target.props.style).toMatchObject({ minHeight: 44 });
	});

	// A long label ("System steered: Where to find the full transcript") keeps
	// its chevron in view: it truncates to one line, and VoiceOver reads it whole.
	it("keeps a long label to one line beside its chevron, spoken in full", () => {
		const label = "System steered: Where to find the full transcript";
		const event = notice({ origin: "steering", steeringKind: "transcript-pointer", text: "Read it.", label });
		const tree = render(<TimelineItem item={event} hubId="hub" sessionRef="event-long" />);
		const text = tree.root.findAll((node) => String(node.type) === "Text").find((node) => textOf(node) === label);
		expect(text?.props.numberOfLines).toBe(1);
		expect(text?.props.style).toMatchObject({ flexShrink: 1 });
		const button = tree.root.findAll((node) => node.props.accessibilityRole === "button")[0];
		expect(button?.props.accessibilityLabel).toBe(label);
	});

	it("opens a labelled steering notice's text", () => {
		const reminder = notice({
			origin: "steering",
			steeringKind: "task-nudge",
			text: "Remember the open task.",
			label: "Task reminder",
		});
		const tree = render(<TimelineItem item={reminder} hubId="hub" sessionRef="event-open" />);
		expect(renderedText(tree)).toContain("Task reminder");
		expect(renderedText(tree)).not.toContain("Remember the open task.");
		act(() => tree.root.findAll((node) => node.props.accessibilityRole === "button")[0].props.onPress());
		expect(renderedText(tree)).toContain("Remember the open task.");
	});

	// An informational warning is one quiet line, but an item that also carries
	// a hint shows it as a quiet second line under the message, and VoiceOver
	// speaks both (the web keeps the hint as hover title and VisuallyHidden text).
	it("reads an informational warning's hint as a quiet second line, spoken in the row", () => {
		const warning = notice({
			family: "informational",
			tone: "system",
			text: "Output clamped to fit the context window",
			hint: "Free some context.",
		});
		const tree = render(<TimelineItem item={warning} hubId="hub" sessionRef="event-hint" />);
		expect(renderedText(tree)).toContain("Output clamped to fit the context window");
		expect(renderedText(tree)).toContain("Free some context.");
		expect(texts(tree.root).find((node) => textOf(node) === "Free some context.")?.props.style).toMatchObject({
			fontSize: 13,
			lineHeight: 18,
			color: INK_LOW,
		});
		const button = tree.root.findAll((node) => node.props.accessibilityRole === "button")[0];
		expect(button?.props.accessibilityLabel).toBe("Output clamped to fit the context window\nFree some context.");
	});
});

const palette = paletteFor("light");

// The colour of the one rule an error or warning row draws down its left edge.
function ruleColor(tree: ReturnType<typeof render>): unknown {
	const rules = tree.root.findAll((node) => typeof node.type === "string" && node.props.style?.borderLeftWidth === 2);
	expect(rules).toHaveLength(1);
	return rules[0]?.props.style.borderLeftColor;
}

// The label VoiceOver reads for a row's title: a warning and an error differ
// in more than colour.
const titleLabels = (tree: ReturnType<typeof render>) =>
	tree.root
		.findAll((node) => typeof node.type === "string" && typeof node.props.accessibilityLabel === "string")
		.map((node) => node.props.accessibilityLabel);

describe("an error", () => {
	const failure = (detail: string, turnId = "turn_2"): TimelineRow => ({
		kind: "failure",
		id: `failure:${turnId}`,
		title: "The turn failed",
		detail,
		turnId,
	});
	const session = (resumeRequired = false) => ({
		resumeRequired,
		turns: [{ id: "turn_1" }, { id: "turn_2" }] as never,
	});
	function show(row: TimelineRow, resumeRequired = false) {
		const onErrorAction = vi.fn();
		const tree = render(
			<TimelineItem
				item={row}
				hubId="hub"
				sessionRef="error"
				errorActionFor={(failed) => errorAction(failed, session(resumeRequired), true)}
				onErrorAction={onErrorAction}
			/>,
		);
		const buttons = tree.root.findAll(
			(node) => node.props.accessibilityRole === "button" && typeof node.props.onPress === "function",
		);
		return { tree, onErrorAction, buttons };
	}

	it("draws a red rule, the title and the detail", () => {
		const { tree } = show(failure("go test exited 1"));
		expect(ruleColor(tree)).toBe(palette.dangerInk);
		expect(titleLabels(tree)).toContain("Error: The turn failed");
		expect(texts(tree.root).find((node) => textOf(node) === "The turn failed")?.props.style).toMatchObject({
			fontWeight: "600",
			fontSize: 15,
		});
		expect(renderedText(tree)).toContain("go test exited 1");
	});

	it("offers Sign in for an expired sign-in", () => {
		const { buttons, onErrorAction } = show(failure("401 Unauthorized"));
		expect(buttons.map((button) => button.props.accessibilityLabel)).toEqual(["Sign in"]);
		buttons[0].props.onPress();
		expect(onErrorAction).toHaveBeenCalledWith("signIn");
	});

	it("offers Resume on a paused session", () => {
		expect(show(failure("go test exited 1"), true).buttons.map((button) => button.props.accessibilityLabel)).toEqual([
			"Resume",
		]);
	});

	it("offers Retry under the latest turn only", () => {
		expect(show(failure("go test exited 1")).buttons.map((button) => button.props.accessibilityLabel)).toEqual([
			"Retry",
		]);
		expect(show(failure("go test exited 1", "turn_1")).buttons).toEqual([]);
	});
});

// A warning is amber (spec 8.2 and the state table: amber means a human is
// needed; red means failed), with its hint as a quiet second line (#3387).
describe("a warning", () => {
	it("draws a daemon warning notice with an amber rule, its text, and its hint", () => {
		const tree = render(
			<TimelineItem
				item={{
					kind: "notice",
					id: "w",
					origin: "system",
					family: "warning",
					tone: "attention",
					eventKind: "warning",
					text: "inspect delegate attention: permission denied",
					hint: "Check the state directory.",
					turnId: "turn_2",
				}}
				hubId="hub"
				sessionRef="warning"
				errorActionFor={() => "retry"}
				onErrorAction={() => {}}
			/>,
		);
		expect(ruleColor(tree)).toBe(palette.attention);
		expect(renderedText(tree)).toContain("inspect delegate attention: permission denied");
		expect(renderedText(tree)).toContain("Check the state directory.");
		expect(titleLabels(tree)).toContain("Warning: inspect delegate attention: permission denied");
		// A warning reports; it offers no Retry or Resume of its own.
		expect(tree.root.findAll((node) => node.props.accessibilityRole === "button")).toHaveLength(0);
	});

	it("draws a warning item amber too, with no Resume or Retry even on a paused session", () => {
		const tree = render(
			<TimelineItem
				item={{ kind: "failure", id: "w1", title: "Low disk", detail: "clean up", attention: true, turnId: "turn_2" }}
				hubId="hub"
				sessionRef="warning"
				errorActionFor={(failed) =>
					errorAction(failed, { resumeRequired: true, turns: [{ id: "turn_1" }, { id: "turn_2" }] as never }, true)
				}
				onErrorAction={() => {}}
			/>,
		);
		expect(ruleColor(tree)).toBe(palette.attention);
		expect(tree.root.findAll((node) => node.props.accessibilityRole === "button")).toHaveLength(0);
	});

	it("keeps a loop detection red", () => {
		const tree = render(
			<TimelineItem
				item={{
					kind: "notice",
					id: "l",
					origin: "system",
					family: "warning",
					tone: "warning",
					eventKind: "loop_detection",
					text: "The agent repeated itself",
				}}
				hubId="hub"
				sessionRef="warning"
			/>,
		);
		expect(ruleColor(tree)).toBe(palette.dangerInk);
		expect(titleLabels(tree)).toContain("Error: The agent repeated itself");
	});
});
