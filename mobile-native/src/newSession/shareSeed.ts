// Starting a session from a share (spec: share intent): the shared text joins
// the New session form's prompt through its one opening path, so the form
// opens on what this phone remembers (last host, project, model, effort) and
// the share adds to it. The store saves on every change, so a seed that
// reports true is already durable.
import type { NewSessionStore } from "./newSessionContext";
import type { LaunchMemory } from "./launchMemory";
import { openForm } from "./openForm";

/** Seeds the form with a share's text and reports whether the draft saved. A
 * false answer means the caller keeps the share pending: the text is not
 * durably anywhere, and closing the sheet over it would lose it. */
export function seedSharedPrompt(
	store: NewSessionStore,
	memory: LaunchMemory,
	share: { id: string; text: string },
): { stop: () => void; saved: boolean } {
	const stop = openForm(store, memory.lastSetup(), { appendPrompt: share.text });
	return { stop, saved: !store.getState().storageError };
}
