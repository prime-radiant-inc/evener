import { describe, expect, it } from "vitest";
import type { RunStep } from "../timeline";
import { stepEvidence } from "./evidence";

function step(label: string, args: Record<string, unknown>, over: Partial<RunStep> = {}): RunStep {
	return {
		kind: "activity",
		id: `${label}-1`,
		label,
		family: "tool",
		state: "completed",
		detail: { arguments: JSON.stringify(args) },
		...over,
	};
}

describe("what a step has to show (spec 8.2)", () => {
	it("draws an edit as a diff, with its counts", () => {
		expect(stepEvidence(step("edit_file", { file_path: "a.go", old_string: "one\ntwo", new_string: "uno" }))).toEqual([
			{ kind: "diff", text: "--- a.go\n+++ a.go\n-one\n-two\n+uno", added: 1, removed: 2 },
		]);
	});

	it("draws a patch as its own diff", () => {
		const patch = "*** Update File: a.go\n@@\n context\n+new\n-old";
		expect(stepEvidence(step("apply_patch", { patch }))).toEqual([{ kind: "diff", text: patch, added: 1, removed: 1 }]);
	});

	it("says which file a write wrote, since the wire carries no prior content to diff", () => {
		expect(stepEvidence(step("write_file", { file_path: "notes.md", content: "hi" }))).toEqual([
			{ kind: "wrote", path: "notes.md" },
		]);
	});

	it("leaves out a file tool's confirmation line, which only repeats its diff", () => {
		const edit = step("edit_file", { file_path: "a.go", old_string: "a", new_string: "b" }, {});
		edit.detail = { ...edit.detail, output: "edited a.go: 1 replacement(s)" };
		expect(stepEvidence(edit).map((evidence) => evidence.kind)).toEqual(["diff"]);
	});

	it("shows a command's output with its line count", () => {
		const shell = step("shell", { command: "go test" });
		shell.detail = { ...shell.detail, output: "ok\nPASS\n" };
		expect(stepEvidence(shell)).toEqual([{ kind: "output", text: "ok\nPASS\n", lines: 2 }]);
	});

	it("shows a failed step's error with its exit code", () => {
		const failed = step("shell", { command: "go test" }, { state: "failed" });
		failed.detail = { ...failed.detail, output: "FAIL", error: "exit status 1", exitCode: 1 };
		expect(stepEvidence(failed)).toEqual([
			{ kind: "output", text: "FAIL", lines: 1 },
			{ kind: "error", text: "exit status 1", exitCode: 1 },
		]);
	});

	it("has nothing to show for a summary-only step", () => {
		expect(stepEvidence(step("edit_file", { file_path: "a.go", old_string: "a", new_string: "b" }, { summaryOnly: true }))).toEqual([]);
	});

	it("has nothing to show for a step that carries none", () => {
		expect(stepEvidence(step("grep", { pattern: "x" }))).toEqual([]);
		expect(stepEvidence(step("apply_patch", {}))).toEqual([]);
		// An edit whose arguments carry neither side has no diff to draw.
		expect(stepEvidence(step("edit_file", {}))).toEqual([]);
		expect(stepEvidence(step("edit_file", { file_path: "a.go" }))).toEqual([]);
	});
});
