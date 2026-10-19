// The app's one share intake, reachable from a sheet that took ownership of a
// share (the New session sheet, which the picker replaces itself with). The
// intake lives in App; this module lets the owning sheet report completion
// without threading a callback through navigation params.
import type { ShareIntake } from "./shareIntake";

let current: ShareIntake | null = null;

/** Installs the app's intake for as long as the app is mounted. */
export function installShareIntake(intake: ShareIntake): void {
	current = intake;
}

/** The installed intake, or null before the app mounts one. */
export function currentShareIntake(): ShareIntake | null {
	return current;
}
