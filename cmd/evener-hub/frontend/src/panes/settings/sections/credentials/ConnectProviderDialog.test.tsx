import { act, cleanup, fireEvent, render as renderComponent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type {
  AuthDeviceStartResponse,
  AuthStatusResponse,
  AuthTestResponse,
  InstanceEntry,
  InstanceListResponse,
} from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { getToasts, resetToastStoreForTests } from "../../../../widgets/toast/store";
import { ConnectProviderDialog } from "./ConnectProviderDialog";
import { CredentialsSection } from "./CredentialsSection";

// Existing cases exercise management, now reached explicitly from discovery.
function render(element: ReactElement) {
  const view = renderComponent(element);
  if (element.type === ConnectProviderDialog) {
    fireEvent.click(screen.getByText("Already configured access on this host?"));
    fireEvent.click(screen.getByRole("button", { name: "Manage existing connections" }));
  }
  return view;
}

test("opens compact discovery by default and keeps management and the full editor returnable", async () => {
  const row = instance({ name: "anthropic", providerId: "anthropic", authModes: ["apiKey"] });
  connectFakeClient({
    instances: [row],
    availableProviders: [
      {
        id: "anthropic",
        name: "Anthropic",
        protocol: row.protocol,
        auth: row.auth,
        implicit: true,
        authModes: ["apiKey"],
        setup: row,
      },
    ],
  });
  renderComponent(<ConnectProviderDialog onClose={() => {}} onConnected={() => {}} />);
  const user = userEvent.setup();
  expect(await screen.findByRole("button", { name: "Anthropic" })).toBeTruthy();
  await user.click(screen.getByText("Already configured access on this host?"));
  await user.click(screen.getByRole("button", { name: "Manage existing connections" }));
  expect(await screen.findByRole("button", { name: "Set API key" })).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Full provider settings" }));
  expect(await screen.findByRole("button", { name: "+ Add provider instance" })).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "+ Add provider instance" }));
  const editor = await screen.findByRole("dialog", { name: "Add provider instance" });
  expect(within(editor).getByLabelText("Protocol")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "All providers" })).toBeNull();
  await user.click(within(editor).getByRole("button", { name: "Cancel" }));
  await user.click(screen.getByRole("button", { name: "Back to connection choices" }));
  expect(await screen.findByRole("button", { name: "Anthropic" })).toBeTruthy();
});

test("full-editor repair returns to the created connection and retained credential draft without duplicate creation", async () => {
  const row = instance({
    name: "team-custom",
    providerId: "openai",
    implicit: false,
    baseUrl: "https://custom.example/v1",
    authModes: ["apiKey"],
  });
  const provider = {
    id: "openai",
    name: "OpenAI",
    protocol: row.protocol,
    auth: row.auth,
    implicit: false,
    authModes: ["apiKey"],
  };
  let created = false;
  let saved = false;
  const listing = (): InstanceListResponse => ({
    instances: created ? [{ ...row, activeSource: saved ? "store" : "none", hasStoredFile: saved }] : [],
    availableProviders: [provider],
  });
  const fake = connectFakeClient(listing());
  fake.on("evener/instance/list", listing);
  fake.on("evener/instance/create", () => {
    created = true;
    return listing();
  });
  fake.on("evener/auth/apiKey/set", () => {
    throw new Error("fixture save failure");
  });
  fake.on("evener/auth/test", () => ({ provider: "team-custom", status: "success", message: "" }));
  const connected = vi.fn();
  renderComponent(<ConnectProviderDialog onClose={() => {}} onConnected={connected} />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "OpenAI" }));
  await user.click(screen.getByRole("button", { name: "Configure provider" }));
  await user.type(screen.getByLabelText("Name"), "team-custom");
  await user.type(screen.getByLabelText("Base URL (optional)"), "https://custom.example/v1");
  await user.click(screen.getByRole("button", { name: "Create" }));
  await user.type(await screen.findByLabelText("API key"), "repair-draft");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  expect(await screen.findByRole("alert")).toBe(document.activeElement);
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "repair-draft");
  await user.click(screen.getByText("Advanced settings"));
  await user.click(screen.getByRole("button", { name: "Open full connection editor" }));
  expect(document.querySelectorAll('[role="dialog"]')).toHaveLength(1);
  expect(screen.queryByLabelText("API key")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Full provider settings" }));
  await user.click(screen.getByRole("button", { name: "Back to connection choices" }));
  expect(screen.queryByLabelText("API key")).toHaveProperty("value", "repair-draft");
  expect(document.querySelectorAll('[role="dialog"]')).toHaveLength(1);
  fake.on("evener/auth/apiKey/set", () => {
    saved = true;
    return {
      provider: "team-custom",
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
      hasStoredFile: true,
    };
  });
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await user.click(await screen.findByRole("button", { name: "Continue" }));
  expect(connected).toHaveBeenCalledWith("team-custom");
  expect(fake.calls.filter((call) => call.method === "evener/instance/create")).toHaveLength(1);
  expect(fake.calls.filter((call) => call.method === "evener/auth/apiKey/set").map((call) => call.params)).toEqual([
    { provider: "team-custom", value: "repair-draft" },
    { provider: "team-custom", value: "repair-draft" },
  ]);
});

// Real wrapper, stores and editors; only AppWire responses are scripted.
function guidedRepair() {
  let row = instance({
    name: "openai",
    providerId: "openai",
    baseUrl: "https://original.example/v1",
    authModes: ["apiKey"],
  });
  const listing = (): InstanceListResponse => ({
    instances: [structuredClone(row)],
    availableProviders: [
      {
        id: "openai",
        name: "OpenAI",
        protocol: "openai-chat",
        auth: "bearer",
        implicit: true,
        authModes: ["apiKey"],
        setup: structuredClone(row),
      },
      {
        id: "anthropic",
        name: "Anthropic",
        protocol: "anthropic",
        auth: "api-key",
        implicit: true,
        authModes: ["apiKey"],
        setup: instance({
          name: "anthropic",
          providerId: "anthropic",
          baseUrl: "https://anthropic.example",
          authModes: ["apiKey"],
        }),
      },
    ],
  });
  const fake = connectFakeClient(listing());
  fake.on("evener/instance/list", listing);
  const status: AuthStatusResponse = {
    provider: "openai",
    supported: true,
    signedIn: true,
    activeSource: "store",
    hasStoredOAuth: false,
    hasStoredFile: true,
  };
  fake.on("evener/auth/apiKey/set", () => {
    row = { ...row, activeSource: "store", hasStoredFile: true };
    return status;
  });
  fake.on("evener/auth/test", () => ({ provider: "openai", status: "success", message: "" }));
  const connected = vi.fn();
  const close = vi.fn();
  const view = renderComponent(<ConnectProviderDialog onClose={close} onConnected={connected} />);
  const user = userEvent.setup();
  return {
    fake,
    user,
    connected,
    close,
    view,
    listing,
    status,
    change: (patch: Partial<InstanceEntry>) => {
      row = { ...row, ...patch };
    },
    async select() {
      await user.click(await screen.findByRole("button", { name: "OpenAI" }));
      await user.type(screen.getByLabelText("API key"), "excursion-draft");
    },
    async leave() {
      await user.click(screen.getByText("Advanced settings"));
      await user.click(screen.getByRole("button", { name: "Open full connection editor" }));
      expect(document.querySelectorAll('[role="dialog"]')).toHaveLength(1);
      expect(screen.queryByLabelText("API key")).toBeNull();
      expect(screen.getByRole("dialog").contains(document.activeElement)).toBe(true);
    },
    async back() {
      await user.click(screen.getByRole("button", { name: "Back to connection choices" }));
      expect(document.querySelectorAll('[role="dialog"]')).toHaveLength(1);
      expect(screen.getByRole("dialog").contains(document.activeElement)).toBe(true);
    },
  };
}

test.each(["save", "refresh", "check", "result"])(
  "repair excursion invalidates %s without hidden dialogs or background checks",
  async (phase) => {
    const h = guidedRepair();
    const save = deferred<AuthStatusResponse>();
    const refresh = deferred<InstanceListResponse>();
    const check = deferred<AuthTestResponse>();
    await h.select();
    if (phase === "save") h.fake.on("evener/auth/apiKey/set", () => save.promise);
    if (phase === "refresh") h.fake.on("evener/instance/list", () => refresh.promise);
    if (phase === "check") h.fake.on("evener/auth/test", () => check.promise);
    await h.user.click(screen.getByRole("button", { name: "Save and check" }));
    if (phase === "result") expect(await screen.findByRole("button", { name: "Continue" })).toBeTruthy();
    else
      expect(
        await screen.findByRole("button", {
          name: phase === "save" ? "Saving…" : phase === "refresh" ? "Refreshing access…" : "Checking model list…",
        }),
      ).toBeTruthy();
    await h.leave();
    const calls = h.fake.calls.length;
    await act(async () => {
      save.resolve(h.status);
      refresh.resolve(h.listing());
      check.resolve({ provider: "openai", status: "success", message: "" });
      await Promise.all([save.promise, refresh.promise, check.promise]);
    });
    expect(h.fake.calls).toHaveLength(calls);
    expect(h.connected).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog").contains(document.activeElement)).toBe(true);
    await h.back();
    expect(screen.getByLabelText("API key")).toHaveProperty("value", "excursion-draft");
    expect(screen.queryByRole("button", { name: "Continue" })).toBeNull();
    // Leaving and returning changed nothing: invalidation is not a reportable
    // change, so no alert may claim the connection or configuration did.
    expect(screen.queryByText(/Connection or configuration changed/)).toBeNull();
    expect(screen.getByRole("button", { name: phase === "save" ? "Save and check" : "Retry check" })).toHaveProperty(
      "disabled",
      false,
    );
    expect(h.fake.calls).toHaveLength(calls);
  },
);

test("a refresh superseded by the credential notification's own refetch is not reported as a failure", async () => {
  const h = guidedRepair();
  await h.select();
  const first = deferred<InstanceListResponse>();
  const second = deferred<InstanceListResponse>();
  let listingCalls = 0;
  h.fake.on("evener/instance/list", () => {
    listingCalls += 1;
    if (listingCalls === 1) return first.promise;
    if (listingCalls === 2) return second.promise;
    return h.listing();
  });

  await h.user.click(screen.getByRole("button", { name: "Save and check" }));
  // The credential save makes the hub emit evener/auth/updated, and the store's
  // own debounced refetch starts while this refresh's listing is still in
  // flight - superseding it.
  act(() => {
    h.fake.emitNotification({ method: "evener/auth/updated", params: {} });
  });
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 300));
  });
  expect(listingCalls).toBe(2);

  // The dropped response then lands: the store applies nothing, so without a
  // second ask the connector would blame the save for the coalescing race.
  await act(async () => {
    first.resolve(h.listing());
  });
  expect(screen.queryByText(/Access could not be refreshed/)).toBeNull();
  expect(await screen.findByRole("button", { name: "Continue" })).toBeTruthy();
});

test("repair excursion cancels a pending guided OAuth start without opening a hidden flow", async () => {
  const h = guidedRepair();
  h.change({ authModes: ["apiKey", "oauth"] });
  await act(async () => credentialsStore.getState().fetch());
  const pending = deferred<AuthDeviceStartResponse>();
  h.fake.on("evener/auth/device/start", () => pending.promise);
  await h.select();
  await h.user.click(screen.getByRole("button", { name: "Sign in" }));
  await h.leave();
  const calls = h.fake.calls.length;
  await act(async () => {
    pending.resolve({
      provider: "openai",
      fallback: true,
      flowId: "",
      userCode: "",
      verificationUrl: "",
      intervalSeconds: 0,
    });
    await pending.promise;
  });
  expect(h.fake.calls).toHaveLength(calls);
  expect(document.querySelectorAll('[role="dialog"]')).toHaveLength(1);
  await h.back();
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "excursion-draft");
  expect(screen.getByRole("button", { name: "Sign in" })).toHaveProperty("disabled", false);
  expect(h.connected).not.toHaveBeenCalled();
});

test.each(["destination", "source"])(
  "repair return requires fresh %s review before checking saved access",
  async (changed) => {
    const h = guidedRepair();
    await h.select();
    await h.user.click(screen.getByRole("button", { name: "Save and check" }));
    expect(await screen.findByRole("button", { name: "Continue" })).toBeTruthy();
    await h.leave();
    await h.user.click(screen.getByRole("button", { name: "Full provider settings" }));
    h.change(changed === "destination" ? { baseUrl: "https://repaired.example/v1" } : { activeSource: "env" });
    await act(async () => credentialsStore.getState().fetch());
    await h.back();
    expect(screen.getByLabelText("API key")).toHaveProperty("value", "excursion-draft");
    expect(screen.queryByRole("button", { name: "Continue" })).toBeNull();
    await h.user.click(screen.getByRole("button", { name: "Retry check" }));
    const review = await screen.findByRole("button", { name: "Use reviewed access and check" });
    expect(h.fake.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);
    expect(h.fake.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(1);
    await h.user.click(review);
    expect(await screen.findByRole("button", { name: "Continue" })).toBeTruthy();
    expect(h.fake.calls.filter((call) => call.method === "evener/auth/test").map((call) => call.params)).toEqual([
      { provider: "openai" },
      { provider: "openai" },
    ]);
  },
);

test("provider identity changes while repairing irreversibly discard the old credential draft", async () => {
  const h = guidedRepair();
  await h.select();
  await h.leave();
  h.change({ providerId: "anthropic" });
  await act(async () => credentialsStore.getState().fetch());
  await h.back();
  expect(screen.getByRole("dialog", { name: "Connect Anthropic" })).toBeTruthy();
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "");
  await h.user.click(screen.getByRole("button", { name: "Save and check" }));
  expect(screen.getByLabelText("API key")).toBe(document.activeElement);
  expect(h.fake.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
  await h.leave();
  h.change({ providerId: "openai" });
  await act(async () => credentialsStore.getState().fetch());
  await h.back();
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "");
});

test.each(["change", "cancel", "dismiss-away"])("%s after repair does not retain a guided secret", async (action) => {
  const h = guidedRepair();
  await h.select();
  await h.leave();
  if (action !== "dismiss-away") await h.back();
  if (action === "change") {
    await h.user.click(screen.getByRole("button", { name: "Change provider" }));
    await h.user.click(screen.getByRole("button", { name: "Anthropic" }));
    expect(screen.getByLabelText("API key")).toHaveProperty("value", "");
    await h.user.click(screen.getByRole("button", { name: "Change provider" }));
  } else {
    await h.user.click(screen.getByRole("button", { name: action === "cancel" ? "Cancel" : "Close" }));
    expect(h.close).toHaveBeenCalledTimes(1);
    h.view.unmount();
    renderComponent(<ConnectProviderDialog onClose={h.close} onConnected={h.connected} />);
  }
  await h.user.click(await screen.findByRole("button", { name: "OpenAI" }));
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "");
  expect(document.querySelectorAll('[role="dialog"]')).toHaveLength(1);
  expect(h.fake.calls.filter((call) => call.method.startsWith("evener/auth/"))).toHaveLength(0);
});

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
  test.each(["onboarding", "settings"])(
    "%s recovers when its first listing is interrupted by reconnect",
    async (view) => {
      const old = deferred<InstanceListResponse>();
      const fake = new FakeClient("ready");
      fake.on("evener/instance/list", () => old.promise);
      connectionStore.getState().connect(fake);
      render(
        view === "onboarding" ? (
          <ConnectProviderDialog onClose={() => {}} onConnected={() => {}} />
        ) : (
          <CredentialsSection sectionId="credentials" />
        ),
      );
      await act(async () => {
        fake.emitStateChange("reconnecting");
        fake.on("evener/instance/list", () => ({
          instances: [instance({ name: "recovered-provider", providerId: "anthropic", authModes: ["apiKey"] })],
          availableProviders: [],
        }));
        fake.emitReady();
        old.resolve({ instances: [], availableProviders: [] });
        await old.promise;
      });
      expect(await screen.findByText("recovered-provider")).toBeTruthy();
      expect(credentialsStore.getState().loading).toBe(false);
    },
  );

  test.each(["save", "refresh"])(
    "a dismissed key editor cannot close a new draft after its %s finishes",
    async (phase) => {
      const row = instance({ name: "work", providerId: "anthropic", authModes: ["apiKey"] });
      const list = { instances: [row], availableProviders: [] };
      const fake = connectFakeClient(list);
      const save = deferred<AuthStatusResponse>();
      const refresh = deferred<InstanceListResponse>();
      fake.on("evener/auth/apiKey/set", () => save.promise);
      render(<ConnectProviderDialog onClose={() => {}} onConnected={() => {}} />);
      const user = userEvent.setup();
      await user.click(await screen.findByRole("button", { name: "Set API key" }));
      await user.type(screen.getByLabelText("API key for work"), "old-key");
      await user.click(screen.getByRole("button", { name: "Save" }));
      const saved: AuthStatusResponse = {
        provider: "work",
        supported: true,
        signedIn: true,
        activeSource: "store",
        hasStoredOAuth: false,
      };
      if (phase === "refresh") {
        fake.on("evener/instance/list", () => refresh.promise);
        await act(async () => {
          save.resolve(saved);
          await save.promise;
        });
      }
      await user.click(screen.getByRole("button", { name: "Close" }));
      if (phase === "refresh") {
        fake.on("evener/instance/list", () => list);
        await act(async () => credentialsStore.getState().fetch());
      }
      await user.click(await screen.findByRole("button", { name: "Set API key" }));
      await user.type(screen.getByLabelText("API key for work"), "new-draft");
      await act(async () => {
        save.resolve(saved);
        refresh.resolve(list);
        await save.promise;
        await refresh.promise;
      });
      expect(screen.getByLabelText("API key for work")).toHaveProperty("value", "new-draft");
      expect(getToasts().some((toast) => toast.kind === "success")).toBe(false);
    },
  );

  test("a removed API-key instance does not reopen its editor when restored", async () => {
    const row = instance({ name: "work", providerId: "anthropic", authModes: ["apiKey"] });
    connectFakeClient({ instances: [row], availableProviders: [] });
    render(<ConnectProviderDialog onClose={() => {}} onConnected={() => {}} />);
    await userEvent.setup().click(await screen.findByRole("button", { name: "Set API key" }));
    await act(async () => credentialsStore.setState({ instances: [] }));
    expect(screen.getByRole("dialog", { name: "Connect provider" })).toBeTruthy();
    await act(async () => credentialsStore.setState({ instances: [row] }));
    expect(screen.getByRole("dialog", { name: "Connect provider" })).toBeTruthy();
  });

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
    const userLayer = "user layer: /Users/jesse/.config/evener/providers.toml";
    connectFakeClient({
      instances: [],
      availableProviders: [
        { id: "anthropic", name: "Anthropic", protocol: "anthropic", auth: "bearer", implicit: false },
      ],
      diagnostics: [userLayer, 'providers.toml: unknown key "type"'],
      userLayer,
      writesRefused: true,
    });
    render(<ConnectProviderDialog onClose={() => {}} onConnected={() => {}} />);

    await screen.findByText('providers.toml: unknown key "type"');
    expect(screen.queryByText(userLayer)).toBeNull();
    const add = screen.getByRole("button", { name: "Add provider instance" }) as HTMLButtonElement;
    expect(add.disabled).toBe(true);
  });

  test("a healthy user-layer location is not presented as a provider warning", async () => {
    const userLayer = "user layer: /Users/jesse/.config/evener/providers.toml";
    connectFakeClient({
      instances: [instance({ name: "work", providerId: "anthropic", authModes: ["apiKey"] })],
      availableProviders: [],
      diagnostics: [userLayer],
      userLayer,
    });
    render(<ConnectProviderDialog onClose={() => {}} onConnected={() => {}} />);

    await screen.findByRole("dialog", { name: "Connect provider" });
    expect(screen.queryByRole("list", { name: "Provider warnings" })).toBeNull();
    expect(screen.queryByText(userLayer)).toBeNull();
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

  test.each(["replacement", "reconnect"])(
    "a %s invalidates a pending test before the registry refresh",
    async (change) => {
      const response = deferred<{ provider: string; status: string; message: string }>();
      const fake = connectFakeClient({
        instances: [instance({ name: "work", providerId: "anthropic", authModes: ["apiKey"] })],
        availableProviders: [],
      });
      fake.on("evener/auth/test", () => response.promise);
      const onConnected = vi.fn();
      render(<ConnectProviderDialog onClose={() => {}} onConnected={onConnected} />);
      await userEvent.setup().click(await screen.findByRole("button", { name: "Test connection" }));
      await act(async () => {
        if (change === "replacement") connectionStore.getState().connect(new FakeClient("connecting"));
        else fake.emitStateChange("reconnecting");
        response.resolve({ provider: "work", status: "success", message: "" });
        await response.promise;
      });
      expect(onConnected).not.toHaveBeenCalled();
      expect(screen.getByRole("button", { name: "Retry test" })).toBeTruthy();
      await act(async () => {
        connectionStore.getState().connect(fake);
        fake.emitReady();
        await credentialsStore.getState().fetch();
      });
      expect(screen.getByRole("button", { name: "Retry test" })).toBeTruthy();
    },
  );

  test("a reconnect invalidates a pending OAuth start before the registry refresh", async () => {
    const start = deferred<{
      provider: string;
      flowId: string;
      userCode: string;
      verificationUrl: string;
      intervalSeconds: number;
    }>();
    const fake = connectFakeClient({
      instances: [instance({ name: "work", providerId: "openai-codex", authModes: ["oauth"] })],
      availableProviders: [],
    });
    fake.on("evener/auth/device/start", () => start.promise);
    render(<ConnectProviderDialog onClose={() => {}} onConnected={() => {}} />);
    await userEvent.setup().click(await screen.findByRole("button", { name: "Sign in" }));
    await act(async () => {
      fake.emitStateChange("reconnecting");
      start.resolve({
        provider: "work",
        flowId: "old-flow",
        userCode: "OLD",
        verificationUrl: "https://login.example",
        intervalSeconds: 5,
      });
      await start.promise;
    });
    expect(screen.getByRole("dialog", { name: "Connect provider" })).toBeTruthy();
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
    expect(
      within(chooser).getByText("Provider configuration refreshed while testing. Test the connection again."),
    ).toBeTruthy();
    expect(within(chooser).getByRole("button", { name: "Retry test" })).toBeTruthy();
  });

  test("an identical instance refresh gives a pending test an explicit retry state", async () => {
    const api = instance({ name: "work", providerId: "anthropic", authModes: ["apiKey"] });
    const firstTest = deferred<{ provider: string; status: string; message: string }>();
    let testCalls = 0;
    const fake = new FakeClient("ready");
    fake.on("evener/instance/list", () => ({ instances: [{ ...api }], availableProviders: [] }));
    fake.on("evener/auth/test", () => {
      testCalls += 1;
      return testCalls === 1 ? firstTest.promise : { provider: "work", status: "success", message: "ignored" };
    });
    connectionStore.getState().connect(fake);
    const onConnected = vi.fn();
    render(<ConnectProviderDialog onClose={() => {}} onConnected={onConnected} />);
    const chooser = await screen.findByRole("dialog", { name: "Connect provider" });
    await userEvent.setup().click(within(chooser).getByRole("button", { name: "Test connection" }));

    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    expect(
      within(chooser).getByText("Provider configuration refreshed while testing. Test the connection again."),
    ).toBeTruthy();
    firstTest.resolve({ provider: "work", status: "success", message: "ignored" });
    await act(async () => {
      await firstTest.promise;
    });
    expect(onConnected).not.toHaveBeenCalled();

    await userEvent.setup().click(within(chooser).getByRole("button", { name: "Retry test" }));
    await vi.waitFor(() => expect(onConnected).toHaveBeenCalledTimes(1));
  });

  test("a late OAuth start cannot replace a subsequently chosen API-key editor", async () => {
    const both = instance({
      name: "work",
      providerId: "custom",
      auth: "optional-bearer",
      authModes: ["apiKey", "oauth"],
    });
    const start = deferred<{
      provider: string;
      flowId: string;
      userCode: string;
      verificationUrl: string;
      intervalSeconds: number;
    }>();
    const fake = connectFakeClient({ instances: [both], availableProviders: [] });
    fake.on("evener/auth/device/start", () => start.promise);
    render(<ConnectProviderDialog onClose={() => {}} onConnected={() => {}} />);
    const chooser = await screen.findByRole("dialog", { name: "Connect provider" });
    const user = userEvent.setup();
    await user.click(within(chooser).getByRole("button", { name: "Sign in" }));
    await user.click(within(chooser).getByRole("button", { name: "Set API key" }));
    await user.type(screen.getByLabelText("API key for work"), "unfinished-secret");

    start.resolve({
      provider: "work",
      flowId: "late-device-flow",
      userCode: "LATE-CODE",
      verificationUrl: "https://verify.example",
      intervalSeconds: 5,
    });
    await act(async () => {
      await start.promise;
    });

    expect(screen.getByRole("dialog", { name: "Set API key for work" })).toBeTruthy();
    expect(screen.getByLabelText("API key for work")).toHaveProperty("value", "unfinished-secret");
    expect(screen.queryByText("LATE-CODE")).toBeNull();
  });

  test("a late successful test cannot close a subsequently chosen API-key editor", async () => {
    const api = instance({ name: "work", providerId: "anthropic", authModes: ["apiKey"] });
    const response = deferred<{ provider: string; status: string; message: string }>();
    const fake = connectFakeClient({ instances: [api], availableProviders: [] });
    fake.on("evener/auth/test", () => response.promise);
    const onConnected = vi.fn();
    render(<ConnectProviderDialog onClose={() => {}} onConnected={onConnected} />);
    const chooser = await screen.findByRole("dialog", { name: "Connect provider" });
    const user = userEvent.setup();
    await user.click(within(chooser).getByRole("button", { name: "Test connection" }));
    await user.click(within(chooser).getByRole("button", { name: "Set API key" }));
    await user.type(screen.getByLabelText("API key for work"), "unfinished-secret");

    response.resolve({ provider: "work", status: "success", message: "ignored" });
    await act(async () => {
      await response.promise;
    });

    expect(onConnected).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog", { name: "Set API key for work" })).toBeTruthy();
    expect(screen.getByLabelText("API key for work")).toHaveProperty("value", "unfinished-secret");
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

  test("saving a credential JSON still requires an explicit successful credential test", async () => {
    const vertex = instance({
      name: "vertex",
      providerId: "google-vertex",
      auth: "gcp-adc",
      authModes: ["adc", "credentialJson"],
    });
    const json = '{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c"}';
    let saved = false;
    const fake = new FakeClient("ready");
    fake.on("evener/instance/list", () => ({
      instances: [saved ? { ...vertex, activeSource: "store", hasStoredFile: true } : vertex],
      availableProviders: [],
    }));
    connectionStore.getState().connect(fake);
    fake.on("evener/auth/credentialJson/set", (params) => {
      expect(params).toEqual({ provider: "vertex", value: json });
      saved = true;
      return {
        provider: "vertex",
        supported: true,
        signedIn: true,
        activeSource: "store",
        authModes: ["adc", "credentialJson"],
        hasStoredOAuth: false,
        hasStoredFile: true,
      };
    });
    fake.on("evener/auth/test", (params) => {
      expect(params).toEqual({ provider: "vertex" });
      return { provider: "vertex", status: "success", message: "untrusted provider message" };
    });
    const onConnected = vi.fn();
    render(<ConnectProviderDialog onClose={() => {}} onConnected={onConnected} />);
    const chooser = await screen.findByRole("dialog", { name: "Connect provider" });
    const user = userEvent.setup();

    expect(within(chooser).queryByRole("button", { name: "Set API key" })).toBeNull();
    await user.click(within(chooser).getByRole("button", { name: "Set credential JSON" }));
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    expect(screen.getByRole("dialog", { name: "Set Google credential JSON for vertex" })).toBeTruthy();

    await user.click(screen.getByLabelText("Credential JSON for vertex"));
    await user.paste(json);
    await user.click(screen.getByRole("button", { name: "Save" }));
    const returnedChooser = await screen.findByRole("dialog", { name: "Connect provider" });
    expect(within(returnedChooser).getByText("Configured via stored credential JSON")).toBeTruthy();
    expect(within(returnedChooser).getByRole("button", { name: "Replace credential JSON" })).toBeTruthy();
    expect(onConnected).not.toHaveBeenCalled();

    await user.click(within(returnedChooser).getByRole("button", { name: "Test connection" }));
    await vi.waitFor(() => expect(onConnected).toHaveBeenCalledTimes(1));
  });

  test("a credential-JSON editor closes if its instance stops offering the mode", async () => {
    const vertex = instance({
      name: "vertex",
      providerId: "google-vertex",
      auth: "gcp-adc",
      authModes: ["adc", "credentialJson"],
    });
    let listCalls = 0;
    const fake = new FakeClient("ready");
    fake.on("evener/instance/list", () => {
      listCalls += 1;
      return { instances: [listCalls === 1 ? vertex : { ...vertex, authModes: ["adc"] }], availableProviders: [] };
    });
    connectionStore.getState().connect(fake);
    render(<ConnectProviderDialog onClose={() => {}} onConnected={() => {}} />);
    await userEvent.setup().click(await screen.findByRole("button", { name: "Set credential JSON" }));
    expect(screen.getByRole("dialog", { name: "Set Google credential JSON for vertex" })).toBeTruthy();

    await act(async () => {
      await credentialsStore.getState().fetch();
    });

    expect(screen.queryByRole("dialog", { name: "Set Google credential JSON for vertex" })).toBeNull();
    expect(screen.getByRole("dialog", { name: "Connect provider" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Set credential JSON" })).toBeNull();
  });

  test("a removed credential-JSON instance does not reopen its editor when restored", async () => {
    const vertex = instance({
      name: "vertex",
      providerId: "google-vertex",
      auth: "gcp-adc",
      authModes: ["adc", "credentialJson"],
    });
    connectFakeClient({ instances: [vertex], availableProviders: [] });
    render(<ConnectProviderDialog onClose={() => {}} onConnected={() => {}} />);
    await userEvent.setup().click(await screen.findByRole("button", { name: "Set credential JSON" }));
    expect(screen.getByRole("dialog", { name: "Set Google credential JSON for vertex" })).toBeTruthy();
    await act(async () => credentialsStore.setState({ instances: [] }));
    expect(screen.getByRole("dialog", { name: "Connect provider" })).toBeTruthy();
    await act(async () => credentialsStore.setState({ instances: [vertex] }));
    expect(screen.getByRole("dialog", { name: "Connect provider" })).toBeTruthy();
    expect(screen.queryByRole("dialog", { name: "Set Google credential JSON for vertex" })).toBeNull();
  });
});
