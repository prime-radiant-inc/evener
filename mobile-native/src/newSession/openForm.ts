// Where New session begins when it opens (spec 11): the draft as it was left,
// when it has a project, a prompt or an image; else the last start remembered
// on this hub; else the hub's most recent project, once the hub has listed them.
// A share's text is appended after that placement, so it inherits the same
// starting point as any other New session.
import { mergeDraftText } from "@evener/appwire-client";
import type { NewSessionStore } from "./newSessionContext";
import type { LaunchSetup } from "./launchSetup";

export interface OpenFormOptions {
	/** Text to add to the prompt after the form is placed (a share). */
	appendPrompt?: string;
}

/** Places the form once, as it opens with its draft loaded. The returned stop
 * lets go of the wait for the hub's recent projects. */
export function openForm(
	store: NewSessionStore,
	lastSetup: LaunchSetup | null,
	{ appendPrompt }: OpenFormOptions = {},
): () => void {
	const form = store.getState();
	const shared = appendPrompt?.trim() ?? "";
	const hasDraft = form.cwd.trim() !== "" || form.prompt.trim() !== "" || form.images.length > 0;
	// A share only adds to the prompt; every other starting rule stands.
	const stop = !hasDraft ? (lastSetup ? (form.applySetup(lastSetup), () => {}) : takeRecentProject(store)) : () => {};
	const current = store.getState().prompt;
	if (shared && !current.includes(shared)) store.getState().setPrompt(mergeDraftText(current, shared));
	return stop;
}

/** Waits for the hub's recent projects, taking the first while the form still
 * has no project of its own. */
function takeRecentProject(store: NewSessionStore): () => void {
	let stop = () => {};
	const takeRecent = () => {
		const state = store.getState();
		const recent = state.projects[0];
		if (state.cwd.trim()) stop();
		else if (recent) {
			stop();
			void state.setCwd(recent);
		}
	};
	stop = store.subscribe(takeRecent);
	takeRecent();
	return () => stop();
}
