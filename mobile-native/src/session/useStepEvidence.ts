// A step's evidence, worked out again only when what it comes from changes.
// sessionRows copies every step on every publish (each streaming frame), and
// the store may rebuild a row's detail too, so the memo can't key on the
// objects. It keys on the step's whole detail as JSON, and works the evidence
// out from that JSON, so every field the projection sets reaches stepEvidence
// with no list here to keep in step with ActivityDetail.
import { useMemo } from "react";
import type { ActivityDetail } from "../projectedRows";
import { type Evidence, type EvidenceSource, stepEvidence } from "./evidence";

export function useStepEvidence(step: EvidenceSource): Evidence[] {
	const { label, summaryOnly } = step;
	const detailJSON = JSON.stringify(step.detail);
	return useMemo(
		() => stepEvidence({ label, summaryOnly, detail: JSON.parse(detailJSON) as ActivityDetail }),
		[label, summaryOnly, detailJSON],
	);
}
