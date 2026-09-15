import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import { captureNewTabs, NEW_TAB_POLICY, openedNewTab } from "../../../../shell/openInNewTab.testSupport";
import { connectionStore } from "../../../../stores/connection";
import { resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { startOAuthFlow } from "./oauthFlow";

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
