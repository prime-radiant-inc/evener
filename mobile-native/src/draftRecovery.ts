import type { ConversationMutationState } from "../../mobile/src/state/conversation";

// A transport loss cannot establish whether the hub accepted an in-flight send.
// Keep its submitted text separate from any draft typed while it was pending.
export function captureUnconfirmedSend(
  mutation: ConversationMutationState | null | undefined,
): string | null {
  return mutation?.kind === "send" && mutation.status === "pending"
    ? mutation.draftSnapshot
    : null;
}

// This is a manual recovery action, never part of reconnect or mutation retry.
export function restoreUnconfirmedDraft(
  currentDraft: string,
  submitted: string,
): string | null {
  return currentDraft === "" ? submitted : null;
}
