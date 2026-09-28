// The question dock as a person sees it (spec 8.4): which question it is on,
// the options in order, moving between questions, folding, and what reaches
// onSend. Its saved answers live in the device's drafts, doubled here in
// memory.
import type { AskQuestionRef } from "@evener/appwire-client";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { DraftDestination } from "../draftRepository";
import type { QuestionSelections } from "../questionAnswers";
import { pressable, render, renderedText, renderHook } from "../renderNative.testkit";
import { QuestionDock } from "./QuestionDock";
import { useQuestionDraft } from "./useQuestionDraft";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

// The device's question drafts, in memory: the same four calls
// draftRepository.ts makes against SQLite.
const saved = vi.hoisted(() => ({
	questions: new Map<string, QuestionSelections>(),
	positions: new Map<string, string>(),
}));
vi.mock("../nativeDrafts", () => {
	const at = (destination: DraftDestination) => `${destination.hubId}\u0000${destination.sessionRef}`;
	return {
		nativeDrafts: () => ({
			readQuestions: (destination: DraftDestination) => saved.questions.get(at(destination)) ?? {},
			writeQuestions: (destination: DraftDestination, _signature: string, selections: QuestionSelections) =>
				void saved.questions.set(at(destination), selections),
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
	{ ready = true, folded = false, error = null }: { ready?: boolean; folded?: boolean; error?: string | null } = {},
) {
	const onSend = vi.fn<(selections: QuestionSelections) => void>();
	const onFold = vi.fn<(folded: boolean) => void>();
	const onOtherAnswer = vi.fn();
	function Dock() {
		const draft = useQuestionDraft(destination, questions);
		return (
			<QuestionDock
				questions={questions}
				draft={draft}
				ready={ready}
				sending={false}
				folded={folded}
				onFold={onFold}
				onOtherAnswer={onOtherAnswer}
				onSend={onSend}
				error={error}
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

describe("useQuestionDraft", () => {
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
