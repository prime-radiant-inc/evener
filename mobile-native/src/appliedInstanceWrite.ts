import {
	isInstanceRemoveApplied,
	isInstanceRenamePersisted,
} from "@evener/appwire-client";

/** The provider-instance writes the hub reports as applied before a later step
 * failed: evener/instance/remove's ErrorInstanceRemoveApplied and
 * evener/instance/edit's ErrorInstanceRenamePersisted (appwire/errors.go).
 * Both are standing writes - the credential is gone, or providers.toml carries
 * the new name - so a client reconciles them rather than presenting them as
 * the operation's failure, whose retry would target an instance that no longer
 * answers to the name it was sent for. */
export type AppliedInstanceWrite = "remove" | "rename";

/** appliedInstanceWrite classifies a rejection as one of those standing
 * writes, or null for every ordinary failure. The appwire client's own
 * discriminator helpers are the one definition of the wire strings, so this
 * matches on data.evenerErrorInfo and never on a message or a code. */
export function appliedInstanceWrite(
	err: unknown,
): AppliedInstanceWrite | null {
	if (isInstanceRemoveApplied(err)) return "remove";
	if (isInstanceRenamePersisted(err)) return "rename";
	return null;
}
