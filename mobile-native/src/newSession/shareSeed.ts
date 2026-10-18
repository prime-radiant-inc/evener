// Starting a session from a share (spec: share intent): the shared text is
// added to the New session form's prompt, after the form has opened on what
// this phone remembers (last host, project, model, effort). The store saves on
// every change, so a seed that reports true is already durable.
import { mergeDraftText } from "@evener/appwire-client";
import type { NewSessionStore } from "./newSessionContext";
import type { LaunchMemory } from "./launchMemory";
import { openForm } from "./openForm";

/** Seeds the form with a share's text and reports whether the draft saved.
 * A false answer means the caller keeps the share pending: the text is not
 * durably anywhere, and closing the sheet over it would lose it. */
export function seedSharedPrompt(
	store: NewSessionStore,
	memory: LaunchMemory,
	share: { id: string; text: string },
): boolean {
	openForm(store, memory.lastSetup());
	const form = store.getState();
	form.setPrompt(mergeDraftText(form.prompt, share.text));
	const after = store.getState();
	return !after.storageError;
}
