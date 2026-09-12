import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
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
    const opened = { opener: {} as unknown };
    const openSpy = vi.spyOn(window, "open").mockReturnValue(opened as unknown as Window);

    const editor = await startOAuthFlow("openai-codex");

    expect(openSpy).toHaveBeenCalledWith("https://auth.example/start", "_blank", "noopener");
    // The features string is not honored by every browser (Safari ignores it),
    // so the handle's opener is nulled as well.
    expect(opened.opener).toBeNull();
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
    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);

    const editor = await startOAuthFlow("openai-codex");

    expect(openSpy).not.toHaveBeenCalled();
    expect(editor).toMatchObject({ kind: "device", userCode: "CODE" });
  });
});
