import type { InstanceEntry, InstanceListResponse, ProviderDescriptor } from "@evener/appwire-client";
import {
  ErrorEndpointConflict,
  FINGERPRINT_UNAVAILABLE_ERROR,
  FINGERPRINT_UNAVAILABLE_TEST_MESSAGE,
  WireError,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { setMutationClientIdentityForTests } from "../../../../stores/mutationClientIdentity";
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
      endpointFingerprint: `fp-${id}`,
      ...extra,
    },
  };
}
const catalogue = [
  provider("anthropic", "Anthropic"),
  provider("openai", "OpenAI"),
  provider("google", "Gemini"),
  provider("openrouter", "OpenRouter"),
  provider("openai-codex", "OpenAI Codex", ["oauth"]),
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
  const openSettings = vi.fn();
  const view = render(<ProviderConnection onClose={close} onConnected={connected} onOpenSettings={openSettings} />);
  return { client, connected, close, openSettings, ...view, user: userEvent.setup() };
}
beforeEach(() => {
  connectionStore.setState({ state: "idle", client: null, serverInfo: undefined });
  resetCredentialsStoreForTests();
  // The auth mutations carry the page identity on the wire now, so the exact
  // call assertions below can pin a value that does not vary per run.
  setMutationClientIdentityForTests("test-tab");
});

test("popular Gemini uses the real google identity, independently of provider naming", async () => {
  setup({ instances: [], availableProviders: [provider("google", "Google Generative AI")] });
  expect(await screen.findByRole("button", { name: "Gemini" })).toBeTruthy();
});

// The front page is where a new account gets added, and Codex is what people
// arrive with: it has to be offered without first switching to the full
// catalogue. Its connection is the OAuth flow - the provider reads no API key
// - so selecting the popular card must reach the Codex sign-in rather than a
// key field.
test("popular grid offers OpenAI Codex and starts its sign-in", async () => {
  const { user, client } = setup();
  await user.click(await screen.findByRole("button", { name: "OpenAI Codex" }));
  expect(await screen.findByRole("dialog", { name: "Connect OpenAI Codex" })).toBeTruthy();
  expect(screen.queryByLabelText("API key")).toBeNull();
  client.on("evener/auth/device/start", () => ({
    provider: "openai-codex",
    flowId: "device",
    userCode: "CODE",
    verificationUrl: "https://auth.example",
    intervalSeconds: 5,
  }));
  await user.click(screen.getByRole("button", { name: "Sign in" }));
  const starts = client.calls.filter((call) => call.method === "evener/auth/device/start");
  expect(starts).toHaveLength(1);
  expect(starts[0]!.params).toMatchObject({ provider: "openai-codex" });
});

// Codex's auth modes are oauth-only, so the key link has nothing to point at -
// but the billing line is exactly the help a subscription user needs: it says
// to sign in instead of pasting a key. It must render even though the provider
// never offers the API-key branch.
test("an oauth-only provider renders its billing line without a key link", async () => {
  const { user } = setup();
  await choose(user, "OpenAI Codex");
  expect(await screen.findByRole("dialog", { name: "Connect OpenAI Codex" })).toBeTruthy();
  expect(screen.queryByRole("link", { name: "Get an API key" })).toBeNull();
  expect(
    screen.getByText("Codex access comes from your ChatGPT/Codex subscription. Sign in instead of pasting a key."),
  ).toBeTruthy();
});

// The apiKey providers keep both halves: the key link and the billing line.
test("an apiKey provider still renders its key link and its billing line", async () => {
  const { user } = setup();
  await choose(user, "Anthropic");
  expect(screen.getByRole("link", { name: "Get an API key" }).getAttribute("href")).toBe(
    "https://console.anthropic.com/settings/keys",
  );
  expect(
    screen.getByText("Claude subscriptions do not include API billing. API usage is billed separately."),
  ).toBeTruthy();
});

// The first screen is the popular set and nothing else. Everything the
// catalogue holds beyond it - including the search that spans the catalogue -
// is behind the reveal, so the choice is short until the user asks for more.
test("the popular providers are what the picker shows first", async () => {
  setup();
  expect(await screen.findByRole("button", { name: "Anthropic" })).toBeTruthy();
  for (const name of ["OpenAI", "Gemini", "OpenRouter", "OpenAI Codex"]) {
    expect(screen.getByRole("button", { name })).toBeTruthy();
  }
  expect(screen.queryByRole("button", { name: "Azure" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Local endpoint" })).toBeNull();
  expect(screen.queryByLabelText("Search providers")).toBeNull();
});

// The reveal swaps the popular set for the whole catalogue (the popular rows
// are in it too), and closing it puts the short list back - one control, both
// directions, no second label to keep in sync.
test("show all providers reveals the rest of the catalogue and closes back to the popular set", async () => {
  const { user } = setup();
  const reveal = await screen.findByText("Show all providers");
  await user.click(reveal);
  expect(screen.getByLabelText("Search providers")).toBeTruthy();
  for (const name of ["Azure", "Google Vertex", "Local endpoint", "Custom endpoint", "Anthropic"]) {
    expect(screen.getByRole("button", { name })).toBeTruthy();
  }
  await user.click(reveal);
  expect(screen.queryByLabelText("Search providers")).toBeNull();
  expect(screen.queryByRole("button", { name: "Azure" })).toBeNull();
  expect(screen.getByRole("button", { name: "Anthropic" })).toBeTruthy();
});

// The reveal is the dialog's own focusable control. jsdom implements the
// focusability rule for a details' first <summary> (helpers/focusing.js) even
// though user-event's own focusable selector does not list <summary>, so DOM
// focus is asserted directly; jsdom then runs no native Enter-to-click for it
// either, which is why the keyboard path's follow-up click is dispatched here
// (the same limitation widgets/disclosure/disclosure.test.tsx documents).
test("the show all providers control is keyboard reachable", async () => {
  const { user } = setup();
  const reveal = await screen.findByText("Show all providers");
  expect(reveal.tagName).toBe("SUMMARY");
  // No tabindex override: the control is in the browser's tab order.
  expect(reveal.getAttribute("tabindex")).toBeNull();
  reveal.focus();
  expect(document.activeElement).toBe(reveal);
  await user.keyboard("{Enter}");
  fireEvent.click(reveal);
  expect(screen.getByLabelText("Search providers")).toBeTruthy();
});

test("a provider named like an Object.prototype member is not popular and gets no help link", async () => {
  const custom = provider("constructor", "Custom endpoint");
  const { user } = setup({ instances: [], availableProviders: [custom] });
  await screen.findByText("Show all providers");
  await act(async () => {
    await credentialsStore.getState().fetch();
  });
  // Popular membership must be an own-key check: "constructor" in HELP is
  // true through the prototype chain, which would promote this custom
  // provider to the popular list and then render
  // Object.prototype.constructor as its help entry - a "Get an API key"
  // anchor with no destination for a provider that has no help metadata.
  expect(screen.queryByRole("button", { name: "Custom endpoint" })).toBeNull();
  await user.click(screen.getByText("Show all providers"));
  await user.click(screen.getByRole("button", { name: "Custom endpoint" }));
  expect(screen.queryByText("Get an API key")).toBeNull();
});

test("device authorization survives its own delayed listing refresh and proceeds to a real check", async () => {
  const { user, client, connected } = setup();
  await choose(user, "OpenAI Codex");
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
    {
      method: "evener/auth/test",
      params: { provider: "openai-codex", expectedEndpointFingerprint: "fp-openai-codex" },
    },
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
  render(<ProviderConnection onClose={() => {}} onConnected={() => {}} onOpenSettings={() => {}} />);
  const alert = await screen.findByRole("alert");
  expect(document.activeElement).toBe(alert);
  expect(screen.queryByText(/private-wire-body/)).toBeNull();
  client.on("evener/instance/list", () => ({ instances: [], availableProviders: structuredClone(catalogue) }));
  await userEvent.setup().click(screen.getByRole("button", { name: "Retry" }));
  expect(await screen.findByRole("button", { name: "Anthropic" })).toBeTruthy();
});

// The dialog this flow is mounted in used to grow a second, narrower copy of
// the settings pane's instance list ("Manage existing connections"). That
// surface is gone: this affordance hands the user to the real provider
// settings, and the hosting dialog closes itself on the way out - both halves
// of that handoff are the dialog's (see ConnectProviderDialog, which pins the
// route it lands on).
test("the existing-connections affordance hands off to the full settings, not a second editor", async () => {
  const { user, openSettings, close } = setup();
  await screen.findByRole("button", { name: "Anthropic" });
  await user.click(screen.getByText("Already configured access on this host?"));
  await user.click(screen.getByRole("button", { name: "Manage existing connections" }));

  expect(openSettings).toHaveBeenCalledTimes(1);
  expect(close).not.toHaveBeenCalled();
  expect(screen.queryByRole("button", { name: "Test connection" })).toBeNull();
});

// Same handoff from inside the guided flow: an affordance labelled "Open full
// connection editor" must open exactly that, not a menu of its own.
test("open full connection editor hands off to the full settings", async () => {
  const { user, openSettings } = setup();
  await choose(user, "Anthropic");
  await user.click(screen.getByText("Advanced settings"));
  await user.click(screen.getByRole("button", { name: "Open full connection editor" }));

  expect(openSettings).toHaveBeenCalledTimes(1);
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

test("a retry with the client gone reports no unhandled rejection", async () => {
  const { user, client } = setup();
  client.on("evener/instance/list", () => {
    throw new Error("list denied");
  });
  await act(async () => {
    await credentialsStore.getState().fetch();
  });
  const retry = await screen.findByRole("button", { name: "Retry" });

  // fetch() rejects on a missing client (credentials.ts's requireClient
  // contract). Reaching the end of this test instead of the runner failing on
  // an unhandled rejection is the assertion; the load error this button lives
  // in stays as the recovery affordance.
  await act(async () => {
    connectionStore.setState({ client: null });
  });
  await user.click(retry);
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
});

test("configuration refresh invalidates a pending OAuth start before it opens a browser", async () => {
  const { user, client } = setup();
  await choose(user, "OpenAI Codex");
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
  vi.unstubAllGlobals();
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
test("show all providers searches the complete catalogue including separate Codex and configured variants", async () => {
  const { user } = setup();
  await user.click(await screen.findByText("Show all providers"));
  const search = screen.getByLabelText("Search providers");
  for (const row of catalogue) {
    await user.clear(search);
    await user.type(search, row.id);
    expect(screen.getByRole("button", { name: row.name })).toBeTruthy();
  }
});

// The search matches the three fields it has always matched - the registry id,
// its display name, and the help label the row prints - not just whatever the
// row happens to say. "Google Generative AI" is the case that separates them:
// it is neither the id nor the label on screen.
test("the catalogue search matches id, display name and help label", async () => {
  const { user } = setup({ instances: [], availableProviders: [provider("google", "Google Generative AI")] });
  await user.click(await screen.findByText("Show all providers"));
  const search = screen.getByLabelText("Search providers");
  for (const query of ["google", "generative", "gemini"]) {
    await user.clear(search);
    await user.type(search, query);
    expect(screen.getByRole("button", { name: "Gemini" })).toBeTruthy();
  }
  await user.clear(search);
  await user.type(search, "vertex");
  expect(screen.queryByRole("button", { name: "Gemini" })).toBeNull();
});

test("a cloud choice opens the complete preselected configuration editor", async () => {
  const { user } = setup();
  await user.click(await screen.findByText("Show all providers"));
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
  // Choosing from the revealed catalogue covers both kinds of provider this
  // helper is used for (popular and not), and keeps one path through the
  // picker for every caller.
  await user.click(await screen.findByText("Show all providers"));
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

// The store refuses a save issued from the previous connection's listing
// (stores/credentials.ts's requireWritableClient): the destination this flow
// anchored on was read by a connection that is gone. That refusal is the
// change the conflict recovery above handles, so it gets the same recovery -
// drop the draft, re-read the listing, and say what changed - instead of the
// flow's own "could not be saved" report for a save that was never sent.
test("a save refused because the held listing belongs to a replaced connection reports the change, not a failed save", async () => {
  const { user, client } = setup();
  scriptSave(client);
  await choose(user);
  await user.type(screen.getByLabelText("API key"), "fixture-key");
  const readsBefore = client.calls.filter((c) => c.method === "evener/instance/list").length;
  // The connection is replaced and its own listing has not been applied yet.
  act(() => credentialsStore.setState({ listingFromPreviousConnection: true }));
  await user.click(screen.getByRole("button", { name: "Save and check" }));

  // Nothing was sent to the replacement connection...
  expect(client.calls.filter((c) => c.method === "evener/auth/apiKey/set")).toHaveLength(0);
  // ...and the refusal names the change rather than reporting a save failure.
  const alert = await screen.findByRole("alert");
  expect(alert.textContent).toContain("connection was replaced");
  expect(screen.queryByText(/could not be saved/)).toBeNull();
  // The draft was aimed at a destination that is gone, so it is dropped, and
  // the flow re-reads the listing to re-anchor on the one now on screen.
  await waitFor(() => expect(screen.getByLabelText("API key")).toHaveProperty("value", ""));
  await waitFor(() =>
    expect(client.calls.filter((c) => c.method === "evener/instance/list").length).toBeGreaterThan(readsBefore),
  );
  // No check ran against a destination this connection never showed.
  expect(client.calls.some((c) => c.method === "evener/auth/test")).toBe(false);
});

// The store refuses a sign-in start issued from the previous connection's
// listing (stores/credentials.ts's requireWritableClient), and this flow's
// Sign in button is not gated on that marker - its `unavailable` reads the
// connection state and loading only, and a failed restore read leaves loading
// false. That refusal is the same change the save/check paths recover from, so
// it is reported as one instead of as a start that failed.
test("a sign-in refused because the held listing belongs to a replaced connection reports the change, not a failed start", async () => {
  const { user, client } = setup();
  client.on("evener/auth/device/start", () => ({
    provider: "openai-codex",
    flowId: "flow",
    userCode: "ABCD",
    verificationUrl: "https://verify",
    intervalSeconds: 5,
    fallback: false,
  }));
  client.on("evener/auth/login/start", () => ({ provider: "openai-codex", flowId: "flow", url: "https://auth" }));
  await choose(user, "OpenAI Codex");

  // The connection is replaced and its own listing has not been applied.
  act(() => credentialsStore.setState({ listingFromPreviousConnection: true }));
  await user.click(screen.getByRole("button", { name: "Sign in" }));

  // Nothing was started on the replacement connection...
  expect(
    client.calls.filter(
      (call) => call.method === "evener/auth/device/start" || call.method === "evener/auth/login/start",
    ),
  ).toHaveLength(0);
  // ...and the refusal names the change rather than reporting a failed start.
  const alert = await screen.findByRole("alert");
  expect(alert.textContent).toContain("connection was replaced");
  expect(screen.queryByText(/Sign-in could not be started/)).toBeNull();
});

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
    {
      method: "evener/auth/apiKey/set",
      params: {
        provider: "anthropic",
        value: "fixture-key",
        expectedEndpointFingerprint: "fp-anthropic",
        originClientId: "test-tab",
      },
    },
    { method: "evener/instance/list", params: {} },
    { method: "evener/auth/test", params: { provider: "anthropic", expectedEndpointFingerprint: "fp-anthropic" } },
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
    expectedEndpointFingerprint: "fp-google-vertex",
    originClientId: "test-tab",
  });
  expect(client.calls.some((c) => c.method === "evener/auth/apiKey/set")).toBe(false);
});
test.each([false, true])("Codex preserves the existing OAuth route (redirect=%s)", async (fallback) => {
  const { user, client } = setup();
  await choose(user, "OpenAI Codex");
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
    endpointFingerprint: "fp-azure-team",
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
    {
      provider: "azure-team",
      value: "retained-draft",
      expectedEndpointFingerprint: "fp-azure-team",
      originClientId: "test-tab",
    },
    {
      provider: "azure-team",
      value: "retained-draft",
      expectedEndpointFingerprint: "fp-azure-team",
      originClientId: "test-tab",
    },
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
test("the originating client's own auth-update echo does not invalidate its save and check", async () => {
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
    // The hub BroadcastAlls the originator its own success echo (notifyAuthUpdated
    // sends {provider, activeSource} to every connected client, this one included),
    // so the real "Save and check" flow runs with its own echo in flight.
    client.emitNotification({
      method: "evener/auth/updated",
      params: { provider: "anthropic", activeSource: "store" },
    });
    await vi.runOnlyPendingTimersAsync();
  });
  vi.useRealTimers();
  await act(async () => {
    pending.resolve({ provider: "anthropic", status: "success", message: "" });
    await pending.promise;
  });
  expect(await screen.findByRole("button", { name: "Continue" })).toBeTruthy();
  expect(screen.queryByText("Connection or configuration changed")).toBeNull();
});
test("the store's own post-save refresh does not invalidate the completed check", async () => {
  // The store schedules its own listing refresh the moment the save succeeds
  // (a 250ms debounce), while the flow sits in its result phase with Continue
  // showing. That refresh is this client's own change - it must not read as
  // "Connection or configuration changed" and yank the success away from the
  // user. The fake-timer echo test above cannot observe this: its save runs
  // under real timers, so the self-refresh is a timeout its fake clock never
  // owns. Here the clock is fake before the save, so the refresh is a timer
  // Testing Library's waitFor runs (through the stubbed `jest` global).
  const { user, client } = setup();
  scriptSave(client);
  await choose(user);
  await user.type(screen.getByLabelText("API key"), "draft");
  vi.stubGlobal("jest", { advanceTimersByTime: vi.advanceTimersByTime });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  // A plain click: `user` was set up on the real clock, and its delays would
  // wait on a timer nothing advances now.
  fireEvent.click(screen.getByRole("button", { name: "Save and check" }));
  expect(await screen.findByRole("button", { name: "Continue" })).toBeTruthy();
  const listReads = () => client.calls.filter((c) => c.method === "evener/instance/list").length;
  const reads = listReads();
  // The self-refresh has been issued and its answer has landed: the store
  // raises `loading` as it issues the read and clears it once the answer lands.
  await waitFor(() => {
    expect(listReads()).toBe(reads + 1);
    expect(credentialsStore.getState().loading).toBe(false);
  });
  expect(screen.getByRole("button", { name: "Continue" })).toBeTruthy();
  expect(screen.queryByText("Connection or configuration changed")).toBeNull();
});
test("a slow save response extends the echo window so its late own echo keeps Continue", async () => {
  const { user, client, connected } = setup();
  const save = deferred<typeof saved>();
  client.on("evener/auth/apiKey/set", () => save.promise);
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await choose(user);
  await user.type(screen.getByLabelText("API key"), "draft");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  expect(screen.getByRole("button", { name: "Saving…" })).toHaveProperty("disabled", true);

  vi.useFakeTimers();
  // A loaded host (or a network-mounted state root) answers the save later
  // than the fixed window measured from the issue: the response lands at
  // 2001ms, and the hub's broadcast follows the response it answers.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(2001);
    client.on("evener/instance/list", () => savedList("anthropic"));
    save.resolve(saved);
    await save.promise;
  });
  await act(async () => {
    client.emitNotification({
      method: "evener/auth/updated",
      params: { provider: "anthropic", activeSource: "store" },
    });
    await vi.runOnlyPendingTimersAsync();
  });
  vi.useRealTimers();

  // The echo is this client's own: the store's coalesced refresh carries the
  // self mark, so the invalidation watch leaves the completed check alone.
  // Read as foreign (the fixed window alone), that same read resets the flow
  // to "Connection or configuration changed" and the user loses a save that
  // actually succeeded.
  expect(await screen.findByRole("button", { name: "Continue" })).toBeTruthy();
  expect(screen.queryByText("Connection or configuration changed")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Continue" }));
  expect(connected).toHaveBeenCalledWith("anthropic");
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
  let newerFetch!: Promise<boolean>;
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
  render(<ProviderConnection onClose={() => {}} onConnected={connected} onOpenSettings={() => {}} />);
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

test("adopting a created instance drops a prior host-access choice instead of inheriting it", async () => {
  const { user, client } = setup(savedList("anthropic"));
  await choose(user);
  await user.type(screen.getByLabelText("API key"), "unused-draft");
  await user.click(screen.getByText("Advanced settings"));
  await user.click(screen.getByRole("button", { name: "Use existing host access" }));
  expect(screen.queryByLabelText("API key")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Configure another instance" }));
  await user.selectOptions(screen.getByLabelText("Base provider"), "openai");
  await user.type(screen.getByLabelText("Name"), "openai-team");
  const row = { ...catalogue[1]!.setup!, name: "openai-team", implicit: false };
  client.on("evener/instance/create", () => ({ instances: [row], availableProviders: structuredClone(catalogue) }));
  await user.click(screen.getByRole("button", { name: "Create" }));
  // openai-team is a different connection with no host-access choice of its
  // own; inheriting anthropic's would skip its credential submission and let
  // the check resolve access the host never had for it.
  expect(await screen.findByLabelText("API key")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Use a new credential" })).toBeNull();
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

// The reload's own read can lose the race to a concurrent write, and a write
// that fails releases the store's loading flag without touching the listing or
// the error field (stores/credentials.ts applyMutation's finally; the store
// suite pins that state as "a failed mutation releases loading from a
// superseded read"). The reload's response was then dropped by the store's
// version guard, so its false verdict is the only thing that says the reload
// did not apply: loading and error are both clear while the store still holds
// the older connection's rows. Adopting whatever same-named row is left there
// anchors the guided flow on a connection this reload never loaded, so the flow
// must report the recovery error instead.
test("a superseded reload read does not baseline a same-named row the store still holds", async () => {
  const { user, client } = setup();
  await choose(user, "Azure");
  const pending = deferred<InstanceListResponse>();
  client.on("evener/instance/create", () => pending.promise);
  await user.click(screen.getByRole("button", { name: "Configure provider" }));
  await user.type(screen.getByLabelText("Name"), "retained-name");
  await user.click(screen.getByRole("button", { name: "Create" }));
  // The create's own listing loses the race with the concurrent read, so the
  // flow reaches its not-ready reload recovery without a row of its own.
  // A superseded create also schedules the store's debounced refetch. Run it
  // here, before the scripted reads below: left on the real clock it lands
  // whenever the test crosses the debounce, and if it lands inside the reload
  // step it takes that step's scripted response and holds the store's loading
  // flag, so the Reload click hits a disabled button and does nothing.
  vi.useFakeTimers();
  await act(async () => {
    await credentialsStore.getState().fetch();
    pending.resolve({ instances: [], availableProviders: structuredClone(catalogue) });
    await pending.promise;
    await vi.runOnlyPendingTimersAsync();
  });
  vi.useRealTimers();
  expect(await screen.findByRole("button", { name: "Reload connection" })).toBeTruthy();
  // The older connection's listing still carries a row under this name. It is
  // hidden, so the flow stays in its recovery state rather than presenting it.
  const staleRow: InstanceEntry = {
    ...catalogue[0]!.setup!,
    name: "retained-name",
    implicit: false,
    hidden: true,
  };
  client.on("evener/instance/list", () => ({
    instances: [structuredClone(staleRow)],
    availableProviders: structuredClone(catalogue),
  }));
  await act(async () => {
    await credentialsStore.getState().fetch();
  });

  const reloadRead = deferred<InstanceListResponse>();
  const retryRead = deferred<InstanceListResponse>();
  const retryIssued = deferred<void>();
  let reads = 0;
  client.on("evener/instance/list", () => {
    if (reads++ === 0) return reloadRead.promise;
    retryIssued.resolve();
    return retryRead.promise;
  });
  // user.click on a disabled button does nothing, so pin that the click
  // reaches the reload rather than letting it silently no-op.
  const reload = screen.getByRole("button", { name: "Reload connection" });
  expect(reload).toHaveProperty("disabled", false);
  await user.click(reload);
  expect(reads).toBe(1);
  // A concurrent write fails while the reload's read is in flight: the write
  // supersedes the read (the store drops the response) and its failure releases
  // loading without recording an error or moving the listing.
  client.on("evener/instance/setDefault", () => {
    throw new Error("write refused");
  });
  await act(async () => {
    await expect(credentialsStore.getState().setDefault("retained-name")).rejects.toThrow("write refused");
  });
  await act(async () => {
    reloadRead.resolve({ instances: [], availableProviders: structuredClone(catalogue) });
    // The reload's own retry must reach the wire before the second write
    // supersedes it.
    await retryIssued.promise;
  });
  // The retry loses the same way: its response is dropped, so the flow's own
  // read never applies and the store is left holding the older row.
  await act(async () => {
    await expect(credentialsStore.getState().setDefault("retained-name")).rejects.toThrow("write refused");
  });
  await act(async () => {
    retryRead.resolve({ instances: [], availableProviders: structuredClone(catalogue) });
    await retryRead.promise;
  });

  expect(
    await screen.findByText(
      "The saved connection could not be loaded. Reload it or open the full editor; do not create it again.",
    ),
  ).toBeTruthy();
  expect(screen.getByRole("button", { name: "Reload connection" })).toBeTruthy();
  expect(screen.queryByLabelText("API key")).toBeNull();
});

// Adoption clears the draft unless the adopted connection's destination is the
// one the value was typed against. "Same destination" means the two rows carry
// the same non-empty endpoint fingerprint: the same provider can host two
// endpoints, so matching providerId alone let a key typed for the first be
// submitted to the second without re-review (roborev round 36). A recovered
// row that carries the same *defined* destination still keeps the value, which
// is what keeps the recovery from demanding a retype. A pair of missing
// fingerprints is not evidence of a shared destination - an unkeyable hub or a
// row the listing has not resolved leaves nothing to compare - so the draft is
// cleared unless both sides carry a defined, matching fingerprint (roborev
// round 37).
test.each([
  {
    base: "anthropic",
    label: "Anthropic",
    baselineFp: "fp-shared",
    rowFp: "fp-shared",
    expected: "anthropic-private-draft",
  },
  { base: "openai", label: "OpenAI", baselineFp: "fp-shared", rowFp: "fp-other", expected: "" },
  { base: "anthropic", label: "Anthropic", baselineFp: undefined, rowFp: undefined, expected: "" },
])(
  "review regression: deferred $base recovery preserves a draft only across a same, defined destination",
  async ({ base, label, baselineFp, rowFp, expected }) => {
    const baselineList: InstanceListResponse = {
      instances: [],
      availableProviders: catalogue.map((row) =>
        row.id === "anthropic"
          ? provider("anthropic", "Anthropic", ["apiKey"], {
              endpointFingerprint: baselineFp,
            })
          : row,
      ),
    };
    const { user, client } = setup(baselineList);
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
      endpointFingerprint: rowFp,
    };
    const recovered: InstanceListResponse = {
      instances: [row],
      availableProviders: structuredClone(catalogue),
    };
    client.on("evener/instance/create", () => pending.promise);
    await user.click(screen.getByRole("button", { name: "Create" }));
    // The create's own response loses the race with the concurrent read, but
    // the reconciling read sees the authored row, so the flow adopts it with
    // the row's own destination rather than staying unresolved.
    await act(async () => {
      await credentialsStore.getState().fetch();
      client.on("evener/instance/list", () => structuredClone(recovered));
      pending.resolve(structuredClone(recovered));
      await pending.promise;
    });
    expect(await screen.findByRole("dialog", { name: `Connect ${label}` })).toBeTruthy();
    expect(screen.getByLabelText("API key")).toHaveProperty("value", expected);
    expect(client.calls.filter((call) => call.method === "evener/instance/create")).toHaveLength(1);
  },
);

// The catalogue with one provider's endpoint fingerprint set: what the hub
// reports when the endpoint moved in a part the sanitized baseUrl cannot show
// (a query parameter naming a different API version, deployment or token).
// activeSource is the credential source the listing reports, so a post-save
// listing can say "store" and leave the destination comparison as the only
// thing that can demand review.
function fingerprintList(fingerprint: string, activeSource = "none"): InstanceListResponse {
  return {
    instances: [],
    availableProviders: catalogue.map((row) =>
      row.id === "anthropic"
        ? provider("anthropic", "Anthropic", ["apiKey"], { endpointFingerprint: fingerprint, activeSource })
        : row,
    ),
  };
}

test("a query-only endpoint change still requires review before the check", async () => {
  const { user, client } = setup(fingerprintList("fp-2024"));
  await choose(user, "Anthropic");
  await user.type(screen.getByLabelText("API key"), "sk-ant-draft");
  // The displayed URL does not move; the endpoint this name resolves to does.
  // The saved listing reports the source the save produced, so the review can
  // only come from the endpoint identity.
  scriptSave(client, fingerprintList("fp-2025", "store"));

  await user.click(screen.getByRole("button", { name: "Save and check" }));

  expect(await screen.findByText("Review changed access before contacting the provider.")).toBeTruthy();
  // Both sanitized copies are the same text, so this pins that the review came
  // from the endpoint identity the fingerprint carries and not from what the
  // user reads.
  expect(screen.getByText("Previous destination: https://anthropic.example/v1")).toBeTruthy();
  expect(screen.getByText("Current destination: https://anthropic.example/v1")).toBeTruthy();
});

test("a credential draft is not saved to a destination that changed since it was typed", async () => {
  const { user, client } = setup(fingerprintList("fp-2024"));
  await choose(user, "Anthropic");
  await user.type(screen.getByLabelText("API key"), "sk-ant-draft");
  // Another client points the name at a different endpoint while the flow sits
  // idle with the draft: the draft was typed against the old destination, and
  // a credential only ever goes to an endpoint the user was shown.
  client.on("evener/instance/list", () => structuredClone(fingerprintList("fp-2025")));
  await act(async () => {
    await credentialsStore.getState().fetch();
  });

  await user.click(screen.getByRole("button", { name: "Save and check" }));

  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "");
  expect(
    await screen.findByText(
      "This connection changed to a different endpoint. Check its destination and enter the key again.",
    ),
  ).toBeTruthy();
});

// The adopted instance is a different connection. The draft typed for the
// previous destination must not survive the adoption, or a key meant for the
// endpoint the user was shown could be saved to the newly created one.
test("adopting a created instance clears a credential draft typed for the previous endpoint", async () => {
  const { user, client } = setup(fingerprintList("fp-original"));
  await choose(user, "Anthropic");
  await user.type(screen.getByLabelText("API key"), "old-endpoint-key");

  const created: InstanceEntry = {
    name: "anthropic-team",
    providerId: "anthropic",
    base: "anthropic",
    baseUrl: "https://moved.example/v1",
    protocol: "openai-chat",
    auth: "bearer",
    authModes: ["apiKey"],
    implicit: false,
    isDefault: false,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: true,
    endpointFingerprint: "fp-created",
  };
  client.on("evener/auth/apiKey/set", () => saved);
  client.on("evener/instance/create", () => {
    const listing: InstanceListResponse = { instances: [created], availableProviders: catalogue };
    client.on("evener/instance/list", () => structuredClone(listing));
    return structuredClone(listing);
  });

  await user.click(screen.getByText("Advanced settings"));
  await user.click(screen.getByRole("button", { name: "Configure another instance" }));
  await user.type(screen.getByLabelText("Name"), "anthropic-team");
  await user.type(screen.getByLabelText(/base url/i), "https://moved.example/v1");
  await user.click(screen.getByRole("button", { name: "Create" }));

  expect(await screen.findByLabelText("API key")).toHaveProperty("value", "");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
});

// Two missing fingerprints are not evidence of the same destination. An
// unkeyable hub, or a created row the listing has not resolved yet, leaves
// nothing to compare, so a value typed for the previous connection must not
// survive adoption into a connection nobody can describe (roborev finding:
// undefined === undefined must not be read as a match).
test("adopting a created instance with no fingerprints on either side clears the draft", async () => {
  const { user, client } = setup(savedList("anthropic", { endpointFingerprint: undefined }));
  await choose(user);
  await user.type(screen.getByLabelText("API key"), "anthropic-private-draft");

  const created: InstanceEntry = {
    ...catalogue[0]!.setup!,
    name: "anthropic-team",
    implicit: false,
    endpointFingerprint: undefined,
  };
  client.on("evener/instance/create", () => ({
    instances: [created],
    availableProviders: structuredClone(catalogue),
  }));

  await user.click(screen.getByText("Advanced settings"));
  await user.click(screen.getByRole("button", { name: "Configure another instance" }));
  await user.type(screen.getByLabelText("Name"), "anthropic-team");
  await user.click(screen.getByRole("button", { name: "Create" }));

  // The flow's baseline (anthropic) and the adopted row both carry no
  // fingerprint; missing identity is not a match, so the draft is dropped.
  expect(await screen.findByLabelText("API key")).toHaveProperty("value", "");
});

// The read a flow performs as its own work has to be marked as this client's,
// or the invalidation watch reads the transition it brings as someone else's
// change and resets the operation that asked for it. The endpoint-refusal
// recovery is that read at its most exposed: it runs while the flow is busy, and
// the refusal it just reported is the outcome the user is owed. An unmarked read
// there (credentialsStore.fetch(), as the flow used before) advances nothing and
// lets the watch reset the flow; a marked one keeps the flow's own outcome. The
// unmarked direction is pinned by "a notification refresh invalidates a pending
// check and a completed result", which ends with a plain fetch() clearing a
// completed result.
test("the flow's own recovery read carries its commitment and leaves the flow usable", async () => {
  const { user, client } = setup(fingerprintList("fp-2024"));
  await choose(user, "Anthropic");
  await user.type(screen.getByLabelText("API key"), "sk-ant-draft");
  client.on("evener/auth/apiKey/set", () => {
    throw new WireError(
      "anthropic no longer resolves to the endpoint this form was opened on: review its destination and enter the credential again",
      -32013,
      { evenerErrorInfo: ErrorEndpointConflict },
    );
  });
  client.on("evener/instance/list", () => structuredClone(fingerprintList("fp-2025")));
  const before = credentialsStore.getState().selfRefresh;

  await user.click(screen.getByRole("button", { name: "Save and check" }));

  expect(
    await screen.findByText(
      "This connection changed to a different endpoint. Check its destination and enter the key again.",
    ),
  ).toBeTruthy();
  expect(credentialsStore.getState().selfRefresh).toBeGreaterThan(before);
  // Nothing is left mid-flight by the read that no longer resets the flow.
  expect(await screen.findByRole("button", { name: "Save and check" })).toHaveProperty("disabled", false);
});

test("the hub's endpoint refusal re-anchors the flow instead of saving to the moved destination", async () => {
  const { user, client } = setup(fingerprintList("fp-2024"));
  await choose(user, "Anthropic");
  await user.type(screen.getByLabelText("API key"), "sk-ant-draft");
  // The name moves between this flow's check and the write. The client cannot
  // see that window, so the hub refuses the asserted endpoint (appwire.Conflict
  // with evenerErrorInfo "conflict").
  client.on("evener/auth/apiKey/set", () => {
    throw new WireError(
      "anthropic no longer resolves to the endpoint this form was opened on: review its destination and enter the credential again",
      -32013,
      { evenerErrorInfo: ErrorEndpointConflict },
    );
  });
  // What the recovery re-read finds: the moved endpoint, nothing stored.
  client.on("evener/instance/list", () => structuredClone(fingerprintList("fp-2025")));

  await user.click(screen.getByRole("button", { name: "Save and check" }));

  // The write asserted the endpoint this flow showed the user.
  const setCall = client.calls.find((call) => call.method === "evener/auth/apiKey/set");
  expect(setCall?.params).toMatchObject({ provider: "anthropic", expectedEndpointFingerprint: "fp-2024" });
  // The draft belonged to the endpoint that is gone: dropped, re-anchored, and
  // said so, rather than left as a save that silently went somewhere else.
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "");
  expect(
    await screen.findByText(
      "This connection changed to a different endpoint. Check its destination and enter the key again.",
    ),
  ).toBeTruthy();
});

// The probe must carry the destination this flow reviewed, so the hub can
// compare it where the request lands. Without the fingerprint on the wire the
// probe would only name the instance, and a name re-pointed between the refresh
// above and the call could send the stored credential to an endpoint the user
// never saw.
test("the check asserts the reviewed endpoint", async () => {
  const { user, client } = setup(fingerprintList("fp-reviewed"));
  await choose(user, "Anthropic");
  await user.type(screen.getByLabelText("API key"), "sk-ant-draft");
  // The post-save listing carries the same destination as the baseline, so the
  // flow goes straight to the check rather than pausing on the review step.
  scriptSave(client, savedList("anthropic", { endpointFingerprint: "fp-reviewed" }));

  await user.click(screen.getByRole("button", { name: "Save and check" }));

  expect(await screen.findByText(/Model list access confirmed/)).toBeTruthy();
  expect(client.calls.find((call) => call.method === "evener/auth/test")?.params).toEqual({
    provider: "anthropic",
    expectedEndpointFingerprint: "fp-reviewed",
  });
});

test("a destination the hub cannot fingerprint refuses the check without a probe", async () => {
  const noAuth = provider("unfingerprinted", "Team endpoint", ["none"], {
    auth: "none",
    credentialRequired: false,
    endpointFingerprint: undefined,
  });
  const { user, client } = setup({ instances: [], availableProviders: [noAuth] });
  await choose(user, "Team endpoint");
  await user.click(screen.getByRole("button", { name: "Check connection" }));
  expect(await screen.findByText(FINGERPRINT_UNAVAILABLE_TEST_MESSAGE)).toBeTruthy();
  expect(client.calls.some((call) => call.method === "evener/auth/test")).toBe(false);
});

// The write-side counterpart of the refusal above: the row still carries its
// destination, but the listing serves no fingerprint, so the write would assert
// an empty endpoint - which the hub accepts rather than validates - and the
// check that follows would refuse, leaving a saved secret that was never
// tested. submit() refuses the write locally, with the same message the
// instance dialogs use for it.
test("a connection the hub cannot key refuses the save instead of writing an unasserted key", async () => {
  const { user, client } = setup(fingerprintList(""));
  await choose(user);
  await user.type(screen.getByLabelText("API key"), "sk-ant-unkeyed");
  await user.click(screen.getByRole("button", { name: "Save and check" }));

  // Nothing reached the hub: no key write, no credential-JSON write, and no
  // probe against an endpoint the hub cannot describe.
  expect(client.calls.find((call) => call.method === "evener/auth/apiKey/set")).toBeUndefined();
  expect(client.calls.some((call) => call.method === "evener/auth/credentialJson/set")).toBe(false);
  expect(client.calls.some((call) => call.method === "evener/auth/test")).toBe(false);
  // The exact shared write-refusal wording, not the check-refusal one.
  expect((await screen.findByRole("alert")).textContent).toBe(FINGERPRINT_UNAVAILABLE_ERROR);
  // The phase stays idle: the same control is still offered rather than a
  // spinner left behind by a write that never happened.
  expect(screen.getByRole("button", { name: "Save and check" })).toHaveProperty("disabled", false);
});

// A refused endpoint assertion is the same change submit() reports: the
// destination moved under the check. It must reset the flow and say so, not
// masquerade as an unreachable endpoint that invites the user to retry against
// a destination that is gone.
test("a refused assertion is reported as a changed connection, not an endpoint failure", async () => {
  const { user, client } = setup(fingerprintList("fp-reviewed"));
  await choose(user, "Anthropic");
  await user.type(screen.getByLabelText("API key"), "sk-ant-draft");
  scriptSave(client, savedList("anthropic", { endpointFingerprint: "fp-reviewed" }));
  // The hub refuses the asserted endpoint (appwire.Conflict with evenerErrorInfo
  // "conflict"): the name no longer resolves to the endpoint the flow reviewed.
  client.on("evener/auth/test", () => {
    throw new WireError(
      "anthropic no longer resolves to the endpoint this form was opened on: review its destination and enter the credential again",
      -32013,
      { evenerErrorInfo: ErrorEndpointConflict },
    );
  });

  await user.click(screen.getByRole("button", { name: "Save and check" }));

  expect(
    await screen.findByText(
      "This connection changed to a different endpoint. Check its destination and enter the key again.",
    ),
  ).toBeTruthy();
  expect(
    screen.queryByText("The provider endpoint could not be reached. Check the endpoint and network connection."),
  ).toBeNull();
});
