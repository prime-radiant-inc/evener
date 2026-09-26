// The matching rules that decide whether a save the credentials store
// superseded still landed. Each row is one case: the entry the draft was
// seeded from (`before`), the params the save sent, the mutation's own
// discarded answer for the saved name (`authoritative`), and the store's
// listing (`instances`). InstanceSheet.test.tsx keeps render tests for the
// wiring around these verdicts: the stale-save warning, the steer onto a
// confirmed rename, and a re-anchor or a refused next save.
import type { InstanceEditParams, InstanceEntry } from "@evener/appwire-client";
import { describe, expect, test } from "vitest";
import { renamedInstanceLanded, supersededSaveLanded } from "./instanceLanding";

function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "providerId">): InstanceEntry {
  return {
    protocol: "openai-chat",
    auth: "bearer",
    implicit: false,
    isDefault: false,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: true,
    ...overrides,
  };
}

/** A row the hub cannot key: no endpointFingerprint at all. */
function unkeyable(fields: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "providerId">): InstanceEntry {
  const row = instance(fields);
  delete row.endpointFingerprint;
  return row;
}

// An authored instance with credentials and no endpoint fingerprint.
const WORK = instance({
  name: "work",
  providerId: "openai",
  protocol: "openai-responses",
  surface: "generic",
  baseUrl: "https://gw.example.test/v1",
  apiKeyEnv: "PORTKEY_KEY",
  credentialHeader: "Authorization=Bearer $PORTKEY_KEY",
  hasStoredFile: true,
  activeSource: "store",
});
const OTHER = instance({ name: "other", providerId: "openai", baseUrl: "https://other.example.test" });
// A keyed instance: the hub serves an endpoint fingerprint for it.
const KEYED = instance({
  name: "work",
  providerId: "openai",
  protocol: "openai-responses",
  baseUrl: "https://gw.example.test/v1",
  endpointFingerprint: "fp-before",
});
// A curated-shadow instance: it holds the curated provider's own name.
const SHADOW = instance({
  name: "openai",
  providerId: "openai",
  protocol: "openai-responses",
  baseUrl: "https://gw.example.test/v1",
  apiKeyEnv: "PORTKEY_KEY",
  endpointFingerprint: "fp-shadow",
});
const BARE_SHADOW = instance({ name: "openai", providerId: "openai", endpointFingerprint: "fp-shadow" });
// An instance with no authored entry.
const CODEX = instance({
  name: "openai-codex",
  providerId: "openai-codex",
  auth: "oauth-openai-codex",
  authModes: ["oauth"],
  implicit: true,
  activeSource: "oauth",
  hasStoredOAuth: true,
  endpointFingerprint: "fp-codex",
});
function vertex(overrides: Partial<InstanceEntry>): InstanceEntry {
  return instance({
    name: "v",
    providerId: "google-vertex-anthropic",
    protocol: "anthropic",
    endpointFingerprint: "fp-before",
    ...overrides,
  });
}

interface LandingCase {
  name: string;
  before: InstanceEntry;
  params: InstanceEditParams;
  authoritative: InstanceEntry | undefined;
  instances: InstanceEntry[];
  landed: boolean;
}

/** One case where the listing holds the mutation's own answer. */
function listing(
  name: string,
  before: InstanceEntry,
  params: InstanceEditParams,
  row: InstanceEntry,
  landed: boolean,
): LandingCase {
  return { name, before, params, authoritative: row, instances: [row], landed };
}

/** One case where the listing holds a foreign row and the mutation answered with its own. */
function superseding(
  name: string,
  before: InstanceEntry,
  params: InstanceEditParams,
  ours: InstanceEntry,
  foreign: InstanceEntry,
  landed: boolean,
): LandingCase {
  return { name, before, params, authoritative: ours, instances: [foreign], landed };
}

const RENAME = { name: "work", newName: "work2", expectedEndpointFingerprint: "fp-before" } as const;

const renameCases: LandingCase[] = [
  {
    name: "a listing that still holds only the old name is not confirmed",
    before: WORK,
    params: { name: "work", newName: "work2" },
    authoritative: { ...WORK, name: "work2" },
    instances: [WORK],
    landed: false,
  },
  listing(
    "the renamed row carrying the captured fingerprint is confirmed",
    { ...WORK, endpointFingerprint: "fp-work" },
    { name: "work", newName: "work2", expectedEndpointFingerprint: "fp-work" },
    { ...WORK, name: "work2", endpointFingerprint: "fp-work" },
    true,
  ),
  listing(
    "a rename that also edits the endpoint is confirmed despite the moved fingerprint",
    WORK,
    { name: "work", newName: "work2", baseUrl: "https://gw.example.test/v1/x" },
    { ...WORK, name: "work2", baseUrl: "https://gw.example.test/v1/x", endpointFingerprint: "fp-work2" },
    true,
  ),
  listing(
    "a new name now held by another instance is not confirmed",
    WORK,
    { name: "work", newName: "work2" },
    { ...OTHER, name: "work2" },
    false,
  ),
  listing(
    "a rename that leaves the endpoint untouched is not confirmed by a differing fingerprint",
    WORK,
    { name: "work", newName: "work2", apiKeyEnv: "PORTKEY_KEY_2" },
    { ...WORK, name: "work2", apiKeyEnv: "PORTKEY_KEY_2", endpointFingerprint: "fp-other" },
    false,
  ),
  listing(
    "a look-alike differing only in auth is not confirmed (unkeyable)",
    WORK,
    { name: "work", newName: "work2" },
    { ...WORK, name: "work2", auth: "oauth-openai-codex" },
    false,
  ),
  listing(
    "a look-alike differing only in auth is not confirmed (keyed)",
    { ...WORK, endpointFingerprint: "fp-work" },
    { name: "work", newName: "work2", expectedEndpointFingerprint: "fp-work" },
    { ...WORK, name: "work2", endpointFingerprint: "fp-work", auth: "oauth-openai-codex" },
    false,
  ),
  listing(
    "a curated shadow's rename is confirmed though the rename pinned base to the old name",
    SHADOW,
    { name: "openai", newName: "openai-work", expectedEndpointFingerprint: "fp-shadow" },
    { ...SHADOW, name: "openai-work", base: "openai" },
    true,
  ),
  listing(
    "an instance with no authored entry is confirmed as the authored row the rename wrote",
    CODEX,
    { name: "openai-codex", newName: "openai-codex-work", expectedEndpointFingerprint: "fp-codex" },
    { ...CODEX, name: "openai-codex-work", base: "openai-codex", implicit: false },
    true,
  ),
  listing(
    "an implicit row at the new name is not confirmed",
    CODEX,
    { name: "openai-codex", newName: "openai-codex-work", expectedEndpointFingerprint: "fp-codex" },
    { ...CODEX, name: "openai-codex-work", implicit: true },
    false,
  ),
  listing(
    "a listing whose base is not the old name is not confirmed",
    BARE_SHADOW,
    { name: "openai", newName: "openai-work", expectedEndpointFingerprint: "fp-shadow" },
    { ...BARE_SHADOW, name: "openai-work", base: "anthropic" },
    false,
  ),
  listing(
    "a captured row with an empty fingerprint fails closed",
    KEYED,
    RENAME,
    { ...KEYED, name: "work2", endpointFingerprint: "" },
    false,
  ),
  listing(
    "a captured row with an empty fingerprint fails closed though the listing agrees",
    KEYED,
    { ...RENAME, baseUrl: "https://gw.example.test/v1/x" },
    { ...KEYED, name: "work2", baseUrl: "https://gw.example.test/v1/x", endpointFingerprint: "" },
    false,
  ),
  listing(
    "a rename that also clears a credential field is not confirmed",
    { ...KEYED, apiKeyEnv: "PORTKEY_KEY" },
    { ...RENAME, clearApiKeyEnv: true },
    instance({
      name: "work2",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
    }),
    false,
  ),
  listing(
    "a rename that clears the endpoint is not confirmed by an arbitrary replacement",
    KEYED,
    { ...RENAME, clearBaseUrl: true },
    { ...KEYED, name: "work2", baseUrl: "https://other.example.test/y", endpointFingerprint: "fp-other" },
    false,
  ),
  listing(
    "a rename to a pathless Base URL is confirmed",
    KEYED,
    { ...RENAME, baseUrl: "https://api.example.test" },
    { ...KEYED, name: "work2", baseUrl: "https://api.example.test", endpointFingerprint: "fp-other" },
    true,
  ),
  superseding(
    "a same-URL entry at a different hidden endpoint is not confirmed",
    { ...KEYED, baseUrl: "https://old.example.test/v1" },
    { ...RENAME, baseUrl: "https://gw.example.test/v1" },
    { ...KEYED, name: "work2", baseUrl: "https://gw.example.test/v1", endpointFingerprint: "fp-ours" },
    { ...KEYED, name: "work2", baseUrl: "https://gw.example.test/v1", endpointFingerprint: "fp-foreign" },
    false,
  ),
  listing(
    "a rename that only changes a variable is confirmed by the captured fingerprint",
    vertex({ vars: { GOOGLE_VERTEX_PROJECT: "p1" }, baseUrl: "https://resolved.example.test/v1" }),
    { name: "v", newName: "v2", vars: { GOOGLE_VERTEX_PROJECT: "p2" }, expectedEndpointFingerprint: "fp-before" },
    vertex({
      name: "v2",
      vars: { GOOGLE_VERTEX_PROJECT: "p2" },
      baseUrl: "https://resolved.example.test/v1",
      endpointFingerprint: "fp-after",
    }),
    true,
  ),
];

describe("renamedInstanceLanded", () => {
  test.each(renameCases)("$name", ({ before, params, authoritative, instances, landed }) => {
    expect(renamedInstanceLanded(instances, before, params, authoritative)).toBe(landed ? params.newName : undefined);
  });

  test("a plain save is never a rename", () => {
    const row = { ...KEYED, baseUrl: "https://gw.example.test/v1/x", endpointFingerprint: "fp-after" };
    expect(
      renamedInstanceLanded([row], KEYED, { name: "work", baseUrl: "https://gw.example.test/v1/x" }, row),
    ).toBeUndefined();
  });
});

const SAVE = { name: "work", expectedEndpointFingerprint: "fp-before" } as const;
const TO_X = { ...SAVE, baseUrl: "https://gw.example.test/v1/x" } as const;
const LANDED_X = { ...KEYED, baseUrl: "https://gw.example.test/v1/x", endpointFingerprint: "fp-after" };
const WITH_KEY = { ...KEYED, apiKeyEnv: "PORTKEY_KEY" };
const SURFACED = { ...KEYED, surface: "generic" };
const VAR_SAVE = (value: string): InstanceEditParams => ({
  name: "v",
  vars: { GOOGLE_VERTEX_PROJECT: value },
  expectedEndpointFingerprint: "fp-before",
});

const plainCases: LandingCase[] = [
  {
    name: "an unkeyable save whose listing has not moved is not confirmed",
    before: WORK,
    params: { name: "work", baseUrl: "https://gw.example.test/v1/x" },
    authoritative: { ...WORK, baseUrl: "https://gw.example.test/v1/x" },
    instances: [WORK],
    landed: false,
  },
  listing("an endpoint save is confirmed by the captured fingerprint", KEYED, TO_X, LANDED_X, true),
  listing(
    "a captured row with an empty fingerprint fails closed though the listing agrees",
    KEYED,
    TO_X,
    { ...LANDED_X, endpointFingerprint: "" },
    false,
  ),
  superseding(
    "a concurrent write in the same field it touched is not confirmed",
    KEYED,
    TO_X,
    { ...LANDED_X, endpointFingerprint: "fp-ours" },
    { ...KEYED, baseUrl: "https://other.example.test/y", endpointFingerprint: "fp-after" },
    false,
  ),
  listing(
    "a look-alike differing only in an untouched variable is not confirmed",
    vertex({ vars: { GOOGLE_VERTEX_PROJECT: "p1", GOOGLE_VERTEX_LOCATION: "loc1" } }),
    VAR_SAVE("p9"),
    vertex({
      vars: { GOOGLE_VERTEX_PROJECT: "p9", GOOGLE_VERTEX_LOCATION: "loc-other" },
      endpointFingerprint: "fp-after",
    }),
    false,
  ),
  listing(
    "an implicit instance is confirmed after the save authors a shadow",
    { ...KEYED, implicit: true },
    TO_X,
    { ...LANDED_X, implicit: false },
    true,
  ),
  listing(
    "a replacement under a differing implicit is not confirmed",
    KEYED,
    TO_X,
    { ...LANDED_X, implicit: true },
    false,
  ),
  listing(
    "a declared URL with a stripped part is confirmed by the captured fingerprint",
    KEYED,
    { ...SAVE, baseUrl: "https://gw.example.test/v1/x?token=abc" },
    LANDED_X,
    true,
  ),
  superseding(
    "an endpoint clear is not confirmed by a same-endpoint replacement",
    KEYED,
    { ...SAVE, clearBaseUrl: true },
    { ...KEYED, baseUrl: "https://inherited.example.test/v1", endpointFingerprint: "fp-after" },
    { ...KEYED, baseUrl: "https://other.example.test/x", endpointFingerprint: "fp-after" },
    false,
  ),
  listing(
    "a credential header is confirmed through the hub's normalization",
    { ...KEYED, credentialHeader: "Authorization=Bearer $OLDKEY" },
    { ...TO_X, credentialHeader: "Authorization = Bearer $NEWKEY" },
    { ...LANDED_X, credentialHeader: "Authorization=Bearer $NEWKEY" },
    true,
  ),
  superseding(
    "a malformed URL is not confirmed against another malformed entry",
    KEYED,
    { ...SAVE, baseUrl: "not a url" },
    { ...KEYED, baseUrl: "not a url", endpointFingerprint: "" },
    { ...KEYED, baseUrl: "also not a url", endpointFingerprint: "fp-after" },
    false,
  ),
  superseding(
    "an unkeyable endpoint-plus-credential clear is not confirmed against redacted metadata",
    unkeyable({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      apiKeyEnv: "PORTKEY_KEY",
    }),
    { name: "work", baseUrl: "https://gw.example.test/v1/x", clearApiKeyEnv: true },
    unkeyable({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1/x",
    }),
    unkeyable({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1/x",
      apiKeyEnv: "HIDDEN_KEY",
    }),
    false,
  ),
  listing(
    "a template variable save is confirmed on the new resolved URL",
    vertex({ vars: { GOOGLE_VERTEX_PROJECT: "p1" }, baseUrl: "https://old.example.test/v1" }),
    VAR_SAVE("p2"),
    vertex({
      vars: { GOOGLE_VERTEX_PROJECT: "p2" },
      baseUrl: "https://new.example.test/v1",
      endpointFingerprint: "fp-after",
    }),
    true,
  ),
  superseding(
    "a variable save is not confirmed against an entry with a different URL override",
    vertex({ vars: { GOOGLE_VERTEX_PROJECT: "p1" }, baseUrl: "https://resolved-one.example.test/v1" }),
    VAR_SAVE("p2"),
    vertex({
      vars: { GOOGLE_VERTEX_PROJECT: "p2" },
      baseUrl: "https://resolved-one.example.test/v1",
      endpointFingerprint: "fp-ours",
    }),
    vertex({
      vars: { GOOGLE_VERTEX_PROJECT: "p2" },
      baseUrl: "https://resolved-other.example.test/v1",
      endpointFingerprint: "fp-foreign",
    }),
    false,
  ),
  superseding(
    "an endpoint-plus-credential clear is not confirmed against conflicting credential metadata",
    WITH_KEY,
    { ...TO_X, clearApiKeyEnv: true },
    { ...WITH_KEY, baseUrl: "https://gw.example.test/v1/x", endpointFingerprint: "fp-after", apiKeyEnv: "" },
    { ...WITH_KEY, baseUrl: "https://gw.example.test/v1/x", endpointFingerprint: "fp-after", apiKeyEnv: "OTHER_KEY" },
    false,
  ),
  superseding(
    "a variable save is not confirmed against a conflicting declared variable",
    vertex({ vars: { GOOGLE_VERTEX_PROJECT: "p1" }, baseUrl: "https://resolved.example.test/v1" }),
    VAR_SAVE("p2"),
    vertex({
      vars: { GOOGLE_VERTEX_PROJECT: "p2" },
      baseUrl: "https://resolved.example.test/v1",
      endpointFingerprint: "fp-after",
    }),
    vertex({
      vars: { GOOGLE_VERTEX_PROJECT: "p3" },
      baseUrl: "https://resolved.example.test/v1",
      endpointFingerprint: "fp-after",
    }),
    false,
  ),
  listing(
    "a credential-only save is confirmed when the listing carries it",
    WITH_KEY,
    { ...SAVE, apiKeyEnv: "NEW_KEY" },
    { ...WITH_KEY, apiKeyEnv: "NEW_KEY" },
    true,
  ),
  superseding(
    "a credential-only save is not confirmed against a replacement's credentials",
    WITH_KEY,
    { ...SAVE, apiKeyEnv: "NEW_KEY" },
    { ...WITH_KEY, apiKeyEnv: "NEW_KEY" },
    { ...WITH_KEY, apiKeyEnv: "FOREIGN_KEY" },
    false,
  ),
  listing(
    "a declared credential the listing does not carry is not confirmed, though the capture agrees",
    WITH_KEY,
    { ...SAVE, apiKeyEnv: "NEW_KEY" },
    { ...WITH_KEY, apiKeyEnv: "FOREIGN_KEY" },
    false,
  ),
  {
    name: "a capture whose credentials disagree with the listing is not confirmed",
    before: { ...SURFACED, apiKeyEnv: "LISTED_KEY" },
    params: { ...SAVE, surface: "openai" },
    authoritative: { ...SURFACED, surface: "openai", apiKeyEnv: "CAPTURED_KEY" },
    instances: [{ ...SURFACED, surface: "openai", apiKeyEnv: "LISTED_KEY" }],
    landed: false,
  },
  listing(
    "a surface save is confirmed when the listing carries it",
    SURFACED,
    { ...SAVE, surface: "openai" },
    { ...SURFACED, surface: "openai" },
    true,
  ),
  superseding(
    "a surface save is not confirmed against a replacement with another surface",
    SURFACED,
    { ...SAVE, surface: "openai" },
    { ...SURFACED, surface: "openai" },
    { ...SURFACED, surface: "anthropic" },
    false,
  ),
  superseding(
    "a surface clear is not confirmed against a replacement with another surface",
    SURFACED,
    { ...SAVE, clearSurface: true },
    { ...SURFACED, surface: "" },
    { ...SURFACED, surface: "anthropic" },
    false,
  ),
  superseding(
    "an unkeyable save is not confirmed without a captured fingerprint",
    unkeyable({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
    }),
    { name: "work", baseUrl: "https://gw.example.test/v1/x" },
    unkeyable({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1/x",
    }),
    unkeyable({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1/x",
    }),
    false,
  ),
  // The draft was seeded before another client changed apiKeyEnv, but the save
  // compares against the row on screen when Save was pressed, which carries it.
  listing(
    "an undeclared field another client changed before the save is confirmed",
    { ...SURFACED, apiKeyEnv: "FOREIGN" },
    { ...SAVE, surface: "openai" },
    { ...SURFACED, surface: "openai", apiKeyEnv: "FOREIGN", endpointFingerprint: "fp-after" },
    true,
  ),
  listing(
    "an undeclared variable another client changed before the save is confirmed",
    vertex({
      vars: { GOOGLE_VERTEX_PROJECT: "p1", GOOGLE_VERTEX_LOCATION: "loc-client" },
      baseUrl: "https://resolved.example.test/v1",
    }),
    VAR_SAVE("p2"),
    vertex({
      vars: { GOOGLE_VERTEX_PROJECT: "p2", GOOGLE_VERTEX_LOCATION: "loc-client" },
      baseUrl: "https://resolved.example.test/v1",
      endpointFingerprint: "fp-after",
    }),
    true,
  ),
];

describe("supersededSaveLanded", () => {
  test.each(plainCases)("$name", ({ before, params, authoritative, instances, landed }) => {
    expect(supersededSaveLanded(instances, before, params, authoritative)).toBe(landed ? instances[0] : undefined);
  });

  test("a rename is never a plain save", () => {
    const row = { ...KEYED, name: "work2" };
    expect(supersededSaveLanded([row, KEYED], KEYED, RENAME, row)).toBeUndefined();
  });
});
