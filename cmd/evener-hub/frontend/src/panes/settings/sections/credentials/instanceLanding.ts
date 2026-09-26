// Whether a save the credentials store superseded still landed as this sheet
// declared it. The store discards a save's own response when a refresh that
// started after it answers first, so InstanceSheet judges the landing from the
// store's current listing instead: renamedInstanceLanded for a rename,
// supersededSaveLanded for a plain save. Both compare the listed row against
// the entry the draft was seeded from, the params the save sent, and the
// mutation's own discarded answer (`authoritative`). Pure functions, so every
// matching rule is pinned directly by instanceLanding.test.ts.

import type { InstanceEditParams, InstanceEntry } from "@evener/appwire-client";

// The entry fields a rename carries over unchanged, and that the store's own
// listing can be compared on. The name alone cannot identify a rename - a
// removal and a recreation under the same name, or another instance renamed
// onto the freed one, both leave an entry there - so a superseded rename only
// belongs to this save when the fields this save did not touch still match.
// endpointFingerprint is a derived field, not an independent fact: the hub
// digests the resolved endpoint, so a request that edits what the digest is
// resolved from necessarily changes it. It can only stand in for the endpoint
// when those fields are not part of the request (see ENDPOINT_AFFECTING_FIELDS).
const RENAME_IDENTITY_FIELDS = [
  "providerId",
  "base",
  "baseUrl",
  "protocol",
  "surface",
  "auth",
  "endpointFingerprint",
  "vars",
  "apiKeyEnv",
  "credentialHeader",
] as const;

/** The request fields the listing's endpointFingerprint is resolved from: the
 * hub digests the transport's resolved endpoint, which baseUrl (the URL
 * override), vars ({VAR} substitution), and the head record selected by
 * protocol/surface each feed. Editing any of them derives a new digest, so the
 * digest cannot be compared as an untouched identity field across such a save. */
const ENDPOINT_AFFECTING_FIELDS = ["baseUrl", "vars", "protocol", "surface"] as const;

/** Every `clear*` flag InstanceEditParams carries, and the entry field each one
 * empties. An explicit map, not a derived "lowercase the first letter"
 * conversion: the params type carries more than one capitalised field name
 * (clearApiKeyEnv among them), so a derivation is one mis-cased flag away from
 * producing a field name the listing never carries and silently disabling the
 * identity comparison that decides whether a rename landed. Typed as a total
 * Record over the flags the type carries, so adding a clear flag to
 * InstanceEditParams fails to compile here until it is mapped. */
type ClearFlagKey = Extract<keyof InstanceEditParams, `clear${string}`>;
export const CLEAR_FIELD_NAMES: Record<ClearFlagKey, string> = {
  clearBaseUrl: "baseUrl",
  clearProtocol: "protocol",
  clearSurface: "surface",
  clearApiKeyEnv: "apiKeyEnv",
  clearCredentialHeader: "credentialHeader",
};

/** The entry fields this save's params changed, whether a field carries a value
 * or a `clear` flag: those are the fields a rename may legitimately differ in. */
export function changedFields(params: InstanceEditParams): Set<string> {
  const changed = new Set<string>();
  for (const key of Object.keys(params)) {
    if (key === "name" || key === "newName") continue;
    changed.add(key in CLEAR_FIELD_NAMES ? CLEAR_FIELD_NAMES[key as ClearFlagKey] : key);
  }
  return changed;
}

/** One entry field as the listing carries it, so two entries can be compared:
 * scalars by value, vars by content rather than by key order. */
export function fieldValue(entry: InstanceEntry, field: string): string {
  const raw = (entry as unknown as Record<string, unknown>)[field];
  if (raw === undefined || raw === null) return "";
  if (typeof raw === "object") {
    const pairs = Object.entries(raw as Record<string, unknown>).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
    return JSON.stringify(pairs);
  }
  return String(raw);
}

/** Whether a rename carried `base` over unchanged. Renaming a curated-shadow
 * instance pins base to the old name: the entry owned the name of the curated
 * provider it shadowed, and that name is what its configuration is inherited
 * through, so the hub writes the old name into base before the name is gone.
 * The rename therefore carries base over as this save's own pinning - before
 * had no base and the listing's base is the name the instance just left - and
 * any other difference in base is a different instance. base is wire-
 * serialized with omitempty, so an empty base arrives absent and fieldValue
 * normalizes it to the empty value this comparison is written for. */
export function baseCarriedByRename(before: InstanceEntry, listed: InstanceEntry): boolean {
  const beforeBase = fieldValue(before, "base");
  if (beforeBase === fieldValue(listed, "base")) return true;
  return beforeBase === "" && listed.base === before.name;
}

/** Whether the variables the store's listing carries are the ones this save
 * left alone. A save's params name only the variables it changed - the wire
 * leaves every other authored variable untouched - so comparing `vars` as one
 * field cannot see an untouched variable's change and would mistake a
 * different instance for this save's own landing. Compare key-by-key across
 * both sides, skipping only the keys this save declared. */
function varsCarriedOver(before: InstanceEntry, listed: InstanceEntry, params: InstanceEditParams): boolean {
  const declared = params.vars ?? {};
  const keys = new Set([...Object.keys(before.vars ?? {}), ...Object.keys(listed.vars ?? {})]);
  for (const key of keys) {
    if (key in declared) continue;
    if ((before.vars?.[key] ?? "") !== (listed.vars?.[key] ?? "")) return false;
  }
  return true;
}

/** Whether the listed entry carries every identity field this save did not
 * touch - the confirmation a superseded rename and a superseded plain save
 * share. `vars` is compared key-by-key (see varsCarriedOver); the derived
 * fields are not compared when this save edited a field they derive from
 * (ENDPOINT_AFFECTING_FIELDS): `endpointFingerprint` is the digest of the
 * resolved endpoint, and `baseUrl` is the RESOLVED URL (the hub substitutes
 * this instance's vars into the provider's template), so editing a var or a
 * URL-affecting field moves both. `base` is the one field whose comparison
 * differs by path - a rename may legitimately pin it, a plain save may not -
 * so the caller supplies that comparison. */
export function untouchedIdentityMatches(
  before: InstanceEntry,
  listed: InstanceEntry,
  params: InstanceEditParams,
  matchesBase: (before: InstanceEntry, listed: InstanceEntry) => boolean,
): boolean {
  const changed = changedFields(params);
  const endpointChanged = ENDPOINT_AFFECTING_FIELDS.some((field) => changed.has(field));
  const untouched = RENAME_IDENTITY_FIELDS.filter(
    (field) =>
      field !== "vars" &&
      !changed.has(field) &&
      !(endpointChanged && (field === "endpointFingerprint" || field === "baseUrl")),
  );
  const matches = untouched.every((field) =>
    field === "base" ? matchesBase(before, listed) : fieldValue(before, field) === fieldValue(listed, field),
  );
  return matches && varsCarriedOver(before, listed, params);
}

/** The credential header reduced the way the hub stores it: the hub splits on
 * the first `=` and trims the name and value before joining them with no
 * surrounding spaces (app_instances.go's credentialHeaderFrom), so a declared
 * `Authorization = Bearer $X` has to be reduced the same way to match the
 * listing's normalized form. */
function normalizedCredentialHeader(raw: string): string {
  const eq = raw.indexOf("=");
  if (eq < 0) return raw.trim();
  return `${raw.slice(0, eq).trim()}=${raw.slice(eq + 1).trim()}`;
}

/** The name this save's rename landed under, or undefined when the store's own
 * listing cannot say that it did: the new name has to be held by the instance
 * this save renamed - matching on the fields this save left alone and carrying
 * the values it declared - not by a later tenant of the freed name that
 * differs in a field this rename also edited.
 *
 * The confirmation requires the mutation's captured new-name row to carry an
 * endpointFingerprint and the listing to carry the same one: that is the only
 * proof of the renamed destination (the sanitized display URL cannot show its
 * hidden parts). Without a fingerprint there is nothing to compare against, so
 * the rename fails closed exactly like the plain path - the user gets a
 * stale-save warning and a reseed rather than the sheet steering onto a name
 * that might be a replacement. */
export function renamedInstanceLanded(
  instances: InstanceEntry[],
  before: InstanceEntry,
  params: InstanceEditParams,
  authoritative: InstanceEntry | undefined,
): string | undefined {
  const newName = params.newName;
  if (newName === undefined) return undefined;
  const listed = instances.find((instance) => instance.name === newName);
  if (listed === undefined) return undefined;
  // Every successful rename authors an entry under the new name
  // (hubInstancesController.Edit writes [providers.<newName>]), so a landed row
  // is never implicit: an implicit row holding the new name is a curated
  // provider the environment (or another client's later change) re-derived
  // there, not this rename's result, and steering the sheet onto it would
  // title one instance with another's values. Reject any implicit landing.
  if (listed.implicit) return undefined;
  if (!untouchedIdentityMatches(before, listed, params, baseCarriedByRename)) return undefined;
  // A capture with no fingerprint cannot prove the renamed destination, so the
  // shared confirmation fails closed and the caller keeps the stale-save path.
  return authoritativeLandingConfirmed(authoritative, listed, params) ? newName : undefined;
}

/** The confirmation shared by the plain-save and rename paths when the
 * mutation's captured row carries an endpoint fingerprint: the listing's
 * fingerprint must equal it, endpoint clears fail closed, and the fields the
 * fingerprint does not cover (credentials, surface, declared vars) must match.
 * One copy, so the two paths cannot drift. */
function authoritativeLandingConfirmed(
  authoritative: InstanceEntry | undefined,
  listed: InstanceEntry,
  params: InstanceEditParams,
): boolean {
  const authoritativeFingerprint = authoritative?.endpointFingerprint ?? "";
  if (authoritativeFingerprint === "") return false;
  if (listed.endpointFingerprint !== authoritativeFingerprint) return false;
  if (params.clearBaseUrl || params.clearProtocol || params.clearSurface) return false;
  if (!authoritativeCredentialsMatch(authoritative, listed)) return false;
  if (!credentialValuesLanded(listed, params)) return false;
  return declaredVarsAndSurfaceLanded(listed, params);
}

/** Whether the listed entry carries the non-endpoint values this save declared
 * for fields the endpoint fingerprint does not cover. The hub's destination
 * identity excludes `surface` and the variables that do not feed the endpoint,
 * so a matching fingerprint alone cannot confirm them; each declared value has
 * to match the listing directly. */
function declaredVarsAndSurfaceLanded(listed: InstanceEntry, params: InstanceEditParams): boolean {
  if (params.protocol !== undefined && listed.protocol !== params.protocol) return false;
  if (params.surface !== undefined && (listed.surface ?? "") !== params.surface) return false;
  for (const [key, value] of Object.entries(params.vars ?? {})) {
    if ((listed.vars?.[key] ?? "") !== value) return false;
  }
  return true;
}

/** Whether the listed entry carries the credential fields this save declared.
 * The endpoint fingerprint proves the DESTINATION, not the credential
 * metadata: apiKeyEnv and credentialHeader are authored fields the listing
 * serves directly (or omits), and a concurrent write can carry the same
 * endpoint but different credential metadata. A declared value has to match
 * after the hub's normalization; a credential clear is unverifiable (the hub
 * omits an authored value it cannot serve, whether this save cleared it or a
 * replacement insists on its own), so it always fails closed. */
function credentialValuesLanded(listed: InstanceEntry, params: InstanceEditParams): boolean {
  if (params.clearApiKeyEnv || params.clearCredentialHeader) return false;
  if (params.apiKeyEnv !== undefined && (listed.apiKeyEnv ?? "") !== params.apiKeyEnv) return false;
  if (
    params.credentialHeader !== undefined &&
    normalizedCredentialHeader(listed.credentialHeader ?? "") !== normalizedCredentialHeader(params.credentialHeader)
  ) {
    return false;
  }
  return true;
}

/** The entry the store's own listing carries for a plain (non-rename) save
 * whose response it superseded, or undefined when the listing cannot be shown
 * to carry this save's own landing. A refresh that starts after a save answers
 * first, and the store discards the save's response as superseded while its
 * listing already holds the change the save declared. The seeded identity
 * anchor was taken before the save, so the derived endpointFingerprint the
 * save produced no longer matches it, and the next save would refuse with the
 * replacement error for an instance nothing replaced.
 *
 * The listing has to match on the fields this save left alone (with the
 * derived baseUrl/endpointFingerprint excused when an endpoint-affecting field
 * changed), and its destination has to be the one this save produced. That
 * destination comes from the mutation's own discarded answer - `authoritative`
 * - whose endpointFingerprint is the hub's keyed identity for the complete
 * resolved endpoint. Comparing that settles vars/protocol/surface-only edits,
 * stripped query/userinfo parts, and path-normalization differences at once,
 * none of which survive in the listing's sanitized baseUrl. When that
 * authoritative row carries no fingerprint (an unkeyable instance) there is no
 * such proof, so the match fails closed. Editing an implicit instance authors a
 * shadow under the same name, so implicit legitimately falls true -> false
 * here; the other direction is a different instance. */
export function supersededSaveLanded(
  instances: InstanceEntry[],
  before: InstanceEntry,
  params: InstanceEditParams,
  authoritative: InstanceEntry | undefined,
): InstanceEntry | undefined {
  if (params.newName !== undefined) return undefined;
  const listed = instances.find((instance) => instance.name === before.name);
  if (listed === undefined) return undefined;
  if (listed.implicit !== before.implicit && !(before.implicit && !listed.implicit)) return undefined;
  const plainBase = (a: InstanceEntry, b: InstanceEntry): boolean => fieldValue(a, "base") === fieldValue(b, "base");
  if (!untouchedIdentityMatches(before, listed, params, plainBase)) return undefined;
  // The destination identity is the mutation's own captured fingerprint. When
  // it is unavailable the listing's sanitized baseUrl cannot stand in for it
  // (it omits query/userinfo, so a replacement at a different hidden endpoint
  // reads the same), and a destination plainly exists: fail closed rather than
  // re-anchor, and let the caller mark the draft stale.
  // The fingerprint settles the destination; the fields it does not cover, the
  // endpoint clears and the credential agreement are confirmed by the shared
  // helper (the rename path uses the same one).
  return authoritativeLandingConfirmed(authoritative, listed, params) ? listed : undefined;
}

/** Whether the mutation's captured post-write row and the store's listing agree
 * on the credential fields the endpoint fingerprint does not cover. The hub
 * omits an authored value it cannot serve, so a replacement whose own hidden
 * credential the hub also omits would read the same otherwise. Shared by the
 * plain-save and rename confirmations so the rule cannot drift between them. */
function authoritativeCredentialsMatch(authoritative: InstanceEntry | undefined, listed: InstanceEntry): boolean {
  if ((authoritative?.apiKeyEnv ?? "") !== (listed.apiKeyEnv ?? "")) return false;
  if ((authoritative?.credentialHeader ?? "") !== (listed.credentialHeader ?? "")) return false;
  return true;
}
