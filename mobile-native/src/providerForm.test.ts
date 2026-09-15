import { expect, it } from "vitest";
import { createProviderParams, editProviderParams, openedEditParams } from "./providerForm";

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
  ).toThrow("$VARIABLE");
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

it("asserts the endpoint fingerprint the editor displayed", () => {
  const instance = {
    name: "work",
    baseUrl: "https://example.test/v1",
    endpointFingerprint: "fp-work",
  };
  expect(editProviderParams(instance, " https://example.test/v1 ")).toEqual({
    name: "work",
    expectedEndpointFingerprint: "fp-work",
  });
  expect(editProviderParams(instance, " https://new.test ")).toEqual({
    name: "work",
    baseUrl: "https://new.test",
    expectedEndpointFingerprint: "fp-work",
  });
  expect(editProviderParams(instance, " ")).toEqual({
    name: "work",
    clearBaseUrl: true,
    expectedEndpointFingerprint: "fp-work",
  });
});

it("asserts nothing for a row the hub could not key", () => {
  expect(
    editProviderParams(
      { name: "work", baseUrl: "https://example.test/v1" },
      " https://new.test ",
    ),
  ).toEqual({ name: "work", baseUrl: "https://new.test" });
});

it("asserts the endpoint the editor was opened on, not the one the listing moved to", () => {
  const opened = {
    name: "work",
    baseUrl: "https://example.test/v1",
    endpointFingerprint: "fp-v1",
  };
  const moved = {
    name: "work",
    baseUrl: "https://example.test/v2",
    endpointFingerprint: "fp-v2",
  };
  // The listing refreshed under the open editor; the save still describes the
  // endpoint the user reviewed, so the hub refuses it rather than writing the
  // stale draft onto the replacement.
  expect(openedEditParams(opened, moved, " https://example.test/v1/x ")).toEqual({
    name: "work",
    baseUrl: "https://example.test/v1/x",
    expectedEndpointFingerprint: "fp-v1",
  });
  // Nothing was opened (a create): the current row is the only one there is.
  expect(openedEditParams(undefined, moved, " https://example.test/v2 ")).toEqual({
    name: "work",
    expectedEndpointFingerprint: "fp-v2",
  });
  expect(openedEditParams(undefined, undefined, "https://example.test")).toBeNull();
});

it("does not treat environment variable names as template keys", () => {
  expect(createProviderParams(
    { ...draft, vars: { REGION: "us", REGION_ENV: "wrong", TENANT_ENV: "wrong" } },
    providers,
  ).vars).toEqual({ REGION: "us" });
  expect(createProviderParams(draft, providers.map((provider) => ({ ...provider, vars: undefined }))).vars).toBeUndefined();
});
