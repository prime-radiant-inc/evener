/** Runs every cleanup `removeHub` owes a removed hub, independently: one
 * cleanup's storage failure must not stop the others from running, since
 * each is the only thing that forgets its own piece of that hub's state.
 * Rethrows the first failure once every cleanup has been attempted, so
 * `removeHub` still reports an accurate "something didn't clean up" (each
 * cleanup already surfaces its own storage failure the way
 * `board/boardMemory.ts`'s `forgetBoard` documents). */
export function runHubCleanups(hubId: string, cleanups: readonly ((hubId: string) => void)[]): void {
	// A flag, not an "is firstError still undefined" check: a cleanup is free
	// to throw undefined or null (both legal), and either would look
	// identical to "nothing failed yet" if the sentinel were the error value
	// itself.
	let failed = false;
	let firstError: unknown;
	for (const cleanup of cleanups) {
		try {
			cleanup(hubId);
		} catch (error) {
			if (!failed) {
				failed = true;
				firstError = error;
			}
		}
	}
	if (failed) throw firstError;
}
