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
const reconciliations = new WeakMap<DraftDocument, Promise<void>>();

export async function reconcileEditedResends(
	runtime: NativeMutationRuntime,
	targetKey: string,
	document: DraftDocument,
): Promise<void> {
	const previous = reconciliations.get(document);
	const current = (previous ?? Promise.resolve())
		.catch(() => undefined)
		.then(async () => {
			if (!ready(document, targetKey)) return;
			const completedIds = [...(document.getSnapshot().record.completedCommandRecoveryIds ?? [])];
			if (completedIds.length) {
				for (const id of completedIds) await runtime.discardRecovery(id, targetKey);
				if (!ready(document, targetKey) || !document.finishCompletedCommandRecovery(completedIds)) return;
			}
			// Markers survive capture clearing, and receipt acknowledgment is idempotent.
			for (const id of document.getSnapshot().record.recoveryResendAcks ?? []) {
				await runtime.acknowledgeRecoveryResend(targetKey, id);
				if (!document.finishRecoveryResendAck(id)) return;
			}
			if (!ready(document, targetKey)) return;
			const sourceIds = document.getSnapshot().record.unconfirmedComposer?.recoveryIds;
			if (!sourceIds?.length) return;
			const receipt = await runtime.readRecoveryResend(targetKey, sourceIds);
			if (!ready(document, targetKey)) return;
			if (receipt) {
				// The captured owner and acknowledgment marker commit together in drafts.
				// Never acknowledge mutation storage before that write succeeds.
				if (!document.settleEditedResend(receipt)) return;
				await runtime.acknowledgeRecoveryResend(targetKey, receipt.clientMutationId);
				document.finishRecoveryResendAck(receipt.clientMutationId);
				return;
			}
			// Neither a missing receipt nor outbox absence proves non-admission. Only
			// freshly retained ALL-source recovery records authorize restoring the capture.
			const retained = (await runtime.read(targetKey)).recovery.filter((source) =>
				sourceIds.includes(source.clientMutationId),
			);
			if (ready(document, targetKey)) document.restoreEditedRecovery(retained);
		});
	reconciliations.set(document, current);
	try {
		await current;
	} finally {
		if (reconciliations.get(document) === current) reconciliations.delete(document);
	}
}
