import { expect, it } from "vitest";
import type { AuthStatusResponse, InstanceEntry } from "@evener/appwire-client";
import { authByProvider, providerStatus, signInKind } from "./providerStatus";

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
