// What a step in a run has to show when you tap it (spec 8.2): an edit's
// diff, the file a write wrote, a command's output, a fetched page, the skill
// an activation loaded, a tool's arguments and result, and an error. Each
// reads the words the tool printed, not the envelope around them (the
// package's toolEvidence readers, which the web's bodies read too). Pure, so
// the rules live apart from how StepEvidence draws them.
import {
	diffStats,
	editDiffText,
	filePathOf,
	lineCount,
	parseArgs,
	prettyJSON,
	shellOutput,
	skillContext,
	str,
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
	// A fetched page: the model's answer (or the page's content), from where.
	| { kind: "page"; text: string; url?: string; bytes?: number }
	// Markdown with a heading: the instructions a skill loaded.
	| { kind: "markdown"; title: string; markdown: string }
	// A tool's arguments or result, pretty-printed.
	| { kind: "json"; label: "Arguments" | "Result"; text: string }
	| { kind: "error"; text: string; exitCode?: number };

/** Output lines shown in the transcript before "Show all N lines". */
export const EVIDENCE_PREVIEW_LINES = 40;

// A file tool's output is a confirmation ("edited a.go: 1 replacement(s)",
// "wrote 12 bytes to a.go") that only repeats what its diff or path says. The
// file tools are the package's edit family.
const isFileTool = (label: string) => toolFamily(label) === "edit";

function diff(text: string): Evidence {
	return { kind: "diff", text, ...diffStats(text) };
}

function output(text: string): Evidence {
	return { kind: "output", text, lines: lineCount(text) };
}

// What a tool's output shows, by its family: a command without its exit
// footer, a fetched page's answer, a skill's instructions, an MCP or other
// tool's JSON pretty-printed; anything else as the tool printed it.
function outputEvidence(label: string, detail: EvidenceSource["detail"]): Evidence[] {
	const text = detail.output ?? "";
	switch (toolFamily(label)) {
		case "shell": {
			const run = shellOutput(text);
			const evidence = run.text ? [output(run.text)] : [];
			const code = detail.exitCode ?? run.exitCode;
			// An error of its own says the exit code with it (below).
			if (code !== undefined && code !== 0 && !detail.error) evidence.push({ kind: "exit", code });
			return evidence;
		}
		case "fetch": {
			const page = webFetchResult(text);
			return page ? [{ kind: "page", ...page }] : text ? [output(text)] : [];
		}
		case "skill": {
			const loaded = skillContext(text);
			return loaded
				? [{ kind: "markdown", title: loaded.name, markdown: loaded.instructions }]
				: text
					? [output(text)]
					: [];
		}
		case "mcp":
		case "tool": {
			const args = detail.arguments ? prettyJSON(detail.arguments) : undefined;
			const result = text ? prettyJSON(text) : undefined;
			const evidence: Evidence[] = args ? [{ kind: "json", label: "Arguments", text: args }] : [];
			if (result) evidence.push({ kind: "json", label: "Result", text: result });
			else if (text) evidence.push(output(text));
			return evidence;
		}
		default:
			return text ? [output(text)] : [];
	}
}

/** The parts of a step its evidence comes from. */
export type EvidenceSource = Pick<RunStep, "label" | "summaryOnly"> & {
	detail: Pick<ActivityDetail, "arguments" | "output" | "error" | "exitCode">;
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
