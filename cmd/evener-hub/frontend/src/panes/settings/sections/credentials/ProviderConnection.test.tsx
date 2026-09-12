import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { InstanceEntry, InstanceListResponse, ProviderDescriptor } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { ProviderConnection } from "./ProviderConnection";

function provider(
  id: string,
  name: string,
  modes = ["apiKey"],
  extra: Partial<InstanceEntry> = {},
): ProviderDescriptor {
  return {
    id,
    name,
    protocol: "openai-chat",
    auth: "bearer",
    implicit: true,
    authModes: modes,
    setup: {
      name: id,
      providerId: id,
      protocol: "openai-chat",
      auth: "bearer",
      implicit: true,
      isDefault: false,
      activeSource: "none",
      hasStoredOAuth: false,
      credentialRequired: true,
      authModes: modes,
      baseUrl: `https://${id}.example/v1`,
      ...extra,
    },
  };
}
const catalogue = [
  provider("anthropic", "Anthropic"),
  provider("openai", "OpenAI"),
  provider("google", "Gemini"),
  provider("openrouter", "OpenRouter"),
  provider("openai-codex", "ChatGPT / Codex", ["oauth"]),
  provider("ollama", "Local endpoint", ["apiKey"], { credentialRequired: false, baseUrl: "http://localhost:8080/v1" }),
  provider("google-vertex", "Google Vertex", ["credentialJson"]),
  {
    id: "azure",
    name: "Azure",
    protocol: "openai-chat",
    auth: "api-key",
    implicit: false,
    authModes: ["apiKey"],
    vars: { resource: "AZURE_RESOURCE" },
  },
  {
    id: "openai-compatible",
    name: "Custom endpoint",
    protocol: "openai-chat",
    auth: "none",
    implicit: false,
    authModes: [],
  },
] satisfies ProviderDescriptor[];
function setup(list: InstanceListResponse = { instances: [], availableProviders: catalogue }) {
  const client = new FakeClient("ready");
  client.on("evener/instance/list", () => structuredClone(list));
  connectionStore.getState().connect(client);
  const connected = vi.fn();
  const close = vi.fn();
  const manage = vi.fn();
  const view = render(<ProviderConnection onClose={close} onConnected={connected} onManage={manage} />);
  return { client, connected, close, manage, ...view, user: userEvent.setup() };
}
beforeEach(() => {
  connectionStore.setState({ state: "idle", client: null, serverInfo: undefined });
  resetCredentialsStoreForTests();
});

test("popular Gemini uses the real google identity, independently of provider naming", async () => {
  setup({ instances: [], availableProviders: [provider("google", "Google Generative AI")] });
  expect(await screen.findByRole("button", { name: "Gemini" })).toBeTruthy();
});

test("device authorization survives its own delayed listing refresh and proceeds to a real check", async () => {
  const { user, client, connected } = setup();
  await choose(user, "ChatGPT / Codex");
  client.on("evener/auth/device/start", () => ({
    provider: "openai-codex",
    flowId: "device",
    userCode: "CODE",
    verificationUrl: "https://auth.example",
    intervalSeconds: 1,
  }));
  client.on("evener/auth/device/poll", () => ({ provider: "openai-codex", state: "authorized" }));
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  vi.useFakeTimers();
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
  });
  const pending = deferred<InstanceListResponse>();
  client.on("evener/instance/list", () => pending.promise);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1000);
  });
  client.on("evener/instance/list", () => savedList("openai-codex", { activeSource: "oauth", hasStoredOAuth: true }));
  await act(async () => {
    pending.resolve(savedList("openai-codex", { activeSource: "oauth", hasStoredOAuth: true }));
    await pending.promise;
  });
  vi.useRealTimers();
  await user.click(await screen.findByRole("button", { name: "Continue" }));
  expect(connected).toHaveBeenCalledWith("openai-codex");
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toEqual([
    { method: "evener/auth/test", params: { provider: "openai-codex" } },
  ]);
});

test("changing base in the full form continues with the actual created provider rather than old vendor help", async () => {
  const { user, client } = setup();
  await choose(user, "Azure");
  await user.click(screen.getByRole("button", { name: "Configure provider" }));
  await user.selectOptions(screen.getByLabelText("Base provider"), "openai");
  await user.type(screen.getByLabelText("Name"), "openai-team");
  const row = { ...catalogue[1]!.setup!, name: "openai-team", implicit: false };
  client.on("evener/instance/create", () => ({ instances: [row], availableProviders: structuredClone(catalogue) }));
  await user.click(screen.getByRole("button", { name: "Create" }));
  expect(await screen.findByRole("dialog", { name: "Connect OpenAI" })).toBeTruthy();
  expect(screen.getByRole("link", { name: "Get an API key" }).getAttribute("href")).toBe(
    "https://platform.openai.com/api-keys",
  );
});

test("discovery load failure focuses recovery and retries the real listing", async () => {
  const client = new FakeClient("ready");
  client.on("evener/instance/list", () => {
    throw new Error("private-wire-body");
  });
  connectionStore.getState().connect(client);
  render(<ProviderConnection onClose={() => {}} onConnected={() => {}} onManage={() => {}} />);
  const alert = await screen.findByRole("alert");
  expect(document.activeElement).toBe(alert);
  expect(screen.queryByText(/private-wire-body/)).toBeNull();
  client.on("evener/instance/list", () => ({ instances: [], availableProviders: structuredClone(catalogue) }));
  await userEvent.setup().click(screen.getByRole("button", { name: "Retry" }));
  expect(await screen.findByRole("button", { name: "Anthropic" })).toBeTruthy();
});

test("resolved setup auth modes override public defaults rather than forcing a vendor key", async () => {
  const overridden = provider("anthropic", "Anthropic", ["apiKey"], {
    authModes: ["oauth"],
    auth: "oauth-openai-codex",
  });
  const { user } = setup({ instances: [], availableProviders: [overridden] });
  await choose(user);
  expect(screen.queryByLabelText("API key")).toBeNull();
  expect(screen.getByRole("button", { name: "Sign in" })).toBeTruthy();
  expect(screen.queryByRole("link", { name: "Get an API key" })).toBeNull();
});

test("a no-auth endpoint never asks for a key", async () => {
  const noAuth = provider("no-auth", "Team endpoint", ["none"], { auth: "none", credentialRequired: false });
  const { user, client, connected } = setup({ instances: [], availableProviders: [noAuth] });
  await choose(user, "Team endpoint");
  expect(screen.queryByLabelText("API key")).toBeNull();
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "unsupported", message: "" }));
  await user.click(screen.getByRole("button", { name: "Check connection" }));
  await user.click(await screen.findByRole("button", { name: "Continue without verification" }));
  expect(connected).toHaveBeenCalledWith("no-auth");
});

test("a discarded create listing retains the created name and reloads instead of creating again", async () => {
  const { user, client } = setup();
  await choose(user, "Azure");
  const pending = deferred<InstanceListResponse>();
  client.on("evener/instance/create", () => pending.promise);
  await user.click(screen.getByRole("button", { name: "Configure provider" }));
  await user.type(screen.getByLabelText("Name"), "retained-name");
  await user.click(screen.getByRole("button", { name: "Create" }));
  const row = { ...catalogue[0]!.setup!, name: "retained-name", providerId: "azure", implicit: false };
  await act(async () => {
    await credentialsStore.getState().fetch();
    pending.resolve({ instances: [row], availableProviders: catalogue });
    await pending.promise;
  });
  expect(await screen.findByRole("button", { name: "Reload connection" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Configure provider" })).toBeNull();
  client.on("evener/instance/list", () => ({ instances: [row], availableProviders: structuredClone(catalogue) }));
  await user.click(screen.getByRole("button", { name: "Reload connection" }));
  expect(await screen.findByLabelText("API key")).toBeTruthy();
  expect(client.calls.filter((c) => c.method === "evener/instance/create")).toHaveLength(1);
});

test("configuration refresh invalidates a pending OAuth start before it opens a browser", async () => {
  const { user, client } = setup();
  await choose(user, "ChatGPT / Codex");
  const pending = deferred<{
    provider: string;
    flowId: string;
    userCode: string;
    verificationUrl: string;
    intervalSeconds: number;
    fallback: boolean;
  }>();
  client.on("evener/auth/device/start", () => pending.promise);
  client.on("evener/auth/login/start", () => ({
    provider: "openai-codex",
    flowId: "old",
    url: "https://auth.example/old",
  }));
  const opened = vi.spyOn(window, "open").mockReturnValue(null);
  await user.click(screen.getByRole("button", { name: "Sign in" }));
  await act(async () => {
    await credentialsStore.getState().fetch();
    pending.resolve({
      provider: "openai-codex",
      flowId: "old",
      userCode: "old",
      verificationUrl: "https://auth.example",
      intervalSeconds: 5,
      fallback: true,
    });
    await pending.promise;
  });
  expect(opened).not.toHaveBeenCalled();
  expect(screen.queryByLabelText("Redirect URL")).toBeNull();
});
afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", client: null, serverInfo: undefined });
  vi.useRealTimers();
  vi.restoreAllMocks();
});

test("discovers a public provider without launch-ready instances and asks only for its masked key", async () => {
  const { user } = setup();
  await user.click(await screen.findByRole("button", { name: "Anthropic" }));
  const key = screen.getByLabelText("API key") as HTMLInputElement;
  expect(key.type).toBe("password");
  expect(key.required).toBe(true);
  expect(document.querySelectorAll("input[required]")).toHaveLength(1);
  expect(screen.getByRole("link", { name: /Get.*API key/ }).getAttribute("href")).toBe(
    "https://console.anthropic.com/settings/keys",
  );
  expect(screen.getByText(/API billing/)).toBeTruthy();
  expect(screen.queryByLabelText("Name")).toBeNull();
  expect(screen.queryByLabelText("Base URL (optional)")).toBeNull();
});
test("all providers searches the complete catalogue including separate Codex and configured variants", async () => {
  const { user } = setup();
  await user.click(await screen.findByRole("button", { name: "All providers" }));
  const search = screen.getByLabelText("Search providers");
  for (const row of catalogue) {
    await user.clear(search);
    await user.type(search, row.id);
    expect(screen.getByRole("button", { name: row.name })).toBeTruthy();
  }
});
test("a cloud choice opens the complete preselected configuration editor", async () => {
  const { user } = setup();
  await user.click(await screen.findByRole("button", { name: "All providers" }));
  await user.click(screen.getByRole("button", { name: "Azure" }));
  await user.click(screen.getByRole("button", { name: "Configure provider" }));
  expect(screen.getByLabelText("Base provider")).toHaveProperty("value", "azure");
  expect(screen.getByLabelText("AZURE_RESOURCE")).toBeTruthy();
  expect(screen.getByLabelText("Protocol")).toBeTruthy();
  expect(screen.getByLabelText("Surface")).toBeTruthy();
  expect(screen.getByLabelText("Credential header (optional)")).toBeTruthy();
});

function savedList(id = "anthropic", extra: Partial<InstanceEntry> = {}): InstanceListResponse {
  const row = { ...catalogue.find((p) => p.id === id)!.setup!, activeSource: "store", hasStoredFile: true, ...extra };
  return { instances: [row], availableProviders: catalogue.map((p) => (p.id === id ? { ...p, setup: row } : p)) };
}
const saved = { provider: "anthropic", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false };
async function choose(user: ReturnType<typeof userEvent.setup>, name = "Anthropic") {
  await user.click(await screen.findByRole("button", { name: "All providers" }));
  await user.click(screen.getByRole("button", { name }));
}
function scriptSave(client: FakeClient, next = savedList()) {
  client.on("evener/auth/apiKey/set", () => {
    client.on("evener/instance/list", () => structuredClone(next));
    return saved;
  });
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "raw-provider-secret" }));
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

test("empty required key stays focused and does not mutate", async () => {
  const { user, client } = setup();
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  expect(document.activeElement).toBe(screen.getByLabelText("API key"));
  expect(screen.getByRole("alert").id).toBe(screen.getByLabelText("API key").getAttribute("aria-describedby"));
  expect(client.calls.filter((c) => c.method !== "evener/instance/list")).toEqual([]);
});
test("saves directly to the real public ID then reloads and checks before explicit named continuation", async () => {
  const { user, client, connected } = setup();
  scriptSave(client);
  await choose(user);
  await user.type(screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  expect(await screen.findByText(/Model list access confirmed/)).toBeTruthy();
  expect(client.calls.slice(1)).toEqual([
    { method: "evener/auth/apiKey/set", params: { provider: "anthropic", value: "fixture-key" } },
    { method: "evener/instance/list", params: {} },
    { method: "evener/auth/test", params: { provider: "anthropic" } },
  ]);
  expect(screen.queryByText("raw-provider-secret")).toBeNull();
  expect(connected).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "Continue" }));
  expect(connected).toHaveBeenCalledWith("anthropic");
});
test("save failure retains the draft and retry saves once more without a premature check", async () => {
  const { user, client } = setup();
  client.on("evener/auth/apiKey/set", () => {
    throw new Error("secret-failure-body");
  });
  await choose(user);
  await user.type(screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  expect(await screen.findByRole("alert")).toBe(document.activeElement);
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "fixture-key");
  expect(screen.queryByText(/secret-failure-body/)).toBeNull();
  expect(client.calls.some((c) => c.method === "evener/auth/test")).toBe(false);
  scriptSave(client);
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  expect(await screen.findByText(/Model list access confirmed/)).toBeTruthy();
});
test.each(["auth_rejected", "endpoint_failure", "configuration_failure", "unsupported"])(
  "%s keeps the saved connection and retries only the check",
  async (status) => {
    const { user, client, connected } = setup();
    scriptSave(client);
    client.on("evener/auth/test", ({ provider }) => ({ provider, status, message: "secret-response" }));
    await choose(user);
    await user.type(screen.getByLabelText("API key"), "fixture-key");
    await user.click(screen.getByRole("button", { name: "Save and check" }));
    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.queryByText(/secret-response/)).toBeNull();
    expect(screen.getByLabelText("API key")).toHaveProperty("value", "fixture-key");
    if (status === "unsupported") {
      await user.click(screen.getByRole("button", { name: "Continue without verification" }));
      expect(connected).toHaveBeenCalledWith("anthropic");
      return;
    }
    client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    await user.click(screen.getByRole("button", { name: "Retry check" }));
    expect(await screen.findByText(/Model list access confirmed/)).toBeTruthy();
    expect(client.calls.filter((c) => c.method === "evener/auth/apiKey/set")).toHaveLength(1);
  },
);
test.each([{ activeSource: "api_key" }, { baseUrl: "https://changed.example/v1" }])(
  "requires explicit source/destination review after save: %j",
  async (change) => {
    const { user, client } = setup();
    scriptSave(client, savedList("anthropic", change));
    await choose(user);
    await user.type(screen.getByLabelText("API key"), "fixture-key");
    await user.click(screen.getByRole("button", { name: "Save and check" }));
    expect(await screen.findByRole("button", { name: "Use reviewed access and check" })).toBeTruthy();
    expect(screen.getByText(/Credential saved/)).toBeTruthy();
    expect(client.calls.some((c) => c.method === "evener/auth/test")).toBe(false);
    await user.click(screen.getByRole("button", { name: "Use reviewed access and check" }));
    expect(await screen.findByText(/Model list access confirmed/)).toBeTruthy();
  },
);
test("failed refresh is not proof of fresh access and does not check stale metadata", async () => {
  const { user, client } = setup();
  await choose(user);
  client.on("evener/auth/apiKey/set", () => {
    client.on("evener/instance/list", () => {
      throw new Error("refresh-secret");
    });
    return saved;
  });
  await user.type(screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(client.calls.some((c) => c.method === "evener/auth/test")).toBe(false);
  scriptSave(client);
  client.on("evener/instance/list", () => savedList());
  await user.click(screen.getByRole("button", { name: "Retry check" }));
  expect(await screen.findByText(/Model list access confirmed/)).toBeTruthy();
  expect(client.calls.filter((c) => c.method === "evener/auth/apiKey/set")).toHaveLength(1);
});
test("optional local access checks the host without clearing or requiring a key", async () => {
  const { user, client } = setup();
  await choose(user, "Local endpoint");
  expect(screen.getByLabelText("API key")).toHaveProperty("required", false);
  expect(screen.getByText("http://localhost:8080/v1")).toBeTruthy();
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await user.click(screen.getByRole("button", { name: "Check connection" }));
  expect(await screen.findByText(/Model list access confirmed/)).toBeTruthy();
  expect(client.calls.filter((c) => c.method.includes("/set") || c.method.includes("/clear"))).toEqual([]);
});
test("ADC JSON is masked and sent to the JSON route, not API-key auth", async () => {
  const { user, client } = setup();
  await choose(user, "Google Vertex");
  const input = screen.getByLabelText("Credential JSON");
  expect(input).toHaveProperty("type", "password");
  client.on("evener/auth/credentialJson/set", () => {
    client.on("evener/instance/list", () => savedList("google-vertex"));
    return { ...saved, provider: "google-vertex" };
  });
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await user.click(input);
  await user.paste('{"type":"service_account"}');
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  expect(await screen.findByText(/Model list access confirmed/)).toBeTruthy();
  expect(client.calls.find((c) => c.method === "evener/auth/credentialJson/set")?.params).toEqual({
    provider: "google-vertex",
    value: '{"type":"service_account"}',
  });
  expect(client.calls.some((c) => c.method === "evener/auth/apiKey/set")).toBe(false);
});
test.each([false, true])("Codex preserves the existing OAuth route (redirect=%s)", async (fallback) => {
  const { user, client } = setup();
  await choose(user, "ChatGPT / Codex");
  expect(screen.queryByLabelText("API key")).toBeNull();
  client.on("evener/auth/device/start", () => ({
    provider: "openai-codex",
    fallback,
    flowId: "device-flow",
    userCode: "ABCD-1234",
    verificationUrl: "https://auth.example/device",
    intervalSeconds: 5,
  }));
  client.on("evener/auth/login/start", () => ({
    provider: "openai-codex",
    flowId: "redirect-flow",
    url: "https://auth.example/authorize",
  }));
  vi.spyOn(window, "open").mockReturnValue(null);
  await user.click(screen.getByRole("button", { name: "Sign in" }));
  if (fallback) expect(await screen.findByLabelText("Redirect URL")).toBeTruthy();
  else expect(await screen.findByText("ABCD-1234")).toBeTruthy();
  expect(client.calls.find((c) => c.method === "evener/auth/device/start")?.params).toEqual({
    provider: "openai-codex",
  });
});

test("settings saved then key failed repairs the actual named connection without duplicate create", async () => {
  const { user, client, connected } = setup();
  await choose(user, "Azure");
  const row: InstanceEntry = {
    name: "azure-team",
    providerId: "azure",
    base: "azure",
    baseUrl: "https://team.azure.example/v1",
    protocol: "openai-chat",
    auth: "api-key",
    authModes: ["apiKey"],
    implicit: false,
    isDefault: false,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: true,
  };
  const created = { instances: [row], availableProviders: catalogue };
  client.on("evener/instance/create", () => {
    client.on("evener/instance/list", () => structuredClone(created));
    return structuredClone(created);
  });
  client.on("evener/auth/apiKey/set", () => {
    throw new Error("write failed");
  });
  await user.click(screen.getByRole("button", { name: "Configure provider" }));
  await user.type(screen.getByLabelText("Name"), "azure-team");
  await user.type(screen.getByLabelText("AZURE_RESOURCE"), "team");
  await user.click(screen.getByRole("button", { name: "Create" }));
  await user.type(await screen.findByLabelText("API key"), "retained-draft");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(screen.getByText(/Connection settings saved as azure-team/)).toBeTruthy();
  client.on("evener/auth/apiKey/set", () => {
    client.on("evener/instance/list", () => ({
      ...created,
      instances: [{ ...row, activeSource: "store", hasStoredFile: true }],
    }));
    return { ...saved, provider: "azure-team" };
  });
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await user.click(await screen.findByRole("button", { name: "Continue" }));
  expect(connected).toHaveBeenCalledWith("azure-team");
  expect(client.calls.filter((c) => c.method === "evener/instance/create")).toHaveLength(1);
  expect(client.calls.filter((c) => c.method === "evener/auth/apiKey/set").map((c) => c.params)).toEqual([
    { provider: "azure-team", value: "retained-draft" },
    { provider: "azure-team", value: "retained-draft" },
  ]);
});
test.each(["save", "refresh", "check"])(
  "changing provider invalidates late %s and isolates the new draft",
  async (phase) => {
    const { user, client, connected } = setup();
    await choose(user);
    const save = deferred<typeof saved>();
    const refresh = deferred<InstanceListResponse>();
    const check = deferred<{ provider: string; status: string; message: string }>();
    client.on("evener/auth/apiKey/set", () => save.promise);
    client.on("evener/auth/test", () => check.promise);
    await user.type(screen.getByLabelText("API key"), "old-draft");
    await user.click(screen.getByRole("button", { name: "Save and check" }));
    if (phase !== "save") {
      client.on("evener/instance/list", () => refresh.promise);
      await act(async () => {
        save.resolve(saved);
        await save.promise;
      });
    }
    if (phase === "check")
      await act(async () => {
        refresh.resolve(savedList());
        await refresh.promise;
      });
    await user.click(screen.getByRole("button", { name: "Change provider" }));
    client.on("evener/instance/list", () => ({ instances: [], availableProviders: structuredClone(catalogue) }));
    await act(async () => {
      save.resolve(saved);
      refresh.resolve(savedList());
      check.resolve({ provider: "anthropic", status: "success", message: "" });
      await Promise.all([save.promise, refresh.promise, check.promise]);
    });
    await choose(user, "OpenAI");
    expect(screen.getByLabelText("API key")).toHaveProperty("value", "");
    await user.type(screen.getByLabelText("API key"), "new-draft");
    expect(connected).not.toHaveBeenCalled();
    expect(screen.getByLabelText("API key")).toHaveProperty("value", "new-draft");
    expect(screen.queryByRole("button", { name: "Continue" })).toBeNull();
  },
);
test("cancel immediately invalidates a late check even before the owner unmounts", async () => {
  const { user, client, close, connected } = setup();
  scriptSave(client);
  const pending = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => pending.promise);
  await choose(user);
  await user.type(screen.getByLabelText("API key"), "draft");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  expect(close).toHaveBeenCalledTimes(1);
  await act(async () => {
    pending.resolve({ provider: "anthropic", status: "success", message: "" });
    await pending.promise;
  });
  expect(connected).not.toHaveBeenCalled();
  expect(screen.queryByRole("button", { name: "Continue" })).toBeNull();
});
test("a notification refresh invalidates a pending check and a completed result", async () => {
  const { user, client } = setup();
  scriptSave(client);
  const pending = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => pending.promise);
  await choose(user);
  await user.type(screen.getByLabelText("API key"), "draft");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  expect(screen.getByRole("button", { name: "Checking model list…" })).toHaveProperty("disabled", true);
  vi.useFakeTimers();
  await act(async () => {
    client.emitNotification({ method: "evener/auth/updated", params: {} });
    await vi.runOnlyPendingTimersAsync();
  });
  vi.useRealTimers();
  await act(async () => {
    pending.resolve({ provider: "anthropic", status: "success", message: "" });
    await pending.promise;
  });
  expect(screen.queryByRole("button", { name: "Continue" })).toBeNull();
  expect(screen.getByRole("alert")).toBeTruthy();
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await user.click(screen.getByRole("button", { name: "Retry check" }));
  expect(await screen.findByRole("button", { name: "Continue" })).toBeTruthy();
  await act(async () => {
    await credentialsStore.getState().fetch();
  });
  expect(screen.queryByRole("button", { name: "Continue" })).toBeNull();
});
test("a superseding pending refresh cannot authorize a check from discarded metadata", async () => {
  const { user, client } = setup();
  await choose(user);
  const own = deferred<InstanceListResponse>();
  const newer = deferred<InstanceListResponse>();
  client.on("evener/auth/apiKey/set", () => {
    client.on("evener/instance/list", () => own.promise);
    return saved;
  });
  await user.type(screen.getByLabelText("API key"), "draft");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  client.on("evener/instance/list", () => newer.promise);
  let newerFetch!: Promise<void>;
  await act(async () => {
    newerFetch = credentialsStore.getState().fetch();
    own.resolve(savedList());
    await own.promise;
  });
  expect(client.calls.some((c) => c.method === "evener/auth/test")).toBe(false);
  await act(async () => {
    newer.resolve(savedList("anthropic", { baseUrl: "https://different.example" }));
    await newerFetch;
  });
  client.on("evener/instance/list", () => savedList("anthropic", { baseUrl: "https://different.example" }));
  // The superseded refresh asks again instead of reporting "could not be
  // refreshed", and what it lands on is this fresh, changed listing - so the
  // flow goes straight to review. The property this test exists for is
  // unchanged: nothing contacts the provider off the discarded metadata.
  expect(await screen.findByRole("button", { name: "Use reviewed access and check" })).toBeTruthy();
  expect(client.calls.some((c) => c.method === "evener/auth/test")).toBe(false);
});
test("reconnect restores initial loading and invalidates late check callbacks", async () => {
  const initial = deferred<InstanceListResponse>();
  const client = new FakeClient("ready");
  client.on("evener/instance/list", () => initial.promise);
  connectionStore.getState().connect(client);
  const connected = vi.fn();
  render(<ProviderConnection onClose={() => {}} onConnected={connected} onManage={() => {}} />);
  const user = userEvent.setup();
  await act(async () => {
    client.emitStateChange("reconnecting");
    client.on("evener/instance/list", () => ({ instances: [], availableProviders: structuredClone(catalogue) }));
    client.emitReady();
    initial.resolve({ instances: [], availableProviders: [] });
    await initial.promise;
  });
  await choose(user);
  scriptSave(client);
  const pending = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => pending.promise);
  await user.type(screen.getByLabelText("API key"), "draft");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await act(async () => {
    client.emitStateChange("reconnecting");
    pending.resolve({ provider: "anthropic", status: "success", message: "" });
    await pending.promise;
    client.emitReady();
  });
  expect(screen.queryByRole("button", { name: "Continue" })).toBeNull();
  expect(connected).not.toHaveBeenCalled();
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "draft");
});
test("hidden setup is not a direct key/check route and settings refusal blocks configuration only", async () => {
  const hidden = provider("anthropic", "Anthropic", ["apiKey"], { hidden: true });
  const { user, client } = setup({ instances: [], availableProviders: [hidden], writesRefused: true });
  await choose(user);
  expect(screen.queryByLabelText("API key")).toBeNull();
  expect(screen.getByRole("button", { name: "Configure provider" })).toHaveProperty("disabled", true);
  expect(client.calls.some((c) => c.method === "evener/auth/test")).toBe(false);
});
test("read-only provider settings do not disable an existing credential store write", async () => {
  const { user, client } = setup({ instances: [], availableProviders: catalogue, writesRefused: true });
  scriptSave(client);
  await choose(user);
  await user.type(screen.getByLabelText("API key"), "draft");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  expect(await screen.findByRole("button", { name: "Continue" })).toBeTruthy();
});
test("switching to existing host access does not send the new key or claim authentication is disabled", async () => {
  const { user, client } = setup(
    savedList("anthropic", { activeSource: "env:ANTHROPIC_API_KEY", hasStoredFile: false }),
  );
  await choose(user);
  await user.type(screen.getByLabelText("API key"), "unused-draft");
  await user.click(screen.getByText("Advanced settings"));
  await user.click(screen.getByRole("button", { name: "Use existing host access" }));
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await user.click(screen.getByRole("button", { name: "Check connection" }));
  expect(await screen.findByRole("button", { name: "Continue" })).toBeTruthy();
  expect(client.calls.some((c) => c.method === "evener/auth/apiKey/set" || c.method === "evener/auth/logout")).toBe(
    false,
  );
});

test("review regression: discarded cross-provider create clears the old credential draft on reload", async () => {
  const { user, client } = setup();
  await choose(user, "Anthropic");
  await user.type(screen.getByLabelText("API key"), "anthropic-private-draft");
  await user.click(screen.getByText("Advanced settings"));
  await user.click(screen.getByRole("button", { name: "Configure another instance" }));
  await user.selectOptions(screen.getByLabelText("Base provider"), "openai");
  await user.type(screen.getByLabelText("Name"), "openai-team");
  const pending = deferred<InstanceListResponse>();
  const row = { ...catalogue[1]!.setup!, name: "openai-team", implicit: false };
  client.on("evener/instance/create", () => pending.promise);
  await user.click(screen.getByRole("button", { name: "Create" }));
  await act(async () => {
    await credentialsStore.getState().fetch();
    pending.resolve({ instances: [row], availableProviders: structuredClone(catalogue) });
    await pending.promise;
  });
  expect(await screen.findByRole("button", { name: "Reload connection" })).toBeTruthy();
  client.on("evener/instance/list", () => ({ instances: [row], availableProviders: structuredClone(catalogue) }));
  await user.click(screen.getByRole("button", { name: "Reload connection" }));
  expect(await screen.findByRole("dialog", { name: "Connect OpenAI" })).toBeTruthy();
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "");
});

test.each([
  { base: "anthropic", label: "Anthropic", via: "reload", expected: "anthropic-private-draft" },
  { base: "anthropic", label: "Anthropic", via: "listing", expected: "anthropic-private-draft" },
  { base: "openai", label: "OpenAI", via: "listing", expected: "" },
])(
  "review regression: deferred $base recovery via $via preserves only same-provider drafts",
  async ({ base, label, via, expected }) => {
    const { user, client } = setup();
    await choose(user, "Anthropic");
    await user.type(screen.getByLabelText("API key"), "anthropic-private-draft");
    await user.click(screen.getByText("Advanced settings"));
    await user.click(screen.getByRole("button", { name: "Configure another instance" }));
    await user.selectOptions(screen.getByLabelText("Base provider"), base);
    await user.type(screen.getByLabelText("Name"), "recovered-team");
    const pending = deferred<InstanceListResponse>();
    const row = {
      ...catalogue.find((candidate) => candidate.id === base)!.setup!,
      name: "recovered-team",
      implicit: false,
    };
    client.on("evener/instance/create", () => pending.promise);
    await user.click(screen.getByRole("button", { name: "Create" }));
    await act(async () => {
      await credentialsStore.getState().fetch();
      pending.resolve({ instances: [row], availableProviders: structuredClone(catalogue) });
      await pending.promise;
    });
    expect(await screen.findByRole("button", { name: "Reload connection" })).toBeTruthy();
    client.on("evener/instance/list", () => ({ instances: [row], availableProviders: structuredClone(catalogue) }));
    if (via === "reload") await user.click(screen.getByRole("button", { name: "Reload connection" }));
    else
      await act(async () => {
        await credentialsStore.getState().fetch();
      });
    expect(await screen.findByRole("dialog", { name: `Connect ${label}` })).toBeTruthy();
    expect(screen.getByLabelText("API key")).toHaveProperty("value", expected);
    expect(client.calls.filter((call) => call.method === "evener/instance/create")).toHaveLength(1);
  },
);
