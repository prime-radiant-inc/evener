// A step's evidence, worked out again only when what it comes from changes.
// sessionRows copies every step on every publish (each streaming frame), and
// the store may rebuild a row's detail too, so the memo keys on the strings
// themselves, never on an object. A task_list step's task list is parsed
// afresh by each projection, so it keys the memo as its JSON.
import { useMemo } from "react";
import type { DetailTask } from "../projectedRows";
import { type Evidence, type EvidenceSource, stepEvidence } from "./evidence";

export function useStepEvidence(step: EvidenceSource): Evidence[] {
	const { label, summaryOnly } = step;
	const { arguments: args, output, error, exitCode, tasks } = step.detail;
	const tasksJSON = tasks === undefined ? undefined : JSON.stringify(tasks);
	return useMemo(() => {
		const parsed = tasksJSON === undefined ? undefined : (JSON.parse(tasksJSON) as readonly DetailTask[]);
		return stepEvidence({ label, summaryOnly, detail: { arguments: args, output, error, exitCode, tasks: parsed } });
	}, [label, summaryOnly, args, output, error, exitCode, tasksJSON]);
}
