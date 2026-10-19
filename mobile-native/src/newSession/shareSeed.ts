// Starting a session from a share (spec: share intent): the shared text joins
// the New session form's prompt through its one opening path, so the form
// opens on what this phone remembers (last host, project, model, effort) and
// the share adds to it. The store saves on every change.
//
// Two conditions make a seed "landed": the form holds the share's text, and it
// saved with no storage error. A store still starting an earlier session
// ignores prompt edits, so nothing is recorded as attempted until the form can
// actually take the text; a later edit to the draft does not undo the landing.
import { mergeDraftText } from "@evener/appwire-client";
import type { NewSessionStore } from "./newSessionContext";
import type { LaunchMemory } from "./launchMemory";
import { openForm } from "./openForm";

export interface ShareSeed {
	/** Lets go of the form's wait for recent projects. */
	stop: () => void;
	/** The share's text as inserted (outer whitespace trimmed). */
	text: string;
	/** Whether the text is in the form and saved right now. */
	saved: boolean;
}

/** Whether the form holds this share's text and saved it. Read live: the seed
 * changes the store inside one effect, and a render's own storageError is from
 * before that change. */
export function shareSeedLanded(store: NewSessionStore, text: string): boolean {
	const form = store.getState();
	return form.storageLoaded && !form.storageError && !!text && form.prompt.includes(text);
}

/** Seeds the form with a share's text. `saved` is false when the store could
 * not take it (it is submitting an earlier start, or the read/save failed), so
 * the caller keeps the share pending rather than dropping it. Calling again
 * with the same share never appends a second copy. */
export function seedSharedPrompt(
	store: NewSessionStore,
	memory: LaunchMemory,
	share: { id: string; text: string },
): ShareSeed {
	const text = share.text.trim();
	const stop = openForm(store, memory.lastSetup(), { appendPrompt: text });
	return { stop, text, saved: shareSeedLanded(store, text) };
}

/** Retries a seed whose first attempt could not be saved, without appending
 * the text again. Returns whether the form now holds it durably. */
export function retrySharedSeed(store: NewSessionStore, text: string): boolean {
	const form = store.getState();
	if (!form.prompt.includes(text)) form.setPrompt(mergeDraftText(form.prompt, text));
	store.getState().retryStorage();
	return shareSeedLanded(store, text);
}
