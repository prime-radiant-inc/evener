import type { DraftDocument } from "./draftDocument";
import type { NativeMutationRuntime } from "./nativeMutationRuntime";

function ready(document: DraftDocument, targetKey: string): boolean {
	const { loaded, submitting, error } = document.getSnapshot();
	return document.targetKey === targetKey && loaded && !submitting && !error;
}

/** Claim first, then append to the latest document. A failed append retains recovery. */
export async function editQueuedMessageIntoDraft(
	runtime: NativeMutationRuntime,
	targetKey: string,
	id: string,
	document: DraftDocument,
): Promise<"restored" | "already-restored" | "blocked" | "lost"> {
	if (!ready(document, targetKey)) return "blocked";
	const retained = (await runtime.read(targetKey)).recovery.find(
		(source) => source.clientMutationId === id && source.recoveryKind === "editing",
	);
	if (!ready(document, targetKey)) return "blocked";
	const source = retained ?? (await runtime.editQueuedMessage(id, targetKey));
	if (!source) return "lost";
	return document.appendEditedRecovery(source);
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
