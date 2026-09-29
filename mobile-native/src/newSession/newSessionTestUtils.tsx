// The context a New session page reads, as NewSessionSheet builds it, for
// tests that mount one page: the hub is "magic-kingdom" (hub-1), its own
// machine is named after it, and the launch memory lives in memory. TestSheet
// runs the sheet's own plugin preview and launch defaults over the context's
// client.
import type { SyncStringStorage } from "../syncStringStorage";
import { LaunchMemory } from "./launchMemory";
import type { ReactNode } from "react";
import { type NewSessionContextValue, NewSessionProvider, type NewSessionStore } from "./newSessionContext";
import { useSheetPlugins } from "./sheetPlugins";
import { useSheetLaunchDefaults } from "./useLaunchDefaults";

export function memoryStorage(): SyncStringStorage {
	const items = new Map<string, string>();
	return {
		getItemSync: (key) => items.get(key) ?? null,
		setItemSync: (key, value) => void items.set(key, value),
		removeItemSync: (key) => void items.delete(key),
	};
}

export function sheetContext(
	store: NewSessionStore,
	over: Partial<NewSessionContextValue> = {},
): NewSessionContextValue {
	return {
		store,
		hubId: "hub-1",
		hubName: "magic-kingdom",
		client: null,
		ready: true,
		hosts: null,
		live: null,
		memory: new LaunchMemory(memoryStorage(), "hub-1"),
		hostLabel: (host) => (host === "local" ? "magic-kingdom" : host),
		plugins: { status: "loading" },
		launchDefaults: null,
		...over,
	};
}

/** The page under test inside the context, with the sheet's plugin preview. */
export function TestSheet({ value, children }: { value: NewSessionContextValue; children: ReactNode }) {
	const plugins = useSheetPlugins(value.store, value.client, value.ready);
	const launchDefaults = useSheetLaunchDefaults(value.store, value.client, value.ready);
	return <NewSessionProvider value={{ ...value, plugins, launchDefaults }}>{children}</NewSessionProvider>;
}
