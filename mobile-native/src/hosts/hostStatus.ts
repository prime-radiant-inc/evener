// A host's state in words (spec 12; ruling 4), from the row evener/host/list
// serves. `midAttach` stays set while the hub's supervisor retries a dropped
// host (observe, cmd/evener-hub/app_host_manage.go), so a host the hub is
// already reaching for offers no Connect.
import type { HostRow } from "@evener/appwire-client";

export interface HostStatus {
	word: "Connected" | "Connecting…" | "Offline · reconnecting" | "Offline";
	/** Offline states take the attention ink: a human may be needed. */
	tone: "ink" | "attention";
	/** Connect shows only for a host the hub isn't attached to or retrying. */
	canConnect: boolean;
	/** The detail page's footer for an offline host, or null. */
	footer: string | null;
}

const UNREACHABLE = "This host is offline, so its sessions can't be reached.";

export function hostStatus(row: HostRow, connecting: boolean): HostStatus {
	if (row.attached) return { word: "Connected", tone: "ink", canConnect: false, footer: null };
	if (connecting) return { word: "Connecting…", tone: "ink", canConnect: false, footer: null };
	if (row.midAttach)
		return {
			word: "Offline · reconnecting",
			tone: "attention",
			canConnect: false,
			footer: `${UNREACHABLE} The hub keeps trying to reach it.`,
		};
	return {
		word: "Offline",
		tone: "attention",
		canConnect: !row.removed,
		footer: `${UNREACHABLE} Connect to reach them.`,
	};
}

/** The gray tag for a host on another Evener version than the hub, from its
 * last-known version (spec 12: "Hub runs 0.9.412"); null when they match or
 * either is unknown. */
export function versionDriftTag(row: Pick<HostRow, "hubVersion">, hubVersion: string | undefined): string | null {
	return row.hubVersion && hubVersion && row.hubVersion !== hubVersion ? `Hub runs ${hubVersion}` : null;
}

/** Spec 12's footer for a connected host on another version. */
export const VERSION_DRIFT_FOOTER =
	"This host runs a different version of Evener than the hub. Sessions keep working. Update Evener on the host when it's convenient.";

const OS_NAMES: Record<string, string> = { darwin: "macOS", linux: "Linux", windows: "Windows" };

/** "macOS · arm64" from the host's last-known facts; null when unknown. */
export function systemLabel(row: Pick<HostRow, "os" | "arch">): string | null {
	const parts = [row.os ? (OS_NAMES[row.os] ?? row.os) : undefined, row.arch].filter((part): part is string => !!part);
	return parts.length > 0 ? parts.join(" · ") : null;
}
