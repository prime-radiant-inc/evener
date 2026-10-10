// A hub's answers to the reads a Board controller makes, for tests that
// mount the Session's fleet (useFleet) against a fleet of their own.
import type { NavigationReadParams, NavigationSessionSummary } from "@evener/appwire-client";
import { manifest, wireSnapshot } from "@evener/appwire-client/testing/navigation";

export interface FleetShape {
	live: NavigationSessionSummary[];
	needsYou: NavigationSessionSummary[];
	sources?: { id: string; label: string }[];
	/** The revision every read answers at; 1 unless a test moves the fleet
	 * on and invalidates it. */
	revision?: number;
}

export const fleetSession = (ref: string, over: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary => ({
	ref,
	host_id: "local",
	session_id: ref,
	title: ref,
	project: "evener",
	state: "idle",
	kind: "session",
	live: true,
	children: [],
	...over,
});

/** The hub's answer to one of the Board controller's reads of `fleet`, or
 * undefined for a method it doesn't make. */
export function answerFleetRead(fleet: FleetShape, method: string, params: unknown): unknown {
	// The Board's notices (S11): a fleet with none.
	if (method === "evener/notices/list") return { notices: [] };
	if (method !== "evener/navigation/read") return undefined;
	const read = params as NavigationReadParams;
	const body = (() => {
		if (read.resource === "manifest")
			return manifest({
				sources: (fleet.sources ?? []).map((source) => ({ ...source, kind: "local", online: true })),
				sections: {
					live: { count: fleet.live.length },
					needs_you: { count: fleet.needsYou.length },
					pin_sections: { count: 0 },
				},
			});
		if (read.resource === "pin_catalog") return { pin_sections: [], remaining: 0 };
		if (read.section === "needs_you") return { sessions: fleet.needsYou, remaining: 0 };
		return { sessions: fleet.live, remaining: 0 };
	})();
	return wireSnapshot(
		{ ...read, representationVersion: 3, offset: read.offset ?? 0, limit: read.limit ?? 50 },
		body,
		`etag-${read.resource}-${fleet.revision ?? 1}`,
		fleet.revision ?? 1,
		"generation-test",
	);
}
