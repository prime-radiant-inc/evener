// Where New session begins when it opens (spec 11): like a session when "New
// session like this" opened it; else the draft as it was left, when it has a
// project, a prompt or an image; else the last start remembered on this hub;
// else the hub's most recent project, once the hub has listed them.
import type { NewSessionStore } from "./newSessionContext";
import type { LaunchSetup, SessionSeed } from "./launchSetup";

/** Places the form once, as it opens with its draft loaded. The returned stop
 * lets go of the wait for the hub's recent projects. */
export function openForm(
	store: NewSessionStore,
	lastSetup: LaunchSetup | null,
	like: SessionSeed | undefined,
): () => void {
	const form = store.getState();
	if (like) {
		form.applySeed(like);
		return () => {};
	}
	if (form.cwd.trim() || form.prompt.trim() || form.images.length > 0) return () => {};
	if (lastSetup) {
		form.applySetup(lastSetup);
		return () => {};
	}
	// Nothing was ever started here: the most recent project, unless one is
	// chosen before the hub lists them.
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
