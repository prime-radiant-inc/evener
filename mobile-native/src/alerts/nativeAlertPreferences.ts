import { Storage } from "expo-sqlite/kv-store";
import { AlertPreferenceStore } from "./alertPreferences";

let store: AlertPreferenceStore | undefined;

/** The one store the banners and Hub > In-app alerts share, so a switch
 * flipped in Hub reaches the next banner at once. */
export function alertPreferences(): AlertPreferenceStore {
	store ??= new AlertPreferenceStore(Storage);
	return store;
}
