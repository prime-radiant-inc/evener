// InstanceSheet: the provider instance's editor (spec 2026-09-07 §2). Opens
// from an InstanceRow tap and IS the edit surface: the authored fields are
// form inputs prefilled from the instance, Save in the sheet footer lights
// up when any differs, and renaming is editing the Name field. Below the
// form sit the layered credential display and the actions the inspector
// this replaced already had (test, set/replace key or credential JSON,
// sign in/refresh OAuth, make default) and the danger zone; the secret
// entry and OAuth flows stay dialogs because they carry write-only values
// or several steps. A wide right Sheet on desktop, a bottom Sheet on mobile
// (useIsMobile, the shell's own source).
//
// The instance is read from the store by name so cross-client changes land
// live. The draft is seeded when a different instance opens and again
// after this sheet's own save lands, never on an unrelated refresh, so
// in-progress edits survive another client's change. The sheet closes
// itself when its instance disappears - except across its own rename,
// where the section re-selects the new name (onRenamed) and the vanish is
// the rename landing, not a removal. Across that vanish the sheet goes on
// showing the instance the rename went out for (renamingFrom), so it never
// unmounts itself mid-rename. A save whose answer arrives after the user has
// dismissed the sheet or picked another row still counts as a write, and is
// toasted as one, but stops there (shownName): re-selecting or reseeding then
// would drag the sheet back to a save the user has walked away from. And a
// save the store discarded as superseded steers nothing on its own answer -
// the store's current list is the only thing that can say what landed.
//
// Owns the one mutation it edits (evener/instance/edit); the section still
// owns what every other action DOES (opening an editor, a confirm, or
// calling the store), the same division of labor as before.

import type { AuthTestResponse, InstanceEditParams, InstanceEntry } from "@evener/appwire-client";
import {
  CONNECTION_REPLACED_ERROR,
  credentialLayers,
  errorText,
  keylessByDesign,
  safeCredentialTestMessage,
  safeCredentialTestResult,
  unconfiguredLabel,
} from "@evener/appwire-client";
import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { useIsMobile } from "../../../../shell/useIsMobile";
import { credentialsStore, isStaleListingRefusal, useCredentialsStore } from "../../../../stores/credentials";
import { Button, Chip, FormRow, Input, Select, Sheet, StatusDot, Switch, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./InstanceSheet.module.css";
import {
  draftFor,
  type InstanceDraft,
  instanceEditParams,
  PROTOCOL_OPTIONS,
  SURFACE_OPTIONS,
  varRows,
} from "./instanceEdit";

const CLASS = {
  headingRow: requireClass(styles.headingRow, "InstanceSheet.module.css", "headingRow"),
  form: requireClass(styles.form, "InstanceSheet.module.css", "form"),
  formError: requireClass(styles.formError, "InstanceSheet.module.css", "formError"),
  layers: requireClass(styles.layers, "InstanceSheet.module.css", "layers"),
  layer: requireClass(styles.layer, "InstanceSheet.module.css", "layer"),
  unconfigured: requireClass(styles.unconfigured, "InstanceSheet.module.css", "unconfigured"),
  metaRow: requireClass(styles.metaRow, "InstanceSheet.module.css", "metaRow"),
  metaLabel: requireClass(styles.metaLabel, "InstanceSheet.module.css", "metaLabel"),
  metaValue: requireClass(styles.metaValue, "InstanceSheet.module.css", "metaValue"),
  actionRows: requireClass(styles.actionRows, "InstanceSheet.module.css", "actionRows"),
  fullRow: requireClass(styles.fullRow, "InstanceSheet.module.css", "fullRow"),
  divider: requireClass(styles.divider, "InstanceSheet.module.css", "divider"),
  testResult: requireClass(styles.testResult, "InstanceSheet.module.css", "testResult"),
};

const LITERAL_HEADER_ERROR = "Credential header must reference a $VARIABLE, never a literal secret.";
const EMPTY_NAME_ERROR = "Name cannot be empty.";
// The save landed, but its response was superseded, so nothing reseeded the
// form: the toast says what the sheet is showing - the user's own draft - and
// how to get the current state.
const STALE_SAVE_WARNING =
  "Saved, but the list changed underneath; your edits were kept — refresh to see the current state";
// The instance under the sheet was replaced by a different one at the same
// name, so the retained draft belongs to an instance that is no longer there.
// The form is reset to the instance now on screen rather than saving stale
// edits onto its replacement.
const CHANGED_INSTANCE_ERROR =
  "This instance was replaced under the same name; the form was reset to the instance now on screen.";

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

/** The entry fields this save's params changed, whether a field carries a value
 * or a `clear` flag: those are the fields a rename may legitimately differ in. */
function changedFields(params: InstanceEditParams): Set<string> {
  const changed = new Set<string>();
  for (const key of Object.keys(params)) {
    if (key === "name" || key === "newName") continue;
    changed.add(key.startsWith("clear") ? key.charAt(5).toLowerCase() + key.slice(6) : key);
  }
  return changed;
}

/** One entry field as the listing carries it, so two entries can be compared:
 * scalars by value, vars by content rather than by key order. */
function fieldValue(entry: InstanceEntry, field: string): string {
  const raw = (entry as unknown as Record<string, unknown>)[field];
  if (raw === undefined || raw === null) return "";
  if (typeof raw === "object") {
    const pairs = Object.entries(raw as Record<string, unknown>).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
    return JSON.stringify(pairs);
  }
  return String(raw);
}

// The immutable identity of the entry a draft belongs to: the name plus the
// fields no edit through this sheet changes. providerId/base/auth pick the
// provider and scheme; endpointFingerprint identifies the complete resolved
// endpoint even when the displayed baseUrl is byte-identical (a query-only
// change is invisible in baseUrl). Provenance (implicit) is deliberately not
// here: the guided flow legitimately re-anchors a draft from a curated setup row
// to the authored row created from it, and the removal path that could hand a
// name to an environment-supplied row closes its sheet instead (see
// CredentialsSection's confirmed removal). The fields a draft edits - baseUrl,
// protocol, surface, vars, apiKeyEnv, credentialHeader - are deliberately not
// here: a change to one of them is this instance edited, not a different
// instance under the same name.
const DRAFT_IDENTITY_FIELDS = ["name", "providerId", "base", "auth", "endpointFingerprint"] as const;

/** The identity a draft was seeded from, so a listing change that leaves the
 * name but puts a different instance under it cannot silently re-target the
 * draft: the save is refused and the form re-anchored to the entry now on
 * screen instead of writing edits typed for the old instance onto its
 * replacement. */
function draftIdentity(entry: InstanceEntry): string {
  const fields = DRAFT_IDENTITY_FIELDS.map((field) => fieldValue(entry, field));
  // A row the hub cannot key serves no fingerprint, and two such rows would
  // otherwise share an identity: fall back to the endpoint the user can see, so
  // a same-name replacement at a different destination is still a different
  // instance even while fingerprinting is unavailable.
  if (fieldValue(entry, "endpointFingerprint") === "") fields.push(fieldValue(entry, "baseUrl"));
  return fields.join("\u0000");
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
function baseCarriedByRename(before: InstanceEntry, listed: InstanceEntry): boolean {
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
function untouchedIdentityMatches(
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

/** An authored URL reduced to the endpoint identity the listing serves. The hub
 * strips userinfo, query and fragment before a Base URL crosses the appwire
 * boundary (cmd/evener-hub/app_instances.go's sanitizeEndpointURL) and serves
 * Go's net/url form; both sides are reduced through this one parser so host
 * case and explicit default ports (where the two libraries disagree) cannot
 * make a representable URL refuse. A URL that does not parse or carries no
 * host reduces to empty, as the hub's does. The path is kept verbatim: the hub
 * preserves meaningful path differences such as a trailing slash, so stripping
 * it here would let `/v1/` and `/v1` match each other. */
function listedEndpoint(raw: string): string {
  const trimmed = raw.trim();
  if (trimmed === "") return "";
  let parsed: URL;
  try {
    parsed = new URL(trimmed);
  } catch {
    return "";
  }
  if (parsed.protocol === "" || parsed.host === "") return "";
  parsed.username = "";
  parsed.password = "";
  parsed.search = "";
  parsed.hash = "";
  return parsed.toString();
}

/** Whether WHATWG parsing leaves this URL's path exactly as authored. It
 * collapses dot-segments (`/a/../b` -> `/b`) and supplies a `/` for a bare
 * authority, while the hub's Go sanitizer preserves the authored path exactly.
 * When parsing changes the path the two libraries can disagree about two
 * distinct endpoints, so a match built on the parsed form is refused. */
function pathPreservedByParser(raw: string): boolean {
  const trimmed = raw.trim();
  if (trimmed === "") return false;
  let parsed: URL;
  try {
    parsed = new URL(trimmed);
  } catch {
    return false;
  }
  if (parsed.protocol === "" || parsed.host === "") return false;
  const authoredPath = /^[a-zA-Z][a-zA-Z0-9+.-]*:\/\/[^/?#]*(\/[^?#]*)?/.exec(trimmed)?.[1] ?? "";
  return authoredPath === parsed.pathname;
}

/** Whether the listed endpoint is the one this save declared. The listed URL
 * cannot carry userinfo, query or fragment (the hub strips them), so a
 * declaration that has any of them is indistinguishable from a concurrent
 * write that differs only there: the match fails closed on a lossy declaration
 * rather than re-anchor the draft onto a foreign endpoint. A declaration the
 * hub cannot key (malformed or hostless) reduces to empty, exactly like any
 * other unkeyable listing, so two distinct invalid destinations would compare
 * equal: the match fails closed on those too. Either side whose path WHATWG
 * parsing rewrites also fails closed, so distinct Go paths cannot be conflated
 * (see pathPreservedByParser). */
function endpointMatches(declared: string, listed: string | undefined): boolean {
  const trimmed = declared.trim();
  if (trimmed === "") return false;
  const want = listedEndpoint(trimmed);
  if (want === "") return false;
  const listedRaw = (listed ?? "").trim();
  if (want !== listedEndpoint(listedRaw)) return false;
  if (!pathPreservedByParser(trimmed) || !pathPreservedByParser(listedRaw)) return false;
  // want !== "" means listedEndpoint parsed this URL with a scheme and host.
  const parsed = new URL(trimmed);
  return parsed.search === "" && parsed.hash === "" && parsed.username === "" && parsed.password === "";
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

/** Whether the listed entry carries the values this save declared for the
 * fields it changed. Without it a concurrent write that differs only in those
 * very fields reads as this save's own landing, and re-anchoring there pins
 * the draft to a foreign instance and lets the next save write onto it.
 *
 * The representation differs per field. A declared Base URL is compared as the
 * sanitized endpoint the listing serves (and fails closed when it carries
 * parts the listing strips, or cannot be keyed at all - see endpointMatches).
 * A `clear` drops the authored value, and the listing then serves either the
 * RESOLVED value (baseUrl/protocol/surface inherit from the base provider) or
 * an OMITTED field (apiKeyEnv/credentialHeader, which the hub omits when the
 * authored value is invalid or a literal secret) - neither is proof of the
 * clear, so an ENDPOINT clear (baseUrl/protocol/surface) always fails closed;
 * a credential clear fails closed only when `failClosedOnCredentialClear` is
 * set, which the plain-save path does and the rename path does not: a rename
 * riding along with a credential clear is confirmed by the untouched identity
 * fields it did not change, and the clear is merely unverifiable. A declared
 * header is compared after the hub's own normalization. A declared var has to
 * be present and equal, key by key. */
function declaredValuesLanded(
  listed: InstanceEntry,
  params: InstanceEditParams,
  failClosedOnCredentialClear: boolean,
): boolean {
  if (params.clearBaseUrl || params.clearProtocol || params.clearSurface) return false;
  if (failClosedOnCredentialClear && (params.clearApiKeyEnv || params.clearCredentialHeader)) return false;
  if (params.baseUrl !== undefined && !endpointMatches(params.baseUrl, listed.baseUrl)) return false;
  if (params.protocol !== undefined && listed.protocol !== params.protocol) return false;
  if (params.surface !== undefined && (listed.surface ?? "") !== params.surface) return false;
  if (params.apiKeyEnv !== undefined && (listed.apiKeyEnv ?? "") !== params.apiKeyEnv) return false;
  if (
    params.credentialHeader !== undefined &&
    normalizedCredentialHeader(listed.credentialHeader ?? "") !== normalizedCredentialHeader(params.credentialHeader)
  ) {
    return false;
  }
  for (const [key, value] of Object.entries(params.vars ?? {})) {
    if ((listed.vars?.[key] ?? "") !== value) return false;
  }
  return true;
}

/** The name this save's rename landed under, or undefined when the store's own
 * listing cannot say that it did: the new name has to be held by the instance
 * this save renamed - matching on the fields this save left alone and carrying
 * the values it declared - not by a later tenant of the freed name that
 * differs in a field this rename also edited. */
function renamedInstanceLanded(
  instances: InstanceEntry[],
  before: InstanceEntry,
  params: InstanceEditParams,
): string | undefined {
  const newName = params.newName;
  if (newName === undefined) return undefined;
  const listed = instances.find((instance) => instance.name === newName);
  if (listed === undefined || listed.implicit !== before.implicit) return undefined;
  if (!untouchedIdentityMatches(before, listed, params, baseCarriedByRename)) return undefined;
  return declaredValuesLanded(listed, params, false) ? newName : undefined;
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
 * none of which survive in the listing's sanitized baseUrl. When the
 * authoritative row carries no fingerprint (an unkeyable instance), fall back
 * to verifying the declared values the listing can represent. Editing an
 * implicit instance authors a shadow under the same name, so implicit
 * legitimately falls true -> false here; the other direction is a different
 * instance. */
function supersededSaveLanded(
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
  const authoritativeFingerprint = authoritative?.endpointFingerprint ?? "";
  if (authoritativeFingerprint !== "") {
    return listed.endpointFingerprint === authoritativeFingerprint ? listed : undefined;
  }
  return declaredValuesLanded(listed, params, true) ? listed : undefined;
}

export interface InstanceSheetProps {
  name: string | null;
  onClose: () => void;
  /** After a successful rename, with the new name: the section re-selects
   * it so the sheet stays open on the same instance. */
  onRenamed: (newName: string) => void;
  onSetApiKey: () => void;
  onSetCredentialJson: () => void;
  onOAuthStart: () => void;
  onClear: () => void;
  onClearStoredKey: () => void;
  onRemove: () => void;
  onSetDefault: () => void;
  onTestCredentials: () => void;
  /** Flips one model row's disabled flag; owned by the section like every
   * other non-edit action. */
  onToggleModel: (model: string, disabled: boolean) => void;
  /** Re-fetches this instance's live listing; owned by the section like
   * every other non-edit action. */
  onRefreshModels: () => void;
  /** A live-model refresh is in flight for this sheet: the Models section
   * says so while the catalog rows stay interactive. */
  modelsRefreshing?: boolean;
  /** Pending toggle keys (`instance/model`, see the section owner):
   * the matching switch is disabled until its write settles. */
  pendingToggles?: ReadonlySet<string>;
  testCredentialsPending?: boolean;
  testCredentialsResult?: AuthTestResponse;
  /** Disables Save/Remove/make default while providers.toml cannot be
   * written (InstanceListResponse.writesRefused, spec §11.3) - Set key/Sign
   * in/Clear/Clear stored key/Test credentials are unaffected: they write
   * the credentials store or an OAuth record, never providers.toml. */
  writesRefused?: boolean;
}

export function InstanceSheet({
  name,
  onClose,
  onRenamed,
  onSetApiKey,
  onSetCredentialJson,
  onOAuthStart,
  onClear,
  onClearStoredKey,
  onRemove,
  onSetDefault,
  onTestCredentials,
  onToggleModel,
  onRefreshModels,
  modelsRefreshing = false,
  pendingToggles,
  testCredentialsPending = false,
  testCredentialsResult,
  writesRefused = false,
}: InstanceSheetProps) {
  const instances = useCredentialsStore((s) => s.instances);
  const availableProviders = useCredentialsStore((s) => s.availableProviders);
  const isMobile = useIsMobile();
  const toast = useToasts();
  const ids = useId();

  const [initial, setInitial] = useState<InstanceDraft | null>(null);
  const [draft, setDraft] = useState<InstanceDraft | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // The instance a rename of this sheet's own went out for, held for the span
  // of the request: the old name leaves the store when the response lands, a
  // beat before the section re-selects the new one, so for that beat the
  // sheet's subject is in neither place. Keeping it here is what carries the
  // sheet across - an `open` that dips false unmounts the panel, replaying
  // its slide-in from off-screen and throwing focus out of the form - and it
  // is also the guard that keeps that vanish from closing the sheet.
  const [renamingFrom, setRenamingFrom] = useState<InstanceEntry | undefined>(undefined);
  // The name the section has the sheet on right now, readable from a save
  // still in flight: handleSave captured `name` from the render it ran in, and
  // the user is free to dismiss the sheet or pick another row before the
  // response lands. Compared against the instance the save went out for, this
  // is what says whether the answer is still this sheet's to act on.
  const shownName = useRef(name);
  // The identity the current draft was seeded from. A listing change can put a
  // different instance under the same name without the name-keyed effect
  // above noticing, so handleSave checks the draft still belongs to the
  // instance it is about to write to.
  const seededIdentity = useRef<string | null>(null);

  const stored = name === null ? undefined : instances.find((i) => i.name === name);
  const instance = stored ?? renamingFrom;
  const template = instance === undefined ? undefined : availableProviders.find((p) => p.id === instance.providerId);

  function seed(inst: InstanceEntry): void {
    const seeded = draftFor(
      inst,
      credentialsStore.getState().availableProviders.find((p) => p.id === inst.providerId),
    );
    setInitial(seeded);
    setDraft(seeded);
    setFormError(null);
    seededIdentity.current = draftIdentity(inst);
  }

  // biome-ignore lint/correctness/useExhaustiveDependencies: reseed only when a different instance opens; a refresh of the same instance must not clobber in-progress edits
  useEffect(() => {
    if (instance === undefined) {
      setInitial(null);
      setDraft(null);
      setFormError(null);
      return;
    }
    seed(instance);
  }, [instance?.name]);

  // A detail sheet is only as alive as its subject: the instance can vanish
  // under an open sheet (its own Remove completing, or another client's
  // change), and an editor for a thing that no longer exists closes itself
  // rather than offering actions on a ghost. Its own rename is the one
  // vanish that is not a removal, and renamingFrom is what tells them apart:
  // across a rename `instance` is still the held one, so this stays quiet.
  useEffect(() => {
    if (name !== null && instance === undefined) onClose();
  }, [name, instance, onClose]);
  // The section moved the selection to the new name: the held instance has
  // done its job, and holding it any longer would keep a ghost on screen.
  // biome-ignore lint/correctness/useExhaustiveDependencies: name is a deliberate trigger-only dep - the body only drops the held instance, but must re-run on every name change to release it
  useEffect(() => {
    setRenamingFrom(undefined);
  }, [name]);
  // A layout effect, not the passive one above: a response can land between
  // the commit that dismissed the sheet and a passive effect, and a mirror
  // that is one beat stale lets exactly the save this guards slip through.
  // useEditorLifetime keeps the dialogs' equivalent flag on layout timing for
  // the same reason.
  useLayoutEffect(() => {
    shownName.current = name;
  }, [name]);

  const open = name !== null && instance !== undefined;

  const params = initial !== null && draft !== null ? instanceEditParams(initial, draft) : null;
  // An emptied Name is an edit the request cannot carry - an empty newName
  // means "unchanged" on the wire, so the diff leaves it out and `params` can
  // come back null with the field visibly cleared. It still counts as dirty:
  // a Save the user cannot press answers the mistake with nothing at all,
  // where a pressable one answers with the reason.
  const emptiedName = draft !== null && draft.name.trim() === "";
  const dirty = params !== null || emptiedName;

  function update(patch: Partial<InstanceDraft>): void {
    setDraft((current) => (current === null ? current : { ...current, ...patch }));
  }
  function updateVar(key: string, value: string): void {
    setDraft((current) => (current === null ? current : { ...current, vars: { ...current.vars, [key]: value } }));
  }

  async function handleSave(): Promise<void> {
    // The action carries its own write gate rather than borrowing the Save
    // button's disabled state: the form submits too, and a refused or
    // in-flight write must not go out through that door either.
    if (busy || writesRefused) return;
    if (instance === undefined) return;
    // The draft belongs to the instance it was seeded from. A removal and
    // recreation under the freed name, or another instance renamed onto it,
    // leaves the name the sheet is on while handing it a different instance:
    // saving then writes edits typed against the old endpoint onto the
    // replacement. Refuse, re-anchor to the entry now on screen, and say so -
    // the same refusal the guided flow makes when its destination moves.
    if (draftIdentity(instance) !== seededIdentity.current) {
      seed(instance);
      setFormError(CHANGED_INSTANCE_ERROR);
      return;
    }
    // Ahead of the `params === null` guard, not behind it: an emptied Name is
    // exactly the edit that leaves params null, so a refusal below would
    // never be reached. Refused rather than sent because the wire reads an
    // empty newName as "unchanged" - the request would succeed and rename
    // nothing while the toast claimed a save.
    if (emptiedName) {
      setFormError(EMPTY_NAME_ERROR);
      return;
    }
    if (params === null) return;
    if (params.credentialHeader !== undefined && !params.credentialHeader.includes("$")) {
      setFormError(LITERAL_HEADER_ERROR);
      return;
    }
    setFormError(null);
    setBusy(true);
    if (params.newName !== undefined) setRenamingFrom(instance);
    try {
      // The store's verdict, not the response, says what landed: a refresh
      // that started after this save answers first, and the store discards
      // this response as superseded. Its listing is then a document nothing
      // holds, so a toast naming it, a reseed from it, or a steer onto a name
      // it alone reports would all show the user a state that is not there.
      // The discarded answer is still the hub's authoritative post-write row
      // for this instance, though, so keep it to compare against the listing
      // now held (see supersededSaveLanded).
      let authoritative: InstanceEntry | undefined;
      const applied = await credentialsStore.getState().edit(params, {
        onSuperseded: (response) => {
          authoritative = response.instances.find((entry) => entry.name === instance.name);
        },
      });
      const listedInstances = credentialsStore.getState().instances;
      // Except when the store's own list holds this save's rename: the same
      // instance, now wearing the name it was given. Holding the name is not
      // enough on its own - a rename frees a name that any other instance can
      // take - so the entry is checked against the instance this save renamed.
      const listedRename = renamedInstanceLanded(listedInstances, instance, params);
      if (applied || listedRename !== undefined) toast.push("success", `Saved ${params.newName ?? instance.name}`);
      // The sheet may have moved on while the request was in flight: dismissed,
      // or pointed at another row. The write stands and the toast above is
      // owed either way, but what follows steers the sheet - onRenamed asks
      // the section to select the new name, and the reseed replaces the draft.
      // Applied late, either one takes a sheet the user has moved somewhere
      // else and drags it back to this save: a dismissed sheet re-opens, a
      // freshly picked row loses the selection or shows another instance's
      // values under its own title.
      if (shownName.current !== instance.name) return;
      if (!applied) {
        if (listedRename !== undefined) {
          onRenamed(listedRename);
        } else {
          // The store's listing may already hold this plain save's own change:
          // a refresh that started after the save answers first, and the store
          // discards the response as superseded while its entry carries what
          // this save declared. The draft is still the user's, but the seeded
          // identity anchor predates the change, so re-anchor it to the entry
          // the save landed as — without reseeding, which would discard the
          // draft. The next Save then compares like against like instead of
          // refusing an instance that was never replaced.
          const landed = supersededSaveLanded(listedInstances, instance, params, authoritative);
          if (landed !== undefined) seededIdentity.current = draftIdentity(landed);
          setRenamingFrom(undefined);
          toast.push("warning", STALE_SAVE_WARNING);
        }
        return;
      }
      if (params.newName !== undefined) {
        onRenamed(params.newName);
      } else {
        const refreshed = credentialsStore.getState().instances.find((i) => i.name === instance.name);
        if (refreshed !== undefined) seed(refreshed);
      }
    } catch (err) {
      // The store's own refusal of a write issued from the previous
      // connection's listing (stores/credentials.ts's requireWritableClient):
      // the draft was seeded from rows that connection read, so nothing was
      // sent. Keep the draft, name the change, and ask for this connection's
      // listing - its arrival is what makes the retry land. Reported as a save
      // failure it would tell the user their edit could not be saved when no
      // save ever went out.
      if (isStaleListingRefusal(err)) {
        void credentialsStore
          .getState()
          .fetch()
          .catch(() => {});
        if (shownName.current === instance.name) {
          setRenamingFrom(undefined);
          setFormError(CONNECTION_REPLACED_ERROR);
        }
        toast.push("warning", CONNECTION_REPLACED_ERROR);
        return;
      }
      const message = errorText(err);
      // The toast is owed wherever the user has gone - they asked for a write
      // that did not happen. The form's error line is not: it belongs to the
      // instance the save went out for, and written into a sheet since
      // pointed elsewhere it blames one instance for another's failure. Same
      // for releasing the rename guard, which only this sheet's save set.
      if (shownName.current === instance.name) {
        setRenamingFrom(undefined);
        setFormError(message);
      }
      toast.push("error", `Save failed: ${message}`);
    } finally {
      setBusy(false);
    }
  }

  const supportsApiKey = instance !== undefined && (instance.authModes ?? []).includes("apiKey");
  const supportsCredentialJson = instance !== undefined && (instance.authModes ?? []).includes("credentialJson");
  const supportsOAuth = instance !== undefined && (instance.authModes ?? []).includes("oauth");
  const showClear = instance !== undefined && (instance.activeSource === "store" || instance.activeSource === "oauth");
  // showClearStoredKey: a stray stored key sits shadowed behind whatever IS
  // active (the same condition credentialLayers uses to render that second,
  // non-effective layer above) - true for an oauth/adc login with a leftover
  // credentials.toml entry, and just as much for a signed-out Codex row a
  // previous Clear left stranded (Clear's Codex branch removes the OAuth
  // record, not the file, when one is active; issue #713). This action
  // always targets the store layer only, so it is safe to offer regardless
  // of what is effective.
  const showClearStoredKey = instance?.hasStoredFile && instance.activeSource !== "store";
  // The danger zone is Clear + Clear stored key + Remove under a divider; an
  // implicit instance with nothing stored offers none of them, and a divider
  // over nothing reads as a rendering bug.
  const showDangerZone = instance !== undefined && (showClear || showClearStoredKey || !instance.implicit);
  const layers = instance === undefined ? [] : credentialLayers(instance);
  const unconfigured = instance === undefined ? null : unconfiguredLabel(instance);
  // The sheet's per-model toggles read the registry's own inventory. The
  // Models section always renders - with its Refresh button - so an
  // instance whose live listing has not arrived yet still offers a manual
  // re-fetch; only the toggles need rows.
  const models = instance?.models ?? [];
  const safeTestResult = testCredentialsResult
    ? safeCredentialTestResult(name ?? "", testCredentialsResult)
    : undefined;
  const clearingBaseUrl =
    instance !== undefined && draft !== null && Boolean(instance.baseUrl) && draft.baseUrl.trim() === "";
  // Non-empty and trimmed on both sides, exactly as instanceEditParams decides
  // whether the request carries a newName: the note and the request must agree
  // on what counts as a rename, and an emptied Name is not one.
  const renaming =
    initial !== null && draft !== null && draft.name.trim() !== "" && draft.name.trim() !== initial.name.trim();

  return (
    <Sheet
      open={open}
      onClose={onClose}
      title={instance?.name ?? ""}
      side={isMobile ? "bottom" : "right"}
      size="wide"
      footer={
        instance !== undefined && (
          <Button onClick={() => void handleSave()} aria-disabled={busy} disabled={!dirty || writesRefused}>
            Save
          </Button>
        )
      }
    >
      {instance !== undefined && (
        <>
          <div className={CLASS.headingRow}>
            <StatusDot state={layers.length > 0 || keylessByDesign(instance) ? "idle" : "ended"} />
            {instance.isDefault && <Chip>★ default</Chip>}
            {instance.implicit && <Chip>from environment</Chip>}
          </div>
          {draft !== null && (
            <form
              className={CLASS.form}
              aria-label={`Edit ${instance.name}`}
              onSubmit={(event) => {
                event.preventDefault();
                void handleSave();
              }}
            >
              <FormRow
                label="Name"
                htmlFor={`${ids}-name`}
                help={
                  instance.implicit
                    ? "This instance comes from the environment and cannot be renamed."
                    : renaming
                      ? `Launch config and past sessions that reference "${instance.name}" keep the old name.`
                      : undefined
                }
              >
                <Input
                  id={`${ids}-name`}
                  value={draft.name}
                  onChange={(event) => update({ name: event.target.value })}
                  disabled={busy || instance.implicit}
                />
              </FormRow>
              <div className={CLASS.metaRow}>
                <span className={CLASS.metaLabel}>Base provider</span>
                <span className={CLASS.metaValue}>{instance.providerId}</span>
              </div>
              <FormRow
                label="Base URL"
                htmlFor={`${ids}-baseurl`}
                help={clearingBaseUrl ? "Resets the endpoint to the provider's default." : undefined}
              >
                <Input
                  id={`${ids}-baseurl`}
                  value={draft.baseUrl}
                  onChange={(event) => update({ baseUrl: event.target.value })}
                  placeholder="https://…"
                  disabled={busy}
                />
              </FormRow>
              <FormRow label="Protocol" htmlFor={`${ids}-protocol`}>
                <Select
                  id={`${ids}-protocol`}
                  value={draft.protocol}
                  onChange={(event) => update({ protocol: event.target.value })}
                  options={PROTOCOL_OPTIONS}
                  disabled={busy}
                />
              </FormRow>
              <FormRow label="Surface" htmlFor={`${ids}-surface`}>
                <Select
                  id={`${ids}-surface`}
                  value={draft.surface}
                  onChange={(event) => update({ surface: event.target.value })}
                  options={SURFACE_OPTIONS}
                  disabled={busy}
                />
              </FormRow>
              {varRows(draft, template).map(({ key, label }) => (
                <FormRow key={key} label={label} htmlFor={`${ids}-var-${key}`}>
                  <Input
                    id={`${ids}-var-${key}`}
                    value={draft.vars[key] ?? ""}
                    onChange={(event) => updateVar(key, event.target.value)}
                    disabled={busy}
                  />
                </FormRow>
              ))}
              <FormRow label="API key environment variable" htmlFor={`${ids}-apikeyenv`}>
                <Input
                  id={`${ids}-apikeyenv`}
                  value={draft.apiKeyEnv}
                  onChange={(event) => update({ apiKeyEnv: event.target.value })}
                  placeholder="e.g. PORTKEY_KEY"
                  disabled={busy}
                />
              </FormRow>
              <FormRow
                label="Credential header"
                htmlFor={`${ids}-credentialheader`}
                help="NAME=VALUE; the value must reference a $VARIABLE, never a literal secret."
              >
                <Input
                  id={`${ids}-credentialheader`}
                  value={draft.credentialHeader}
                  onChange={(event) => update({ credentialHeader: event.target.value })}
                  placeholder="Authorization=Bearer $VAR"
                  disabled={busy}
                />
              </FormRow>
              {formError !== null && (
                <p className={CLASS.formError} role="alert">
                  {formError}
                </p>
              )}
            </form>
          )}
          {unconfigured !== null ? (
            <p className={CLASS.unconfigured}>{unconfigured}</p>
          ) : (
            <div className={CLASS.layers}>
              {layers.map((layer) => (
                <div key={layer.source} className={CLASS.layer}>
                  <span>↳ {layer.label}</span>
                  <Chip tone={layer.effective ? "alive" : "neutral"}>{layer.effective ? "effective" : "shadowed"}</Chip>
                </div>
              ))}
            </div>
          )}
          {/* Every action below takes Save's `busy` gate, not just the form:
              the write in flight may be a rename, and until it settles the
              sheet still shows the instance under its old name. An action
              fired in that window goes out against the name the write is
              moving away from - and the credential ones would recreate under
              it the orphan the rename just moved. */}
          {instance !== undefined && (
            <>
              <h3>Models</h3>
              <div className={CLASS.fullRow}>
                {/* Refresh is a read: the RPC deliberately skips
                    refuseWhenBroken, so it stays available while
                    providers.toml cannot be written. */}
                <Button variant="quiet" onClick={onRefreshModels} aria-disabled={modelsRefreshing} disabled={busy}>
                  {modelsRefreshing ? "Refreshing live models…" : "Refresh live models"}
                </Button>
              </div>
              <div className={CLASS.actionRows}>
                {models.map((row) => (
                  <div key={row.id} className={CLASS.fullRow}>
                    <Switch
                      label={row.id}
                      checked={!row.disabled}
                      pending={pendingToggles?.has(`${name}/${row.id}`) ?? false}
                      disabled={busy || writesRefused}
                      onChange={(checked) => onToggleModel(row.id, !checked)}
                    />
                  </div>
                ))}
              </div>
            </>
          )}
          <div className={CLASS.actionRows}>
            <div className={CLASS.fullRow}>
              <Button
                variant="quiet"
                onClick={onTestCredentials}
                aria-disabled={testCredentialsPending}
                disabled={busy}
              >
                {testCredentialsPending ? "Testing credentials…" : "Test credentials"}
              </Button>
            </div>
            {safeTestResult && (
              <p className={CLASS.testResult} role="status">
                {safeTestResult.status}: {safeCredentialTestMessage(safeTestResult.status)}
              </p>
            )}
            {supportsApiKey && (
              <div className={CLASS.fullRow}>
                <Button variant="quiet" onClick={onSetApiKey} disabled={busy}>
                  {instance.hasStoredFile ? "Replace key" : "Set key"}
                </Button>
              </div>
            )}
            {supportsCredentialJson && (
              <div className={CLASS.fullRow}>
                <Button variant="quiet" onClick={onSetCredentialJson} disabled={busy}>
                  {instance.hasStoredFile ? "Replace credential JSON" : "Set credential JSON"}
                </Button>
              </div>
            )}
            {supportsOAuth && (
              <div className={CLASS.fullRow}>
                <Button variant="quiet" onClick={onOAuthStart} disabled={busy}>
                  {instance.hasStoredOAuth ? "Refresh OAuth" : "Sign in…"}
                </Button>
              </div>
            )}
            {!instance.isDefault && (
              <div className={CLASS.fullRow}>
                <Button variant="quiet" onClick={onSetDefault} disabled={busy || writesRefused}>
                  ★ make default
                </Button>
              </div>
            )}
          </div>
          {showDangerZone && (
            <>
              <hr className={CLASS.divider} />
              <div className={CLASS.actionRows}>
                {showClearStoredKey && (
                  <div className={CLASS.fullRow}>
                    <Button variant="dangerQuiet" onClick={onClearStoredKey} disabled={busy}>
                      {supportsCredentialJson ? "Clear stored credential JSON" : "Clear stored key"}
                    </Button>
                  </div>
                )}
                {showClear && (
                  <div className={CLASS.fullRow}>
                    <Button variant="dangerQuiet" onClick={onClear} disabled={busy}>
                      Clear
                    </Button>
                  </div>
                )}
                {!instance.implicit && (
                  <div className={CLASS.fullRow}>
                    <Button variant="danger" onClick={onRemove} disabled={busy || writesRefused}>
                      Remove
                    </Button>
                  </div>
                )}
              </div>
            </>
          )}
        </>
      )}
    </Sheet>
  );
}
