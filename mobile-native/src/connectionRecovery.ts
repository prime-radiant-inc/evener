import type { TerminalReason } from "../../cmd/evener-hub/frontend/src/protocol/client";

export type ConnectionFailureKind = "protocol" | "transport";

export function connectionFailure(terminalReason: TerminalReason): {
	kind: ConnectionFailureKind;
	message: string;
} {
	if (terminalReason === "protocol")
		return {
			kind: "protocol",
			message:
				"This app and hub need compatible versions. Update them together, then reconnect.",
		};
	return {
		kind: "transport",
		message:
			"Could not connect. Check the hub address, token, and network, then retry.",
	};
}
