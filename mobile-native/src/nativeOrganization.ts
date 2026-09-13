import * as Crypto from "expo-crypto";
import { Storage } from "expo-sqlite/kv-store";
import { forkCheckpoints } from "./forkCheckpointRepository";
import { nativeNavigationActions } from "./navigationActionRepository";
import { pinAssignmentDrafts } from "./pinAssignmentDrafts";
import { pinSectionDrafts } from "./pinSectionDrafts";

const backend = {
	createId: () => Crypto.randomUUID(),
	get(key: string): unknown {
		const value = Storage.getItemSync(key);
		return value === null ? null : JSON.parse(value);
	},
	set(key: string, value: unknown) {
		Storage.setItemSync(key, JSON.stringify(value));
	},
	deleteIf(key: string, expected: unknown) {
		if (Storage.getItemSync(key) !== JSON.stringify(expected)) return false;
		Storage.removeItemSync(key);
		return true;
	},
};
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
