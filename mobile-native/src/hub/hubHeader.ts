import type { UpdateCheckResponse } from "@evener/appwire-client";

/** The line under the hub's name at the top of the Hub (spec 12): the
 * connection, the running version, and whether an update is waiting.
 * `connection` is the shared connection line (null while live); `check` is
 * the last evener/update/check answer, or null before one lands. A build the
 * hub can't update itself (applicable false: a dev build) says nothing about
 * updates. */
export function hubStatusLine(connection: string | null, check: UpdateCheckResponse | null): string {
	const parts = [connection ?? "Connected"];
	if (check?.currentVersion) parts.push(`evener ${check.currentVersion}`);
	if (check?.applicable) parts.push(check.updateAvailable ? "Update available" : "up to date");
	return parts.join(" · ");
}
