// The question dock as a person sees it (spec 8.4): which question it is on,
// the options in order, moving between questions, folding, and what reaches
// onSend. Its saved answers live in the device's drafts, doubled here in
// memory.
import type { AskQuestionRef } from "@evener/appwire-client";
import { useState } from "react";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DraftDestination } from "../draftRepository";
import type { QuestionSelections } from "../questionAnswers";
import { AccessibilityInfo } from "react-native";
import {
	composerFocusedAs,
	dockBody,
	keyboard,
	pressable,
	render,
	renderedText,
	renderHook,
	textOf,
} from "../renderNative.testkit";
import { QuestionDock } from "./QuestionDock";
import { useQuestionDraft } from "./useQuestionDraft";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

// The device's question drafts, in memory: the same four calls
// draftRepository.ts makes against SQLite.
const saved = vi.hoisted(() => ({
	questions: new Map<string, QuestionSelections>(),
	positions: new Map<string, string>(),
	// The device's drafts can't be read, or can't be written, while set.
	readsFail: false,
	writesFail: false,
}));
vi.mock("../nativeDrafts", () => {
	const at = (destination: DraftDestination) => `${destination.hubId}\u0000${destination.sessionRef}`;
	return {
		nativeDrafts: () => ({
			readQuestions: (destination: DraftDestination) => {
				if (saved.readsFail) throw new Error("database is locked");
				return saved.questions.get(at(destination)) ?? {};
			},
			writeQuestions: (destination: DraftDestination, _signature: string, selections: QuestionSelections) => {
				if (saved.writesFail) throw new Error("disk full");
				saved.questions.set(at(destination), selections);
			},
			readQuestionPosition: (destination: DraftDestination, keys: string[]) => {
				const key = saved.positions.get(at(destination));
				return key && keys.includes(key) ? key : keys[0];
			},
			writeQuestionPosition: (destination: DraftDestination, key: string) =>
				void saved.positions.set(at(destination), key),
		}),
	};
});

beforeEach(() => {
	saved.questions.clear();
	saved.positions.clear();
	saved.readsFail = false;
	saved.writesFail = false;
});

const destination = { hubId: "hub-1", sessionRef: "local:s1" };

const question = (key: string, header: string, labels: string[], recommended?: string): AskQuestionRef => ({
	key,
	callId: "call-1",
	header,
	question: `${header}?`,
	why: `Why ${header.toLowerCase()}`,
	options: labels.map((label) => ({
		label,
		detail: `About ${label.toLowerCase()}`,
		...(label === recommended ? { recommended: true } : {}),
	})),
	multiSelect: false,
});

const two = [question("q1", "Flags", ["Keep them", "Drop them"]), question("q2", "Tests", ["Run them", "Skip them"])];

function mount(
	questions: AskQuestionRef[],
	{
		ready = true,
		folded = false,
		error = null,
		composerFocused = false,
	}: { ready?: boolean; folded?: boolean; error?: string | null; composerFocused?: boolean } = {},
) {
	const composerFocus = composerFocusedAs(composerFocused);
	const onSend = vi.fn<(selections: QuestionSelections) => void>();
	const onFold = vi.fn<(folded: boolean) => void>();
	const onOtherAnswer = vi.fn();
	function Dock() {
		const draft = useQuestionDraft(destination, questions);
		const [isFolded, setFolded] = useState(folded);
		return (
			<QuestionDock
				questions={questions}
				draft={draft}
				ready={ready}
				sending={false}
				folded={isFolded}
				onFold={(next) => {
					onFold(next);
					setFolded(next);
				}}
				onOtherAnswer={onOtherAnswer}
				onSend={onSend}
				error={error}
				composerFocus={composerFocus}
			/>
		);
	}
	const tree = render(<Dock />);
	return { tree, onSend, onFold, onOtherAnswer };
}

function press(tree: ReactTestRenderer, label: string) {
	const target = pressable(tree, label);
	if (!target) throw new Error(`no pressable labelled ${label}`);
	act(() => target.props.onPress());
}

function optionLabels(tree: ReactTestRenderer) {
	return tree.root
		.findAll((node) => String(node.type) === "Pressable" && node.props.accessibilityRole === "radio")
		.map((node) => node.props.accessibilityLabel);
}

describe("the question dock (spec 8.4)", () => {
	it("says which question it is on, with its why", () => {
		const { tree } = mount(two);
		const text = renderedText(tree);
		expect(text).toContain("Question 1 of 2");
		expect(text).toContain("Flags?");
		expect(text).toContain("Why flags");
		expect(pressable(tree, "Previous question")).toBeUndefined();
	});

	it("lists the recommended option first, says so, and starts with it chosen", () => {
		const { tree } = mount([question("q1", "Flags", ["Keep them", "Drop them"], "Drop them")]);
		expect(optionLabels(tree)).toEqual(["Drop them, Recommended", "Keep them"]);
		expect(renderedText(tree)).toContain("· Recommended");
		expect(pressable(tree, "Drop them, Recommended")?.props.accessibilityState).toMatchObject({ checked: true });
		expect(renderedText(tree)).toContain("Send answer");
	});

	it("reads each option's recommendation and detail to VoiceOver", () => {
		const { tree } = mount([question("q1", "Flags", ["Keep them", "Drop them"], "Drop them")]);
		expect(pressable(tree, "Drop them, Recommended")?.props.accessibilityHint).toBe("About drop them");
		expect(pressable(tree, "Keep them")?.props.accessibilityHint).toBe("About keep them");
	});

	it("keys options by where the agent offered them, so a repeated label renders without a duplicate-key report", () => {
		const errors: string[] = [];
		const spy = vi.spyOn(console, "error").mockImplementation((...args) => {
			errors.push(args.map(String).join(" "));
		});
		try {
			const { tree } = mount([question("q1", "Flags", ["Keep them", "Keep them"])]);
			expect(optionLabels(tree)).toEqual(["Keep them", "Keep them"]);
		} finally {
			spy.mockRestore();
		}
		expect(errors.filter((line) => /same key/.test(line))).toEqual([]);
	});

	it("moves to the next question when you choose, and back with Previous question", () => {
		const { tree } = mount(two);
		press(tree, "Keep them");
		expect(renderedText(tree)).toContain("Question 2 of 2");
		press(tree, "Previous question");
		expect(renderedText(tree)).toContain("Question 1 of 2");
		expect(pressable(tree, "Keep them")?.props.accessibilityState).toMatchObject({ checked: true });
	});

	it("says Next question, then Send answers, and sends both answers", () => {
		const { tree, onSend } = mount(two);
		expect(pressable(tree, "Next question")).toBeDefined();
		press(tree, "Next question");
		expect(renderedText(tree)).toContain("Question 2 of 2");
		press(tree, "Skip them");
		// Answering the last question returns to the one still unanswered.
		expect(renderedText(tree)).toContain("Question 1 of 2");
		press(tree, "Drop them");
		expect(pressable(tree, "Send answers")).toBeUndefined();
		press(tree, "Next question");
		press(tree, "Send answers");
		expect(onSend).toHaveBeenCalledWith({
			q1: { note: "", resolution: { kind: "option", labels: ["Drop them"] } },
			q2: { note: "", resolution: { kind: "option", labels: ["Skip them"] } },
		});
	});

	it("folds, and folded says how many questions wait", () => {
		const open = mount(two);
		press(open.tree, "Fold");
		expect(open.onFold).toHaveBeenCalledWith(true);
		const folded = mount(two, { folded: true });
		expect(renderedText(folded.tree)).toContain("Answer 2 questions");
		expect(renderedText(folded.tree)).not.toContain("Question 1 of 2");
		press(folded.tree, "Answer 2 questions");
		expect(folded.onFold).toHaveBeenCalledWith(false);
	});

	it("says why an answer didn't go, folded too", () => {
		const error = "Could not confirm delivery. Your answers are retained; check delivery before trying again.";
		const open = mount(two, { error });
		expect(renderedText(open.tree)).toContain(error);
		const folded = mount(two, { folded: true, error });
		expect(renderedText(folded.tree)).toContain(error);
		expect(renderedText(folded.tree)).toContain("Answer 2 questions");
	});

	it("counts every question on the folded bar while each has only its recommendation", () => {
		const seeded = [
			question("q1", "Flags", ["Keep them", "Drop them"], "Drop them"),
			question("q2", "Tests", ["Run them", "Skip them"], "Run them"),
		];
		const { tree } = mount(seeded, { folded: true });
		expect(renderedText(tree)).toContain("Answer 2 questions");
	});

	it("hands Other answer… to the composer", () => {
		const { tree, onOtherAnswer } = mount(two);
		press(tree, "Other answer…");
		expect(onOtherAnswer).toHaveBeenCalledTimes(1);
	});

	it("sends nothing while it isn't ready", () => {
		const { tree, onSend } = mount([question("q1", "Flags", ["Keep them", "Drop them"], "Drop them")], {
			ready: false,
		});
		const send = pressable(tree, "Send answer");
		expect(send?.props.accessibilityState).toMatchObject({ disabled: true });
		expect(pressable(tree, "Drop them, Recommended")?.props.accessibilityState).toMatchObject({ disabled: true });
		act(() => send?.props.onPress());
		expect(onSend).not.toHaveBeenCalled();
	});
});

describe("a question taller than the room the screen gives the dock", () => {
	it("scrolls the question and its options, while the header and the answer controls stay put", () => {
		const { tree } = mount(two);
		const body = dockBody(tree, "question-dock");
		const scrolled = textOf(body.scroller);
		expect(scrolled).toContain("Flags?");
		expect(scrolled).toContain("Why flags");
		expect(scrolled).toContain("About drop them");
		expect(scrolled).not.toContain("Question 1 of 2");
		expect(body.holds("Keep them")).toBe(true);
		for (const label of ["Fold", "Other answer…", "Next question"]) expect(body.holds(label)).toBe(false);
	});

	it("starts the next question at its top, wherever the last one was scrolled to", () => {
		const { tree } = mount(two);
		const scroller = () => tree.root.findAll((node) => String(node.type) === "ScrollView")[0];
		const first = scroller();
		press(tree, "Next question");
		expect(renderedText(tree)).toContain("Tests?");
		// A new scroller, so no offset carries over from the question before.
		expect(scroller()).not.toBe(first);
	});
});

describe("while you type your own answer (spec 8.4, Other answer…)", () => {
	// The composer is back under the dock and the keyboard is up for it.
	function typing() {
		const mounted = mount(two, { composerFocused: true });
		act(() => keyboard.show());
		return mounted;
	}
	afterEach(() => keyboard.reset());

	it("shows only the header and the scrolling question, with no options or answer controls", () => {
		const { tree } = typing();
		const body = dockBody(tree, "question-dock");
		expect(textOf(body.scroller)).toContain("Flags?");
		expect(textOf(body.scroller)).toContain("Why flags");
		expect(renderedText(tree)).toContain("Question 1 of 2");
		expect(pressable(tree, "Fold")).toBeDefined();
		expect(optionLabels(tree)).toEqual([]);
		for (const label of ["Other answer…", "Next question", "Send answers"])
			expect(pressable(tree, label)).toBeUndefined();
	});

	it("offers Show options, which lowers the keyboard, brings the options back and says so", () => {
		const { tree } = typing();
		expect(pressable(tree, "Show options")?.props.accessibilityHint).toBe("Hides the keyboard");
		const announced = vi.mocked(AccessibilityInfo.announceForAccessibility);
		announced.mockClear();
		press(tree, "Show options");
		expect(optionLabels(tree)).toEqual(["Keep them", "Drop them"]);
		expect(pressable(tree, "Show options")).toBeUndefined();
		expect(announced).toHaveBeenCalledWith("Options shown");
	});

	it("says nothing about options when you fold the dock while typing", () => {
		const { tree } = typing();
		const announced = vi.mocked(AccessibilityInfo.announceForAccessibility);
		announced.mockClear();
		press(tree, "Fold");
		expect(announced).not.toHaveBeenCalledWith("Options shown");
	});

	it("says nothing about options when the keyboard goes down on a folded dock", () => {
		const { tree } = typing();
		press(tree, "Fold");
		const announced = vi.mocked(AccessibilityInfo.announceForAccessibility);
		announced.mockClear();
		act(() => keyboard.hide());
		expect(renderedText(tree)).toContain("Answer 2 questions");
		expect(announced).not.toHaveBeenCalledWith("Options shown");
	});

	it("says Options shown once when the keyboard goes down on a dock unfolded while typing", () => {
		const { tree } = typing();
		press(tree, "Fold");
		press(tree, "Answer 2 questions");
		const announced = vi.mocked(AccessibilityInfo.announceForAccessibility);
		announced.mockClear();
		act(() => keyboard.hide());
		expect(announced.mock.calls.filter(([words]) => words === "Options shown")).toHaveLength(1);
	});

	it("says nothing about options when you unfold with the keyboard down", () => {
		const { tree } = mount(two, { composerFocused: true, folded: true });
		const announced = vi.mocked(AccessibilityInfo.announceForAccessibility);
		announced.mockClear();
		press(tree, "Answer 2 questions");
		expect(optionLabels(tree)).toEqual(["Keep them", "Drop them"]);
		expect(announced).not.toHaveBeenCalledWith("Options shown");
	});

	it("keeps the options while the keyboard is up for something else, or down under the composer", () => {
		const other = mount(two);
		act(() => keyboard.show());
		expect(optionLabels(other.tree)).toEqual(["Keep them", "Drop them"]);
		act(() => keyboard.hide());
		expect(optionLabels(mount(two, { composerFocused: true }).tree)).toEqual(["Keep them", "Drop them"]);
	});

	it("opens already typing when the keyboard was up before the dock mounted", () => {
		act(() => keyboard.show());
		expect(optionLabels(mount(two, { composerFocused: true }).tree)).toEqual([]);
	});

	it("has no Show options while the options are showing", () => {
		expect(pressable(mount(two).tree, "Show options")).toBeUndefined();
	});
});

describe("saved answers that couldn't be read", () => {
	it("reads them again when the folded bar opens, and the dock can answer again", () => {
		saved.questions.set(`${destination.hubId}\u0000${destination.sessionRef}`, {
			q1: { note: "", resolution: { kind: "option", labels: ["Drop them"] } },
		});
		saved.readsFail = true;
		const { tree } = mount(two, { folded: true });
		saved.readsFail = false;
		press(tree, "Answer 2 questions");
		expect(pressable(tree, "Keep them")?.props.accessibilityState).toMatchObject({ disabled: false });
		expect(pressable(tree, "Drop them")?.props.accessibilityState).toMatchObject({ checked: true });
	});
});

describe("useQuestionDraft", () => {
	it("reloads saved answers that couldn't be read", () => {
		saved.questions.set(`${destination.hubId}\u0000${destination.sessionRef}`, {
			q2: { note: "", resolution: { kind: "option", labels: ["Run them"] } },
		});
		saved.readsFail = true;
		const draft = renderHook(() => useQuestionDraft(destination, two));
		expect(draft.result.current).toMatchObject({ loaded: false, error: "Saved answers could not be loaded." });
		// Still unreadable: it stays unloaded, and says so.
		act(() => draft.result.current.reload());
		expect(draft.result.current.loaded).toBe(false);
		saved.readsFail = false;
		act(() => draft.result.current.reload());
		expect(draft.result.current).toMatchObject({
			loaded: true,
			error: null,
			selections: { q2: { note: "", resolution: { kind: "option", labels: ["Run them"] } } },
		});
	});

	it("keeps answers it couldn't save when asked to reload, and saves them on the next change", () => {
		const draft = renderHook(() => useQuestionDraft(destination, two));
		saved.writesFail = true;
		act(() =>
			draft.result.current.setSelections((values) => ({
				...values,
				q1: { note: "", resolution: { kind: "option", labels: ["Keep them"] } },
			})),
		);
		expect(draft.result.current.error).not.toBeNull();
		act(() => draft.result.current.reload());
		expect(draft.result.current.selections.q1?.resolution).toEqual({ kind: "option", labels: ["Keep them"] });
		saved.writesFail = false;
		act(() =>
			draft.result.current.setSelections((values) => ({
				...values,
				q2: { note: "", resolution: { kind: "option", labels: ["Skip them"] } },
			})),
		);
		expect(draft.result.current.error).toBeNull();
		expect(saved.questions.get(`${destination.hubId}\u0000${destination.sessionRef}`)).toMatchObject({
			q1: { resolution: { kind: "option", labels: ["Keep them"] } },
			q2: { resolution: { kind: "option", labels: ["Skip them"] } },
		});
	});

	it("keeps a chosen answer and your place across a remount", () => {
		const first = renderHook(() => useQuestionDraft(destination, two));
		act(() => {
			first.result.current.setSelections((values) => ({
				...values,
				q2: { note: "", resolution: { kind: "option", labels: ["Run them"] } },
			}));
			first.result.current.setActiveIndex(1);
		});
		first.unmount();
		const second = renderHook(() => useQuestionDraft(destination, two));
		expect(second.result.current).toMatchObject({
			loaded: true,
			error: null,
			activeIndex: 1,
			selections: { q2: { note: "", resolution: { kind: "option", labels: ["Run them"] } } },
		});
	});
});
