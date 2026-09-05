import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { InstanceEntry, InstanceListResponse } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { resetToastStoreForTests } from "../../../../widgets/toast/store";
import { ConnectProviderDialog } from "./ConnectProviderDialog";

function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "providerId">): InstanceEntry {
  return {
    protocol: "openai-chat",
    auth: "bearer",
    implicit: true,
    isDefault: false,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: true,
    ...overrides,
  };
}

function connectFakeClient(list: InstanceListResponse): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("evener/instance/list", () => list);
  connectionStore.getState().connect(fake);
  return fake;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
  resetToastStoreForTests();
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("ConnectProviderDialog", () => {
  test("saving an API key still requires an explicit successful credential test", async () => {
    const anthropic = instance({ name: "work", providerId: "anthropic", authModes: ["apiKey"] });
    let saved = false;
    const fake = new FakeClient("ready");
    fake.on("evener/instance/list", () => ({
      instances: [saved ? { ...anthropic, activeSource: "store", hasStoredFile: true } : anthropic],
      availableProviders: [],
    }));
    connectionStore.getState().connect(fake);
    fake.on("evener/auth/apiKey/set", (params) => {
      expect(params).toEqual({ provider: "work", value: "sk-test-value" });
      saved = true;
      return {
        provider: "work",
        supported: true,
        signedIn: true,
        activeSource: "store",
        authModes: ["apiKey"],
        hasStoredOAuth: false,
        hasStoredFile: true,
      };
    });
    fake.on("evener/auth/test", (params) => {
      expect(params).toEqual({ provider: "work" });
      return { provider: "work", status: "success", message: "untrusted provider message" };
    });
    const onConnected = vi.fn();
    render(<ConnectProviderDialog onClose={() => {}} onConnected={onConnected} />);
    const chooser = await screen.findByRole("dialog", { name: "Connect provider" });

    expect(fake.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(0);
    await userEvent.setup().click(within(chooser).getByRole("button", { name: "Set API key" }));
    expect(screen.getAllByRole("dialog")).toHaveLength(1);

    await userEvent.setup().type(screen.getByLabelText("API key for work"), "sk-test-value");
    await userEvent.setup().click(screen.getByRole("button", { name: "Save" }));
    const returnedChooser = await screen.findByRole("dialog", { name: "Connect provider" });
    expect(within(returnedChooser).getByText("Configured via stored API key")).toBeTruthy();
    expect(onConnected).not.toHaveBeenCalled();

    await userEvent.setup().click(within(returnedChooser).getByRole("button", { name: "Test connection" }));
    await vi.waitFor(() => expect(onConnected).toHaveBeenCalledTimes(1));
  });

  test("redirect OAuth returns to the chooser and still requires a successful test", async () => {
    const codex = instance({
      name: "personal",
      providerId: "openai-codex",
      auth: "oauth-openai-codex",
      authModes: ["oauth"],
    });
    const fake = connectFakeClient({ instances: [codex], availableProviders: [] });
    fake.on("evener/auth/device/start", (params) => {
      expect(params).toEqual({ provider: "personal" });
      return {
        provider: "personal",
        flowId: "unused-device-flow",
        userCode: "unused",
        verificationUrl: "https://verify.example",
        intervalSeconds: 5,
        fallback: true,
      };
    });
    fake.on("evener/auth/login/start", (params) => {
      expect(params).toEqual({ provider: "personal" });
      return { provider: "personal", flowId: "redirect-flow", url: "https://auth.example/start" };
    });
    fake.on("evener/auth/login/complete", (params) => {
      expect(params).toEqual({
        provider: "personal",
        flowId: "redirect-flow",
        redirectUrl: "https://localhost/callback?code=ok",
      });
      return {
        status: {
          provider: "personal",
          supported: true,
          signedIn: true,
          activeSource: "oauth",
          authModes: ["oauth"],
          hasStoredOAuth: true,
        },
      };
    });
    fake.on("evener/auth/test", () => ({ provider: "personal", status: "success", message: "ignored" }));
    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
    const onConnected = vi.fn();
    render(<ConnectProviderDialog onClose={() => {}} onConnected={onConnected} />);
    const chooser = await screen.findByRole("dialog", { name: "Connect provider" });

    await userEvent.setup().click(within(chooser).getByRole("button", { name: "Sign in" }));
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    expect(openSpy).toHaveBeenCalledWith("https://auth.example/start", "_blank", "noopener");
    await userEvent.setup().type(screen.getByLabelText("Redirect URL"), "https://localhost/callback?code=ok");
    await userEvent.setup().click(screen.getByRole("button", { name: "Finish" }));

    const returnedChooser = await screen.findByRole("dialog", { name: "Connect provider" });
    expect(onConnected).not.toHaveBeenCalled();
    await userEvent.setup().click(within(returnedChooser).getByRole("button", { name: "Test connection" }));
    await vi.waitFor(() => expect(onConnected).toHaveBeenCalledTimes(1));
  });

  test("registry diagnostics remain visible and writesRefused disables adding an instance", async () => {
    connectFakeClient({
      instances: [],
      availableProviders: [
        { id: "anthropic", name: "Anthropic", protocol: "anthropic", auth: "bearer", implicit: false },
      ],
      diagnostics: ['providers.toml: unknown key "type"'],
      writesRefused: true,
    });
    render(<ConnectProviderDialog onClose={() => {}} onConnected={() => {}} />);

    await screen.findByText('providers.toml: unknown key "type"');
    const add = screen.getByRole("button", { name: "Add provider instance" }) as HTMLButtonElement;
    expect(add.disabled).toBe(true);
  });

  test("a failed credential test stays open with a safe message and can be retried", async () => {
    const api = instance({ name: "work", providerId: "anthropic", authModes: ["apiKey"] });
    const fake = connectFakeClient({ instances: [api], availableProviders: [] });
    let attempts = 0;
    fake.on("evener/auth/test", (params) => {
      expect(params).toEqual({ provider: "work" });
      attempts += 1;
      return attempts === 1
        ? { provider: "work", status: "auth_rejected", message: "provider echoed secret sk-leak" }
        : { provider: "work", status: "success", message: "provider success prose" };
    });
    const onConnected = vi.fn();
    render(<ConnectProviderDialog onClose={() => {}} onConnected={onConnected} />);
    const chooser = await screen.findByRole("dialog", { name: "Connect provider" });

    await userEvent.setup().click(within(chooser).getByRole("button", { name: "Test connection" }));
    await screen.findByText("The provider rejected these credentials. Replace the key or sign in again.");
    expect(document.body.textContent).not.toContain("sk-leak");
    expect(onConnected).not.toHaveBeenCalled();

    await userEvent.setup().click(within(chooser).getByRole("button", { name: "Retry test" }));
    await vi.waitFor(() => expect(onConnected).toHaveBeenCalledTimes(1));
    expect(attempts).toBe(2);
  });

  test("cancelling the API-key editor destroys its secret and returns to the chooser", async () => {
    const api = instance({ name: "work", providerId: "anthropic", authModes: ["apiKey"] });
    connectFakeClient({ instances: [api], availableProviders: [] });
    const onClose = vi.fn();
    render(<ConnectProviderDialog onClose={onClose} onConnected={() => {}} />);
    const chooser = await screen.findByRole("dialog", { name: "Connect provider" });
    const user = userEvent.setup();

    await user.click(within(chooser).getByRole("button", { name: "Set API key" }));
    await user.type(screen.getByLabelText("API key for work"), "secret-that-must-disappear");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    const returnedChooser = await screen.findByRole("dialog", { name: "Connect provider" });
    expect(document.body.textContent).not.toContain("secret-that-must-disappear");
    expect(onClose).not.toHaveBeenCalled();

    await user.click(within(returnedChooser).getByRole("button", { name: "Set API key" }));
    expect(screen.getByLabelText("API key for work")).toHaveProperty("value", "");
  });

  test("a registry load failure can be retried without closing the dialog", async () => {
    const keyless = instance({
      name: "ollama",
      providerId: "ollama",
      auth: "none",
      authModes: ["none"],
      credentialRequired: false,
    });
    const fake = new FakeClient("ready");
    let attempts = 0;
    fake.on("evener/instance/list", () => {
      attempts += 1;
      if (attempts === 1) throw new Error("registry unavailable");
      return { instances: [keyless], availableProviders: [] };
    });
    connectionStore.getState().connect(fake);
    render(<ConnectProviderDialog onClose={() => {}} onConnected={() => {}} />);

    await screen.findByRole("alert");
    await userEvent.setup().click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findAllByText("ollama")).toHaveLength(2);
    expect(attempts).toBe(2);
  });

  test("a keyless instance offers connection testing without a credential editor", async () => {
    const keyless = instance({
      name: "ollama",
      providerId: "ollama",
      auth: "none",
      authModes: ["none"],
      credentialRequired: false,
    });
    const fake = connectFakeClient({ instances: [keyless], availableProviders: [] });
    fake.on("evener/auth/test", (params) => {
      expect(params).toEqual({ provider: "ollama" });
      return { provider: "ollama", status: "success", message: "ignored" };
    });
    const onConnected = vi.fn();
    render(<ConnectProviderDialog onClose={() => {}} onConnected={onConnected} />);
    const chooser = await screen.findByRole("dialog", { name: "Connect provider" });

    expect(within(chooser).queryByRole("button", { name: /API key|Sign in/ })).toBeNull();
    await userEvent.setup().click(within(chooser).getByRole("button", { name: "Test connection" }));
    await vi.waitFor(() => expect(onConnected).toHaveBeenCalledTimes(1));
  });

  test("device authorization returns to the chooser before the explicit connection test", async () => {
    const codex = instance({
      name: "personal",
      providerId: "openai-codex",
      auth: "oauth-openai-codex",
      authModes: ["oauth"],
    });
    const fake = connectFakeClient({ instances: [codex], availableProviders: [] });
    fake.on("evener/auth/device/start", () => ({
      provider: "personal",
      flowId: "device-flow",
      userCode: "ABCD-EFGH",
      verificationUrl: "https://verify.example",
      intervalSeconds: 1,
    }));
    fake.on("evener/auth/device/poll", (params) => {
      expect(params).toEqual({ provider: "personal", flowId: "device-flow" });
      return { state: "authorized" };
    });
    fake.on("evener/auth/test", () => ({ provider: "personal", status: "success", message: "ignored" }));
    const onConnected = vi.fn();
    render(<ConnectProviderDialog onClose={() => {}} onConnected={onConnected} />);
    const signIn = await screen.findByRole("button", { name: "Sign in" });
    vi.useFakeTimers();

    await act(async () => fireEvent.click(signIn));
    expect(screen.getByText("ABCD-EFGH")).toBeTruthy();
    await act(() => vi.advanceTimersByTimeAsync(1000));
    const returnedChooser = screen.getByRole("dialog", { name: "Connect provider" });
    expect(onConnected).not.toHaveBeenCalled();

    await act(async () => fireEvent.click(within(returnedChooser).getByRole("button", { name: "Test connection" })));
    expect(onConnected).toHaveBeenCalledTimes(1);
  });

  test("a credential test result is discarded when the instance list changes underneath it", async () => {
    const first = instance({ name: "work", providerId: "anthropic", authModes: ["apiKey"] });
    const changed = { ...first, baseUrl: "https://changed.example" };
    const response = deferred<{ provider: string; status: string; message: string }>();
    let listCalls = 0;
    const fake = new FakeClient("ready");
    fake.on("evener/instance/list", () => {
      listCalls += 1;
      return { instances: [listCalls === 1 ? first : changed], availableProviders: [] };
    });
    fake.on("evener/auth/test", () => response.promise);
    connectionStore.getState().connect(fake);
    const onConnected = vi.fn();
    render(<ConnectProviderDialog onClose={() => {}} onConnected={onConnected} />);
    const chooser = await screen.findByRole("dialog", { name: "Connect provider" });
    await userEvent.setup().click(within(chooser).getByRole("button", { name: "Test connection" }));

    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    response.resolve({ provider: "work", status: "success", message: "ignored" });
    await act(async () => {
      await response.promise;
    });

    expect(onConnected).not.toHaveBeenCalled();
    expect(within(chooser).getByRole("button", { name: "Test connection" })).toBeTruthy();
  });

  test("an OAuth start that finishes after unmount cannot open a browser or complete the flow", async () => {
    const codex = instance({
      name: "personal",
      providerId: "openai-codex",
      auth: "oauth-openai-codex",
      authModes: ["oauth"],
    });
    const start = deferred<{
      provider: string;
      flowId: string;
      userCode: string;
      verificationUrl: string;
      intervalSeconds: number;
      fallback: boolean;
    }>();
    const fake = connectFakeClient({ instances: [codex], availableProviders: [] });
    fake.on("evener/auth/device/start", () => start.promise);
    fake.on("evener/auth/login/start", () => ({
      provider: "personal",
      flowId: "redirect-flow",
      url: "https://auth.example/start",
    }));
    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
    const onConnected = vi.fn();
    const rendered = render(<ConnectProviderDialog onClose={() => {}} onConnected={onConnected} />);
    const chooser = await screen.findByRole("dialog", { name: "Connect provider" });
    await userEvent.setup().click(within(chooser).getByRole("button", { name: "Sign in" }));
    rendered.unmount();

    start.resolve({
      provider: "personal",
      flowId: "device-flow",
      userCode: "unused",
      verificationUrl: "https://verify.example",
      intervalSeconds: 5,
      fallback: true,
    });
    await act(async () => {
      await start.promise;
    });

    expect(openSpy).not.toHaveBeenCalled();
    expect(onConnected).not.toHaveBeenCalled();
  });

  test("an API-key editor closes if its registry instance disappears", async () => {
    const api = instance({ name: "work", providerId: "anthropic", authModes: ["apiKey"] });
    let listCalls = 0;
    const fake = new FakeClient("ready");
    fake.on("evener/instance/list", () => {
      listCalls += 1;
      return { instances: listCalls === 1 ? [api] : [], availableProviders: [] };
    });
    connectionStore.getState().connect(fake);
    render(<ConnectProviderDialog onClose={() => {}} onConnected={() => {}} />);
    await userEvent.setup().click(await screen.findByRole("button", { name: "Set API key" }));
    expect(screen.getByRole("dialog", { name: "Set API key for work" })).toBeTruthy();

    await act(async () => {
      await credentialsStore.getState().fetch();
    });

    expect(screen.queryByRole("dialog", { name: "Set API key for work" })).toBeNull();
    expect(screen.getByRole("dialog", { name: "Connect provider" })).toBeTruthy();
  });
});
