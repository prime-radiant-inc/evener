// credentialLabels.ts is the pure-logic half of the Credentials section
// (parity-m7-settings.md §7c, updated for the provider registry's instance
// wire shape - spec docs/superpowers/specs/2026-08-28-provider-registry-
// design.md §11.3): computing the credential display from InstanceEntry's
// activeSource/credentialRequired/auth fields and the providerId grouping -
// no rendering, no store access, easily unit-tested in isolation.
import type { AuthTestResponse, InstanceEntry } from "../../../../protocol/types.gen";

const STORED_KEY_LABEL = "Configured via stored API key";

export interface CredentialLayerView {
  source: string;
  label: string;
  effective: boolean;
}

// activeSourceLabel is the single source of truth for every ActiveSource
// value the registry sends (spec §11.3's vocabulary: api_key |
// credential_headers | store | env:<VAR> | oauth | adc | none). "env:<VAR>"
// carries its variable name in the string itself, so it is matched by
// prefix, not an exact value. "none" - nothing currently resolves - splits
// three ways on credentialRequired and the instance's own auth scheme,
// since a scheme that never wants a credential (auth: none) reads
// differently from one that merely allows an optional one (optional-
// bearer) or one that plainly lacks a required key.
export function activeSourceLabel(instance: InstanceEntry): string {
  const source = instance.activeSource;
  if (source.startsWith("env:")) return `Configured via environment variable (${source.slice(4)})`;
  switch (source) {
    case "api_key":
      return "Configured via providers.toml";
    case "credential_headers":
      return "Configured via a credential header";
    case "store":
      return STORED_KEY_LABEL;
    case "oauth":
      return instance.storedEmail ? `Configured via OAuth (${instance.storedEmail})` : "Configured via OAuth";
    case "adc":
      return "Configured via Application Default Credentials";
    case "none":
      if (instance.credentialRequired) return "Not configured";
      return instance.auth === "none" ? "No credentials required" : "No key set · optional";
    default:
      return source;
  }
}

// credentialLayers lists the credential line(s) the detail sheet shows: the
// effective source first, then any credential this instance holds that the
// resolution passed over (spec §10: api_key > credential_headers > store >
// env). hasStoredFile is read straight from the credential store
// (instanceStatus, cmd/evener-hub/app_auth.go), independently of what won,
// so a stored key behind providers.toml or a credential header renders as
// shadowed. shadowedEnvVar answers the same question for the environment
// layer: activeSource only ever names the winner, so a set-but-losing
// variable has no other way onto the wire - the hub reports it separately
// (issue #712). The two can coexist (an api_key can shadow both a stored
// key and a set variable at once). Empty when nothing has ever resolved
// (activeSource "none"); see activeSourceLabel for that case's own message.
export function credentialLayers(instance: InstanceEntry): CredentialLayerView[] {
  if (instance.activeSource === "none") return [];
  const layers: CredentialLayerView[] = [
    { source: instance.activeSource, label: activeSourceLabel(instance), effective: true },
  ];
  if (instance.hasStoredFile && instance.activeSource !== "store") {
    layers.push({ source: "store", label: STORED_KEY_LABEL, effective: false });
  }
  if (instance.shadowedEnvVar) {
    layers.push({
      source: `env:${instance.shadowedEnvVar}`,
      label: `Configured via environment variable (${instance.shadowedEnvVar})`,
      effective: false,
    });
  }
  return layers;
}

// keylessByDesign: the instance holds no credential and none is wanted -
// the hub's credentialRequired gate (InstanceEntry, appwire/types.go) says
// there is nothing to look for, as with an auth-none provider or a gateway
// on the optional-bearer scheme. Both halves of the display key on this one
// bit: the words activeSourceLabel returns and the heading's status dot,
// which otherwise disagreed about the same instance.
export function keylessByDesign(instance: InstanceEntry): boolean {
  return instance.activeSource === "none" && !instance.credentialRequired;
}

// unconfiguredLabel: the single-line message shown INSTEAD of the layered
// display when credentialLayers(instance) is empty - just activeSourceLabel
// for the "none" case, which already covers required vs. optional vs.
// never-wanted.
export function unconfiguredLabel(instance: InstanceEntry): string | null {
  return instance.activeSource === "none" ? activeSourceLabel(instance) : null;
}

// styleInfoText is an instance's endpoint in one line: protocol has no
// omitempty on the wire, so there is always something to show.
export function styleInfoText(instance: InstanceEntry): string {
  return instance.baseUrl ? `${instance.protocol} · base ${instance.baseUrl}` : instance.protocol;
}

export interface InstanceProviderGroup {
  providerId: string;
  instances: InstanceEntry[];
}

// groupByProvider groups instances by their registry providerId, in
// first-seen order from the RPC response - never re-sorted client-side
// (parity-m7-settings.md §7b). providerId, not `base`, is the grouping
// key: `base` is blank whenever an instance's own name already is the
// registry id (InstanceEntry, appwire/types.go), so a custom-named
// instance built on a curated provider (base: "groq") lands in the SAME
// group as that provider's own implicit instance, not a group of its own.
export function groupByProvider(instances: InstanceEntry[]): InstanceProviderGroup[] {
  const groups: InstanceProviderGroup[] = [];
  const byProvider = new Map<string, InstanceProviderGroup>();
  for (const instance of instances) {
    let group = byProvider.get(instance.providerId);
    if (!group) {
      group = { providerId: instance.providerId, instances: [] };
      byProvider.set(instance.providerId, group);
      groups.push(group);
    }
    group.instances.push(instance);
  }
  return groups;
}

// ENDPOINT_FAILURE_MESSAGE is both the endpoint_failure status's own words
// and what an unrecognized status falls back to, so it is named once.
const ENDPOINT_FAILURE_MESSAGE =
  "The provider endpoint could not be reached. Check the endpoint and network connection.";
const CREDENTIAL_TEST_MESSAGES: Record<string, string> = {
  success: "Credentials verified.",
  missing: "No credentials are configured for this instance. Add a key or sign in first.",
  auth_rejected: "The provider rejected these credentials. Replace the key or sign in again.",
  endpoint_failure: ENDPOINT_FAILURE_MESSAGE,
  configuration_failure: "Provider configuration could not be loaded. Check the instance settings.",
  unsupported: "This provider does not support harmless credential verification.",
};

export function safeCredentialTestResult(provider: string, response: AuthTestResponse): AuthTestResponse {
  const message = CREDENTIAL_TEST_MESSAGES[response.status];
  if (message) return { provider, status: response.status, message };
  return { provider, status: "endpoint_failure", message: ENDPOINT_FAILURE_MESSAGE };
}

export function safeCredentialTestMessage(status: string): string {
  return CREDENTIAL_TEST_MESSAGES[status] ?? ENDPOINT_FAILURE_MESSAGE;
}
