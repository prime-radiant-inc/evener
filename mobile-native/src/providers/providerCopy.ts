// What the provider screens say in their own words (spec 5: what happened and
// the one thing to do, never the plumbing between). The hub's rejection text
// can echo submitted credentials, so none of it reaches the screen; these
// sentences stand in for it.
import { endpointMoved, endpointUncheckable } from "@evener/appwire-client";

/** A write the hub didn't answer for: it may have landed, so nothing says
 * it failed. */
export const UNCONFIRMED_CHANGE = "The hub didn't confirm the change. Check the provider list and try again.";

/** The same for a key or a credential JSON. */
export const UNCONFIRMED_CREDENTIAL =
	"The hub didn't confirm the credential was saved. Check the provider's status and try again.";

/** A removal or rename the hub applied before a later step failed: it stands,
 * so the list is what to check, not the action to retry. */
export function appliedButFailed(done: "removed" | "renamed"): string {
	return `The provider was ${done}, but a later step failed. Check the list.`;
}

/** The hub refused the destination the action asserted. */
export const ENDPOINT_CHANGED_WARNING = endpointMoved("nothing was changed");

/** No fingerprint to assert: a clear or a removal, which sends no key. */
export const FINGERPRINT_UNAVAILABLE_ACTION_MESSAGE = endpointUncheckable("nothing was changed");

/** The same for a credential save, neutral about which kind. */
export const FINGERPRINT_UNAVAILABLE_CREDENTIAL_MESSAGE = endpointUncheckable("nothing was saved");

/** The listing's failure, ahead of the reason. */
export const PROVIDERS_NOT_LOADED = "Couldn't load the providers";
