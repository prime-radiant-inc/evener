import type { AskQuestionRef, SandboxEscalationRequested } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import { answerWithText, approvalCard, foldedLabel, orderedOptions, primaryLabel, questionHeader } from "./askDockCopy";

const question = (key: string, header: string): AskQuestionRef => ({
	key,
	callId: `call-${key}`,
	header,
	question: `${header}?`,
	options: [
		{ label: "Keep them", detail: "Add the flags" },
		{ label: "Drop them", detail: "Remove the options", recommended: true },
	],
	multiSelect: false,
});

describe("the question dock's words (spec 8.4)", () => {
	it("numbers questions only when there are several", () => {
		expect(questionHeader(0, 2)).toBe("Question 1 of 2");
		expect(questionHeader(1, 2)).toBe("Question 2 of 2");
		expect(questionHeader(0, 1)).toBe("Question");
	});

	it("says Next question until the last, then Send answer or Send answers", () => {
		expect(primaryLabel(1, 2)).toBe("Next question");
		expect(primaryLabel(undefined, 2)).toBe("Send answers");
		expect(primaryLabel(undefined, 1)).toBe("Send answer");
	});

	it("folds to a bar that says what's left", () => {
		expect(foldedLabel(2)).toBe("Answer 2 questions");
		expect(foldedLabel(1)).toBe("Answer the question");
	});

	it("lists the recommended option first and keeps the agent's order otherwise", () => {
		expect(orderedOptions(question("q1", "Implied options").options).map((option) => option.label)).toEqual([
			"Drop them",
			"Keep them",
		]);
	});
});

describe("what your text answers (ruling 14)", () => {
	it("answers the only question and composes the reply", () => {
		const result = answerWithText([question("q1", "Implied options")], {}, 0, "  Drop them  ");
		expect(result.nextIndex).toBeUndefined();
		expect(result.message).toBe('[answers]\n1. [Implied options] → free text: "Drop them"');
	});

	it("answers one of two, keeps the other's answer, and returns to the unanswered one", () => {
		const questions = [question("q1", "First"), question("q2", "Second")];
		const partial = answerWithText(questions, {}, 0, "my own answer");
		expect(partial.message).toBeNull();
		expect(partial.nextIndex).toBe(1);
		const finished = answerWithText(questions, partial.selections, 1, "second answer");
		expect(finished.message).toBe(
			'[answers]\n1. [First] → free text: "my own answer"\n2. [Second] → free text: "second answer"',
		);
	});

	it("ignores blank text", () => {
		const result = answerWithText([question("q1", "Only")], {}, 0, "   ");
		expect(result).toEqual({ selections: {}, message: null, nextIndex: 0 });
	});
});

describe("the approval's words (spec 8.4, ruling 12)", () => {
	const request = (over: Partial<SandboxEscalationRequested> = {}): SandboxEscalationRequested => ({
		threadId: "thread-1",
		ref: "local:s1",
		escalationId: "esc-1",
		mode: "workspace-write",
		tool: "write_file",
		kind: "file_tool",
		deniedPath: "/Users/jesse/sites/docs/index.html",
		...over,
	});

	it("names a write outside the workspace and grants the one file", () => {
		expect(approvalCard(request())).toEqual({
			wants: "Wants to write outside the workspace",
			tool: "write_file",
			target: "/Users/jesse/sites/docs/index.html",
			scope: "This session can only write inside its project folder.",
			partiallyRan: false,
			primary: { label: "Allow this file only", detail: "It will ask again for the next one" },
		});
	});

	it("names a read, and a write in a read-only session", () => {
		expect(approvalCard(request({ tool: "read_file" }))).toMatchObject({
			wants: "Wants to read outside the workspace",
			scope: "This session can only read inside its project folder.",
		});
		expect(approvalCard(request({ mode: "read-only", tool: "edit_file" }))).toMatchObject({
			wants: "Wants to write a file",
			scope: "This session is read-only.",
		});
	});

	it("allows once when the hub couldn't name the path, and says when part of it ran", () => {
		expect(approvalCard(request({ deniedPath: "<denied>", partiallyRan: true }))).toMatchObject({
			target: "",
			partiallyRan: true,
			primary: { label: "Allow once", detail: "Just this action" },
		});
	});
});
