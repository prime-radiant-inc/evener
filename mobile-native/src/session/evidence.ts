// What a step in a run has to show when you tap it (spec 8.2): an edit's
// diff, the file a write wrote, a command's output, and an error. Pure, so
// the rules live apart from how StepEvidence draws them.
import { diffStats, editDiffText, filePathOf, lineCount, parseArgs, str, toolFamily } from "@evener/appwire-client";
import type { ActivityDetail } from "../projectedRows";
import type { RunStep } from "../timeline";

export type Evidence =
	| { kind: "output"; text: string; lines: number }
	| { kind: "diff"; text: string; added: number; removed: number }
	| { kind: "wrote"; path: string }
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
	} else if (detail.output) {
		evidence.push({ kind: "output", text: detail.output, lines: lineCount(detail.output) });
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
