import { parseTaskState } from "@evener/appwire-client";
import { type ToolWireCall, toolWireStep } from "@evener/appwire-client/testing/toolWireFixtures";
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
		// Its trailing newline goes with the shell tool's exit footer.
		expect(stepEvidence(shell)).toEqual([{ kind: "output", text: "ok\nPASS", lines: 2 }]);
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
		expect(
			stepEvidence(step("edit_file", { file_path: "a.go", old_string: "a", new_string: "b" }, { summaryOnly: true })),
		).toEqual([]);
	});

	it("has nothing to show for a step that carries none", () => {
		expect(stepEvidence(step("grep", { pattern: "x" }))).toEqual([]);
		expect(stepEvidence(step("apply_patch", {}))).toEqual([]);
		// An edit whose arguments carry neither side has no diff to draw.
		expect(stepEvidence(step("edit_file", {}))).toEqual([]);
		expect(stepEvidence(step("edit_file", { file_path: "a.go" }))).toEqual([]);
	});
});

// What each core tool's evidence reads as, from what the daemon actually sends
// (agent/testdata/toolwire): the words, not the envelope around them.
describe("each tool's evidence, as the tools print it", () => {
	const real = (call: ToolWireCall) => {
		const item = toolWireStep(call);
		return stepEvidence({
			label: item.toolName ?? "",
			detail: {
				arguments: item.argumentsJSON,
				output: item.output,
				error: item.error,
				exitCode: item.exitCode,
				tasks: parseTaskState(item.raw) ?? undefined,
			},
		});
	};

	it("shows a task_list call's tasks as a checklist, with the note the call added", () => {
		expect(real("call_task_list_done")).toEqual([
			{
				kind: "tasks",
				tasks: [
					{ id: 1, status: "done", description: "Reproduce the settle race", note: "Reproduced in 3 of 20 runs." },
					{ id: 2, status: "in_progress", description: "Order the drain before settle" },
					{ id: 3, status: "open", description: "Run the race detector again" },
				],
			},
		]);
	});

	it("shows only the notes a task_list call added, not ones from before", () => {
		const tasks = real("call_task_list_view")[0];
		expect(tasks?.kind === "tasks" && tasks.tasks.map((task) => task.note)).toEqual([undefined, undefined, undefined]);
	});

	it("shows what task_list printed when the call returned no task list", () => {
		// A daemon from before the list rode the result, or a replayed
		// transcript from then.
		expect(stepEvidence({ label: "task_list", detail: { output: "Updated 1→done." } })).toEqual([
			{ kind: "output", text: "Updated 1→done.", lines: 1 },
		]);
	});

	it("shows a command's output without the shell tool's exit footer", () => {
		expect(real("call_shell")).toEqual([{ kind: "output", text: "package agent", lines: 1 }]);
	});

	it("says a windowed output shows only its start and end", () => {
		const evidence = real("call_shell_windowed");
		expect(evidence.at(-1)).toEqual({ kind: "note", text: "A long output: only its start and end are here" });
	});

	it("says a command whose wait timed out is still running in the background", () => {
		expect(real("call_shell_timeout")).toEqual([
			{ kind: "output", text: "started", lines: 1 },
			{ kind: "note", text: "Still running in the background after its wait timed out" },
		]);
	});

	it("says a directly backgrounded command is still running, with no timeout", () => {
		expect(stepEvidence({ label: "shell", detail: { output: "started\n[running in background as job_x]" } })).toEqual([
			{ kind: "output", text: "started", lines: 1 },
			{ kind: "note", text: "Still running in the background" },
		]);
	});

	it("says a command exited nonzero, when that is all it printed", () => {
		expect(real("call_shell_failed")).toEqual([{ kind: "exit", code: 1 }]);
	});

	it("shows a fetched page's answer, where it came from and its size, not its JSON", () => {
		expect(real("call_web_fetch")).toEqual([
			{
				kind: "page",
				text: "The release notes list three fixes to the tree settle pass.",
				url: "https://example.com/release-notes",
				bytes: 48213,
			},
		]);
	});

	it("shows the instructions a skill loaded, as markdown", () => {
		expect(real("call_use_skill")).toEqual([
			{
				kind: "markdown",
				title: "systematic-debugging",
				markdown: "# Systematic debugging\n\nFind the root cause first.\n",
			},
		]);
	});

	// A skill's markdown is the skill author's, so its images never load a
	// remote URL on the phone: each reads as its alt text.
	it("shows a skill's images as their alt text, never loading them", () => {
		const loaded = `<skill-context>\n${JSON.stringify({
			name: "diagrams",
			instructions:
				"# Diagrams\n\n![the flow](https://example.com/flow.png)\n\nThen ![](https://t.test/x.gif) done.\n\n![by ref][logo] and ![short]\n\n[logo]: https://t.test/logo.png",
		})}\n</skill-context>`;
		expect(stepEvidence({ label: "use_skill", detail: { output: loaded } })).toEqual([
			{
				kind: "markdown",
				title: "diagrams",
				markdown: "# Diagrams\n\nthe flow\n\nThen  done.\n\nby ref and short\n\n[logo]: https://t.test/logo.png",
			},
		]);
	});

	// -1 is the shell tool's sentinel for a command stopped by a signal or by
	// evener's runtime limit, not an exit code, so it reads as no exit at all.
	it("never says a command exited -1", () => {
		expect(stepEvidence({ label: "shell", detail: { output: "partial\n[exit -1]", exitCode: -1 } })).toEqual([
			{ kind: "output", text: "partial", lines: 1 },
		]);
	});

	it("shows arguments that aren't a JSON object as they were sent", () => {
		expect(stepEvidence({ label: "github__search", detail: { arguments: "plain words", output: "" } })).toEqual([
			{ kind: "output", text: "plain words", lines: 1 },
		]);
	});

	it("pretty-prints an MCP tool's arguments and result", () => {
		expect(real("call_mcp")).toEqual([
			{
				kind: "json",
				label: "Arguments",
				text: '{\n  "body": "Seen in go test -race.",\n  "title": "Tree settle races the drain"\n}',
			},
			{
				kind: "json",
				label: "Result",
				text: '{\n  "number": 3210,\n  "url": "https://github.com/prime-radiant-inc/evener/issues/3210"\n}',
			},
		]);
	});

	it("shows an uncovered tool's arguments, and its output as it printed it", () => {
		expect(real("call_unknown")).toEqual([
			{ kind: "json", label: "Arguments", text: '{\n  "note_to_self": "Next: run the race detector."\n}' },
			{ kind: "output", text: "compacted", lines: 1 },
		]);
	});

	it("leaves a read's, a search's and a listing's output as the tool printed it", () => {
		for (const call of ["call_read_file", "call_grep", "call_list_dir", "call_web_search"] as const) {
			const output = toolWireStep(call).output ?? "";
			expect(real(call)).toEqual([{ kind: "output", text: output, lines: expect.any(Number) }]);
		}
	});
});
