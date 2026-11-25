import type { HubProfile, HubProfiles } from "./connection";

export async function removeSavedHub(
	profiles: Pick<HubProfiles, "remove" | "list">,
	deviceData: { removeHub(id: string): void | Promise<void> },
	id: string,
) {
	let error: string | null = null;
	let removed = false;
	try {
		await profiles.remove(id);
		removed = true;
	} catch {
		error = "The hub or its credentials could not be fully removed from this device.";
	}
	// SecureStore can fail after removing the index entry. Reconcile the actual
	// list before deleting drafts, so a failed removal keeps a usable hub's text.
	let remaining: HubProfile[] | null = null;
	try {
		remaining = await profiles.list();
		removed = !remaining.some((profile) => profile.id === id);
	} catch {
		error = "The saved hub list could not be refreshed. Reopen the app to reload it.";
	}
	if (removed) {
		try {
			await deviceData.removeHub(id);
		} catch {
			error = "The hub was removed, but some local data could not be deleted from this device.";
		}
	}
	return { profiles: remaining, removed, error };
}
