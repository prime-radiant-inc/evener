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
		const numbers = (header: string) => header.match(/\d+/g)?.map(Number) ?? [];
		expect(numbers(questionHeader(0, 2))).toEqual([1, 2]);
		expect(numbers(questionHeader(1, 2))).toEqual([2, 2]);
		expect(numbers(questionHeader(0, 1))).toEqual([]);
	});

	it("says Next question until the last, then Send answer or Send answers", () => {
		// Moving on reads the same however many there are; sending reads one way
		// for one answer and another for several.
		expect(primaryLabel(1, 2)).toBe(primaryLabel(2, 5));
		const labels = [primaryLabel(1, 2), primaryLabel(undefined, 2), primaryLabel(undefined, 1)];
		expect(new Set(labels).size).toBe(labels.length);
		expect(primaryLabel(undefined, 2)).toBe(primaryLabel(undefined, 5));
	});

	it("folds to a bar that says what's left", () => {
		expect(foldedLabel(2)).toContain("2");
		expect(foldedLabel(1)).not.toMatch(/\d/);
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
		expect(approvalCard(request())).toMatchObject({
			tool: "write_file",
			target: "/Users/jesse/sites/docs/index.html",
			partiallyRan: false,
			primary: { kind: "file" },
		});
	});

	it("names a read, and a write in a read-only session, each its own way", () => {
		const write = approvalCard(request());
		const read = approvalCard(request({ tool: "read_file" }));
		const readOnly = approvalCard(request({ mode: "read-only", tool: "edit_file" }));
		expect(new Set([write.wants, read.wants, readOnly.wants]).size).toBe(3);
		expect(new Set([write.scope, read.scope, readOnly.scope]).size).toBe(3);
		// Writing in a workspace session reads the same whichever write tool asks.
		expect(approvalCard(request({ tool: "edit_file" }))).toMatchObject({ wants: write.wants, scope: write.scope });
	});

	it("allows once when the hub couldn't name the path, and says when part of it ran", () => {
		const card = approvalCard(request({ deniedPath: "<denied>", partiallyRan: true }));
		expect(card).toMatchObject({ target: "", partiallyRan: true, primary: { kind: "once" } });
		expect(card.primary.label).not.toBe(approvalCard(request()).primary.label);
	});
});
