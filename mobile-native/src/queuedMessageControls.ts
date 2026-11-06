import type { DraftDocument } from "./draftDocument";
import type { NativeMutationRuntime } from "./nativeMutationRuntime";

function ready(document: DraftDocument, targetKey: string): boolean {
	const { loaded, submitting, error } = document.getSnapshot();
	return document.targetKey === targetKey && loaded && !submitting && !error;
}

function loadedForTarget(document: DraftDocument, targetKey: string): boolean {
	return document.targetKey === targetKey && document.getSnapshot().loaded;
}

/** Claim first, then append to the latest document. A failed append retains recovery. */
export async function editQueuedMessageIntoDraft(
	runtime: NativeMutationRuntime,
	targetKey: string,
	id: string,
	document: DraftDocument,
): Promise<"restored" | "already-restored" | "blocked" | "lost"> {
	if (!ready(document, targetKey)) return "blocked";
	const retained = await runtime.retainedRecoveryForAction(id, targetKey);
	if (retained) {
		if (!ready(document, targetKey)) return "blocked";
		if (!retained.isCurrent()) return "lost";
		if (!clearMatchingCapture(document, retained.record.draftCaptureToken)) return "blocked";
		if (!ready(document, targetKey)) return "blocked";
		if (!retained.isCurrent()) return "lost";
		return document.appendEditedRecovery(retained.record);
	}
	const queued = await runtime.queuedMessageForAction(id, targetKey, "edit");
	if (!queued) return "lost";
	if (!ready(document, targetKey)) return "blocked";
	if (!clearMatchingCapture(document, queued.draftCaptureToken)) return "blocked";
	if (!ready(document, targetKey)) return "blocked";
	const source = await runtime.editQueuedMessage(id, targetKey);
	if (!source) return "lost";
	return document.appendEditedRecovery(source);
}

/** Remove one exact durable queue row after transferring a matching uncertain
 * capture to that row. A missing/different token does not block the action. */
export async function removeQueuedMessageWithDraftCapture(
	runtime: NativeMutationRuntime,
	targetKey: string,
	id: string,
	document: DraftDocument,
): Promise<boolean> {
	if (!loadedForTarget(document, targetKey)) return false;
	const queued = await runtime.queuedMessageForAction(id, targetKey, "remove");
	if (!queued || !loadedForTarget(document, targetKey)) return false;
	if (!clearMatchingCapture(document, queued.draftCaptureToken)) return false;
	if (!loadedForTarget(document, targetKey)) return false;
	return runtime.removeQueuedMessage(id, targetKey);
}

function clearMatchingCapture(document: DraftDocument, token: string | undefined): boolean {
	return token === undefined || document.clearMatchingUnconfirmedCapture(token);
}

// Serialize fresh proof reads with this document's local reconciliation. A later
// call must observe the ownership left by its predecessor, including a failure.
const reconciliations = new WeakMap<DraftDocument, Promise<ReconciliationOutcome>>();

/** The reconciliation's outcome. "deferred" when an ownership read came back
 * unknown and left the capture's settle or restore owed to a later run - the
 * caller schedules that run instead of leaving the deferral to some unrelated
 * refresh. */
export type ReconciliationOutcome = "complete" | "deferred";

export async function reconcileEditedResends(
	runtime: NativeMutationRuntime,
	targetKey: string,
	document: DraftDocument,
	ownership: () => ReadonlySet<string> | undefined = () => new Set<string>(),
): Promise<ReconciliationOutcome> {
	const previous = reconciliations.get(document);
	const current = (previous ?? Promise.resolve())
		.catch(() => undefined)
		.then(async (): Promise<ReconciliationOutcome> => {
			if (!ready(document, targetKey)) return "complete";
			const completedIds = [...(document.getSnapshot().record.completedCommandRecoveryIds ?? [])];
			if (completedIds.length) {
				for (const id of completedIds) await runtime.discardRecovery(id, targetKey);
				if (!ready(document, targetKey) || !document.finishCompletedCommandRecovery(completedIds)) return "complete";
			}
			// Markers survive capture clearing, and receipt acknowledgment is idempotent.
			for (const id of document.getSnapshot().record.recoveryResendAcks ?? []) {
				await runtime.acknowledgeRecoveryResend(targetKey, id);
				if (!document.finishRecoveryResendAck(id)) return "complete";
			}
			if (!ready(document, targetKey)) return "complete";
			const sourceIds = document.getSnapshot().record.unconfirmedComposer?.recoveryIds;
			if (!sourceIds?.length) return "complete";
			// A source whose submission is still running on this chain belongs to
			// the ledger's open obligation until it completes. Restoring or
			// settling it here would clear the capture the completion needs to
			// attach, so an open obligation owns this whole capture: skip the
			// restore-capable steps and let the completion reconcile it.
			// Ownership is read here, inside the serialized operation, never
			// trusted from queue time: a handoff can adopt a running
			// command's capture into this document while this reconciliation
			// waits behind its predecessor.
			let ownershipUnknown = false;
			const excluded = ownership();
			if (excluded === undefined) {
				// Unknown ownership deferred this reconciliation: report it, so
				// the caller schedules the retry instead of waiting on the user.
				return "deferred";
			}
			if (sourceIds.some((id) => excluded.has(id))) return "complete";
			// Ownership and capture identity are revalidated across the reads
			// below: a capture that changed hands or changed identity
			// mid-flight is never restored or settled here.
			const captureToken = document.getSnapshot().record.unconfirmedCaptureToken;
			const stale = () => {
				const record = document.getSnapshot().record;
				if (record.unconfirmedCaptureToken !== captureToken) return true;
				const now = ownership();
				if (now === undefined) {
					ownershipUnknown = true;
					return true;
				}
				return sourceIds.some((id) => now.has(id));
			};
			const receipt = await runtime.readRecoveryResend(targetKey, sourceIds);
			if (!ready(document, targetKey) || stale()) return ownershipUnknown ? "deferred" : "complete";
			if (receipt) {
				// The captured owner and acknowledgment marker commit together in drafts.
				// Never acknowledge mutation storage before that write succeeds.
				if (!document.settleEditedResend(receipt)) return "complete";
				await runtime.acknowledgeRecoveryResend(targetKey, receipt.clientMutationId);
				document.finishRecoveryResendAck(receipt.clientMutationId);
				return "complete";
			}
			// Neither a missing receipt nor outbox absence proves non-admission. Only
			// freshly retained ALL-source recovery records authorize restoring the capture.
			const retained = (await runtime.read(targetKey)).recovery.filter((source) =>
				sourceIds.includes(source.clientMutationId),
			);
			if (ready(document, targetKey) && !stale()) document.restoreEditedRecovery(retained);
			return ownershipUnknown ? "deferred" : "complete";
		});
	reconciliations.set(document, current);
	try {
		return await current;
	} finally {
		if (reconciliations.get(document) === current) reconciliations.delete(document);
	}
}
