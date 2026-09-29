// The hub's launch defaults for the chosen host and project: what Access and
// More options fall back to when the sheet leaves a setting alone (ruling 14).
// They come from evener/launch/resolve on that host (ruling 2) with none of the
// sheet's own overrides, so "effective" is the hub's default for the project.
import type { LaunchConfigLayer } from "@evener/appwire-client";
import type { NewSessionService } from "../../../mobile/src/services/newSession";
import { useHostRead } from "./useHostRead";

export type LaunchDefaults = Pick<
	LaunchConfigLayer,
	"sandbox" | "sandboxNet" | "contextStrategy" | "maxSubagentDepth" | "maxRounds"
>;

const FIELDS = ["sandbox", "sandboxNet", "contextStrategy", "maxSubagentDepth", "maxRounds"] as const;

async function readDefaults(service: NewSessionService, host: string, cwd: string): Promise<LaunchDefaults> {
	const { effective } = await service.resolveLaunch(host, cwd, {});
	const defaults: Record<string, unknown> = {};
	for (const field of FIELDS) if (effective[field] !== undefined) defaults[field] = effective[field];
	return defaults as LaunchDefaults;
}

/** Null before the hub answers for this host and project, and after it
 * couldn't; the pages then speak of "the hub's default" with no value. */
export function useLaunchDefaults(host: string, cwd: string): LaunchDefaults | null {
	return useHostRead(host, cwd, readDefaults);
}
