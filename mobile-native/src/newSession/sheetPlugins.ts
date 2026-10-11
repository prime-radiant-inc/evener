// The plugins New session would start with (spec 11): one plugin preview for
// the whole sheet, on the chosen host, so the form's Plugins row, the Start
// gate and the checklist read the same answer. It previews only while the
// sheet is ready: a client that isn't would only produce a failure to show.
import {
	type AppwireClient,
	type LaunchConfigLayer,
	type PluginPreviewResponse,
	type PluginSelectionError,
	type PluginSelectionState,
	pluginSelectionFromOverrides,
	pluginSelectionIssues,
	selectedPluginNames,
} from "@evener/appwire-client";
import { useStore } from "zustand";
import {
	type PluginPreviewLoadState,
	usePluginPreview,
} from "../../../cmd/evener-hub/frontend/src/panes/spawn/usePluginPreview";
import type { NewSessionStore } from "./newSessionContext";
import { useHubRevision } from "./useHostRead";

const PLUGIN_CHANGES = ["evener/plugin/updated", "evener/launch/updated"] as const;

/** Stands in for the client before the sheet has one; the preview is off then,
 * so it is never asked anything. */
const NO_CLIENT = {
	request: () => Promise.reject(new Error("The sheet has no connection.")),
} as unknown as AppwireClient;

export function useSheetPlugins(
	store: NewSessionStore,
	client: AppwireClient | null,
	ready: boolean,
): PluginPreviewLoadState {
	const source = useStore(store, (form) => form.source);
	const cwd = useStore(store, (form) => form.cwd.trim());
	const launchOverrides = useStore(store, (form) => form.launchOverrides);
	const revision = useHubRevision(client, PLUGIN_CHANGES);
	return usePluginPreview({
		client: client ?? NO_CLIENT,
		cwd,
		host: source,
		launchOverrides,
		pluginRevision: revision,
		enabled: ready && !!client && !!cwd,
	}).state;
}

export interface PluginChoice {
	selection: PluginSelectionState;
	/** The last preview's answer, kept while a newer one loads; null before one. */
	response: PluginPreviewResponse | null;
	/** The plugins the session would start with. */
	on: string[];
	total: number;
	/** The selection's blocking problems, which hold Start. */
	issues: PluginSelectionError[];
}

export function pluginChoice(launchOverrides: LaunchConfigLayer, state: PluginPreviewLoadState): PluginChoice {
	const selection = pluginSelectionFromOverrides(launchOverrides);
	const response = state.response ?? null;
	return {
		selection,
		response,
		on: response ? selectedPluginNames(selection, response) : [],
		total: response?.plugins.length ?? 0,
		issues: response ? pluginSelectionIssues(selection, response) : [],
	};
}
