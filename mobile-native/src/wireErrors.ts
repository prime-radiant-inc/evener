// What a hub's refusal means, for a method the phone can live without. An
// older hub or daemon that doesn't know a method answers JSON-RPC's
// MethodNotFound, which is the phone's cue to keep its fallback.
import { WireError } from "@evener/appwire-client";

const METHOD_NOT_FOUND = -32601;

/** The hub doesn't know the method: an older hub or daemon. */
export function isMethodNotFound(error: unknown): boolean {
	return error instanceof WireError && (error.code === METHOD_NOT_FOUND || error.evenerErrorInfo === "methodNotFound");
}
