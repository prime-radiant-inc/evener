// A step's evidence, worked out again only when what it comes from changes.
// sessionRows copies every step on every publish (each streaming frame), and
// the store may rebuild a row's detail too, so the memo keys on the strings
// themselves, never on an object.
import { useMemo } from "react";
import { type Evidence, type EvidenceSource, stepEvidence } from "./evidence";

export function useStepEvidence(step: EvidenceSource): Evidence[] {
	const { label, summaryOnly } = step;
	const { arguments: args, output, error, exitCode } = step.detail;
	return useMemo(
		() => stepEvidence({ label, summaryOnly, detail: { arguments: args, output, error, exitCode } }),
		[label, summaryOnly, args, output, error, exitCode],
	);
}
