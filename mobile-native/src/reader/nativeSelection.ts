import {
	contextForRange,
	sourcePositions,
	type AcceptedSourceLease,
	type CapturedEvidence,
	type CapturedPassage,
	type SourceRange,
} from "@evener/appwire-client";

export interface NativeDocumentSelection {
	displayedSnapshotId: string;
	mappingId: string;
	sourceRange: SourceRange;
	selectedRenderedText: string;
	phase: "changing" | "completed" | "cleared";
}

export interface NativeSavedRange {
	operationId: string;
	sourceRange: SourceRange;
}

export function captureNativePassage(
	event: NativeDocumentSelection,
	source: string,
	lease: AcceptedSourceLease,
	evidence: CapturedEvidence,
): CapturedPassage {
	if (event.phase !== "completed") throw new Error("Native selection has not completed");
	if (!event.displayedSnapshotId || event.displayedSnapshotId !== lease.displayedSnapshotId) {
		throw new Error("Native selection belongs to a different displayed snapshot");
	}
	if (!event.mappingId || !event.selectedRenderedText) throw new Error("Native selection has no mapped passage");
	const sourceRange = { ...event.sourceRange };
	const positions = sourcePositions(source, sourceRange);
	const context = contextForRange(source, sourceRange);
	return {
		evidence: {
			...evidence,
			fingerprint: { ...evidence.fingerprint, coverage: { ...evidence.fingerprint.coverage } },
		},
		sourceRange,
		...positions,
		selectedRenderedText: event.selectedRenderedText,
		contextBefore: context.before,
		contextAfter: context.after,
		mappingId: event.mappingId,
		displayedSnapshotId: event.displayedSnapshotId,
	};
}
