import type { InstanceEntry } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { captureNewTabs, NEW_TAB_POLICY, openedNewTab } from "../../../../shell/openInNewTab.testSupport";
import { connectionStore } from "../../../../stores/connection";
import { resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { startOAuthFlow, supportsHostDeviceSignIn } from "./oauthFlow";

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  vi.restoreAllMocks();
});

describe("startOAuthFlow", () => {
  test("the redirect fallback opens the authorize URL without an opener", async () => {
    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    fake.on("evener/auth/device/start", () => ({
      provider: "openai-codex",
      flowId: "flow",
      userCode: "CODE",
      verificationUrl: "https://verify",
      intervalSeconds: 1,
      fallback: true,
    }));
    fake.on("evener/auth/login/start", () => ({
      provider: "openai-codex",
      flowId: "flow",
      url: "https://auth.example/start",
    }));
    const anchors = captureNewTabs();

    const editor = await startOAuthFlow("openai-codex");

    // The anchor's rel decides the new document's opener policy, and the opener
    // is what decides whether it runs with a copy of this tab's sessionStorage.
    // That copy is made as the document is created, so no fix-up on a returned
    // window handle could undo it.
    expect(openedNewTab(anchors)).toEqual({
      url: "https://auth.example/start",
      target: "_blank",
      rel: NEW_TAB_POLICY,
    });
    expect(editor).toMatchObject({ kind: "oauth-redirect", authUrl: "https://auth.example/start" });
  });

  test("the device flow opens no browser window", async () => {
    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    fake.on("evener/auth/device/start", () => ({
      provider: "openai-codex",
      flowId: "flow",
      userCode: "CODE",
      verificationUrl: "https://verify",
      intervalSeconds: 5,
    }));
    const anchors = captureNewTabs();

    const editor = await startOAuthFlow("openai-codex");

    expect(anchors).toHaveLength(0);
    expect(editor).toMatchObject({ kind: "device", userCode: "CODE" });
  });
});

// The instance rows the support predicate is asked about carry the wire's own
// two fields: `auth` is the resolved transport scheme and `providerId` the
// registry id the instance is built on (appwire-client's InstanceEntry;
// cmd/evener-hub/app_instances.go's entryFor fills both).
function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "providerId">): InstanceEntry {
  return {
    protocol: "openai-responses",
    auth: "bearer",
    implicit: false,
    isDefault: false,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: true,
    ...overrides,
  };
}

describe("supportsHostDeviceSignIn", () => {
  test("offers the action for the curated Codex provider", () => {
    expect(
      supportsHostDeviceSignIn(
        instance({
          name: "openai-codex",
          providerId: "openai-codex",
          auth: "oauth-openai-codex",
          authModes: ["oauth"],
          implicit: true,
        }),
      ),
    ).toBe(true);
  });

  test("offers it for an instance signed in through Codex OAuth on another base", () => {
    // `auth` is authored independently of `base` (llm/registry/load.go's
    // instanceRecord folds the instance's own fields over the base it inherits,
    // llm/registry/merge.go's mergeTransport takes the layer's auth), and the
    // hub's own gate is the resolved scheme (cmd/evener-hub/app_auth.go's
    // instanceIsCodex: inst.Auth == registry.AuthOAuthOpenAICodex). The host
    // accepts this instance, so the affordance has to be offered for it: gating
    // on the provider id silently withheld a sign-in that works.
    expect(
      supportsHostDeviceSignIn(
        instance({
          name: "work",
          base: "openai",
          providerId: "openai",
          auth: "oauth-openai-codex",
          authModes: ["oauth"],
        }),
      ),
    ).toBe(true);
  });

  test("withholds it for an openai-codex-based instance carrying another scheme", () => {
    // The other direction of the same mismatch: the provider id says Codex while
    // the scheme the host gates on says otherwise, so the button's only possible
    // outcome is the host's "OAuth is not supported for instance" refusal.
    expect(
      supportsHostDeviceSignIn(
        instance({
          name: "codex-gateway",
          base: "openai-codex",
          providerId: "openai-codex",
          auth: "bearer",
          authModes: ["apiKey"],
        }),
      ),
    ).toBe(false);
  });
});
