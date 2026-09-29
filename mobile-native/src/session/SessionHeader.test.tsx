import type { ReactNode } from "react";
import { AccessibilityInfo, Animated, Text } from "react-native";
import { act, type ReactTestInstance, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, describe, expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import { keyboard, render, renderedText, renderHook } from "../renderNative.testkit";
import { NotesBar } from "./NotesBar";
import { type HeaderHiding, nextHeaderHiding, SessionHeader, useHeaderHiding } from "./SessionHeader";
import type { ChipKind, ContextChip } from "./sessionState";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
// The connection status (BoardToolbar's, spec 14) reads the provider.
vi.mock("../ConnectionProvider", () => ({
	useConnection: () => ({ state: "ready", fatal: false, downSince: null, lastLiveAt: null }),
}));

const palette = paletteFor("light");

afterEach(() => {
	keyboard.reset();
	vi.restoreAllMocks();
});

const subagents: ContextChip = {
	kind: "subagents",
	label: "Subagents 3",
	failed: "1 failed",
	attention: false,
	accessibilityLabel: "Subagents, 3, 1 failed",
};
const tasks: ContextChip = {
	kind: "tasks",
	label: "Tasks 2/5",
	attention: false,
	accessibilityLabel: "Tasks, 2 of 5 done",
};
const goal: ContextChip = { kind: "goal", label: "Goal", attention: false, accessibilityLabel: "Goal" };
const blockedGoal: ContextChip = { ...goal, attention: true, accessibilityLabel: "Goal, blocked" };
const queue: ContextChip = {
	kind: "queue",
	label: "Queue 2",
	attention: false,
	accessibilityLabel: "2 queued messages",
};

function header(
	over: {
		status?: string | null;
		chips?: readonly ContextChip[];
		hidden?: boolean;
		composerKeyboard?: boolean;
		onChip?: (kind: ChipKind) => void;
		find?: ReactNode;
		notes?: ReactNode;
		glassTop?: number;
	} = {},
) {
	return (
		<SessionHeader
			status={over.status ?? null}
			chips={over.chips ?? []}
			hidden={over.hidden ?? false}
			composerKeyboard={over.composerKeyboard}
			notes={over.notes}
			glassTop={over.glassTop}
			onChip={over.onChip ?? (() => {})}
			find={over.find}
		/>
	);
}

const chipButtons = (tree: ReactTestRenderer) => tree.root.findAll((node) => node.type === ("Pressable" as never));
const textNode = (tree: ReactTestRenderer, text: string) =>
	tree.root.find((node) => node.type === ("Text" as never) && [node.props.children].flat()[0] === text);
/** The chips row: the element whose transform hides it. */
const chipsRow = (tree: ReactTestRenderer) =>
	tree.root.find((node) => node.type === ("Animated.View" as never) && node.props.onLayout !== undefined);
const translateY = (row: ReactTestInstance) => (row.props.style.transform[0].translateY as { value: number }).value;

async function flushReduceMotion() {
	await act(async () => {
		await Promise.resolve();
	});
}

describe("the connection bar (spec 8.1, 14)", () => {
	it("shows each status text, and nothing when null", () => {
		for (const status of ["Reconnecting…", "Offline · updated 3m ago", "Update needed"]) {
			expect(renderedText(render(header({ status, chips: [goal] })))).toContain(status);
		}
		expect(renderedText(render(header({ chips: [goal] })))).toBe("Goal");
	});

	it("is 13/18 low ink, centered, in tabular figures, 24pt tall and never hides", () => {
		const tree = render(header({ status: "Reconnecting…", chips: [goal], hidden: true }));
		const text = textNode(tree, "Reconnecting…");
		expect(text.props.style).toMatchObject({
			fontSize: 13,
			lineHeight: 18,
			color: palette.inkLow,
			textAlign: "center",
			fontVariant: ["tabular-nums"],
		});
		const bar = text.parent;
		expect(bar?.props.style).toMatchObject({ minHeight: 24, backgroundColor: palette.page });
		// The bar sits outside the chips row, so hiding the row leaves it alone.
		expect(chipsRow(tree).findAll((node) => node === text)).toEqual([]);
	});

	it("offers nothing to press", () => {
		const tree = render(header({ status: "Offline · updated 3m ago" }));
		expect(chipButtons(tree)).toEqual([]);
		expect(tree.root.findAll((node) => node.props.accessibilityRole === "button")).toEqual([]);
	});

	it("explains Update needed with a hint", () => {
		const tree = render(header({ status: "Update needed" }));
		expect(textNode(tree, "Update needed").props.accessibilityHint).toBe(
			"This app and the hub need compatible versions. Update the app from TestFlight, or update Evener on the hub.",
		);
		expect(
			textNode(render(header({ status: "Reconnecting…" })), "Reconnecting…").props.accessibilityHint,
		).toBeUndefined();
	});

	it("leaves the connection status unannounced: it is ambient and changes often (#2903)", () => {
		const announce = vi.mocked(AccessibilityInfo.announceForAccessibility);
		announce.mockClear();
		const tree = render(header({ status: "Reconnecting…", chips: [goal] }));
		act(() => tree.update(header({ status: "Offline · updated 3m ago", chips: [goal] })));
		expect(announce).not.toHaveBeenCalled();
	});
});

describe("the chips row (spec 8.1)", () => {
	it("renders each chip's label with its symbol, and a failure count in danger ink", () => {
		const tree = render(header({ chips: [subagents, tasks, goal, queue] }));
		expect(renderedText(tree)).toBe("Subagents 3  ·  1 failed Tasks 2/5 Goal Queue 2");
		expect(tree.root.findAllByType("SymbolView" as never).map((node) => node.props.name)).toEqual([
			"person.2",
			"checklist",
			"target",
			"tray",
		]);
		expect(textNode(tree, "1 failed").props.style).toMatchObject({ color: palette.dangerInk });
		expect(textNode(tree, "Tasks 2/5").props.style).toMatchObject({
			fontSize: 15,
			lineHeight: 20,
			color: palette.inkHi,
			fontVariant: ["tabular-nums"],
		});
	});

	it("draws Files with doc.text, and a blue dot after its label when a document is new or changed", () => {
		const files: ContextChip = {
			kind: "files",
			label: "Files 4",
			attention: false,
			dot: false,
			accessibilityLabel: "Files, 4",
		};
		const symbols = (chip: ContextChip) =>
			render(header({ chips: [chip] }))
				.root.findAllByType("SymbolView" as never)
				.map((node) => node.props);
		expect(symbols(files).map((props) => props.name)).toEqual(["doc.text"]);
		const dotted = symbols({ ...files, dot: true });
		expect(dotted.map((props) => props.name)).toEqual(["doc.text", "circle.fill"]);
		expect(dotted[1]).toMatchObject({ size: 8, tintColor: palette.accent });
	});

	it("draws each chip as a 32pt capsule with a 44pt hit area, inset fill and an edge border", () => {
		const [chip] = chipButtons(render(header({ chips: [tasks] })));
		expect(chip?.props.style({ pressed: false })).toMatchObject({
			height: 32,
			borderRadius: 16,
			borderWidth: 1,
			borderColor: palette.edge,
			backgroundColor: palette.inset,
		});
		expect(chip?.props.hitSlop).toEqual({ top: 6, bottom: 6 });
		expect(chip?.findByType("SymbolView" as never).props).toMatchObject({ size: 13, tintColor: palette.inkMid });
	});

	it("draws a blocked goal in attention ink over the attention fill", () => {
		const tree = render(header({ chips: [blockedGoal] }));
		const [chip] = chipButtons(tree);
		expect(chip?.props.style({ pressed: false })).toMatchObject({ backgroundColor: palette.attentionBg });
		expect(textNode(tree, "Goal").props.style).toMatchObject({ color: palette.attentionInk });
		expect(chip?.findByType("SymbolView" as never).props.tintColor).toBe(palette.attentionInk);
	});

	it("labels each chip for VoiceOver and calls onChip with its kind", () => {
		const onChip = vi.fn();
		const tree = render(header({ chips: [subagents, tasks, goal, queue], onChip }));
		const chips = chipButtons(tree);
		expect(chips.map((chip) => chip.props.accessibilityLabel)).toEqual([
			"Subagents, 3, 1 failed",
			"Tasks, 2 of 5 done",
			"Goal",
			"2 queued messages",
		]);
		expect(chips.map((chip) => chip.props.accessibilityRole)).toEqual(["button", "button", "button", "button"]);
		for (const chip of chips) act(() => chip.props.onPress());
		expect(onChip.mock.calls.map(([kind]) => kind)).toEqual(["subagents", "tasks", "goal", "queue"]);
	});

	it("scrolls sideways with 16pt sides and an 8pt gap, fading its trailing edge only when it overflows", () => {
		const tree = render(header({ chips: [subagents, tasks, goal, queue] }));
		const scroll = tree.root.findByType("ScrollView" as never);
		expect(scroll.props.horizontal).toBe(true);
		expect(scroll.props.contentContainerStyle).toMatchObject({ paddingHorizontal: 16, gap: 8 });
		const fade = () => tree.root.findAll((node) => node.props.testID === "chips-fade");
		expect(fade()).toEqual([]);
		act(() => {
			scroll.props.onLayout({ nativeEvent: { layout: { width: 390, height: 48 } } });
			scroll.props.onContentSizeChange(300, 48);
		});
		expect(fade()).toEqual([]);
		act(() => scroll.props.onContentSizeChange(520, 48));
		expect(fade()).toHaveLength(1);
		expect(fade()[0]?.props.pointerEvents).toBe("none");
		expect(fade()[0]?.props.style.experimental_backgroundImage).toBe(
			`linear-gradient(to right, ${palette.page}00, ${palette.page})`,
		);
	});
});

describe("the find bar in the chips' place (spec 8.7)", () => {
	it("replaces the chips while find is open", () => {
		const tree = render(header({ chips: [goal, tasks], find: <FindStandIn /> }));
		expect(chipButtons(tree)).toEqual([]);
		expect(tree.root.findAll((node) => node.props.testID === "find-stand-in")).toHaveLength(1);
	});

	it("shows even when the session has no chips", () => {
		const tree = render(header({ find: <FindStandIn /> }));
		expect(tree.root.findAll((node) => node.props.testID === "find-stand-in")).toHaveLength(1);
	});

	it("never slides away while you scroll through matches", async () => {
		const tree = render(header({ chips: [goal], find: <FindStandIn />, hidden: true }));
		await flushReduceMotion();
		act(() => chipsRow(tree).props.onLayout({ nativeEvent: { layout: { width: 390, height: 52, x: 0, y: 0 } } }));
		expect(translateY(chipsRow(tree))).toBe(0);
	});
});

function FindStandIn() {
	return <Text testID="find-stand-in">Find</Text>;
}

describe("hiding on scroll (spec 8.1)", () => {
	function measured(tree: ReactTestRenderer) {
		act(() => chipsRow(tree).props.onLayout({ nativeEvent: { layout: { width: 390, height: 48, x: 0, y: 24 } } }));
	}

	it("slides the chips row up by its height over 200ms, and back", async () => {
		const timing = vi.spyOn(Animated, "timing");
		const tree = render(header({ status: "Reconnecting…", chips: [goal] }));
		await flushReduceMotion();
		measured(tree);
		expect(translateY(chipsRow(tree))).toBe(0);
		act(() => tree.update(header({ status: "Reconnecting…", chips: [goal], hidden: true })));
		expect(timing).toHaveBeenLastCalledWith(
			expect.anything(),
			expect.objectContaining({ toValue: -48, duration: 200 }),
		);
		expect(translateY(chipsRow(tree))).toBe(-48);
		act(() => tree.update(header({ status: "Reconnecting…", chips: [goal] })));
		expect(timing).toHaveBeenLastCalledWith(expect.anything(), expect.objectContaining({ toValue: 0, duration: 200 }));
		expect(translateY(chipsRow(tree))).toBe(0);
	});

	// Slid away behind the nav bar, the row is out of VoiceOver's reach too,
	// or it would read chips no one can see.
	it("hides the slid-away row from VoiceOver, and gives it back when it returns", async () => {
		const tree = render(header({ chips: [goal] }));
		await flushReduceMotion();
		measured(tree);
		expect(chipsRow(tree).props).toMatchObject({
			accessibilityElementsHidden: false,
			importantForAccessibility: "auto",
		});
		act(() => tree.update(header({ chips: [goal], hidden: true })));
		expect(chipsRow(tree).props).toMatchObject({
			accessibilityElementsHidden: true,
			importantForAccessibility: "no-hide-descendants",
		});
		act(() => tree.update(header({ chips: [goal] })));
		expect(chipsRow(tree).props).toMatchObject({
			accessibilityElementsHidden: false,
			importantForAccessibility: "auto",
		});
	});

	it("keeps the find bar in VoiceOver's reach while hidden, since it never slides away", async () => {
		const tree = render(header({ chips: [goal], find: <FindStandIn />, hidden: true }));
		await flushReduceMotion();
		expect(chipsRow(tree).props).toMatchObject({
			accessibilityElementsHidden: false,
			importantForAccessibility: "auto",
		});
	});

	// While you type in the composer the chips and note step aside too; the
	// header reads the keyboard itself, so the keyboard coming and going
	// re-renders it alone.
	it("slides the row away while the keyboard is up for the composer, and back when it lowers", async () => {
		const tree = render(header({ chips: [goal], composerKeyboard: true }));
		await flushReduceMotion();
		measured(tree);
		act(() => keyboard.show());
		expect(translateY(chipsRow(tree))).toBe(-48);
		act(() => keyboard.hide());
		expect(translateY(chipsRow(tree))).toBe(0);
	});

	it("keeps the row while the keyboard is up for something other than the composer", async () => {
		const tree = render(header({ chips: [goal], composerKeyboard: false }));
		await flushReduceMotion();
		measured(tree);
		act(() => keyboard.show());
		expect(translateY(chipsRow(tree))).toBe(0);
	});

	it("jumps with no animation under Reduce Motion", async () => {
		vi.spyOn(AccessibilityInfo, "isReduceMotionEnabled").mockResolvedValue(true);
		const timing = vi.spyOn(Animated, "timing");
		const tree = render(header({ chips: [goal] }));
		await flushReduceMotion();
		measured(tree);
		act(() => tree.update(header({ chips: [goal], hidden: true })));
		expect(timing).not.toHaveBeenCalled();
		expect(translateY(chipsRow(tree))).toBe(-48);
	});

	/** A scroll offset from the person's drag (a number) or from the app
	 * moving the list itself (reading-position restore, scroll to latest). */
	type Step = number | { programmatic: number };
	const programmatic = (y: number): Step => ({ programmatic: y });
	const apply = (state: HeaderHiding, step: Step) =>
		typeof step === "number" ? nextHeaderHiding(state, step, true) : nextHeaderHiding(state, step.programmatic, false);

	it("hides after more than 8pt dragged down, and shows on any upward scroll or at the top", () => {
		const cases: Array<{ name: string; from: Step[]; to: Step; hidden: boolean }> = [
			{ name: "8pt down stays", from: [0], to: 8, hidden: false },
			{ name: "9pt down hides", from: [0], to: 9, hidden: true },
			{ name: "a step under 8pt after a turn stays", from: [0, 50, 45], to: 49, hidden: false },
			{ name: "small steps add up", from: [0, 50, 45, 49], to: 54, hidden: true },
			{ name: "any upward scroll shows", from: [0, 200], to: 199, hidden: false },
			{ name: "down again after an upward scroll counts from the turn", from: [0, 200, 150], to: 158, hidden: false },
			{ name: "and hides past 8pt from the turn", from: [0, 200, 150], to: 159, hidden: true },
			{ name: "the top shows", from: [0, 200], to: 0, hidden: false },
			{ name: "the bounce above the top shows", from: [0, 200], to: -30, hidden: false },
			{ name: "a programmatic jump down leaves the chips shown", from: [], to: programmatic(5000), hidden: false },
			{ name: "a programmatic jump leaves hidden chips hidden", from: [0, 200], to: programmatic(5000), hidden: true },
			{
				name: "a drag after a programmatic jump counts from where it landed",
				from: [programmatic(5000)],
				to: 5008,
				hidden: false,
			},
			{ name: "and hides past 8pt from there", from: [programmatic(5000)], to: 5009, hidden: true },
			{
				name: "a programmatic move up leaves hidden chips hidden",
				from: [0, 200],
				to: programmatic(100),
				hidden: true,
			},
			{ name: "a programmatic move to the top shows", from: [0, 200], to: programmatic(0), hidden: false },
		];
		for (const { name, from, to, hidden } of cases) {
			let state: HeaderHiding = { hidden: false, lastY: 0, turnY: 0 };
			for (const step of from) state = apply(state, step);
			expect(apply(state, to).hidden, name).toBe(hidden);
		}
	});

	it("useHeaderHiding follows the list's scroll offsets", () => {
		const hook = renderHook(() => useHeaderHiding());
		expect(hook.result.current.hidden).toBe(false);
		act(() => hook.result.current.onScroll(40, false));
		expect(hook.result.current.hidden).toBe(false);
		act(() => hook.result.current.onScroll(50, true));
		expect(hook.result.current.hidden).toBe(true);
		act(() => hook.result.current.onScroll(30, true));
		expect(hook.result.current.hidden).toBe(false);
	});
});

it("renders nothing with no chips and no status", () => {
	expect(render(header()).toJSON()).toBeNull();
});

it("never says Reconnect, Connected or Refresh", () => {
	for (const status of ["Reconnecting…", "Offline · updated 3m ago", "Offline", "Update needed", null]) {
		const tree = render(header({ status, chips: [subagents, tasks, blockedGoal, queue] }));
		const labels = tree.root
			.findAll((node) => typeof node.props.accessibilityLabel === "string")
			.map((node) => node.props.accessibilityLabel as string);
		for (const said of [renderedText(tree), ...labels]) {
			expect(said).not.toMatch(/Reconnect\b|Connected|Refresh/);
		}
	}
});

// Where the nav bar is the system's glass, one glass spans the bar and the
// rows under it (the iOS pattern for a bar with a search field or segmented
// control): the header starts at the screen's top, leaves the bar its room,
// and draws its rows clear on the glass. The glass shrinks back to the bar as
// the rows slide away, moving with them.
describe("under the nav bar's glass (spec 16.3)", () => {
	const glass = (tree: ReactTestRenderer) => tree.root.findAll((node) => String(node.type) === "GlassView");
	const glassSlide = (tree: ReactTestRenderer) => {
		const [layer] = glass(tree);
		if (!layer?.parent) throw new Error("no glass");
		return translateY(layer.parent);
	};
	const barRoom = (tree: ReactTestRenderer) =>
		tree.root.find((node) => node.props.testID === "nav-bar-room").props.style.height;
	const preview = { glyph: "person" as const, text: "Your note" };
	const statusLine = (tree: ReactTestRenderer) => {
		const line = textNode(tree, "Reconnecting…").parent;
		if (!line) throw new Error("no status line");
		return line;
	};
	const measured = (tree: ReactTestRenderer) =>
		act(() => chipsRow(tree).props.onLayout({ nativeEvent: { layout: { width: 390, height: 48, x: 0, y: 64 } } }));

	it("spans one glass from the screen's top through the rows, and draws the rows clear on it", async () => {
		const tree = render(
			header({
				chips: [goal],
				status: "Reconnecting…",
				notes: <NotesBar preview={preview} onPress={() => {}} onGlass />,
				glassTop: 64,
			}),
		);
		await flushReduceMotion();
		expect(glass(tree)).toHaveLength(1);
		expect(glass(tree)[0]?.props.glassEffectStyle).toBe("regular");
		expect(barRoom(tree)).toBe(64);
		const chipsFill = tree.root.findAll((node) => node.props.testID === "chips-row")[0]?.props.style.backgroundColor;
		expect(chipsFill).toBe("transparent");
		expect(statusLine(tree).props.style.backgroundColor).toBe("transparent");
		const note = tree.root.findAll(
			(node) => String(node.type) === "Pressable" && node.props.accessibilityLabel === "Your note",
		)[0];
		expect(note?.props.style({ pressed: false }).backgroundColor).toBe("transparent");
	});

	it("shrinks the glass back to the bar as the rows slide away", async () => {
		const tree = render(header({ chips: [goal], glassTop: 64 }));
		await flushReduceMotion();
		measured(tree);
		expect(glassSlide(tree)).toBe(0);
		act(() => tree.update(header({ chips: [goal], glassTop: 64, hidden: true })));
		expect(glassSlide(tree)).toBe(-48);
		expect(translateY(chipsRow(tree))).toBe(-48);
	});

	it("still draws the glass behind the bar with no rows under it", () => {
		const tree = render(header({ glassTop: 64 }));
		expect(glass(tree)).toHaveLength(1);
		expect(barRoom(tree)).toBe(64);
	});

	// The chips row still fades at its trailing edge on the glass, so a
	// cut-off chip reads as "there's more".
	it("keeps the chips' overflow fade on the glass", () => {
		const tree = render(header({ chips: [goal], glassTop: 64 }));
		const row = tree.root.find((node) => node.props.testID === "chips-row");
		const scroller = row.findAll((node) => node.props.horizontal === true)[0];
		act(() => scroller?.props.onLayout({ nativeEvent: { layout: { width: 300, height: 48, x: 0, y: 0 } } }));
		act(() => scroller?.props.onContentSizeChange(500, 48));
		expect(tree.root.findAll((node) => node.props.testID === "chips-fade")).toHaveLength(1);
	});

	it("clips its sliding rows once, at their own top", () => {
		const tree = render(header({ chips: [goal], glassTop: 64 }));
		const clips = tree.root.findAll((node) => node.props.style?.overflow === "hidden");
		expect(clips).toHaveLength(1);
		expect(clips[0]?.findAll((node) => node.props.testID === "chips-row")).toHaveLength(1);
	});

	it("keeps the opaque page fill, and no glass, without it", () => {
		const tree = render(header({ chips: [goal] }));
		expect(glass(tree)).toEqual([]);
		const chipsFill = tree.root.findAll((node) => node.props.testID === "chips-row")[0]?.props.style.backgroundColor;
		expect(chipsFill).toBe(paletteFor("light").page);
	});
});
