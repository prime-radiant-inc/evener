// credentialLabels.ts is the pure-logic half of the Credentials section
// (parity-m7-settings.md §7c): computing the layered credential display
// (oauth > file > env precedence, effective vs. shadowed) and the type
// grouping, from InstanceEntry's boolean/string fields - no rendering, no
// store access, easily unit-tested in isolation.
import type { AuthTestResponse, InstanceEntry } from "../../../../protocol/types.gen";

export type CredentialSourceKind = "oauth" | "file" | "env";

export interface CredentialLayerView {
  source: CredentialSourceKind;
  label: string;
  effective: boolean;
}

const SOURCE_LABEL: Record<CredentialSourceKind, string> = {
  oauth: "Configured via OAuth",
  file: "Configured via stored API key",
  env: "Configured via environment variable",
};

// credentialLayers lists every credential layer PRESENT on `instance`, in
// fixed precedence order oauth > file > env - the first entry is always the
// effective one (an instance can carry an OAuth sign-in shadowing a stored
// key, e.g. OpenAI). Empty when nothing is configured at all (see
// unconfiguredLabel for that case's own message).
export function credentialLayers(instance: InstanceEntry): CredentialLayerView[] {
  const layers: CredentialLayerView[] = [];
  if (instance.hasStoredOAuth) {
    layers.push({
      source: "oauth",
      label: instance.storedEmail ? `${SOURCE_LABEL.oauth} (${instance.storedEmail})` : SOURCE_LABEL.oauth,
      effective: false,
    });
  }
  if (instance.hasStoredFile) layers.push({ source: "file", label: SOURCE_LABEL.file, effective: false });
  if (instance.envVar) layers.push({ source: "env", label: SOURCE_LABEL.env, effective: false });
  const first = layers[0];
  if (first) first.effective = true;
  return layers;
}

// unconfiguredLabel: the single-line message shown INSTEAD of the layered
// display when credentialLayers(instance) is empty - mirrors the legacy's
// sourceLabel() lookup for the absent/none/unknown cases (file/env/oauth are
// unreachable here, since any of those being true would make
// credentialLayers non-empty).
export function unconfiguredLabel(instance: InstanceEntry): string | null {
  if (credentialLayers(instance).length > 0) return null;
  switch (instance.activeSource) {
    case "none":
      return "No credentials required";
    case "absent":
      return "Not configured";
    default:
      return instance.activeSource || "Not configured";
  }
}

export interface InstanceTypeGroup {
  type: string;
  instances: InstanceEntry[];
}

// groupByType groups instances by `.type`, in first-seen order from the RPC
// response - never re-sorted client-side (parity-m7-settings.md §7b).
export function groupByType(instances: InstanceEntry[]): InstanceTypeGroup[] {
  const groups: InstanceTypeGroup[] = [];
  const byType = new Map<string, InstanceTypeGroup>();
  for (const instance of instances) {
    let group = byType.get(instance.type);
    if (!group) {
      group = { type: instance.type, instances: [] };
      byType.set(instance.type, group);
      groups.push(group);
    }
    group.instances.push(instance);
  }
  return groups;
}

const CREDENTIAL_TEST_MESSAGES: Record<string, string> = {
  success: "Credentials verified.",
  missing: "No credentials are configured for this instance. Add a key or sign in first.",
  auth_rejected: "The provider rejected these credentials. Replace the key or sign in again.",
  endpoint_failure: "The provider endpoint could not be reached. Check the endpoint and network connection.",
  configuration_failure: "Provider configuration could not be loaded. Check the instance settings.",
  unsupported: "This provider does not support harmless credential verification.",
};
const ENDPOINT_FAILURE_MESSAGE = "The provider endpoint could not be reached. Check the endpoint and network connection.";

export function safeCredentialTestResult(provider: string, response: AuthTestResponse): AuthTestResponse {
  const message = CREDENTIAL_TEST_MESSAGES[response.status];
  if (message) return { provider, status: response.status, message };
  return { provider, status: "endpoint_failure", message: ENDPOINT_FAILURE_MESSAGE };
}

export function safeCredentialTestMessage(status: string): string {
  return CREDENTIAL_TEST_MESSAGES[status] ?? ENDPOINT_FAILURE_MESSAGE;
}
