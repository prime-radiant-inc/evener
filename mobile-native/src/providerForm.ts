import type {
  InstanceCreateParams,
  InstanceEditParams,
  ProviderDescriptor,
} from "@evener/appwire-client";
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
/** editProviderParams carries the endpoint fingerprint the editor displayed, so
 * the hub can refuse an edit whose name another client re-pointed between the
 * listing this editor was opened from and the RPC
 * (appwire.InstanceEditParams.ExpectedEndpointFingerprint). A row the hub could
 * not key serves no fingerprint and asserts nothing, which the hub accepts. */
export function editProviderParams(
  instance: { name: string; baseUrl?: string; endpointFingerprint?: string },
  value: string,
): InstanceEditParams {
  const params: InstanceEditParams = { name: instance.name };
  if (instance.endpointFingerprint)
    params.expectedEndpointFingerprint = instance.endpointFingerprint;
  const baseUrl = value.trim();
  if (baseUrl === (instance.baseUrl || "")) return params;
  if (baseUrl) params.baseUrl = baseUrl;
  else params.clearBaseUrl = true;
  return params;
}
