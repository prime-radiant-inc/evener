// The hub's launch defaults for the chosen host and project: what Access and
// More options fall back to when the sheet leaves a setting alone (ruling 14).
// They come from evener/launch/resolve on that host (ruling 2) with none of the
// sheet's own overrides, so "effective" is the hub's default for the project.
import type { LaunchConfigLayer } from "@evener/appwire-client";
import { useEffect, useState } from "react";
import { createNewSessionService } from "../../../mobile/src/services/newSession";
import { useNewSession } from "./newSessionContext";

export type LaunchDefaults = Pick<
	LaunchConfigLayer,
	"sandbox" | "sandboxNet" | "contextStrategy" | "maxSubagentDepth" | "maxRounds"
>;

const FIELDS = ["sandbox", "sandboxNet", "contextStrategy", "maxSubagentDepth", "maxRounds"] as const;

function defaultsOf(effective: LaunchConfigLayer): LaunchDefaults {
	const defaults: Record<string, unknown> = {};
	for (const field of FIELDS) if (effective[field] !== undefined) defaults[field] = effective[field];
	return defaults as LaunchDefaults;
}

/** Null before the hub answers for this host and project, and after it
 * couldn't; the pages then speak of "the hub's default" with no value. An
 * answer for a place the form has left is dropped. */
export function useLaunchDefaults(host: string, cwd: string): LaunchDefaults | null {
	const { client, ready } = useNewSession();
	const project = cwd.trim();
	const place = JSON.stringify([host, project]);
	const [known, setKnown] = useState<{ place: string; defaults: LaunchDefaults | null } | null>(null);
	useEffect(() => {
		if (!client || !ready || !project) return;
		let current = true;
		createNewSessionService(client)
			.resolveLaunch(host, project, {})
			.then(
				(resolved) => current && setKnown({ place, defaults: defaultsOf(resolved.effective) }),
				() => current && setKnown({ place, defaults: null }),
			);
		return () => {
			current = false;
		};
	}, [client, ready, host, project, place]);
	return known?.place === place ? known.defaults : null;
}
