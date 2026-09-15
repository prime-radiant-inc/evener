// credentialLabels.ts is the pure-logic half of the Credentials section
// (parity-m7-settings.md §7c, updated for the provider registry's instance
// wire shape - spec docs/superpowers/specs/2026-08-28-provider-registry-
// design.md §11.3): computing the credential display from InstanceEntry's
// activeSource/credentialRequired/auth fields and the providerId grouping -
// no rendering, no store access, easily unit-tested in isolation.
import { WireError } from "../../../../protocol/errors";
import type { AuthTestResponse, InstanceEntry } from "../../../../protocol/types.gen";

const STORED_KEY_LABEL = "Configured via stored API key";
const STORED_CREDENTIAL_JSON_LABEL = "Configured via stored credential JSON";

// storedLabel names the store layer for what the scheme reads from it: a
// credential JSON for gcp-adc, an API key for everything else.
function storedLabel(instance: InstanceEntry): string {
  return instance.auth === "gcp-adc" ? STORED_CREDENTIAL_JSON_LABEL : STORED_KEY_LABEL;
}

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
      return storedLabel(instance);
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
    layers.push({ source: "store", label: storedLabel(instance), effective: false });
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

// isEndpointConflict recognizes the hub's refusal of an asserted destination
// (appwire.Conflict: code -32013 with data.evenerErrorInfo "conflict"): the
// name no longer resolves where the client asserting it was told it does. Every
// credential flow presents this as a changed connection rather than a failure
// of the endpoint itself.
export function isEndpointConflict(err: unknown): boolean {
  return err instanceof WireError && err.evenerErrorInfo === "conflict";
}

// ENDPOINT_CHANGED_TEST_MESSAGE is what a credential test says when the hub
// refuses its asserted destination: the name moved since the listing the row
// was read from, so testing again has to start from the destination now on
// screen.
export const ENDPOINT_CHANGED_TEST_MESSAGE =
  "This connection changed to a different endpoint. Check its destination and test again.";

// FINGERPRINT_UNAVAILABLE_ERROR is what a credential write says when the row has
// a destination but serves no fingerprint: the hub accepts an empty assertion
// rather than validating it, so the save is refused locally with the same
// "review its destination" remedy as a moved endpoint - the listing that carries
// the fingerprint again is what makes the save work.
export const FINGERPRINT_UNAVAILABLE_ERROR =
  "The hub cannot check this endpoint right now, so the key was not sent. Review its destination and try again once it can be checked.";

// FINGERPRINT_UNAVAILABLE_TEST_MESSAGE is what a credential test says when the
// row has a destination but no fingerprint to assert: the hub would have
// nothing to compare and would dial whatever the name resolves to now, so the
// check is refused before it is sent (instanceDialogs refuses a write the same
// way).
export const FINGERPRINT_UNAVAILABLE_TEST_MESSAGE =
  "The hub cannot check this endpoint right now, so the test was not run. Review its destination and try again once it can be checked.";

// fingerprintUnavailable reports the row a credential test must not run for: it
// has a destination, and the listing could not key a fingerprint for it. A row
// with no destination - a provider without a base URL - is not this case, and
// keeps testing as before: there is nothing for the hub to check, which is the
// rule instanceDialogs applies to a write.
export function fingerprintUnavailable(row: InstanceEntry | undefined): boolean {
  return row !== undefined && (row.baseUrl ?? "") !== "" && (row.endpointFingerprint ?? "") === "";
}
