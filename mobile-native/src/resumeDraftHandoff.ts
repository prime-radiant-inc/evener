import type { DraftLibrary } from "./draftLibrary";

/** Carry a destination's edited draft and recovery ownership to the identity
 * an identity-changing resume resolved to. Every resume path takes this one
 * handoff: the session screen's Send, the background outbox flush, and a
 * message sent from a screen above the session. */
export type ResumeDraftHandoff = (hubId: string, fromRef: string, toRef: string) => void;

export function handoffResumeDraft(
	drafts: Pick<DraftLibrary, "handoff">,
	hubId: string,
	fromRef: string,
	toRef: string,
): void {
	if (fromRef === toRef) return;
	drafts.handoff({ hubId, sessionRef: fromRef }, { hubId, sessionRef: toRef });
}
