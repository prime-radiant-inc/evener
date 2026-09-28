import * as Crypto from "expo-crypto";
import { Storage } from "expo-sqlite/kv-store";
import { forkCheckpoints } from "./forkCheckpointRepository";
import { nativeNavigationActions } from "./navigationActionRepository";
import { rawStringDraftBackend } from "./nativePreferenceDrafts";
import { pinAssignmentDrafts } from "./pinAssignmentDrafts";
import { pinSectionDrafts } from "./pinSectionDrafts";

// The organization draft's device backend is the shared one D6 hardened: its
// deleteIf compares stored bytes to the identity canonically (key order and
// whitespace normalized), so clearing a record whose bytes are an older
// build's formatting still matches the value rather than wedging the draft.
const backend = rawStringDraftBackend(Storage, () => Crypto.randomUUID());
export const organizationJournal = (hubId: string) =>
	nativeNavigationActions(hubId, backend);
export const pinDrafts = (hubId: string, ref: string) =>
	pinAssignmentDrafts(hubId, ref, backend);
export const sectionDrafts = (hubId: string, sectionId: string) =>
	pinSectionDrafts(hubId, sectionId, backend);
export const forkJournal = (hubId: string, parentRef: string) =>
	forkCheckpoints(hubId, parentRef, backend);
export function removeOrganizationData(hubId: string) {
	Storage.removeItemSync(`evener.native.navigation-action.${hubId}`);
	const prefixes = [
		"evener.native.pin-assignment.",
		"evener.native.pin-section-name.",
		"evener.native.fork-checkpoint.",
	];
	for (const key of Storage.getAllKeysSync()) {
		const prefix = prefixes.find((prefix) => key.startsWith(prefix));
		if (!prefix) continue;
		let scope: unknown;
		try {
			scope = JSON.parse(key.slice(prefix.length));
		} catch {
			continue;
		}
		if (Array.isArray(scope) && scope[0] === hubId) Storage.removeItemSync(key);
	}
}
