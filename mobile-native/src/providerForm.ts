import type { InstanceCreateParams, InstanceEditParams, ProviderDescriptor } from "@evener/appwire-client";
export interface ProviderDraft {
	name: string;
	base: string;
	baseUrl: string;
	vars: Record<string, string>;
	apiKeyEnv: string;
	credentialHeader: string;
}
export function createProviderParams(draft: ProviderDraft, providers: ProviderDescriptor[]): InstanceCreateParams {
	const provider = providers.find((item) => item.id === draft.base);
	if (!provider) throw new Error("Select an available base provider.");
	const name = draft.name.trim();
	if (!name) throw new Error("Name is required.");
	const credentialHeader = draft.credentialHeader.trim();
	if (credentialHeader && !credentialHeader.includes("$"))
		throw new Error("Credential header must reference a $VARIABLE or run a $(command), never a literal secret.");
	const entries = Object.keys(provider.vars ?? {})
		.map((key) => [key, draft.vars[key]?.trim() ?? ""] as const)
		.filter(([, value]) => value);
	return {
		name,
		base: provider.id,
		baseUrl: draft.baseUrl.trim(),
		vars: entries.length ? Object.fromEntries(entries) : undefined,
		apiKeyEnv: draft.apiKeyEnv.trim() || undefined,
		credentialHeader: credentialHeader || undefined,
	};
}
export function editProviderParams(
	instance: { name: string; baseUrl?: string; endpointFingerprint?: string },
	value: string,
): InstanceEditParams {
	const baseUrl = value.trim();
	const params: InstanceEditParams =
		baseUrl === (instance.baseUrl || "")
			? { name: instance.name }
			: baseUrl
				? { name: instance.name, baseUrl }
				: { name: instance.name, clearBaseUrl: true };
	// The endpoint the row resolved to when this editor was opened travels with
	// the save as the assertion the hub checks: a name another client has
	// re-pointed since must not have its replacement edited. A row the hub could
	// not fingerprint asserts nothing.
	if (instance.endpointFingerprint) params.expectedEndpointFingerprint = instance.endpointFingerprint;
	return params;
}

/** Whether a draft differs from the one the editor opened with, field by
 * field, as the saved request would: spaces around a value, or a variable
 * typed and cleared again, are no change. */
export function draftChanged(opened: ProviderDraft, draft: ProviderDraft): boolean {
	// Compared as the request would carry them: trimmed, and a variable left
	// blank is unset.
	const setVars = (vars: Record<string, string>) =>
		Object.entries(vars)
			.map(([key, value]) => [key, value.trim()] as const)
			.filter(([, value]) => value !== "")
			.sort(([a], [b]) => a.localeCompare(b));
	const fields = ["name", "base", "baseUrl", "apiKeyEnv", "credentialHeader"] as const;
	return (
		fields.some((field) => opened[field].trim() !== draft[field].trim()) ||
		JSON.stringify(setVars(opened.vars)) !== JSON.stringify(setVars(draft.vars))
	);
}
