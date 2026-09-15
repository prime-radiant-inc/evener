import { expect, it } from "vitest";
import { isEndpointConflict, WireError } from "../../appwire-client/typescript/errors";
import {
  createProviderParams,
  editProviderParams,
  ENDPOINT_CHANGED_MESSAGE,
  endpointConflictFor,
} from "./providerForm";

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

it("recognizes the hub's endpoint refusal, and only that", () => {
  const conflict = new WireError("moved", -32013, {
    evenerErrorInfo: "conflict",
  });
  expect(isEndpointConflict(conflict)).toBe(true);
  expect(isEndpointConflict(new WireError("boom", -32000))).toBe(false);
  expect(isEndpointConflict(new Error("boom"))).toBe(false);
});

it("claims a moved connection only for a rejected edit", () => {
  const conflict = new WireError("moved", -32013, {
    evenerErrorInfo: "conflict",
  });
  expect(endpointConflictFor(conflict, true)).toBe(ENDPOINT_CHANGED_MESSAGE);
  // A create asserts no destination, so its conflict is a name collision and
  // must fall through to the generic failure rather than blame the connection.
  expect(endpointConflictFor(conflict, false)).toBeNull();
  expect(endpointConflictFor(new WireError("boom", -32000), true)).toBeNull();
  expect(endpointConflictFor(new Error("boom"), true)).toBeNull();
});

it("does not treat environment variable names as template keys", () => {
  expect(createProviderParams(
    { ...draft, vars: { REGION: "us", REGION_ENV: "wrong", TENANT_ENV: "wrong" } },
    providers,
  ).vars).toEqual({ REGION: "us" });
  expect(createProviderParams(draft, providers.map((provider) => ({ ...provider, vars: undefined }))).vars).toBeUndefined();
});
