import type {
  InstanceCreateParams,
  InstanceEditParams,
  ProviderDescriptor,
} from "../../appwire-client/typescript/types.gen";
export interface ProviderDraft {
  name: string;
  base: string;
  baseUrl: string;
  vars: Record<string, string>;
  apiKeyEnv: string;
  credentialHeader: string;
}
export function createProviderParams(
  draft: ProviderDraft,
  providers: ProviderDescriptor[],
): InstanceCreateParams {
  const provider = providers.find((item) => item.id === draft.base);
  if (!provider) throw new Error("Select an available base provider.");
  const name = draft.name.trim();
  if (!name) throw new Error("Name is required.");
  const credentialHeader = draft.credentialHeader.trim();
  if (credentialHeader && !credentialHeader.includes("$"))
    throw new Error(
      "Credential header must reference a $VARIABLE, never a literal secret.",
    );
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
  instance: { name: string; baseUrl?: string },
  value: string,
): InstanceEditParams {
  const baseUrl = value.trim();
  if (baseUrl === (instance.baseUrl || "")) return { name: instance.name };
  return baseUrl
    ? { name: instance.name, baseUrl }
    : { name: instance.name, clearBaseUrl: true };
}
