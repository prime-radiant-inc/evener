import { expect, it } from "vitest";
import type { AuthStatusResponse, InstanceEntry } from "@evener/appwire-client";
import { authByProvider, providerStatus, signInKind, statusOf } from "./providerStatus";

type Facts = Pick<InstanceEntry, "activeSource" | "authModes" | "credentialRequired">;
const instance = (over: Partial<Facts>): Facts => ({
	activeSource: "none",
	authModes: [],
	credentialRequired: true,
	...over,
});

const cases: [Partial<Facts>, Pick<AuthStatusResponse, "needsLogin"> | undefined, string, string][] = [
	[{ activeSource: "oauth", authModes: ["oauth"] }, { needsLogin: true }, "Sign-in expired", "attention"],
	[{ activeSource: "oauth", authModes: ["oauth"] }, { needsLogin: false }, "Signed in", "ink"],
	[{ activeSource: "oauth", authModes: ["oauth"] }, undefined, "Signed in", "ink"],
	[{ activeSource: "store", authModes: ["apiKey"] }, undefined, "Key set", "ink"],
	[{ activeSource: "env:LUNAROUTE_API_KEY", authModes: ["apiKey"] }, undefined, "Key set", "ink"],
	[{ activeSource: "api_key" }, undefined, "Key set", "ink"],
	[{ activeSource: "adc", authModes: ["credentialJson"] }, undefined, "Key set", "ink"],
	[{ activeSource: "none", authModes: ["oauth"] }, undefined, "Not signed in", "ink"],
	[{ activeSource: "none", authModes: ["apiKey"] }, undefined, "No key", "ink"],
	[{ activeSource: "none", credentialRequired: false }, undefined, "No sign-in needed", "ink"],
];

it.each(cases)("%o with %o → %s", (over, auth, word, tone) => {
	expect(providerStatus(instance(over), auth)).toEqual({ word, tone });
});

it("indexes the hub's sign-in statuses by provider", () => {
	const statuses = [
		{ provider: "codex-jesse-fsck.com", needsLogin: true },
		{ provider: "lunaroute" },
	] as AuthStatusResponse[];
	expect(authByProvider(statuses).get("codex-jesse-fsck.com")?.needsLogin).toBe(true);
	expect(authByProvider(statuses).has("meta")).toBe(false);
});

it("says how a provider signs in", () => {
	expect(signInKind({ authModes: ["oauth", "apiKey"] })).toBe("Account");
	expect(signInKind({ authModes: ["apiKey"] })).toBe("API key");
	expect(signInKind({ authModes: ["credentialJson"] })).toBe("API key");
	expect(signInKind({ authModes: [] })).toBe("None");
});

it("says nothing of an account sign-in until the hub's statuses have been read", () => {
	// An expired sign-in would otherwise read "Signed in" from the row alone.
	const oauth = { ...instance({ activeSource: "oauth", authModes: ["oauth"] }), name: "codex" };
	expect(statusOf(oauth, null)).toBeNull();
	expect(statusOf(oauth, new Map())).toEqual({ word: "Signed in", tone: "ink" });
	expect(statusOf(oauth, authByProvider([{ provider: "codex", needsLogin: true } as AuthStatusResponse]))).toEqual({
		word: "Sign-in expired",
		tone: "attention",
	});
	// A key's state is on the row itself.
	const key = { ...instance({ activeSource: "store", authModes: ["apiKey"] }), name: "lunaroute" };
	expect(statusOf(key, null)).toEqual({ word: "Key set", tone: "ink" });
});

// The hub reports a credential the provider rejected as the status's error
// (#3539): the row reads "Error" in red, with the hub's sentence for the
// detail. An expired sign-in still reads as that, since signing in is the fix.
it("reads a rejected credential as Error", () => {
	const error = "The provider rejected this credential (HTTP 401). Replace the key or sign in again.";
	expect(providerStatus(instance({ activeSource: "store", authModes: ["apiKey"] }), { error })).toEqual({
		word: "Error",
		tone: "danger",
		detail: error,
	});
	expect(
		providerStatus(instance({ activeSource: "oauth", authModes: ["oauth"] }), { needsLogin: true, error }),
	).toEqual({ word: "Sign-in expired", tone: "attention" });
	const key = { ...instance({ activeSource: "store", authModes: ["apiKey"] }), name: "lunaroute" };
	expect(statusOf(key, authByProvider([{ provider: "lunaroute", error } as AuthStatusResponse]))).toEqual({
		word: "Error",
		tone: "danger",
		detail: error,
	});
});
