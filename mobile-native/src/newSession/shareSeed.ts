// Starting a session from a share (spec: share intent): the shared text joins
// the New session form's prompt through its one opening path, so the form
// opens on what this phone remembers (last host, project, model, effort) and
// the share adds to it. The store saves on every change, so a seed that
// reports true is already durable.
import type { NewSessionStore } from "./newSessionContext";
import type { LaunchMemory } from "./launchMemory";
import { openForm } from "./openForm";

/** The merge each store is seeding, by share. A sheet reopened on the same
 * failed share retries that merge instead of appending the text again. */
const seeded = new WeakMap<NewSessionStore, { id: string; prompt: string }>();

export interface ShareSeed {
	/** Lets go of the form's wait for recent projects. */
	stop: () => void;
	/** The prompt this seed expects the form to hold. */
	prompt: string;
	/** Whether that prompt is saved and readable right now. */
	saved: boolean;
}

/** Seeds the form with a share's text. `saved` is false when the store could
 * not take it (it is submitting an earlier start, or the draft read/save
 * failed), so the caller keeps the share pending rather than dropping it. */
export function seedSharedPrompt(
	store: NewSessionStore,
	memory: LaunchMemory,
	share: { id: string; text: string },
): ShareSeed {
	const prior = seeded.get(store);
	let stop = () => {};
	if (prior?.id === share.id) {
		// The same share reached this store before: its text is already in the
		// prompt (saved or not), so retry the save rather than appending again.
		store.getState().retryStorage();
	} else {
		stop = openForm(store, memory.lastSetup(), { appendPrompt: share.text });
		seeded.set(store, { id: share.id, prompt: store.getState().prompt });
	}
	const record = seeded.get(store) as { id: string; prompt: string };
	return { stop, prompt: record.prompt, saved: shareSeedLanded(store, record.prompt, share.text) };
}

/** Whether the form holds the share's merged prompt and saved it. Read live:
 * the seed changes the store inside one effect, and a render's own
 * storageError is from before that change. */
export function shareSeedLanded(store: NewSessionStore, prompt: string, text: string): boolean {
	const form = store.getState();
	return form.storageLoaded && !form.storageError && form.prompt === prompt && prompt.includes(text);
}
