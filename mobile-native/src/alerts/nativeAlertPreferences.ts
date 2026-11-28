import { Storage } from "expo-sqlite/kv-store";
import { AlertPreferenceStore } from "./alertPreferences";

let store: AlertPreferenceStore | undefined;

/** Device-wide choices shared by the foreground banners and workspace settings. */
export function alertPreferences(): AlertPreferenceStore {
	store ??= new AlertPreferenceStore(Storage);
	return store;
}
