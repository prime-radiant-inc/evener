import type { TerminalReason } from "@evener/appwire-client";

export type ConnectionFailureKind = "protocol" | "transport";

/** Spec 14's sentence for a close no retry can fix, in its one home: the
 * Board's notice, the Session bar's hint, a never-loaded page and the
 * connection's own failure copy all say it. */
export const INCOMPATIBLE_VERSIONS =
	"This app and the hub need compatible versions. Update the app from TestFlight, or update Evener on the hub.";

/** Why the connection closed, in words a person can act on. The app retries
 * a transport failure on its own (hubConnection.ts), so neither message asks
 * anyone to reconnect. */
export function connectionFailure(terminalReason: TerminalReason): {
	kind: ConnectionFailureKind;
	message: string;
} {
	if (terminalReason === "protocol") return { kind: "protocol", message: INCOMPATIBLE_VERSIONS };
	return {
		kind: "transport",
		message: "Couldn't reach the hub. Check its address, its token and your network.",
	};
}
