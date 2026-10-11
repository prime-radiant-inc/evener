// The hub's launch defaults for the chosen host and project: what Access and
// More options fall back to when the sheet leaves a setting alone (ruling 14).
// The sheet reads them once, from evener/launch/resolve on that host (ruling 2)
// with none of its own overrides, so "effective" is the hub's default for the
// project, and again whenever the hub's launch settings change. The pages read
// them from the sheet's context.
import type { AppwireClient, LaunchConfigLayer } from "@evener/appwire-client";
import { useStore } from "zustand";
import type { NewSessionService } from "../../../mobile/src/services/newSession";
import type { NewSessionStore } from "./newSessionContext";
import { useHostRead, useHubRevision } from "./useHostRead";

export type LaunchDefaults = Pick<
	LaunchConfigLayer,
	"sandbox" | "sandboxNet" | "contextStrategy" | "maxSubagentDepth" | "maxRounds"
>;

const FIELDS = ["sandbox", "sandboxNet", "contextStrategy", "maxSubagentDepth", "maxRounds"] as const;

const LAUNCH_CHANGES = ["evener/launch/updated"] as const;

async function readDefaults(service: NewSessionService, host: string, cwd: string): Promise<LaunchDefaults> {
	const { effective } = await service.resolveLaunch(host, cwd, {});
	const defaults: Record<string, unknown> = {};
	for (const field of FIELDS) if (effective[field] !== undefined) defaults[field] = effective[field];
	return defaults as LaunchDefaults;
}

/** Null before the hub answers for this host and project, and after it
 * couldn't; the pages then speak of "the hub's default" with no value. */
export function useSheetLaunchDefaults(
	store: NewSessionStore,
	client: AppwireClient | null,
	ready: boolean,
): LaunchDefaults | null {
	const source = useStore(store, (form) => form.source);
	const cwd = useStore(store, (form) => form.cwd);
	const revision = useHubRevision(client, LAUNCH_CHANGES);
	return useHostRead(client, ready, source, cwd, readDefaults, revision);
}
