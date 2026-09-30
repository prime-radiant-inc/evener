import { expect, it } from "vitest";
import type { AuthStatusResponse, InstanceEntry } from "@evener/appwire-client";
import { authByProvider, providerStatus, signedInAccount, signInKind, statusOf } from "./providerStatus";

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

// The account a provider signs in with (settings audit M5): the hub's live
// status first, then the account its stored sign-in names.
it("names the signed-in account, from the live status first, then the stored sign-in", () => {
	const oauth = { activeSource: "oauth" };
	expect(signedInAccount(oauth, { email: "jesse@example.com", storedEmail: "old@example.com" })).toBe(
		"jesse@example.com",
	);
	expect(signedInAccount(oauth, { storedEmail: "old@example.com" })).toBe("old@example.com");
	expect(signedInAccount({ ...oauth, storedEmail: "row@example.com" }, undefined)).toBe("row@example.com");
	expect(signedInAccount({ ...oauth, storedEmail: " " }, { email: "" })).toBeNull();
	expect(signedInAccount(oauth, undefined)).toBeNull();
});

it("names the account without the padding around it", () => {
	expect(signedInAccount({ activeSource: "oauth" }, { email: "  jesse@example.com " })).toBe("jesse@example.com");
});

// Only a provider signed in with an account names one: a key that is in use
// leaves a stored sign-in's email behind unnamed.
it("names no account for a provider whose key is in use, even with a stored sign-in", () => {
	expect(
		signedInAccount({ activeSource: "store", storedEmail: "old@example.com" }, { storedEmail: "old@example.com" }),
	).toBeNull();
});
