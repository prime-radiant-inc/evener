import { expect, it } from "vitest";
import { createProviderParams, editProviderParams } from "./providerForm";

const providers = [
  {
    id: "gateway",
    protocol: "openai-chat",
    auth: "apiKey",
    implicit: false,
    varsEnv: ["REGION_ENV", "TENANT_ENV"],
    vars: { REGION: "REGION_ENV", TENANT: "TENANT_ENV" },
  },
];
const draft = {
  name: " work ",
  base: "gateway",
  baseUrl: " https://example.test/v1 ",
  vars: { REGION: " us ", TENANT: " ", OLD_PROVIDER: "discard" },
  apiKeyEnv: " WORK_KEY ",
  credentialHeader: " Authorization: Bearer $WORK_KEY ",
};
it("creates only values belonging to the selected server provider", () => {
  expect(createProviderParams(draft, providers)).toEqual({
    name: "work",
    base: "gateway",
    baseUrl: "https://example.test/v1",
    vars: { REGION: "us" },
    apiKeyEnv: "WORK_KEY",
    credentialHeader: "Authorization: Bearer $WORK_KEY",
  });
});
it("rejects absent providers, blank names and literal credential headers", () => {
  expect(() =>
    createProviderParams({ ...draft, base: "removed" }, providers),
  ).toThrow("provider");
  expect(() =>
    createProviderParams({ ...draft, name: " " }, providers),
  ).toThrow("Name");
  expect(() =>
    createProviderParams(
      { ...draft, credentialHeader: "Bearer literal" },
      providers,
    ),
  ).toThrow(
    "Credential header must reference a $VARIABLE or run a $(command), never a literal secret.",
  );
});

it("accepts a command expression as credential material", () => {
  expect(
    createProviderParams(
      { ...draft, credentialHeader: "Bearer $(get-work-key)" },
      providers,
    ),
  ).toMatchObject({
    credentialHeader: "Bearer $(get-work-key)",
  });
});
it("leaves blank optional creation settings inherited", () => {
  expect(
    createProviderParams(
      { ...draft, vars: {}, apiKeyEnv: " ", credentialHeader: "" },
      providers,
    ),
  ).toMatchObject({
    vars: undefined,
    apiKeyEnv: undefined,
    credentialHeader: undefined,
  });
});
it("distinguishes unchanged endpoint, replacement and explicit reset", () => {
  const instance = { name: "work", baseUrl: "https://example.test/v1" };
  expect(editProviderParams(instance, " https://example.test/v1 ")).toEqual({
    name: "work",
  });
  expect(editProviderParams(instance, " https://new.test ")).toEqual({
    name: "work",
    baseUrl: "https://new.test",
  });
  expect(editProviderParams(instance, " ")).toEqual({
    name: "work",
    clearBaseUrl: true,
  });
  expect(editProviderParams({ name: "work" }, " ")).toEqual({ name: "work" });
});

// The editor was opened on one row of the listing, so its endpoint travels
// with the save as the assertion the hub checks: a name another client has
// re-pointed since must not have its replacement edited or renamed. A row the
// hub could not fingerprint asserts nothing.
it("asserts the endpoint the edited row was opened on", () => {
  expect(
    editProviderParams(
      {
        name: "work",
        baseUrl: "https://example.test/v1",
        endpointFingerprint: "fp-work",
      },
      " https://new.test ",
    ),
  ).toEqual({
    name: "work",
    baseUrl: "https://new.test",
    expectedEndpointFingerprint: "fp-work",
  });
  expect(
    editProviderParams(
      { name: "work", baseUrl: "https://example.test/v1" },
      "https://new.test",
    ),
  ).toEqual({ name: "work", baseUrl: "https://new.test" });
});

it("does not treat environment variable names as template keys", () => {
  expect(createProviderParams(
    { ...draft, vars: { REGION: "us", REGION_ENV: "wrong", TENANT_ENV: "wrong" } },
    providers,
  ).vars).toEqual({ REGION: "us" });
  expect(createProviderParams(draft, providers.map((provider) => ({ ...provider, vars: undefined }))).vars).toBeUndefined();
});
