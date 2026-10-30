import type { UpdateCheckResponse } from "@evener/appwire-client";

/** The line under the hub's name at the top of the Hub (spec 12): the
 * connection, the running version, and whether an update is waiting.
 * `connection` is the shared connection line, which stays null while live and
 * through the first 2 seconds of a drop; `ready` tells those apart, so the
 * header says "Connecting…" rather than a connection it doesn't have. `check` is
 * the last evener/update/check answer, or null before one lands. A build the
 * hub can't update itself (applicable false: a dev build) says nothing about
 * updates. */
export function hubStatusLine(ready: boolean, connection: string | null, check: UpdateCheckResponse | null): string {
	const parts = [hubConnectionWord(ready, connection)];
	if (check?.currentVersion) parts.push(`evener ${check.currentVersion}`);
	// A failed channel lookup leaves applicable true and updateAvailable
	// false; it says nothing about whether this build is current.
	if (check?.applicable && !check.checkError) parts.push(check.updateAvailable ? "Update available" : "up to date");
	return parts.join(" · ");
}

/** The connected hub's state in words: the connection status when it has
 * something to say, else Connected or Connecting…. */
export function hubConnectionWord(ready: boolean, connection: string | null): string {
	return connection ?? (ready ? "Connected" : "Connecting…");
}

/** Which of hubConnectionWord's cases applies: the shared connection line
 * speaking ("line"), else connected or connecting. */
export function hubConnectionState(ready: boolean, connection: string | null): "line" | "connected" | "connecting" {
	if (connection) return "line";
	return ready ? "connected" : "connecting";
}

/** This app's version for About (spec 12): "0.1.0 (5)", the version with its
 * build. With one missing it shows the other, and with both, "Unknown". */
export function appVersionText(version: string | null, build: string | null): string {
	if (version && build) return `${version} (${build})`;
	return version || build || "Unknown";
}
