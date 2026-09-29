// Why Start is disabled, and which row says so (spec 11; rulings 20 and 21).
// Only what the phone itself checked blames a row; a hub rejection after
// Start is shown as the hub said it.
import type { HostRow } from "@evener/appwire-client";
import { LOCAL_HOST } from "../../../cmd/evener-hub/frontend/src/stores/hostRouting";

export type HostReach = "local" | "connected" | "offline" | "missing" | "pending";

/** Where the chosen host stands. Before the hub lists its hosts the answer is
 * pending, and pending never blocks: the hub checks the host at Start, as the
 * web learned not to guess from a list still in flight (Spawn.tsx,
 * submittedSource). */
export function hostReach(host: string, rows: readonly HostRow[] | null): HostReach {
	if (host === LOCAL_HOST) return "local";
	if (rows === null) return "pending";
	const row = rows.find((candidate) => candidate.name === host);
	if (!row) return "missing";
	return row.attached ? "connected" : "offline";
}

export interface StartInput {
	/** The connection is ready for this hub. */
	ready: boolean;
	/** Storage loading, a start or model read in flight, or images processing. */
	busy: boolean;
	cwd: string;
	host: string;
	hostLabel: string;
	reach: HostReach;
	/** The plugin preview's blocking problems (pluginSelectionIssues). */
	pluginIssues: readonly { name: string; reason: string }[];
}

export interface StartBlock {
	/** The row the reason belongs to, or null when no row is to blame. */
	field: "host" | "project" | "plugins" | null;
	/** The sentence to show, or null when the row or the status line already
	 * says it. */
	message: string | null;
}

export function startBlock(input: StartInput): StartBlock | null {
	if (!input.ready || input.busy) return { field: null, message: null };
	if (input.reach === "offline")
		return { field: "host", message: `${input.hostLabel} is offline. Connect it or choose another host.` };
	if (input.reach === "missing")
		return { field: "host", message: `${input.hostLabel} is no longer a host on this hub. Choose another host.` };
	if (!input.cwd.trim()) return { field: "project", message: null };
	if (input.pluginIssues.length > 0)
		return {
			field: "plugins",
			message: input.pluginIssues.map((issue) => `${issue.name}: ${issue.reason}`).join("\n"),
		};
	return null;
}
