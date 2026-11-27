// Where New session begins when it opens (spec 11): the draft as it was left,
// when it has a project, a prompt or an image; else the last start remembered
// on this hub; else the hub's most recent project, once the hub has listed them.
import type { NewSessionStore } from "./newSessionContext";
import type { LaunchSetup } from "./launchSetup";

/** Places the form once, as it opens with its draft loaded. The returned stop
 * lets go of the wait for the hub's recent projects. */
export function openForm(store: NewSessionStore, lastSetup: LaunchSetup | null): () => void {
	const form = store.getState();
	const hasDraft = form.cwd.trim() !== "" || form.prompt.trim() !== "" || form.images.length > 0;
	if (hasDraft) return () => {};
	if (!lastSetup) return takeRecentProject(store);
	form.applySetup(lastSetup);
	return () => {};
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
