// What a step in a run has to show when you tap it (spec 8.2): an edit's
// diff, the file a write wrote, a command's output, a fetched page, the skill
// an activation loaded, a task list, a tool's arguments and result, and an
// error. Each
// reads the words the tool printed, not the envelope around them (the
// package's toolEvidence readers, which the web's bodies read too). Pure, so
// the rules live apart from how StepEvidence draws them.
import {
	diffStats,
	editDiffText,
	filePathOf,
	freshNotes,
	lineCount,
	parseArgs,
	prettyJSON,
	type ShellOutput,
	shellOutput,
	skillContext,
	str,
	type TaskRow,
	toolFamily,
	webFetchResult,
} from "@evener/appwire-client";
import type { ActivityDetail } from "../projectedRows";
import type { RunStep } from "../timeline";

export type Evidence =
	| { kind: "output"; text: string; lines: number }
	| { kind: "diff"; text: string; added: number; removed: number }
	| { kind: "wrote"; path: string }
	// A command that exited nonzero with no error of its own.
	| { kind: "exit"; code: number }
	// What the shell tool's footer says besides the exit: a timed-out wait, a
	// command still running, an output only partly here.
	| { kind: "note"; text: string }
	// A fetched page: the model's answer (or the page's content), from where.
	| { kind: "page"; text: string; url?: string; bytes?: number }
	// Markdown with a heading: the instructions a skill loaded.
	| { kind: "markdown"; title: string; markdown: string }
	// The task list a task_list call returned, each task with the note this
	// call added to it.
	| { kind: "tasks"; tasks: ChecklistTask[] }
	// A tool's arguments or result, pretty-printed.
	| { kind: "json"; label: "Arguments" | "Result"; text: string }
	| { kind: "error"; text: string; exitCode?: number };

/** A task in a task_list step's checklist, with the note the call added. */
export type ChecklistTask = Pick<TaskRow, "id" | "status" | "description"> & { note?: string };

/** Output lines shown in the transcript before "Show all N lines". */
export const EVIDENCE_PREVIEW_LINES = 40;

// A file tool's output is a confirmation ("edited a.go: 1 replacement(s)",
// "wrote 12 bytes to a.go") that only repeats what its diff or path says. The
// file tools are the package's edit family.
const isFileTool = (label: string) => toolFamily(label) === "edit";

function diff(text: string): Evidence {
	return { kind: "diff", text, ...diffStats(text) };
}

// A tool's output as it printed it, or nothing for an empty one.
function rawOutput(text: string): Evidence[] {
	return text ? [{ kind: "output", text, lines: lineCount(text) }] : [];
}

// A skill's markdown is its author's, and the phone's markdown view loads
// images from their URLs, so each image, inline (![alt](url)), by reference
// (![alt][ref]) or shortcut (![alt]), reads as its alt text instead.
function withoutImages(markdown: string): string {
	return markdown.replace(/!\[([^\]]*)\](?:\([^)]*\)|\[[^\]]*\])?/g, "$1");
}

// What the shell tool's footer says besides the exit.
function shellNotes(run: ShellOutput): Evidence[] {
	const notes: Evidence[] = [];
	if (run.windowed) notes.push({ kind: "note", text: "A long output: only its start and end are here" });
	if (run.stillRunning)
		notes.push({
			kind: "note",
			text: run.timedOut
				? "Still running in the background after its wait timed out"
				: "Still running in the background",
		});
	else if (run.timedOut) notes.push({ kind: "note", text: "Timed out" });
	return notes;
}

// What a tool's output shows, by its family: a command without its exit
// footer, a fetched page's answer, a skill's instructions, a task list as a
// checklist, an MCP or other tool's JSON pretty-printed; anything else as the
// tool printed it.
function outputEvidence(label: string, detail: EvidenceSource["detail"]): Evidence[] {
	const text = detail.output ?? "";
	switch (toolFamily(label)) {
		case "shell": {
			const run = shellOutput(text);
			const evidence = rawOutput(run.text);
			const code = detail.exitCode ?? run.exitCode;
			// An error of its own says the exit code with it (below). -1 is the
			// shell tool's sentinel for a command stopped by a signal or by
			// evener's runtime limit, not an exit code.
			if (code !== undefined && code !== 0 && code !== -1 && !detail.error) evidence.push({ kind: "exit", code });
			evidence.push(...shellNotes(run));
			return evidence;
		}
		case "fetch": {
			const page = webFetchResult(text);
			return page?.text === undefined ? rawOutput(text) : [{ kind: "page", ...page, text: page.text }];
		}
		case "skill": {
			const loaded = skillContext(text);
			return loaded
				? [{ kind: "markdown", title: loaded.name, markdown: withoutImages(loaded.instructions) }]
				: rawOutput(text);
		}
		case "tasks": {
			// No list, or one with no tasks in it: what the tool printed says more
			// than an empty checklist.
			if (!detail.tasks?.length) return rawOutput(text);
			const notes = freshNotes({ argumentsJSON: detail.arguments });
			const tasks = detail.tasks.map(({ id, status, description }) => {
				const note = notes.get(id);
				return { id, status, description, ...(note === undefined ? {} : { note }) };
			});
			return [{ kind: "tasks", tasks }];
		}
		case "mcp":
		case "tool": {
			const args = detail.arguments ? prettyJSON(detail.arguments) : undefined;
			const result = text ? prettyJSON(text) : undefined;
			// Arguments that aren't a JSON object or array show as they were
			// sent, as the web's MCPToolArguments shows them.
			const evidence: Evidence[] = args
				? [{ kind: "json", label: "Arguments", text: args }]
				: rawOutput(detail.arguments?.trim() ? detail.arguments : "");
			if (result) evidence.push({ kind: "json", label: "Result", text: result });
			else evidence.push(...rawOutput(text));
			return evidence;
		}
		default:
			return rawOutput(text);
	}
}

/** The parts of a step its evidence comes from. */
export type EvidenceSource = Pick<RunStep, "label" | "summaryOnly"> & {
	detail: Pick<ActivityDetail, "arguments" | "output" | "error" | "exitCode" | "tasks">;
};

export function stepEvidence(step: EvidenceSource): Evidence[] {
	// At Intent a settled step shows only its summary (the summary-only ruling).
	if (step.summaryOnly) return [];
	const { detail } = step;
	const evidence: Evidence[] = [];
	if (isFileTool(step.label)) {
		const args = parseArgs(detail.arguments);
		const path = filePathOf(args);
		if (step.label === "edit_file") {
			const oldString = str(args, "old_string");
			const newString = str(args, "new_string");
			// Arguments that carry neither side (a malformed call) have no diff.
			if (oldString !== undefined || newString !== undefined)
				evidence.push(diff(editDiffText(path ?? "", oldString ?? "", newString ?? "")));
		} else if (step.label === "apply_patch") {
			const patch = str(args, "patch");
			if (patch) evidence.push(diff(patch));
		} else if (path) {
			evidence.push({ kind: "wrote", path });
		}
	} else {
		evidence.push(...outputEvidence(step.label, detail));
	}
	if (detail.error) {
		evidence.push({
			kind: "error",
			text: detail.error,
			...(detail.exitCode === undefined ? {} : { exitCode: detail.exitCode }),
		});
	}
	return evidence;
}
