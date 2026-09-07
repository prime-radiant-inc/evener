import * as Crypto from "expo-crypto";
import { Storage } from "expo-sqlite/kv-store";
import { nativeNavigationActions } from "./navigationActionRepository";
import { pinAssignmentDrafts } from "./pinAssignmentDrafts";

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
export function removeOrganizationData(hubId: string) {
	Storage.removeItemSync(`evener.native.navigation-action.${hubId}`);
	const prefix = "evener.native.pin-assignment.";
	for (const key of Storage.getAllKeysSync()) {
		if (!key.startsWith(prefix)) continue;
		let scope: unknown;
		try {
			scope = JSON.parse(key.slice(prefix.length));
		} catch {
			continue;
		}
		if (Array.isArray(scope) && scope[0] === hubId) Storage.removeItemSync(key);
	}
}
