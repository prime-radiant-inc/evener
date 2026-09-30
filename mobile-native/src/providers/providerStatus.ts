// A provider's sign-in state in words (spec 12's Providers; ruling 6), from
// its instance row and evener/auth/list's status for it. "Expires in 3d"
// waits for the sign-in expiry the hub doesn't expose yet (S11b).
import type { AuthStatusResponse, InstanceEntry } from "@evener/appwire-client";

export interface ProviderStatus {
	word: "Sign-in expired" | "Signed in" | "Key set" | "Not signed in" | "No key" | "No sign-in needed";
	/** Only an expired sign-in needs a person: it is also the Board's notice. */
	tone: "ink" | "attention";
}

export function providerStatus(
	instance: Pick<InstanceEntry, "activeSource" | "authModes" | "credentialRequired">,
	auth: Pick<AuthStatusResponse, "needsLogin"> | undefined,
): ProviderStatus {
	if (auth?.needsLogin) return { word: "Sign-in expired", tone: "attention" };
	if (instance.activeSource === "oauth") return { word: "Signed in", tone: "ink" };
	if (instance.activeSource === "none") {
		if (!instance.credentialRequired) return { word: "No sign-in needed", tone: "ink" };
		return { word: instance.authModes?.includes("oauth") ? "Not signed in" : "No key", tone: "ink" };
	}
	return { word: "Key set", tone: "ink" };
}

/** A provider's state for the list, the Hub home and the detail: null while
 * the hub's statuses haven't been read and the provider signs in with an
 * account, whose "Signed in" or "Sign-in expired" only the statuses can tell
 * apart. A key's state is on its row. */
export function statusOf(
	instance: Pick<InstanceEntry, "name" | "activeSource" | "authModes" | "credentialRequired">,
	statuses: ReadonlyMap<string, Pick<AuthStatusResponse, "needsLogin">> | null,
): ProviderStatus | null {
	if (statuses === null && instance.activeSource === "oauth") return null;
	return providerStatus(instance, statuses?.get(instance.name));
}

export function authByProvider(statuses: readonly AuthStatusResponse[]): ReadonlyMap<string, AuthStatusResponse> {
	return new Map(statuses.map((status) => [status.provider, status]));
}

/** The account a provider signs in with, for its detail page: the hub's live
 * status's email, else the email its stored sign-in names (the status's, then
 * the instance row's), trimmed. Null when none names one, and for a provider
 * that isn't signed in with an account: a key in use leaves a stored
 * sign-in's email behind, which isn't the account it uses. */
export function signedInAccount(
	instance: Pick<InstanceEntry, "activeSource" | "storedEmail">,
	auth: Pick<AuthStatusResponse, "email" | "storedEmail"> | undefined,
): string | null {
	if (instance.activeSource !== "oauth") return null;
	for (const email of [auth?.email, auth?.storedEmail, instance.storedEmail]) {
		const trimmed = email?.trim();
		if (trimmed) return trimmed;
	}
	return null;
}

/** How a provider signs in, for its detail page: an account (OAuth), a key
 * (an API key or a credential file), or nothing. */
export function signInKind(instance: Pick<InstanceEntry, "authModes">): "Account" | "API key" | "None" {
	const modes = instance.authModes ?? [];
	if (modes.includes("oauth")) return "Account";
	if (modes.includes("apiKey") || modes.includes("credentialJson")) return "API key";
	return "None";
}
