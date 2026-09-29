// The creation stores whose hub was removed (#3104). A start of theirs that
// lands has no one left to tell, since the hub is gone. Kept apart from
// creations.ts, which reaches the device's draft storage, so the form can ask
// without loading it.
import type { NewSessionStore } from "./newSessionContext";

const retired = new WeakSet<NewSessionStore>();

export function retireCreation(store: NewSessionStore): void {
	retired.add(store);
}

/** Whether this store's hub was removed. */
export function creationRetired(store: NewSessionStore): boolean {
	return retired.has(store);
}
