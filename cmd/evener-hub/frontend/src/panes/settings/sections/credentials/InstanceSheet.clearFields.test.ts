// changedFields decides which entry fields this save changed, and that set is
// what renamedInstanceLanded compares the listing against to decide whether a
// rename landed. A `clear*` request flag names one of those entry fields, so a
// flag normalized to the wrong field name (aPIKeyEnv, say) silently drops the
// field from the comparison - the entry's apiKeyEnv then reads as untouched
// while the save moved it, and the persisted-rename path refuses to follow the
// instance. These tests pin the normalization to an explicit map, field by
// field, rather than to a derivation that only lowercases one letter.
import type { InstanceEditParams } from "@evener/appwire-client";
import { describe, expect, test } from "vitest";
import { CLEAR_FIELD_NAMES, changedFields } from "./instanceLanding";

describe("changedFields maps each clear flag to its entry field", () => {
  test("clearApiKeyEnv normalizes to the camel-cased entry field apiKeyEnv", () => {
    const params: InstanceEditParams = { name: "a", clearApiKeyEnv: true };
    expect(changedFields(params)).toEqual(new Set(["apiKeyEnv"]));
  });

  test("every clear flag normalizes to its entry field", () => {
    const params: InstanceEditParams = {
      name: "a",
      clearBaseUrl: true,
      clearProtocol: true,
      clearSurface: true,
      clearApiKeyEnv: true,
      clearCredentialHeader: true,
    };
    expect(changedFields(params)).toEqual(new Set(["baseUrl", "protocol", "surface", "apiKeyEnv", "credentialHeader"]));
  });

  // The map is exhaustive over the params type, not a best-effort sample: every
  // `clear*` key InstanceEditParams carries is present and names its field. A
  // new flag added to the type without a mapping is then a compile error at the
  // Record declaration, and a mapping whose target drifts is caught here.
  test("the map covers exactly the clear flags the params type carries", () => {
    expect(CLEAR_FIELD_NAMES).toEqual({
      clearBaseUrl: "baseUrl",
      clearProtocol: "protocol",
      clearSurface: "surface",
      clearApiKeyEnv: "apiKeyEnv",
      clearCredentialHeader: "credentialHeader",
    });
  });

  test("value-bearing fields keep their own name, and name/newName are dropped", () => {
    const params: InstanceEditParams = {
      name: "a",
      newName: "b",
      baseUrl: "https://x.example.test",
      vars: { A: "b" },
    };
    expect(changedFields(params)).toEqual(new Set(["baseUrl", "vars"]));
  });
});
