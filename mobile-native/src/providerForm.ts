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
/** The instance fields an edit reads off a row. */
interface EditableInstance {
  name: string;
  baseUrl?: string;
  endpointFingerprint?: string;
}

/** editProviderParams carries the endpoint fingerprint the row was served with,
 * so the hub can refuse an edit whose name another client re-pointed between
 * that listing and the RPC
 * (appwire.InstanceEditParams.ExpectedEndpointFingerprint). A row the hub could
 * not key serves no fingerprint and asserts nothing, which the hub accepts. */
export function editProviderParams(
  instance: EditableInstance,
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

/** openedEditParams is editProviderParams over the row the editor was OPENED on,
 * falling back to the row the listing holds now when nothing was opened (a
 * create). The listing refreshes under an open editor on every
 * evener/auth/updated broadcast - including the hub's own instance-edit
 * broadcasts - so a fingerprint read live at save time would swap the assertion
 * out from under a stale draft and let the save write onto the replacement
 * instead of being refused. Null when there is no row at all. */
export function openedEditParams(
  opened: EditableInstance | undefined,
  current: EditableInstance | undefined,
  value: string,
): InstanceEditParams | null {
  const row = opened ?? current;
  return row === undefined ? null : editProviderParams(row, value);
}
