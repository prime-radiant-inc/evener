// What every New session page reads from the sheet around it (ruling 1): the
// creation store, the hub, its client and the connection's readiness, the
// hub's hosts and live sessions, and this phone's memory of starts. It lives
// apart from NewSessionSheet.tsx, the navigator, so the pages the sheet mounts
// can read it without importing their own navigator.
import type { AppwireClient } from "@evener/appwire-client";
import { createContext, useContext } from "react";
import type { HostsController } from "../hosts/hostsController";
import type { LiveSessionsReader } from "../hosts/liveCounts";
import type { createNewSessionStore } from "../newSession";
import type { PluginPreviewLoadState } from "../../../cmd/evener-hub/frontend/src/panes/spawn/usePluginPreview";
import type { LaunchMemory } from "./launchMemory";

/** The sheet's pages. SessionOptions is interim until PR 10. */
export type NewSessionRoutes = {
	Form: undefined;
	Host: undefined;
	Project: undefined;
	Browse: { dir: string };
	Model: undefined;
	SessionOptions: undefined;
};

export type NewSessionStore = ReturnType<typeof createNewSessionStore>;

export interface NewSessionContextValue {
	store: NewSessionStore;
	hubId: string;
	hubName: string;
	/** The client pages read through: the live one while ready, the last ready
	 * one while a retry dials (useRenderClient's contract, connectionDisplay.ts). */
	client: AppwireClient | null;
	ready: boolean;
	/** The hub's hosts and its live sessions, per client; null before one. */
	hosts: HostsController | null;
	live: LiveSessionsReader | null;
	memory: LaunchMemory;
	/** The one plugin preview for the chosen host and project (sheetPlugins.ts). */
	plugins: PluginPreviewLoadState;
	/** A host's name on screen: the hub's own machine is named after the hub
	 * (ruling 3). */
	hostLabel(host: string): string;
}

const NewSessionContext = createContext<NewSessionContextValue | null>(null);
export const NewSessionProvider = NewSessionContext.Provider;

export function useNewSession(): NewSessionContextValue {
	const value = useContext(NewSessionContext);
	if (!value) throw new Error("A New session page must render inside NewSessionSheet.");
	return value;
}
