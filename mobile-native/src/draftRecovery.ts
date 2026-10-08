// This is a manual recovery action, never part of reconnect or mutation retry.
export function restoreUnconfirmedDraft(currentDraft: string, submitted: string): string | null {
	return currentDraft === "" ? submitted : null;
}
