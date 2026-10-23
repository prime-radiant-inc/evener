import type { InstanceEntry, InstanceListResponse, ProviderDescriptor } from "@evener/appwire-client";
import { ErrorEndpointConflict, WireError } from "@evener/appwire-client";
import { deferred as sharedDeferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { NEW_TAB_POLICY } from "../../../../shell/openInNewTab.testSupport";
import { connectionStore } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { setMutationClientIdentityForTests } from "../../../../stores/mutationClientIdentity";
import { enterText } from "../../../../textEntryTestUtils";
import { ProviderConnection } from "./ProviderConnection";

// The flow's state, read from its markers rather than from the words it shows:
// the alert by its data-reason, the confirmed check by its status's data-state,
// and the reveal by the disclosure's own testid.
const alertReason = async (): Promise<string | null> => (await screen.findByRole("alert")).getAttribute("data-reason");
const reasonShown = (reason: string): boolean =>
  document.querySelector(`[role="alert"][data-reason="${reason}"]`) !== null;
// A current successful check retires the flow and calls onConnected once; the
// owner (not the connector) is what removes it from the page. Tests wrap the
// connector in this owner so the completion path is the production one.
function Owner({
  onConnected,
  onClose,
  onOpenSettings,
}: {
  onConnected(name?: string): void;
  onClose(): void;
  onOpenSettings(): void;
}) {
  const [open, setOpen] = useState(true);
  if (!open) return null;
  return (
    <ProviderConnection
      onClose={() => {
        setOpen(false);
        onClose();
      }}
      onConnected={(name) => {
        onConnected(name);
        setOpen(false);
      }}
      onOpenSettings={onOpenSettings}
    />
  );
}
const revealAllProviders = async (): Promise<HTMLElement> => {
  const summary = (await screen.findByTestId("show-all-providers")).querySelector("summary");
  if (!(summary instanceof HTMLElement)) throw new Error("Missing show-all-providers summary");
  return summary;
};

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
  const view = render(<Owner onClose={close} onConnected={connected} onOpenSettings={openSettings} />);
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
  expect(await screen.findByRole("dialog", { name: "OpenAI Codex" })).toBeTruthy();
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
  expect(await screen.findByRole("dialog", { name: "OpenAI Codex" })).toBeTruthy();
  expect(screen.queryByRole("link", { name: "Get an API key" })).toBeNull();
  expect(screen.getByTestId("billing-note")).toBeTruthy();
});

test("a signed-in oauth connection reports its sign-in, not a missing key", async () => {
  const codex = provider("openai-codex", "OpenAI Codex", ["oauth"], {
    activeSource: "oauth",
    storedEmail: "person@example.com",
  });
  const { user } = setup({ instances: [], availableProviders: [codex] });
  await choose(user, "OpenAI Codex");

  expect(screen.getByText("Signed in")).toBeTruthy();
  expect(screen.queryByText("Needs a key")).toBeNull();
});

// The apiKey providers keep both halves: the key link and the billing line.
test("an apiKey provider still renders its key link and its billing line", async () => {
  const { user } = setup();
  await choose(user, "Anthropic");
  expect(screen.getByRole("link", { name: "Get an API key" }).getAttribute("href")).toBe(
    "https://console.anthropic.com/settings/keys",
  );
  expect(screen.getByRole("link", { name: "Get an API key" }).getAttribute("rel")).toBe(NEW_TAB_POLICY);
  expect(screen.getByTestId("billing-note")).toBeTruthy();
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

// An endpoint outside the catalogue is added through openai-compatible, whose
// setup row carries no usable base URL. It has to be reachable from the short
// list, under the name that says what it is for, rather than only after the
// reader opens the whole catalogue.
test("the custom endpoint is in the popular set and opens the connection card", async () => {
  const { user } = setup({
    instances: [],
    availableProviders: [provider("openai-compatible", "OpenAI-compatible endpoint")],
  });
  const choice = await screen.findByRole("button", { name: "Custom endpoint" });
  expect(screen.queryByLabelText("Search providers")).toBeNull();
  await user.click(choice);
  expect(screen.getByRole("dialog", { name: "Custom endpoint" })).toBeTruthy();
  expect(screen.getByLabelText("API key")).toBeTruthy();
});

// The card's rows and its full editor are the components the settings sheet
// renders too, so the two surfaces cannot drift into different connection UIs.
test("the connection card renders its facts and settings through the shared components", async () => {
  const { user } = setup();
  await choose(user);
  expect(screen.getByTestId("connection-facts")).toBeTruthy();
  await act(async () => {
    fireEvent.click(screen.getByText("All connection settings"));
  });
  expect(screen.getByTestId("connection-settings")).toBeTruthy();
});

// The reveal swaps the popular set for the whole catalogue (the popular rows
// are in it too), and closing it puts the short list back - one control, both
// directions, no second label to keep in sync.
test("show all providers reveals the rest of the catalogue and closes back to the popular set", async () => {
  const { user } = setup();
  const reveal = await revealAllProviders();
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
  const reveal = await revealAllProviders();
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
  await revealAllProviders();
  await act(async () => {
    await credentialsStore.getState().fetch();
  });
  // Popular membership must be an own-key check: "constructor" in HELP is
  // true through the prototype chain, which would promote this custom
  // provider to the popular list and then render
  // Object.prototype.constructor as its help entry - a "Get an API key"
  // anchor with no destination for a provider that has no help metadata.
  expect(screen.queryByRole("button", { name: "Custom endpoint" })).toBeNull();
  await user.click(await revealAllProviders());
  await user.click(screen.getByRole("button", { name: "Custom endpoint" }));
  expect(screen.queryByRole("link")).toBeNull();
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
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("openai-codex"));
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toEqual([
    {
      method: "evener/auth/test",
      params: { provider: "openai-codex", expectedEndpointFingerprint: "fp-openai-codex" },
    },
  ]);
});

test("redirect authorization survives its own delayed listing refresh and proceeds to a real check", async () => {
  const { user, client, connected } = setup();
  await choose(user, "OpenAI Codex");
  client.on("evener/auth/device/start", () => ({
    provider: "openai-codex",
    fallback: true,
    flowId: "redirect",
    userCode: "",
    verificationUrl: "",
    intervalSeconds: 5,
  }));
  client.on("evener/auth/login/start", () => ({
    provider: "openai-codex",
    flowId: "redirect",
    url: "https://auth.example/authorize",
  }));
  client.on("evener/auth/login/complete", () => ({
    status: {
      provider: "openai-codex",
      supported: true,
      signedIn: true,
      activeSource: "oauth",
      hasStoredOAuth: true,
    },
  }));
  vi.spyOn(window, "open").mockReturnValue(null);
  await user.click(screen.getByRole("button", { name: "Sign in" }));
  const refresh = deferred<InstanceListResponse>();
  let reads = 0;
  client.on("evener/instance/list", () => {
    reads += 1;
    return reads === 1
      ? refresh.promise
      : savedList("openai-codex", { activeSource: "oauth", hasStoredOAuth: true, configRevision: "rev-oauth" });
  });
  await enterText(user, screen.getByLabelText("Redirect URL"), "https://auth.example/callback?code=1");
  await user.click(screen.getByRole("button", { name: "Finish" }));
  expect(reads).toBe(1);

  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await act(async () => {
    refresh.resolve(
      savedList("openai-codex", { activeSource: "oauth", hasStoredOAuth: true, configRevision: "rev-oauth" }),
    );
    await refresh.promise;
  });

  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("openai-codex"));
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toEqual([
    {
      method: "evener/auth/test",
      params: { provider: "openai-codex", expectedEndpointFingerprint: "fp-openai-codex" },
    },
  ]);
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("device authorization interrupted by reconnect still completes automatically", async () => {
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
  // Hold the child's first post-authorization read: the authorization has
  // already been confirmed when this read is out.
  const held = deferred<InstanceListResponse>();
  client.on("evener/instance/list", () => held.promise);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1000);
  });

  // Transport reconnects to the SAME destination, without cancelling.
  await act(async () => {
    client.emitStateChange("reconnecting");
  });
  await act(async () => {
    client.emitStateChange("ready");
  });
  await act(async () => {
    held.resolve(savedList("openai-codex", { activeSource: "oauth", hasStoredOAuth: true }));
    await held.promise;
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1000);
  });
  vi.useRealTimers();

  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("openai-codex"));
  expect(client.calls.filter((call) => call.method === "evener/auth/device/start")).toHaveLength(1);
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);
  expect(reasonShown("changed")).toBe(false);
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("redirect authorization interrupted by reconnect still completes automatically", async () => {
  const { user, client, connected } = setup();
  await choose(user, "OpenAI Codex");
  client.on("evener/auth/device/start", () => ({
    provider: "openai-codex",
    fallback: true,
    flowId: "redirect",
    userCode: "",
    verificationUrl: "",
    intervalSeconds: 5,
  }));
  client.on("evener/auth/login/start", () => ({
    provider: "openai-codex",
    flowId: "redirect",
    url: "https://auth.example/authorize",
  }));
  client.on("evener/auth/login/complete", () => ({
    status: {
      provider: "openai-codex",
      supported: true,
      signedIn: true,
      activeSource: "oauth",
      hasStoredOAuth: true,
    },
  }));
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  vi.spyOn(window, "open").mockReturnValue(null);
  await user.click(screen.getByRole("button", { name: "Sign in" }));
  const held = deferred<InstanceListResponse>();
  client.on("evener/instance/list", () => held.promise);
  await enterText(user, screen.getByLabelText("Redirect URL"), "https://auth.example/callback?code=1");
  await user.click(screen.getByRole("button", { name: "Finish" }));

  vi.useFakeTimers();
  await act(async () => {
    client.emitStateChange("reconnecting");
  });
  await act(async () => {
    client.emitStateChange("ready");
  });
  await act(async () => {
    held.resolve(savedList("openai-codex", { activeSource: "oauth", hasStoredOAuth: true }));
    await held.promise;
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1000);
  });
  vi.useRealTimers();

  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("openai-codex"));
  expect(client.calls.filter((call) => call.method === "evener/auth/login/complete")).toHaveLength(1);
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);
  expect(reasonShown("changed")).toBe(false);
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("a created connection's own provider drives the card rather than the chosen catalogue entry", async () => {
  const { user, client, connected } = setup();
  await choose(user, "Azure");
  await enterName(user, "openai-team");
  await enterText(user, screen.getByLabelText("Endpoint URL"), "https://team.openai.example/v1");
  // The card creates the base the catalogue entry names, but the row the
  // listing returns can resolve to a different provider; that row is what the
  // flow completes with.
  const row = {
    ...catalogue[1]!.setup!,
    name: "openai-team",
    implicit: false,
    baseUrl: "https://team.openai.example/v1",
  };
  client.on("evener/instance/create", () => {
    client.on("evener/instance/list", () => ({ instances: [row], availableProviders: structuredClone(catalogue) }));
    return { instances: [row], availableProviders: structuredClone(catalogue) };
  });
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  // The created row is the one the flow completes: its own provider, not the
  // catalogue entry the user chose to reach the add card.
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("openai-team"));
  expect(client.calls.find((call) => call.method === "evener/auth/test")?.params).toMatchObject({
    provider: "openai-team",
  });
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
test("the card's own settings disclosure hands off to the full settings", async () => {
  const { user, openSettings } = setup();
  await choose(user, "Anthropic");
  await act(async () => {
    fireEvent.click(screen.getByText("All connection settings"));
  });
  await user.click(screen.getByRole("button", { name: "Manage all connections" }));

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

test("provider choices expose resolved authentication capabilities and select that exact provider", async () => {
  const overridden = provider("anthropic", "Anthropic", ["apiKey"], {
    authModes: ["oauth"],
    auth: "oauth-openai-codex",
  });
  const apiKey = provider("openai", "OpenAI", ["apiKey"]);
  const { user } = setup({ instances: [], availableProviders: [overridden, apiKey] });

  await waitFor(() => expect(document.querySelector('[data-auth-modes="oauth"]')).not.toBeNull());
  const oauthChoice = document.querySelector('[data-auth-modes="oauth"]');
  expect(oauthChoice?.textContent).not.toBe("");
  await user.click(oauthChoice as HTMLElement);

  expect(screen.getByRole("dialog", { name: "Anthropic" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Sign in" })).toBeTruthy();
  expect(screen.queryByLabelText("API key")).toBeNull();
});

test("provider authentication capabilities describe the choice without changing its accessible name", async () => {
  setup({ instances: [], availableProviders: [provider("anthropic", "Anthropic", ["apiKey"])] });

  const choice = await screen.findByRole("button", { name: "Anthropic" });
  const descriptionId = choice.getAttribute("aria-describedby");
  expect(descriptionId).not.toBeNull();
  const description = document.getElementById(descriptionId as string);
  expect(description).not.toBeNull();
  expect(description?.textContent).not.toBe("");
  expect(description?.getAttribute("aria-hidden")).toBeNull();
});

test("a no-auth endpoint never asks for a key", async () => {
  const noAuth = provider("no-auth", "Team endpoint", ["none"], { auth: "none", credentialRequired: false });
  const { user, client, connected } = setup({ instances: [], availableProviders: [noAuth] });
  await choose(user, "Team endpoint");
  expect(screen.queryByLabelText("API key")).toBeNull();
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "unsupported", message: "" }));
  await user.click(screen.getByRole("button", { name: "Test connection" }));
  await user.click(await screen.findByRole("button", { name: "Continue without verification" }));
  expect(connected).toHaveBeenCalledWith("no-auth");
});

test("a discarded create listing retains the created name and reloads instead of creating again", async () => {
  const { user, client } = setup();
  await choose(user, "Azure");
  const pending = deferred<InstanceListResponse>();
  client.on("evener/instance/create", () => pending.promise);
  await enterName(user, "retained-name");
  await enterText(user, screen.getByLabelText("Endpoint URL"), "https://team.example/v1");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  // The create's own answer is discarded, and the confirming read lands a row
  // that is not ready yet: the card keeps the created name and offers the
  // reload recovery rather than another create.
  const row = {
    ...catalogue[0]!.setup!,
    name: "retained-name",
    providerId: "azure",
    implicit: false,
    baseUrl: "",
  };
  const listing: InstanceListResponse = { instances: [row], availableProviders: structuredClone(catalogue) };
  await act(async () => {
    await credentialsStore.getState().fetch();
    client.on("evener/instance/list", () => structuredClone(listing));
    pending.resolve(structuredClone(listing));
    await pending.promise;
  });
  expect(await screen.findByRole("button", { name: "Reload" })).toBeTruthy();
  expect(screen.getByRole("dialog", { name: "retained-name" })).toBeTruthy();
  client.on("evener/instance/list", () => ({
    instances: [{ ...row, baseUrl: "https://team.example/v1" }],
    availableProviders: structuredClone(catalogue),
  }));
  await user.click(screen.getByRole("button", { name: "Reload" }));
  expect(await screen.findByRole("button", { name: "Add key" })).toBeTruthy();
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

test("a changed destination invalidates a pending OAuth start before it opens a browser", async () => {
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
    client.on("evener/instance/list", () => ({
      instances: [],
      availableProviders: catalogue.map((candidate) =>
        candidate.id === "openai-codex"
          ? provider("openai-codex", "OpenAI Codex", ["oauth"], {
              endpointFingerprint: "fp-moved",
              baseUrl: "https://moved.example/v1",
            })
          : candidate,
      ),
    }));
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

test("an unchanged listing read keeps a pending OAuth start and opens its browser", async () => {
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
    flowId: "kept",
    url: "https://auth.example/kept",
  }));
  await user.click(screen.getByRole("button", { name: "Sign in" }));
  await act(async () => {
    // The rows and the selected destination are unchanged: only the read's own
    // loading transition moves, which is not a change this start must retire.
    await credentialsStore.getState().fetch();
    pending.resolve({
      provider: "openai-codex",
      flowId: "kept",
      userCode: "kept",
      verificationUrl: "https://auth.example",
      intervalSeconds: 5,
      fallback: true,
    });
    await pending.promise;
  });
  // The start was not retired: its redirect editor opened and the browser
  // handoff went through the shared lossless opener path once.
  expect(client.calls.filter((call) => call.method === "evener/auth/login/start")).toHaveLength(1);
  expect(screen.getByLabelText("Redirect URL")).toBeTruthy();
  expect(reasonShown("changed")).toBe(false);
});

const PENDING_WRITE_CASES = [
  { name: "API key", provider: "Anthropic", label: "API key", method: "evener/auth/apiKey/set" },
  {
    name: "credential JSON",
    provider: "Google Vertex",
    label: "Credential JSON",
    method: "evener/auth/credentialJson/set",
  },
] as const;
test.each(PENDING_WRITE_CASES)(
  "an unchanged listing read during a pending $name write does not retire its automatic check",
  async ({ provider: providerName, label, method }) => {
    const { user, client, connected } = setup();
    const write = deferred<typeof saved>();
    client.on(method, () => write.promise);
    client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    await choose(user, providerName);
    await enterText(user, screen.getByLabelText(label), "fixture-only-key");
    await user.click(screen.getByRole("button", { name: "Save and test" }));
    expect(screen.getByRole("button", { name: "Saving…" })).toHaveProperty("disabled", true);

    // An ordinary read of the same unchanged destination - its loading
    // transition, then its answer - is not a change this pending write has to
    // be retired for.
    await act(async () => {
      client.on("evener/instance/list", () => savedList(providerName === "Anthropic" ? "anthropic" : "google-vertex"));
      await credentialsStore.getState().fetch();
    });

    await act(async () => {
      write.resolve(saved);
      await write.promise;
    });

    await waitFor(() =>
      expect(connected).toHaveBeenCalledExactlyOnceWith(providerName === "Anthropic" ? "anthropic" : "google-vertex"),
    );
    expect(reasonShown("changed")).toBe(false);
    expect(screen.queryByRole("button", { name: "Use reviewed access and test" })).toBeNull();
    expect(client.calls.filter((call) => call.method === method)).toHaveLength(1);
    expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);
  },
);
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
  expect(screen.getByTestId("billing-note")).toBeTruthy();
  expect(screen.queryByLabelText("Name")).toBeNull();
  expect(screen.queryByLabelText("Base URL (optional)")).toBeNull();
});
test("show all providers searches the complete catalogue including separate Codex and configured variants", async () => {
  const { user } = setup();
  await user.click(await revealAllProviders());
  const search = screen.getByLabelText("Search providers");
  for (const row of catalogue) {
    await user.clear(search);
    await enterText(user, search, row.id);
    expect(screen.getByRole("button", { name: row.name })).toBeTruthy();
  }
});

// The search matches the three fields it has always matched - the registry id,
// its display name, and the help label the row prints - not just whatever the
// row happens to say. "Google Generative AI" is the case that separates them:
// it is neither the id nor the label on screen.
test("the catalogue search matches id, display name and help label", async () => {
  const { user } = setup({ instances: [], availableProviders: [provider("google", "Google Generative AI")] });
  await user.click(await revealAllProviders());
  const search = screen.getByLabelText("Search providers");
  for (const query of ["google", "generative", "gemini"]) {
    await user.clear(search);
    await enterText(user, search, query);
    expect(screen.getByRole("button", { name: "Gemini" })).toBeTruthy();
  }
  await user.clear(search);
  await user.type(search, "vertex");
  expect(screen.queryByRole("button", { name: "Gemini" })).toBeNull();
});

test("a cloud choice with no usable setup opens the card's add state", async () => {
  const { user } = setup();
  await user.click(await revealAllProviders());
  await user.click(screen.getByRole("button", { name: "Azure" }));
  expect(screen.getByRole("dialog", { name: "New connection" })).toBeTruthy();
  expect(screen.getByLabelText("Name")).toHaveProperty("value", "azure");
  expect(screen.getByLabelText("Endpoint URL")).toHaveProperty("value", "");
  expect(screen.getByLabelText("API key")).toHaveProperty("type", "password");
  expect(screen.getByRole("button", { name: "Save and test" })).toBeTruthy();
  // The add card is the card, not a second editor behind a button.
  expect(screen.queryByText("All connection settings")).toBeNull();
});

function savedList(id = "anthropic", extra: Partial<InstanceEntry> = {}): InstanceListResponse {
  const row = { ...catalogue.find((p) => p.id === id)!.setup!, activeSource: "store", hasStoredFile: true, ...extra };
  return { instances: [row], availableProviders: catalogue.map((p) => (p.id === id ? { ...p, setup: row } : p)) };
}
/** The saved Anthropic row repointed at another endpoint, as a listing. */
function movedList(): InstanceListResponse {
  return savedList("anthropic", { baseUrl: "https://moved.example/v1", endpointFingerprint: "fp-moved" });
}
const saved = { provider: "anthropic", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false };
/** The azure-team row the create-flow tests author; a fresh object per call so
 * a listing read cannot mutate another read's baseline. */
function azureTeamRow(): InstanceEntry {
  return {
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
}
async function choose(user: ReturnType<typeof userEvent.setup>, name = "Anthropic") {
  // Choosing from the revealed catalogue covers both kinds of provider this
  // helper is used for (popular and not), and keeps one path through the
  // picker for every caller. The reveal stays open across a Change provider,
  // so it is only clicked when the catalogue is not already showing.
  const reveal = await revealAllProviders();
  if (screen.queryByLabelText("Search providers") === null) await user.click(reveal);
  await user.click(screen.getByRole("button", { name }));
}
/** The add card's Name field starts with the provider id, so filling it is a
 * replace rather than an append. */
async function enterName(user: ReturnType<typeof userEvent.setup>, value: string) {
  const field = screen.getByLabelText("Name");
  await user.clear(field);
  await enterText(user, field, value);
}
/** The key row's host-access link is inside the row while it is editing. */
async function openHostAccess(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: "use this host's access instead" }));
}
function scriptSave(client: FakeClient, next = savedList()) {
  client.on("evener/auth/apiKey/set", () => {
    client.on("evener/instance/list", () => structuredClone(next));
    return saved;
  });
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "raw-provider-secret" }));
}
function scriptCreate(client: FakeClient, listing: InstanceListResponse) {
  client.on("evener/instance/create", () => {
    client.on("evener/instance/list", () => structuredClone(listing));
    return structuredClone(listing);
  });
}
function createCalls(client: FakeClient) {
  return client.calls.filter((call) => call.method === "evener/instance/create");
}
function editCalls(client: FakeClient) {
  return client.calls.filter((call) => call.method === "evener/instance/edit");
}
function scriptEdit(client: FakeClient, listing: InstanceListResponse) {
  client.on("evener/instance/edit", () => structuredClone(listing));
}
function scriptListing(client: FakeClient, listing: InstanceListResponse) {
  client.on("evener/instance/list", () => structuredClone(listing));
}
/** Runs one editor save until its create is unconfirmed: the create's own
 * answer was superseded and every confirming read failed. */
async function unconfirmedCreate(
  client: FakeClient,
  start: () => Promise<void>,
  listing: InstanceListResponse,
): Promise<void> {
  const create = deferred<InstanceListResponse>();
  client.on("evener/instance/create", () => create.promise);
  await start();
  await waitFor(() => expect(createCalls(client)).toHaveLength(1));
  client.on("evener/instance/list", () => {
    throw new Error("listing unavailable");
  });
  await act(async () => {
    await credentialsStore.getState().fetch();
    create.resolve(structuredClone(listing));
    await create.promise;
  });
  expect(await alertReason()).toBe("save-unconfirmed");
}
/** Runs one editor edit until its confirmation is unconfirmed: the edit's own
 * answer was superseded and every confirming read failed. */
async function unconfirmedEdit(
  client: FakeClient,
  start: () => Promise<void>,
  listing: InstanceListResponse,
): Promise<void> {
  const edit = deferred<InstanceListResponse>();
  client.on("evener/instance/edit", () => edit.promise);
  await start();
  await waitFor(() => expect(editCalls(client)).toHaveLength(1));
  client.on("evener/instance/list", () => {
    throw new Error("listing unavailable");
  });
  await act(async () => {
    await credentialsStore.getState().fetch();
    edit.resolve(structuredClone(listing));
    await edit.promise;
  });
  expect(await alertReason()).toBe("save-unconfirmed");
}
/** The create's own answer and the listing a concurrent read reconciled it to. */
function supersededCreateFixtures(rowFp: string | undefined): {
  authored: InstanceListResponse;
  reconciled: InstanceListResponse;
} {
  const authoredRow: InstanceEntry = {
    ...catalogue[0]!.setup!,
    name: "recovered-team",
    providerId: "azure",
    base: "azure",
    implicit: false,
    baseUrl: "https://recovered.example/v1",
    endpointFingerprint: "fp-authored",
  };
  const reconciledRow: InstanceEntry = { ...authoredRow, endpointFingerprint: rowFp };
  return {
    authored: { instances: [authoredRow], availableProviders: structuredClone(catalogue) },
    reconciled: { instances: [reconciledRow], availableProviders: structuredClone(catalogue) },
  };
}
async function startRecoveredCreate(user: ReturnType<typeof userEvent.setup>) {
  await choose(user, "Anthropic");
  await enterText(user, screen.getByLabelText("API key"), "old-draft");
  await user.click(screen.getByRole("button", { name: "Change provider" }));
  await choose(user, "Azure");
  await enterName(user, "recovered-team");
  await enterText(user, screen.getByLabelText("Endpoint URL"), "https://recovered.example/v1");
  await enterText(user, screen.getByLabelText("API key"), "add-card-key");
}
/** Drives a discovery-only endpoint save until its create is unconfirmed:
 * the create's own answer was superseded and every confirming read failed. */
async function unconfirmedEndpointCreate(
  user: ReturnType<typeof userEvent.setup>,
  client: FakeClient,
  endpoint: string,
  createdBaseUrl = endpoint,
  fingerprint = "fp-proxy",
): Promise<{ created: InstanceEntry; listing: InstanceListResponse }> {
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Edit" }));
  const field = screen.getByLabelText("Endpoint URL");
  await user.clear(field);
  await enterText(user, field, endpoint);
  const created: InstanceEntry = {
    ...catalogue[0]!.setup!,
    name: "anthropic",
    implicit: false,
    baseUrl: createdBaseUrl,
    endpointFingerprint: fingerprint,
  };
  const listing: InstanceListResponse = { instances: [created], availableProviders: structuredClone(catalogue) };
  await unconfirmedCreate(
    client,
    async () => {
      await user.click(screen.getByRole("button", { name: "Save" }));
    },
    listing,
  );
  return { created, listing };
}
function deferred<T>() {
  const { promise, resolve, reject } = sharedDeferred<T>();
  return { promise, resolve, reject: (reason?: unknown) => reject(reason) };
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
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  const readsBefore = client.calls.filter((c) => c.method === "evener/instance/list").length;
  // The connection is replaced and its own listing has not been applied yet.
  act(() => credentialsStore.setState({ listingFromPreviousConnection: true }));
  await user.click(screen.getByRole("button", { name: "Save and test" }));

  // Nothing was sent to the replacement connection...
  expect(client.calls.filter((c) => c.method === "evener/auth/apiKey/set")).toHaveLength(0);
  // ...and the refusal names the change rather than reporting a save failure.
  expect(await alertReason()).toBe("connection-replaced");
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
  expect(await alertReason()).toBe("connection-replaced");
});

test("empty required key stays focused and does not mutate", async () => {
  const { user, client } = setup();
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  expect(document.activeElement).toBe(screen.getByLabelText("API key"));
  expect(screen.getByRole("alert").id).toBe(screen.getByLabelText("API key").getAttribute("aria-describedby"));
  expect(client.calls.filter((c) => c.method !== "evener/instance/list")).toEqual([]);
});
test("saves directly to the real public ID then reloads and checks before completing with its name", async () => {
  const { user, client, connected } = setup();
  scriptSave(client);
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
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
  expect(screen.queryByRole("dialog")).toBeNull();
});
test("save failure retains the draft and retry saves once more without a premature check", async () => {
  const { user, client, connected } = setup();
  client.on("evener/auth/apiKey/set", () => {
    throw new Error("secret-failure-body");
  });
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  expect(await screen.findByRole("alert")).toBe(document.activeElement);
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "fixture-key");
  expect(screen.queryByText(/secret-failure-body/)).toBeNull();
  expect(client.calls.some((c) => c.method === "evener/auth/test")).toBe(false);
  scriptSave(client);
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
});
test.each(["auth_rejected", "endpoint_failure", "configuration_failure", "unsupported"])(
  "%s keeps the saved connection and retries only the check",
  async (status) => {
    const { user, client, connected } = setup();
    scriptSave(client);
    client.on("evener/auth/test", ({ provider }) => ({ provider, status, message: "secret-response" }));
    await choose(user);
    await enterText(user, screen.getByLabelText("API key"), "fixture-key");
    await user.click(screen.getByRole("button", { name: "Save and test" }));
    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.queryByText(/secret-response/)).toBeNull();
    // The key row is collapsed after a successful save: the saved state is
    // what the card shows, and the draft is no longer an editable field.
    expect(screen.getByTestId("credential-saved")).toBeTruthy();
    expect(screen.queryByLabelText("API key")).toBeNull();
    if (status === "unsupported") {
      await user.click(screen.getByRole("button", { name: "Continue without verification" }));
      expect(connected).toHaveBeenCalledWith("anthropic");
      return;
    }
    client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    await user.click(screen.getByRole("button", { name: "Test again" }));
    await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
    expect(client.calls.filter((c) => c.method === "evener/auth/apiKey/set")).toHaveLength(1);
  },
);
test("a temporary endpoint failure retries on the paced schedule and completes once after one write", async () => {
  const { user, client, connected } = setup();
  scriptSave(client);
  const pending = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => pending.promise);
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await screen.findByRole("button", { name: "Testing…" });
  let attempts = 1;
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    pending.resolve({ provider: "anthropic", status: "endpoint_failure", message: "" });
    await pending.promise;
  });
  expect(attempts).toBe(1);
  expect(connected).not.toHaveBeenCalled();
  // Saved access plus a scheduled automatic retry is said out loud, so a
  // transient failure does not read as a terminal rejection.
  const scheduledNotice = document.querySelector<HTMLElement>('[data-retry="scheduled"]');
  expect(scheduledNotice).not.toBeNull();
  expect(scheduledNotice?.getAttribute("role")).toBe("status");
  expect(scheduledNotice?.textContent?.trim()).not.toBe("");

  // First retry is one second after the failed check, the next two seconds.
  client.on("evener/auth/test", ({ provider }) => {
    attempts += 1;
    return { provider, status: attempts === 2 ? "endpoint_failure" : "success", message: "" };
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(999);
  });
  expect(attempts).toBe(1);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1);
  });
  expect(attempts).toBe(2);
  expect(connected).not.toHaveBeenCalled();
  expect(document.querySelector('[data-retry="scheduled"]')).toBeTruthy();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1999);
  });
  expect(attempts).toBe(2);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1);
  });
  expect(attempts).toBe(3);
  expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic");
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(1);
  expect(client.calls.filter((call) => call.method === "thread/start")).toHaveLength(0);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
});

// The hub refuses a probe result that a credential write superseded while the
// check ran (appwire.Conflict with evenerErrorInfo "conflict"). This is not an
// endpoint failure: the saved access is retained, the dialog stays open, and
// the paced retry rereads the listing and checks the replacement without
// saving it again.
test("a superseded credential check keeps saved access and retries to fresh success", async () => {
  const { user, client, close, connected } = setup();
  scriptSave(client);
  const listReads = () => client.calls.filter((call) => call.method === "evener/instance/list").length;
  let probes = 0;
  const first = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => {
    probes += 1;
    return first.promise;
  });
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await screen.findByRole("button", { name: "Testing…" });

  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    first.reject(
      new WireError("Credentials changed while this check was running.", -32013, { evenerErrorInfo: "conflict" }),
    );
    await first.promise.catch(() => {});
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(reasonShown("access-changed")).toBe(true);
  expect(connected).not.toHaveBeenCalled();
  expect(close).not.toHaveBeenCalled();
  expect(screen.getByRole("dialog")).toBeTruthy();
  // The credential that was saved before the check stays saved.
  expect(screen.getByTestId("credential-saved")).toBeTruthy();
  expect(screen.getByRole("button", { name: /^Close, access stays saved$/ })).toBeTruthy();
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(1);

  const readsBeforeRetry = listReads();
  client.on("evener/auth/test", ({ provider }) => {
    probes += 1;
    return { provider, status: "success", message: "" };
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(999);
  });
  expect(probes).toBe(1);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1);
  });
  expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic");
  expect(probes).toBe(2);
  expect(listReads()).toBeGreaterThan(readsBeforeRetry);
  // Recovery tested the replacement; it did not save it again.
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(1);
});

test("a superseded credential check retries to the replacement's own rejection", async () => {
  const { user, client, connected } = setup();
  scriptSave(client);
  let probes = 0;
  const first = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => {
    probes += 1;
    return first.promise;
  });
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await screen.findByRole("button", { name: "Testing…" });

  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    first.reject(
      new WireError("Credentials changed while this check was running.", -32013, { evenerErrorInfo: "conflict" }),
    );
    await first.promise.catch(() => {});
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(reasonShown("access-changed")).toBe(true);
  expect(connected).not.toHaveBeenCalled();
  expect(screen.getByTestId("credential-saved")).toBeTruthy();

  client.on("evener/auth/test", ({ provider }) => {
    probes += 1;
    return { provider, status: "auth_rejected", message: "" };
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1000);
  });
  expect(reasonShown("check-failed")).toBe(true);
  expect(probes).toBe(2);
  expect(connected).not.toHaveBeenCalled();
  // The replacement's rejection is a terminal answer about that credential,
  // so no further retry is pending and the saved access remains.
  expect(document.querySelector('[data-retry="scheduled"]')).toBeNull();
  expect(screen.getByTestId("credential-saved")).toBeTruthy();
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(1);
});

// A credential-header selector edit can leave the public revision equal while
// replacing the key the check sent. The server now refuses that superseded
// probe too, and the refreshed row that carries the change must retire the old
// attempt: the connector may not send another check to the replacement before
// the person reviews it, and it must not treat the refusal as a fresh save.
test("a refreshed same-revision credential-header row waits for review instead of checking the replacement", async () => {
  const { user, client, connected } = setup(
    savedList("anthropic", {
      configRevision: "rev-hdr",
      credentialHeader: "Authorization=Bearer $KEY_A",
    }),
  );
  const pending = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => pending.promise);
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Test again" }));
  expect(screen.getByRole("button", { name: "Testing…" })).toHaveProperty("disabled", true);

  vi.useFakeTimers();
  await act(async () => {
    client.on("evener/instance/list", () =>
      savedList("anthropic", {
        configRevision: "rev-hdr",
        credentialHeader: "Authorization=Bearer $KEY_B",
      }),
    );
    client.emitNotification({ method: "evener/auth/updated", params: { provider: "anthropic" } });
    await vi.runOnlyPendingTimersAsync();
  });
  vi.useRealTimers();
  expect(await alertReason()).toBe("changed");

  // The hub's refusal of the old attempt must not revive completion or start a
  // check against the changed access.
  await act(async () => {
    pending.reject(
      new WireError("Credentials changed while this check was running.", -32013, { evenerErrorInfo: "conflict" }),
    );
    await pending.promise.catch(() => {});
  });
  expect(connected).not.toHaveBeenCalled();
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);

  // The replacement is checked only through the existing review action.
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await user.click(screen.getByRole("button", { name: "Test again" }));
  expect(await screen.findByTestId("access-review")).toBeTruthy();
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);
  await user.click(screen.getByRole("button", { name: "Use reviewed access and test" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
});

// The other order: the hub's generic Conflict reaches the connector while the
// row on screen is still the old access. The paced retry then performs the real
// store refresh, and that refresh discovers the changed credential header
// under the same public revision. The connector must stop at review rather than
// check the replacement, and recovery happens only after the review action.
test("a superseded check whose retry discovers changed credential-header access waits for review", async () => {
  const { user, client, connected } = setup(
    savedList("anthropic", {
      configRevision: "rev-hdr",
      credentialHeader: "Authorization=Bearer $KEY_A",
    }),
  );
  const first = deferred<{ provider: string; status: string; message: string }>();
  let probes = 0;
  client.on("evener/auth/test", () => {
    probes += 1;
    return first.promise;
  });
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Test again" }));
  await screen.findByRole("button", { name: "Testing…" });

  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    first.reject(
      new WireError("Credentials changed while this check was running.", -32013, { evenerErrorInfo: "conflict" }),
    );
    await first.promise.catch(() => {});
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(reasonShown("access-changed")).toBe(true);
  expect(connected).not.toHaveBeenCalled();
  expect(probes).toBe(1);

  // The scheduled retry's own read carries the replacement access.
  client.on("evener/instance/list", () =>
    savedList("anthropic", {
      configRevision: "rev-hdr",
      credentialHeader: "Authorization=Bearer $KEY_B",
    }),
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1000);
  });
  vi.useRealTimers();
  expect(await screen.findByTestId("access-review")).toBeTruthy();
  expect(probes).toBe(1);
  expect(connected).not.toHaveBeenCalled();
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);

  client.on("evener/auth/test", ({ provider }) => {
    probes += 1;
    return { provider, status: "success", message: "" };
  });
  await user.click(screen.getByRole("button", { name: "Use reviewed access and test" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
  expect(probes).toBe(2);
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
});

test("saved OAuth access awaiting an automatic retry does not invite another sign-in", async () => {
  const { user, client } = setup(savedList("openai-codex", { activeSource: "oauth", hasStoredOAuth: true }));
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "endpoint_failure", message: "" }));
  await choose(user, "OpenAI Codex");
  expect(screen.getByRole("button", { name: "Sign in" })).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Test again" }));

  await waitFor(() => expect(reasonShown("check-failed")).toBe(true));
  const scheduledNotice = document.querySelector<HTMLElement>('[data-retry="scheduled"]');
  expect(scheduledNotice).not.toBeNull();
  expect(scheduledNotice?.getAttribute("role")).toBe("status");
  expect(scheduledNotice?.textContent?.trim()).not.toBe("");
  // Recovery is already running on stored access: offering a fresh acquisition
  // would invite a needless sign-in.
  expect(screen.queryByRole("button", { name: "Sign in" })).toBeNull();
  // Manual retry and dismissal are still the person's.
  expect(screen.getByRole("button", { name: "Test again" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Close, access stays saved" })).toBeTruthy();
});

test("a manual retry cancels the scheduled one so checks do not overlap", async () => {
  const { user, client, connected } = setup();
  scriptSave(client);
  const first = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => first.promise);
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await screen.findByRole("button", { name: "Testing…" });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    first.resolve({ provider: "anthropic", status: "endpoint_failure", message: "" });
    await first.promise;
  });

  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Test again" }));
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic");
  await act(async () => {
    await vi.advanceTimersByTimeAsync(15000);
  });
  expect(connected).toHaveBeenCalledTimes(1);
});

test.each(["auth_rejected", "missing", "configuration_failure"])("%s stops automatic retry", async (status) => {
  const { user, client, connected } = setup();
  scriptSave(client);
  const first = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => first.promise);
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await screen.findByRole("button", { name: "Testing…" });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    first.resolve({ provider: "anthropic", status, message: "" });
    await first.promise;
  });
  expect(reasonShown("check-failed")).toBe(true);
  const attempts = client.calls.filter((call) => call.method === "evener/auth/test").length;
  expect(attempts).toBe(1);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(60000);
  });
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(attempts);
  expect(connected).not.toHaveBeenCalled();
});

test("unmounting clears the scheduled retry", async () => {
  const mounted = setup();
  scriptSave(mounted.client);
  const first = deferred<{ provider: string; status: string; message: string }>();
  mounted.client.on("evener/auth/test", () => first.promise);
  await choose(mounted.user);
  await enterText(mounted.user, screen.getByLabelText("API key"), "fixture-key");
  await mounted.user.click(screen.getByRole("button", { name: "Save and test" }));
  await screen.findByRole("button", { name: "Testing…" });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    first.resolve({ provider: "anthropic", status: "endpoint_failure", message: "" });
    await first.promise;
  });
  const attempts = mounted.client.calls.filter((call) => call.method === "evener/auth/test").length;
  mounted.unmount();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(60000);
  });
  expect(mounted.client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(attempts);
});

test("switching provider clears the scheduled retry belonging to the old target", async () => {
  const { user, client, connected } = setup();
  scriptSave(client);
  const first = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => first.promise);
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await screen.findByRole("button", { name: "Testing…" });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    first.resolve({ provider: "anthropic", status: "endpoint_failure", message: "" });
    await first.promise;
  });
  const attempts = client.calls.filter((call) => call.method === "evener/auth/test").length;
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Change provider" }));
  });
  client.on("evener/instance/list", () => ({ instances: [], availableProviders: structuredClone(catalogue) }));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(60000);
  });
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(attempts);
  expect(connected).not.toHaveBeenCalled();
});

test("a scheduled retry survives reconnect and re-anchors with the recovered client", async () => {
  const { user, client, connected } = setup();
  scriptSave(client);
  const first = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => first.promise);
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await screen.findByRole("button", { name: "Testing…" });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    first.resolve({ provider: "anthropic", status: "endpoint_failure", message: "" });
    await first.promise;
  });
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await act(async () => {
    client.emitStateChange("reconnecting");
    await vi.advanceTimersByTimeAsync(1000);
  });
  // The retry came due while the transport was down: it waits rather than
  // spending its attempt on a request that cannot land.
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);
  await act(async () => {
    client.emitReady();
    await vi.advanceTimersByTimeAsync(2000);
  });
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(2);
  expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic");
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(1);
});

test("a retry after the client is replaced re-anchors on the replacement, not the old listing", async () => {
  const { user, client, connected } = setup();
  scriptSave(client);
  const first = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => first.promise);
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await screen.findByRole("button", { name: "Testing…" });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    first.resolve({ provider: "anthropic", status: "endpoint_failure", message: "" });
    await first.promise;
  });

  const replacement = new FakeClient("ready");
  replacement.on("evener/instance/list", () => structuredClone(savedList()));
  replacement.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await act(async () => {
    connectionStore.getState().connect(replacement);
  });
  // A same-controller replacement is transport, not credential removal. The
  // saved access stays visible and a manual recovery must only read/check.
  expect(screen.getByRole("button", { name: /^Close, access stays saved$/i })).toBeTruthy();
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Test again" }));
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic");
  expect(replacement.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(1);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(15000);
  });
  expect(connected).toHaveBeenCalledTimes(1);
  expect(replacement.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(1);
});

test("a reconnect during an in-flight check schedules a fresh check and completes once", async () => {
  const { user, client, connected } = setup();
  scriptSave(client);
  const first = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => first.promise);
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await screen.findByRole("button", { name: "Testing…" });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    client.emitStateChange("reconnecting");
    first.resolve({ provider: "anthropic", status: "success", message: "" });
    await first.promise;
  });
  expect(connected).not.toHaveBeenCalled();
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await act(async () => {
    client.emitReady();
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1000);
  });
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(2);
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(1);
  expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic");
});

test("a reconnect during an in-flight listing read schedules a fresh read and completes once", async () => {
  const { user, client, connected } = setup();
  client.on("evener/auth/apiKey/set", () => saved);
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  const refresh = deferred<InstanceListResponse>();
  client.on("evener/instance/list", () => refresh.promise);
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await screen.findByRole("button", { name: "Reading providers…" });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    client.emitStateChange("reconnecting");
    refresh.resolve(savedList());
    await refresh.promise;
  });
  expect(connected).not.toHaveBeenCalled();
  client.on("evener/instance/list", () => savedList());
  await act(async () => {
    client.emitReady();
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1000);
  });
  expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic");
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(1);
});

test("a reconnect during an automatic retry check schedules another check and completes once", async () => {
  const { user, client, connected } = setup();
  scriptSave(client);
  const first = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => first.promise);
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await screen.findByRole("button", { name: "Testing…" });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    first.resolve({ provider: "anthropic", status: "endpoint_failure", message: "" });
    await first.promise;
  });
  const second = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => second.promise);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1000);
  });
  expect(screen.getByRole("button", { name: "Testing…" })).toHaveProperty("disabled", true);
  await act(async () => {
    client.emitStateChange("reconnecting");
    second.resolve({ provider: "anthropic", status: "success", message: "" });
    await second.promise;
  });
  expect(connected).not.toHaveBeenCalled();
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await act(async () => {
    client.emitReady();
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1000);
  });
  expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic");
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(3);
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(1);
});

test("changing provider retires a scheduled retry", async () => {
  const { user, client, connected } = setup();
  scriptSave(client);
  const first = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => first.promise);
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await screen.findByRole("button", { name: "Testing…" });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    first.resolve({ provider: "anthropic", status: "endpoint_failure", message: "" });
    await first.promise;
  });
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Change provider" }));
  });
  expect(screen.getByRole("button", { name: "Anthropic" })).toBeTruthy();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(60000);
  });
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);
  expect(connected).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "Anthropic" })).toBeTruthy();
});

test("switching to host access retires a scheduled retry for the credential path", async () => {
  const { user, client, connected } = setup();
  scriptSave(client);
  const first = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => first.promise);
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await screen.findByRole("button", { name: "Testing…" });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    first.resolve({ provider: "anthropic", status: "endpoint_failure", message: "" });
    await first.promise;
  });
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Replace" }));
  });
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "use this host's access instead" }));
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(60000);
  });
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);
  expect(connected).not.toHaveBeenCalled();
});

test("a thrown probe is retried automatically and completes once without another write", async () => {
  const { user, client, connected } = setup();
  scriptSave(client);
  const first = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => first.promise);
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await screen.findByRole("button", { name: "Testing…" });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    first.reject(new Error("probe transport down"));
    await first.promise.catch(() => {});
  });
  expect(reasonShown("check-failed")).toBe(true);
  expect(connected).not.toHaveBeenCalled();
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1000);
  });
  expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic");
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(2);
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(1);
});

test("a failed listing read is retried automatically and completes without another write", async () => {
  const { user, client, connected } = setup();
  client.on("evener/auth/apiKey/set", () => saved);
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  const refresh = deferred<InstanceListResponse>();
  client.on("evener/instance/list", () => refresh.promise);
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await screen.findByRole("button", { name: "Reading providers…" });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    refresh.reject(new Error("listing transport down"));
    await refresh.promise.catch(() => {});
  });
  expect(reasonShown("refresh-failed")).toBe(true);
  expect(connected).not.toHaveBeenCalled();
  client.on("evener/instance/list", () => savedList());
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1000);
  });
  expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic");
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(1);
});

test("a newer dialog owns a late result from the previous target", async () => {
  const { user, client, connected } = setup();
  scriptSave(client);
  const pending = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => pending.promise);
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await screen.findByRole("button", { name: "Testing…" });
  await user.click(screen.getByRole("button", { name: "Change provider" }));
  client.on("evener/instance/list", () => ({ instances: [], availableProviders: structuredClone(catalogue) }));
  await choose(user, "OpenAI");
  expect(screen.getByRole("dialog", { name: "OpenAI" })).toBeTruthy();
  await act(async () => {
    pending.resolve({ provider: "anthropic", status: "success", message: "" });
    await pending.promise;
  });
  expect(connected).not.toHaveBeenCalled();
  expect(screen.getByRole("dialog", { name: "OpenAI" })).toBeTruthy();
});

test("a check shows one progress state instead of sign-in plus a competing check action", async () => {
  const { user, client } = setup();
  await choose(user, "OpenAI Codex");
  const pending = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => pending.promise);
  await user.click(screen.getByRole("button", { name: "Test connection" }));
  expect(screen.queryByRole("button", { name: "Sign in" })).toBeNull();
  expect(screen.getByRole("button", { name: "Testing…" })).toHaveProperty("disabled", true);
  await act(async () => {
    pending.resolve({ provider: "openai-codex", status: "endpoint_failure", message: "" });
    await pending.promise;
  });
  expect(await screen.findByRole("button", { name: "Test again" })).toHaveProperty("disabled", false);
});

test("manual retry after redirect sign-in keeps the OAuth source and does not review unchanged access", async () => {
  const { user, client, connected } = setup();
  await choose(user, "OpenAI Codex");
  client.on("evener/auth/device/start", () => ({
    provider: "openai-codex",
    fallback: true,
    flowId: "redirect",
    userCode: "",
    verificationUrl: "",
    intervalSeconds: 5,
  }));
  client.on("evener/auth/login/start", () => ({
    provider: "openai-codex",
    flowId: "redirect",
    url: "https://auth.example/authorize",
  }));
  client.on("evener/auth/login/complete", () => ({
    status: {
      provider: "openai-codex",
      supported: true,
      signedIn: true,
      activeSource: "oauth",
      hasStoredOAuth: true,
    },
  }));
  client.on("evener/instance/list", () =>
    savedList("openai-codex", { activeSource: "oauth", hasStoredOAuth: true, configRevision: "rev-oauth" }),
  );
  let probes = 0;
  client.on("evener/auth/test", ({ provider }) => {
    probes += 1;
    return { provider, status: probes === 1 ? "endpoint_failure" : "success", message: "" };
  });
  vi.spyOn(window, "open").mockReturnValue(null);
  await user.click(screen.getByRole("button", { name: "Sign in" }));
  await enterText(user, screen.getByLabelText("Redirect URL"), "https://auth.example/callback?code=1");
  await user.click(screen.getByRole("button", { name: "Finish" }));
  expect(await alertReason()).toBe("check-failed");
  expect(screen.getByRole("button", { name: /^Close, access stays saved$/i })).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Test again" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("openai-codex"));
  expect(screen.queryByTestId("access-review")).toBeNull();
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(2);
  expect(client.calls.filter((call) => call.method === "evener/auth/device/start")).toHaveLength(1);
});

test("manual retry after device sign-in keeps the OAuth source and does not review unchanged access", async () => {
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
  client.on("evener/instance/list", () =>
    savedList("openai-codex", { activeSource: "oauth", hasStoredOAuth: true, configRevision: "rev-oauth" }),
  );
  let probes = 0;
  client.on("evener/auth/test", ({ provider }) => {
    probes += 1;
    return { provider, status: probes === 1 ? "endpoint_failure" : "success", message: "" };
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1000);
  });
  expect(reasonShown("check-failed")).toBe(true);
  expect(screen.getByRole("button", { name: /^Close, access stays saved$/i })).toBeTruthy();
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Test again" }));
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(connected).toHaveBeenCalledExactlyOnceWith("openai-codex");
  expect(screen.queryByTestId("access-review")).toBeNull();
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(2);
  expect(client.calls.filter((call) => call.method === "evener/auth/device/start")).toHaveLength(1);
});

test("cancelling OAuth drops a late authorization result", async () => {
  const { user, client, connected } = setup();
  await choose(user, "OpenAI Codex");
  client.on("evener/auth/device/start", () => ({
    provider: "openai-codex",
    flowId: "device",
    userCode: "CODE",
    verificationUrl: "https://auth.example",
    intervalSeconds: 1,
  }));
  const late = deferred<{ provider: string; state: string }>();
  client.on("evener/auth/device/poll", () => late.promise);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1000);
  });
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  });
  await act(async () => {
    late.resolve({ provider: "openai-codex", state: "authorized" });
    await late.promise;
    await vi.advanceTimersByTimeAsync(5000);
  });
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(0);
  expect(connected).not.toHaveBeenCalled();
});

test("dismissal stays available while a check is in flight", async () => {
  const { user, client, close } = setup();
  scriptSave(client);
  const pending = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => pending.promise);
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await user.click(screen.getByRole("button", { name: /^Close, access stays saved$/i }));
  expect(close).toHaveBeenCalledTimes(1);
  await act(async () => {
    pending.resolve({ provider: "anthropic", status: "success", message: "" });
    await pending.promise;
  });
});
test("saved access survives a failed check and dismissal, then reopens without another save", async () => {
  const first = setup();
  scriptSave(first.client);
  first.client.on("evener/auth/test", ({ provider }) => ({ provider, status: "auth_rejected", message: "private" }));
  await choose(first.user);
  await enterText(first.user, screen.getByLabelText("API key"), "fixture-key");
  await first.user.click(screen.getByRole("button", { name: "Save and test" }));
  expect(await alertReason()).toBe("check-failed");
  expect(first.client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(1);

  await first.user.click(screen.getByRole("button", { name: /^Close, access stays saved$/i }));
  expect(first.close).toHaveBeenCalledTimes(1);
  first.unmount();

  first.client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  const connected = vi.fn();
  const reopened = render(<Owner onClose={() => {}} onConnected={connected} onOpenSettings={() => {}} />);
  const user = userEvent.setup();
  await choose(user);
  expect(screen.getByTestId("credential-saved")).toBeTruthy();
  expect(screen.queryByLabelText("API key")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Test again" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
  expect(first.client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(1);
  reopened.unmount();
});
test("editing reopened stored access can be dismissed without a write while the original stays saved", async () => {
  const { user, client, close } = setup(savedList());
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Replace" }));
  await enterText(user, screen.getByLabelText("API key"), "replacement-draft");

  await user.click(screen.getByRole("button", { name: /^Close, access stays saved$/i }));

  expect(close).toHaveBeenCalledTimes(1);
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
});
test("a failed stored-access replacement keeps the original access and truthful dismissal", async () => {
  const { user, client, close } = setup(savedList());
  client.on("evener/auth/apiKey/set", () => {
    throw new Error("replacement refused");
  });
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Replace" }));
  await enterText(user, screen.getByLabelText("API key"), "replacement-draft");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  expect(await alertReason()).toBe("save-failed");

  await user.click(screen.getByRole("button", { name: /^Close, access stays saved$/i }));

  expect(close).toHaveBeenCalledTimes(1);
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(1);
});
test.each([{ activeSource: "api_key" }, { baseUrl: "https://changed.example/v1" }])(
  "requires explicit source/destination review after save: %j",
  async (change) => {
    const { user, client, connected } = setup();
    scriptSave(client, savedList("anthropic", change));
    await choose(user);
    await enterText(user, screen.getByLabelText("API key"), "fixture-key");
    await user.click(screen.getByRole("button", { name: "Save and test" }));
    expect(await screen.findByRole("button", { name: "Use reviewed access and test" })).toBeTruthy();
    expect(screen.getByTestId("credential-saved")).toBeTruthy();
    expect(client.calls.some((c) => c.method === "evener/auth/test")).toBe(false);
    await user.click(screen.getByRole("button", { name: "Use reviewed access and test" }));
    await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
  },
);
test("manual retry after accepting reviewed access keeps the accepted source", async () => {
  const endpoint = catalogue.find((row) => row.id === "ollama")!.setup!;
  const environmentRow: InstanceEntry = {
    ...endpoint,
    activeSource: "env:OLLAMA_API_KEY",
    hasStoredFile: false,
  };
  const { user, client, connected } = setup({ instances: [environmentRow], availableProviders: catalogue });
  await choose(user, "Local endpoint");
  const storedRow: InstanceEntry = { ...environmentRow, activeSource: "store", hasStoredFile: true };
  client.on("evener/instance/list", () => ({
    instances: [structuredClone(storedRow)],
    availableProviders: catalogue,
  }));
  let probes = 0;
  client.on("evener/auth/test", ({ provider }) => {
    probes += 1;
    return { provider, status: probes === 1 ? "endpoint_failure" : "success", message: "" };
  });
  await user.click(screen.getByRole("button", { name: "Test connection" }));
  await user.click(await screen.findByRole("button", { name: "Use reviewed access and test" }));
  expect(await alertReason()).toBe("check-failed");
  await user.click(screen.getByRole("button", { name: "Test again" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("ollama"));
  expect(screen.queryByTestId("access-review")).toBeNull();
  expect(probes).toBe(2);
  expect(client.calls.filter((call) => call.method.includes("/set") || call.method.includes("/clear"))).toEqual([]);
});
test("failed refresh is not proof of fresh access and does not check stale metadata", async () => {
  const { user, client, connected } = setup();
  await choose(user);
  client.on("evener/auth/apiKey/set", () => {
    client.on("evener/instance/list", () => {
      throw new Error("refresh-secret");
    });
    return saved;
  });
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(client.calls.some((c) => c.method === "evener/auth/test")).toBe(false);
  scriptSave(client);
  client.on("evener/instance/list", () => savedList());
  await user.click(screen.getByRole("button", { name: "Test again" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
  expect(client.calls.filter((c) => c.method === "evener/auth/apiKey/set")).toHaveLength(1);
});
test("optional local access checks the host without clearing or requiring a key", async () => {
  const { user, client, connected } = setup();
  await choose(user, "Local endpoint");
  expect(screen.getByLabelText("API key")).toHaveProperty("required", false);
  expect(screen.getByText("http://localhost:8080/v1")).toBeTruthy();
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await user.click(screen.getByRole("button", { name: "Test connection" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("ollama"));
  expect(client.calls.filter((c) => c.method.includes("/set") || c.method.includes("/clear"))).toEqual([]);
});
test("ADC JSON is masked and sent to the JSON route, not API-key auth", async () => {
  const { user, client, connected } = setup();
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
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("google-vertex"));
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
  const row = azureTeamRow();
  const created = { instances: [row], availableProviders: catalogue };
  client.on("evener/instance/create", () => {
    client.on("evener/instance/list", () => structuredClone(created));
    return structuredClone(created);
  });
  client.on("evener/auth/apiKey/set", () => {
    throw new Error("write failed");
  });
  await enterName(user, "azure-team");
  await enterText(user, screen.getByLabelText("Endpoint URL"), "https://team.azure.example/v1");
  await enterText(user, screen.getByLabelText("API key"), "retained-draft");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  expect(await screen.findByRole("alert")).toBeTruthy();
  // The connection exists now and the card keeps its name and draft: the
  // primary action retries the key write against the created connection.
  expect(screen.getByRole("dialog", { name: "azure-team" })).toBeTruthy();
  expect(screen.queryByTestId("credential-saved")).toBeNull();
  client.on("evener/auth/apiKey/set", () => {
    client.on("evener/instance/list", () => ({
      ...created,
      instances: [{ ...row, activeSource: "store", hasStoredFile: true }],
    }));
    return { ...saved, provider: "azure-team" };
  });
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("azure-team"));
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
    await enterText(user, screen.getByLabelText("API key"), "old-draft");
    await user.click(screen.getByRole("button", { name: "Save and test" }));
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
    await enterText(user, screen.getByLabelText("API key"), "new-draft");
    expect(connected).not.toHaveBeenCalled();
    expect(screen.getByLabelText("API key")).toHaveProperty("value", "new-draft");
  },
);
test("cancelling closes the owner and a late check cannot complete", async () => {
  const { user, client, close, connected } = setup();
  scriptSave(client);
  const pending = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => pending.promise);
  await choose(user);
  await user.type(screen.getByLabelText("API key"), "draft");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await user.click(screen.getByRole("button", { name: /^(Cancel|Close, access stays saved)$/ }));
  expect(close).toHaveBeenCalledTimes(1);
  await act(async () => {
    pending.resolve({ provider: "anthropic", status: "success", message: "" });
    await pending.promise;
  });
  expect(connected).not.toHaveBeenCalled();
  expect(screen.queryByRole("dialog")).toBeNull();
});
test("a notification refresh of an unchanged target preserves a pending check", async () => {
  const { user, client, connected } = setup();
  scriptSave(client);
  const pending = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => pending.promise);
  await choose(user);
  await user.type(screen.getByLabelText("API key"), "draft");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  expect(screen.getByRole("button", { name: "Testing…" })).toHaveProperty("disabled", true);
  vi.useFakeTimers();
  await act(async () => {
    client.emitNotification({ method: "evener/auth/updated", params: {} });
    await vi.runOnlyPendingTimersAsync();
  });
  vi.useRealTimers();
  expect(reasonShown("changed")).toBe(false);
  await act(async () => {
    pending.resolve({ provider: "anthropic", status: "success", message: "" });
    await pending.promise;
  });
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(reasonShown("changed")).toBe(false);
});

// A check is evidence about the one target it named, not about the listing as a
// whole. Another provider's update schedules a real refetch, and the fresh rows
// replace every array even when this target did not move - that replacement
// must not retract the in-flight check or its confirmed result.
test("an unchanged target survives another provider's refresh during its check", async () => {
  const { user, client, connected } = setup(savedList("anthropic", { configRevision: "rev-1" }));
  const pending = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => pending.promise);
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Test again" }));
  expect(screen.getByRole("button", { name: "Testing…" })).toHaveProperty("disabled", true);

  const other: InstanceEntry = {
    ...catalogue[1]!.setup!,
    name: "openai-team",
    implicit: false,
    activeSource: "store",
  };
  const refreshed = savedList("anthropic", { configRevision: "rev-1" });
  refreshed.instances = [...refreshed.instances, other];
  vi.useFakeTimers();
  await act(async () => {
    client.on("evener/instance/list", () => structuredClone(refreshed));
    client.emitNotification({ method: "evener/auth/updated", params: { provider: "openai" } });
    await vi.runOnlyPendingTimersAsync();
  });
  vi.useRealTimers();
  expect(credentialsStore.getState().instances).toHaveLength(2);
  expect(reasonShown("changed")).toBe(false);

  await act(async () => {
    pending.resolve({ provider: "anthropic", status: "success", message: "" });
    await pending.promise;
  });
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
  expect(reasonShown("changed")).toBe(false);
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);
});

// The same refetch that is unrelated when the target stands still is a real
// change when the target's own access identity moves. The check's result was
// taken against the old row and must never confirm the new one.
test.each([
  ["configRevision", { configRevision: "rev-2" }],
  ["query-only endpoint fingerprint", { endpointFingerprint: "fp-moved" }],
])("a foreign target %s change retires a pending check and its late result", async (_label, change) => {
  const { user, client, connected } = setup(savedList("anthropic", { configRevision: "rev-1" }));
  const pending = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => pending.promise);
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Test again" }));
  expect(screen.getByRole("button", { name: "Testing…" })).toHaveProperty("disabled", true);

  vi.useFakeTimers();
  await act(async () => {
    client.on("evener/instance/list", () => savedList("anthropic", { configRevision: "rev-1", ...change }));
    client.emitNotification({ method: "evener/auth/updated", params: { provider: "openai" } });
    await vi.runOnlyPendingTimersAsync();
  });
  vi.useRealTimers();
  expect(await alertReason()).toBe("changed");

  await act(async () => {
    pending.resolve({ provider: "anthropic", status: "success", message: "" });
    await pending.promise;
  });
  expect(connected).not.toHaveBeenCalled();
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);
});

// A delayed acquisition refresh is self-marked, so it is not a "foreign"
// transition by the store's definition. When it carries a different target
// revision than the one the check anchored on, it is still stale evidence: the
// old check must not confirm the target that refresh installed.
test("a self-marked refresh that moves the checked target cannot confirm its old result", async () => {
  const { user, client, connected } = setup(savedList("anthropic", { configRevision: "rev-1" }));
  const pending = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => pending.promise);
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Test again" }));
  expect(screen.getByRole("button", { name: "Testing…" })).toHaveProperty("disabled", true);

  await act(async () => {
    client.on("evener/instance/list", () => savedList("anthropic", { configRevision: "rev-2" }));
    await credentialsStore.getState().fetchSelf();
  });
  expect(await alertReason()).toBe("changed");

  await act(async () => {
    pending.resolve({ provider: "anthropic", status: "success", message: "" });
    await pending.promise;
  });
  expect(connected).not.toHaveBeenCalled();
});
test("the originating client's own auth-update echo does not invalidate its save and check", async () => {
  const { user, client, connected } = setup();
  scriptSave(client);
  const pending = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => pending.promise);
  await choose(user);
  await user.type(screen.getByLabelText("API key"), "draft");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  expect(screen.getByRole("button", { name: "Testing…" })).toHaveProperty("disabled", true);
  vi.useFakeTimers();
  await act(async () => {
    // The hub BroadcastAlls the originator its own success echo (notifyAuthUpdated
    // sends {provider, activeSource} to every connected client, this one included),
    // so the real "Save and test" flow runs with its own echo in flight.
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
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
  expect(reasonShown("changed")).toBe(false);
});
test("the store's own post-save refresh does not prevent exactly-once completion", async () => {
  // The store schedules its own listing refresh the moment the save succeeds
  // (a 250ms debounce). The flow's awaited reconciliation consumes that queued
  // self refresh into its own read, so the timer cannot land later as an
  // unowned change that yanks the success away - and the flow completes
  // exactly once. The clock is fake before the save, so a timer this flow
  // failed to consume would fire inside the advance below.
  const { user, client, connected } = setup();
  scriptSave(client);
  await choose(user);
  await user.type(screen.getByLabelText("API key"), "draft");
  vi.stubGlobal("jest", { advanceTimersByTime: vi.advanceTimersByTime });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  // A plain click: `user` was set up on the real clock, and its delays would
  // wait on a timer nothing advances now.
  fireEvent.click(screen.getByRole("button", { name: "Save and test" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
  const listReads = () => client.calls.filter((c) => c.method === "evener/instance/list").length;
  const reads = listReads();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(300);
  });
  expect(listReads()).toBe(reads);
  expect(credentialsStore.getState().loading).toBe(false);
  expect(connected).toHaveBeenCalledTimes(1);
  expect(reasonShown("changed")).toBe(false);
});
test("the store's own debounced post-save refresh does not invalidate an in-flight check", async () => {
  // The original flake (issue #1435): the store's own debounced listing refetch
  // (250ms after the save) landed while the credential check was still in
  // flight; read as foreign, it reset the flow, so the check's answer never
  // reached Continue. The sibling test above covers the completed-check phase;
  // this one holds the check pending so the in-flight window is pinned.
  const { user, client, connected } = setup();
  scriptSave(client);
  const pending = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => pending.promise);
  await choose(user);
  await user.type(screen.getByLabelText("API key"), "draft");
  // A fake clock owns the store's 250ms debounce, so it cannot fire until this
  // test fires it - the check is guaranteed to still be in flight when its
  // answer lands.
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  fireEvent.click(screen.getByRole("button", { name: "Save and test" }));
  // Flush the save/refresh microtask chain to the pending check without
  // advancing the clock (0ms drains microtasks, fires no timer).
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(screen.getByRole("button", { name: "Testing…" })).toHaveProperty("disabled", true);
  // Fire the store's own debounced refresh while the check is pending; its
  // answer installs a fresh rows array, which reads as a listing move.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(300);
  });
  vi.useRealTimers();
  await act(async () => {
    pending.resolve({ provider: "anthropic", status: "success", message: "" });
    await pending.promise;
  });
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
  expect(reasonShown("changed")).toBe(false);
});
test("a slow save response extends the echo window and still completes exactly once", async () => {
  const { user, client, connected } = setup();
  const save = deferred<typeof saved>();
  client.on("evener/auth/apiKey/set", () => save.promise);
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await choose(user);
  await user.type(screen.getByLabelText("API key"), "draft");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
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
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
  expect(reasonShown("changed")).toBe(false);
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
  await user.click(screen.getByRole("button", { name: "Save and test" }));
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
  expect(await screen.findByRole("button", { name: "Use reviewed access and test" })).toBeTruthy();
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
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await act(async () => {
    client.emitStateChange("reconnecting");
    pending.resolve({ provider: "anthropic", status: "success", message: "" });
    await pending.promise;
    client.emitReady();
  });
  expect(connected).not.toHaveBeenCalled();
  // The reconnect invalidated the late check; the saved draft is still the
  // replacement the row would submit.
  await user.click(screen.getByRole("button", { name: "Replace" }));
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "draft");
});
// hidden is the hub's "this provider's base URL variable is unset" flag, not a
// host-configured connection: fresh hosts list Azure, Vertex and compatible
// endpoints this way, and the card has to let the user supply the destination.
test("a hidden setup row is configurable instead of read-only", async () => {
  const hidden = provider("azure", "Azure", ["apiKey"], { hidden: true, baseUrl: "" });
  const { user, client } = setup({ instances: [], availableProviders: [hidden] });
  await choose(user, "Azure");

  expect(screen.getByRole("dialog", { name: "New connection" })).toBeTruthy();
  expect(screen.queryByText(/read-only/i)).toBeNull();
  const endpoint = screen.getByLabelText("Endpoint URL");
  await enterText(user, endpoint, "https://team.azure.example/v1");

  const created: InstanceEntry = {
    ...hidden.setup!,
    base: "azure",
    auth: "api-key",
    implicit: false,
    hidden: false,
    baseUrl: "https://team.azure.example/v1",
    endpointFingerprint: "fp-azure",
  };
  const listing: InstanceListResponse = { instances: [created], availableProviders: [hidden] };
  scriptCreate(client, listing);
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));

  await user.click(screen.getByRole("button", { name: "Save and test" }));

  await waitFor(() => expect(client.calls.some((call) => call.method === "evener/instance/create")).toBe(true));
  expect(client.calls.find((call) => call.method === "evener/instance/create")?.params).toMatchObject({
    name: "azure",
    base: "azure",
    baseUrl: "https://team.azure.example/v1",
  });
});

// The other hidden shape: an authored instance whose destination no longer
// resolves. It is already listed, so the card edits it in place rather than
// entering add mode and asking the hub to create the same name again.
test("a listed hidden instance is edited instead of created again", async () => {
  const hidden = savedList("anthropic", {
    hidden: true,
    baseUrl: "",
    endpointFingerprint: undefined,
  }).instances[0]!;
  const { user, client } = setup({ instances: [hidden], availableProviders: catalogue });
  await choose(user);
  expect(screen.queryByRole("dialog", { name: "New connection" })).toBeNull();

  await user.click(screen.getByRole("button", { name: "Edit" }));
  await enterText(user, screen.getByLabelText("Endpoint URL"), "https://restored.example/v1");

  const restored: InstanceEntry = {
    ...hidden,
    hidden: false,
    baseUrl: "https://restored.example/v1",
    endpointFingerprint: "fp-restored",
  };
  const listing: InstanceListResponse = { instances: [restored], availableProviders: structuredClone(catalogue) };
  client.on("evener/instance/edit", () => {
    client.on("evener/instance/list", () => structuredClone(listing));
    return structuredClone(listing);
  });

  await user.click(screen.getByRole("button", { name: "Save" }));

  await waitFor(() => expect(editCalls(client)).toHaveLength(1));
  expect(createCalls(client)).toHaveLength(0);
  expect(await screen.findByText("https://restored.example/v1")).toBeTruthy();
});

// writesRefused is the hub saying its instance mutators would be rejected -
// provider settings are read-only - while credential store writes still work.
test("a host that refuses provider settings shows them read-only", async () => {
  const setupRow = provider("anthropic", "Anthropic", ["apiKey"]);
  const { user, client } = setup({ instances: [], availableProviders: [setupRow], writesRefused: true });
  await choose(user);

  expect(document.querySelector('[data-state="writes-refused"]')).not.toBeNull();
  // The key row remains usable; the instance editors do not.
  expect(screen.getByLabelText("API key")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Edit" })).toHaveProperty("disabled", true);
  expect(screen.getByRole("button", { name: "Save and test" })).toHaveProperty("disabled", false);
  expect(client.calls.some((c) => c.method === "evener/auth/test")).toBe(false);
});

// findSetup also returns discovery-only setup rows: providers the catalogue
// lists but no instance of, because no credential or authored configuration
// exists yet. Editing one as an instance is rejected by the hub, so the card
// authors the instance with the editor's fields instead.
test("a discovery-only row saves its endpoint by authoring the instance", async () => {
  const setupRow = provider("anthropic", "Anthropic", ["apiKey"]);
  const { user, client } = setup({ instances: [], availableProviders: [setupRow] });
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Edit" }));
  const field = screen.getByLabelText("Endpoint URL");
  await user.clear(field);
  await enterText(user, field, "https://proxy.example/v1");

  const created: InstanceEntry = {
    ...setupRow.setup!,
    name: "anthropic",
    implicit: false,
    baseUrl: "https://proxy.example/v1",
    endpointFingerprint: "fp-proxy",
  };
  const listing: InstanceListResponse = { instances: [created], availableProviders: [setupRow] };
  scriptCreate(client, listing);

  await user.click(screen.getByRole("button", { name: "Save" }));

  await waitFor(() => expect(client.calls.some((call) => call.method === "evener/instance/create")).toBe(true));
  expect(client.calls.find((call) => call.method === "evener/instance/create")?.params).toMatchObject({
    name: "anthropic",
    base: "anthropic",
    baseUrl: "https://proxy.example/v1",
  });
  expect(editCalls(client)).toHaveLength(0);
  expect(await screen.findByText("https://proxy.example/v1")).toBeTruthy();
});

test("a discovery-only row saves its configuration by authoring the instance", async () => {
  const setupRow = provider("anthropic", "Anthropic", ["apiKey"]);
  const { user, client } = setup({ instances: [], availableProviders: [setupRow] });
  await choose(user);
  await user.click(screen.getByText("All connection settings"));
  await enterText(user, screen.getByLabelText("API key env"), "TEAM_KEY");

  const created: InstanceEntry = { ...setupRow.setup!, name: "anthropic", implicit: false, apiKeyEnv: "TEAM_KEY" };
  const listing: InstanceListResponse = { instances: [created], availableProviders: [setupRow] };
  scriptCreate(client, listing);

  await user.click(screen.getByRole("button", { name: "Save settings" }));

  await waitFor(() => expect(client.calls.some((call) => call.method === "evener/instance/create")).toBe(true));
  expect(client.calls.find((call) => call.method === "evener/instance/create")?.params).toMatchObject({
    name: "anthropic",
    base: "anthropic",
    apiKeyEnv: "TEAM_KEY",
  });
  expect(editCalls(client)).toHaveLength(0);
});

// A write whose confirming read fails is not confirmed, so the card keeps the
// editor and its draft and reports the failure instead of closing optimistically.
// While the confirmation is owed, no mutation control is offered: the only
// action is a read-only reload.
test("an unconfirmed editor create keeps the draft and reports the failure", async () => {
  const { user, client } = setup();
  await unconfirmedEndpointCreate(user, client, "https://proxy.example/v1");

  expect(screen.getByLabelText("Endpoint URL")).toHaveProperty("value", "https://proxy.example/v1");
  expect(screen.getByRole("button", { name: "Reload connection" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Save" })).toHaveProperty("disabled", true);
  expect(screen.queryByRole("button", { name: "Save and test" })).toBeNull();
  expect(createCalls(client)).toHaveLength(1);
  expect(editCalls(client)).toHaveLength(0);
});

// The gated confirmation replaces the receipt: an unconfirmed create may not
// issue a second create, and a read-only reload settles it against the row the
// write authored.
test("an unconfirmed create offers only Reload connection, never a second create", async () => {
  const { user, client } = setup();
  const { listing } = await unconfirmedEndpointCreate(user, client, "https://proxy.example/v1");
  expect(createCalls(client)).toHaveLength(1);
  expect(screen.getByRole("button", { name: "Save" })).toHaveProperty("disabled", true);
  expect(screen.queryByRole("button", { name: "Save and test" })).toBeNull();

  // Enter in the field cannot start a second create while the confirmation is owed.
  await user.type(screen.getByLabelText("Endpoint URL"), "{Enter}");
  expect(createCalls(client)).toHaveLength(1);

  scriptListing(client, listing);
  await user.click(screen.getByRole("button", { name: "Reload connection" }));

  await waitFor(() => expect(screen.queryByLabelText("Endpoint URL")).toBeNull());
  expect(await screen.findByText("https://proxy.example/v1")).toBeTruthy();
  expect(createCalls(client)).toHaveLength(1);
  expect(editCalls(client)).toHaveLength(0);
});

// The same gate covers a settings write: Save settings is disabled and Enter in
// a settings field cannot issue a second edit until a listing read lands.
test("an unconfirmed settings write cannot issue a second edit before a read lands", async () => {
  const { user, client } = setup(savedList());
  await choose(user);
  await user.click(screen.getByText("All connection settings"));
  await enterText(user, screen.getByLabelText("API key env"), "TEAM_KEY");

  const authored: InstanceEntry = { ...savedList().instances[0]!, apiKeyEnv: "TEAM_KEY" };
  const authoredListing: InstanceListResponse = {
    instances: [authored],
    availableProviders: structuredClone(catalogue),
  };
  await unconfirmedEdit(
    client,
    async () => {
      await user.click(screen.getByRole("button", { name: "Save settings" }));
    },
    authoredListing,
  );

  expect(screen.getByRole("button", { name: "Save settings" })).toHaveProperty("disabled", true);
  await user.type(screen.getByLabelText("API key env"), "{Enter}");
  expect(editCalls(client)).toHaveLength(1);

  // The reload's read confirms the authored row; the draft matches it, so the
  // editor settles and Save is available again on the confirmed row.
  scriptListing(client, authoredListing);
  await user.click(screen.getByRole("button", { name: "Reload connection" }));
  await waitFor(() => expect(screen.queryByLabelText("API key env")).toBeNull());
  expect(editCalls(client)).toHaveLength(1);
});

// A field changed after the failed confirmation is kept, and the reload's
// confirmed row becomes the baseline for the next save: a same-destination
// settings change stays an edit of that row.
test("a changed settings field is saved after the reload confirms the row", async () => {
  const { user, client } = setup(savedList());
  await choose(user);
  await user.click(screen.getByText("All connection settings"));
  await enterText(user, screen.getByLabelText("API key env"), "TEAM_KEY");

  const authored: InstanceEntry = { ...savedList().instances[0]!, apiKeyEnv: "TEAM_KEY" };
  const authoredListing: InstanceListResponse = {
    instances: [authored],
    availableProviders: structuredClone(catalogue),
  };
  await unconfirmedEdit(
    client,
    async () => {
      await user.click(screen.getByRole("button", { name: "Save settings" }));
    },
    authoredListing,
  );

  const field = screen.getByLabelText("API key env");
  await user.clear(field);
  await enterText(user, field, "OTHER_KEY");
  scriptListing(client, authoredListing);
  await user.click(screen.getByRole("button", { name: "Reload connection" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Save settings" })).toHaveProperty("disabled", false));

  const second: InstanceEntry = { ...authored, apiKeyEnv: "OTHER_KEY" };
  const secondListing: InstanceListResponse = { instances: [second], availableProviders: structuredClone(catalogue) };
  scriptListing(client, secondListing);
  scriptEdit(client, secondListing);
  await user.click(screen.getByRole("button", { name: "Save settings" }));

  await waitFor(() => expect(editCalls(client)).toHaveLength(2));
  expect(editCalls(client)[1]!.params).toMatchObject({
    name: "anthropic",
    apiKeyEnv: "OTHER_KEY",
    expectedEndpointFingerprint: "fp-anthropic",
  });
});

// A reload that lands the authored row settles the editor even when a previous
// read (the store's own refresh) already installed that row.
test("a reload settles an unconfirmed create whose listing already landed", async () => {
  const { user, client } = setup();
  const { listing } = await unconfirmedEndpointCreate(user, client, "https://proxy.example/v1");

  scriptListing(client, listing);
  await act(async () => {
    await credentialsStore.getState().fetch();
  });
  expect(screen.getByRole("button", { name: "Reload connection" })).toBeTruthy();

  await user.click(screen.getByRole("button", { name: "Reload connection" }));
  await waitFor(() => expect(screen.queryByLabelText("Endpoint URL")).toBeNull());
  expect(createCalls(client)).toHaveLength(1);
  expect(editCalls(client)).toHaveLength(0);
});

// A destination that moved from the editor's opening claim and does not carry
// the draft runs the same review as a save. The confirmed row becomes the
// authority, so reopening the editor saves against it rather than creating.
test("a reload reviews a moved destination, then the changed settings save against the row", async () => {
  const setupRow = provider("anthropic", "Anthropic", ["apiKey"]);
  const { user, client } = setup({ instances: [], availableProviders: [setupRow] });
  await choose(user);
  await user.click(screen.getByText("All connection settings"));
  await enterText(user, screen.getByLabelText("API key env"), "TEAM_KEY");

  const created: InstanceEntry = {
    ...setupRow.setup!,
    name: "anthropic",
    implicit: false,
    endpointFingerprint: "fp-settings",
    apiKeyEnv: "TEAM_KEY",
  };
  const listing: InstanceListResponse = { instances: [created], availableProviders: [setupRow] };
  await unconfirmedCreate(
    client,
    async () => {
      await user.click(screen.getByRole("button", { name: "Save settings" }));
    },
    listing,
  );

  // Clear the field, so the confirmed row does not carry the current draft and
  // its destination moved from the setup row the editor opened on.
  await user.clear(screen.getByLabelText("API key env"));
  scriptListing(client, listing);
  await user.click(screen.getByRole("button", { name: "Reload connection" }));
  await waitFor(() => expect(reasonShown("endpoint-changed")).toBe(true));
  await waitFor(() => expect(screen.queryByLabelText("API key env")).toBeNull());

  // Reopening starts from the confirmed instance row; the clear is saved as an
  // edit against that row, not as a second create.
  await user.click(screen.getByText("All connection settings"));
  await user.clear(screen.getByLabelText("API key env"));
  const cleared: InstanceEntry = { ...created, apiKeyEnv: undefined };
  const clearedListing: InstanceListResponse = { instances: [cleared], availableProviders: [setupRow] };
  scriptListing(client, clearedListing);
  scriptEdit(client, clearedListing);
  await user.click(screen.getByRole("button", { name: "Save settings" }));

  await waitFor(() => expect(editCalls(client)).toHaveLength(1));
  const params = editCalls(client)[0]!.params as Record<string, unknown>;
  expect(params).toMatchObject({ name: "anthropic", clearApiKeyEnv: true });
  expect(params.apiKeyEnv).toBeUndefined();
  expect(editCalls(client)[0]!.params).toMatchObject({ expectedEndpointFingerprint: "fp-settings" });
  expect(createCalls(client)).toHaveLength(1);
});

// The seed the editor opened with is not the row the write authored. The reload
// reviews the moved destination, and the reopened editor edits the confirmed
// row back to the seed.
test("a reload reviews the moved endpoint, then the seed edits the confirmed row", async () => {
  const { user, client } = setup();
  const { listing } = await unconfirmedEndpointCreate(user, client, "https://proxy.example/v1");

  const field = screen.getByLabelText("Endpoint URL");
  await user.clear(field);
  await enterText(user, field, "https://anthropic.example/v1");
  scriptListing(client, listing);
  await user.click(screen.getByRole("button", { name: "Reload connection" }));
  await waitFor(() => expect(reasonShown("endpoint-changed")).toBe(true));
  await waitFor(() => expect(screen.queryByLabelText("Endpoint URL")).toBeNull());

  await user.click(screen.getByRole("button", { name: "Edit" }));
  const reopened = screen.getByLabelText("Endpoint URL");
  await user.clear(reopened);
  await enterText(user, reopened, "https://anthropic.example/v1");
  const seed: InstanceEntry = {
    ...listing.instances[0]!,
    baseUrl: "https://anthropic.example/v1",
    endpointFingerprint: "fp-anthropic",
  };
  const seedListing: InstanceListResponse = { instances: [seed], availableProviders: structuredClone(catalogue) };
  scriptListing(client, seedListing);
  scriptEdit(client, seedListing);
  await user.click(screen.getByRole("button", { name: "Save" }));

  await waitFor(() => expect(editCalls(client)).toHaveLength(1));
  expect(editCalls(client)[0]!.params).toMatchObject({
    name: "anthropic",
    baseUrl: "https://anthropic.example/v1",
    expectedEndpointFingerprint: "fp-proxy",
  });
  expect(createCalls(client)).toHaveLength(1);
});

// The sanitized display value is what the reload can compare: without the
// deleted raw submitted endpoint, a draft equal to the confirmed display is
// treated as the authored write and the editor settles. An explicit removal of
// a query the listing hides is not re-applied on this path.
test("a sanitized endpoint draft settles on the confirmed display after a lost write", async () => {
  const { user, client } = setup();
  const { listing } = await unconfirmedEndpointCreate(
    user,
    client,
    "https://host.example/v1?tenant=a",
    "https://host.example/v1",
    "fp-query",
  );

  const field = screen.getByLabelText("Endpoint URL");
  await user.clear(field);
  await enterText(user, field, "https://host.example/v1");
  scriptListing(client, listing);

  await user.click(screen.getByRole("button", { name: "Reload connection" }));
  await waitFor(() => expect(screen.queryByLabelText("Endpoint URL")).toBeNull());
  expect(createCalls(client)).toHaveLength(1);
  expect(editCalls(client)).toHaveLength(0);
});

// A failed follow-up write after a reload keeps the confirmed row's claim, so
// the retry edits that row instead of reading it as an external move and
// discarding the newer draft.
test("a failed follow-up after a reload keeps the confirmed claim", async () => {
  const { user, client } = setup();
  const { listing } = await unconfirmedEndpointCreate(user, client, "https://proxy.example/v1");

  const field = screen.getByLabelText("Endpoint URL");
  await user.clear(field);
  await enterText(user, field, "https://second.example/v1");
  scriptListing(client, listing);
  await user.click(screen.getByRole("button", { name: "Reload connection" }));
  await waitFor(() => expect(reasonShown("endpoint-changed")).toBe(true));
  await waitFor(() => expect(screen.queryByLabelText("Endpoint URL")).toBeNull());

  await user.click(screen.getByRole("button", { name: "Edit" }));
  const reopened = screen.getByLabelText("Endpoint URL");
  await user.clear(reopened);
  await enterText(user, reopened, "https://second.example/v1");
  client.on("evener/instance/edit", () => {
    throw new Error("write refused");
  });
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(editCalls(client)).toHaveLength(1));
  expect(await alertReason()).toBe("save-failed");

  const second: InstanceEntry = {
    ...listing.instances[0]!,
    baseUrl: "https://second.example/v1",
    endpointFingerprint: "fp-second",
  };
  const secondListing: InstanceListResponse = { instances: [second], availableProviders: structuredClone(catalogue) };
  scriptListing(client, secondListing);
  scriptEdit(client, secondListing);
  await user.click(screen.getByRole("button", { name: "Save" }));

  await waitFor(() => expect(editCalls(client)).toHaveLength(2));
  expect(editCalls(client)[1]!.params).toMatchObject({
    name: "anthropic",
    baseUrl: "https://second.example/v1",
    expectedEndpointFingerprint: "fp-proxy",
  });
  expect(createCalls(client)).toHaveLength(1);
});

// A settings edit whose confirming read failed is re-anchored on the confirmed
// row by the reload; the next save clears the field the write set.
test("a settings reload re-anchors the clear on the confirmed row", async () => {
  const { user, client } = setup(savedList());
  await choose(user);
  await user.click(screen.getByText("All connection settings"));
  await enterText(user, screen.getByLabelText("API key env"), "TEAM_KEY");

  const authored: InstanceEntry = { ...savedList().instances[0]!, apiKeyEnv: "TEAM_KEY" };
  const authoredListing: InstanceListResponse = {
    instances: [authored],
    availableProviders: structuredClone(catalogue),
  };
  await unconfirmedEdit(
    client,
    async () => {
      await user.click(screen.getByRole("button", { name: "Save settings" }));
    },
    authoredListing,
  );

  await user.clear(screen.getByLabelText("API key env"));
  scriptListing(client, authoredListing);
  await user.click(screen.getByRole("button", { name: "Reload connection" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Save settings" })).toHaveProperty("disabled", false));

  const cleared: InstanceEntry = { ...authored, apiKeyEnv: undefined };
  const clearedListing: InstanceListResponse = { instances: [cleared], availableProviders: structuredClone(catalogue) };
  scriptListing(client, clearedListing);
  scriptEdit(client, clearedListing);
  await user.click(screen.getByRole("button", { name: "Save settings" }));

  await waitFor(() => expect(editCalls(client)).toHaveLength(2));
  const params = editCalls(client)[1]!.params as Record<string, unknown>;
  expect(params).toMatchObject({ name: "anthropic", clearApiKeyEnv: true });
  expect(params.expectedEndpointFingerprint).toBe("fp-anthropic");
});

// Matching settings are not a destination proof. A hidden endpoint change is
// still a moved destination and must go through the review.
test("a query-only endpoint change is reviewed even when the settings draft matches", async () => {
  const { user, client } = setup(savedList());
  await choose(user);
  await user.click(screen.getByText("All connection settings"));
  await enterText(user, screen.getByLabelText("API key env"), "TEAM_KEY");
  const authored: InstanceEntry = { ...savedList().instances[0]!, apiKeyEnv: "TEAM_KEY" };
  const authoredListing: InstanceListResponse = {
    instances: [authored],
    availableProviders: structuredClone(catalogue),
  };
  await unconfirmedEdit(
    client,
    async () => {
      await user.click(screen.getByRole("button", { name: "Save settings" }));
    },
    authoredListing,
  );

  const moved: InstanceEntry = { ...authored, endpointFingerprint: "fp-hidden" };
  scriptListing(client, { instances: [moved], availableProviders: structuredClone(catalogue) });
  await user.click(screen.getByRole("button", { name: "Reload connection" }));

  await waitFor(() => expect(reasonShown("endpoint-changed")).toBe(true));
  expect(screen.queryByLabelText("API key env")).toBeNull();
});

// A variable changed after the failed confirmation is an unsaved draft; the
// reload must keep it in the editor rather than closing on the scalar match.
test("a variable changed while confirmation is owed keeps its draft", async () => {
  const list = savedList("google-vertex", { vars: { project: "project-a" } });
  const savedRow: InstanceEntry = list.instances[0]!;
  const { user, client } = setup(list);
  await choose(user, "Google Vertex");
  await user.click(screen.getByText("All connection settings"));
  await user.clear(screen.getByLabelText("project"));
  await enterText(user, screen.getByLabelText("project"), "project-b");
  const authored: InstanceEntry = { ...savedRow, vars: { project: "project-b" } };
  const authoredListing: InstanceListResponse = { instances: [authored], availableProviders: list.availableProviders };
  await unconfirmedEdit(
    client,
    async () => {
      await user.click(screen.getByRole("button", { name: "Save settings" }));
    },
    authoredListing,
  );

  const field = screen.getByLabelText("project");
  await user.clear(field);
  await enterText(user, field, "project-c");
  scriptListing(client, authoredListing);
  await user.click(screen.getByRole("button", { name: "Reload connection" }));

  await waitFor(() => expect(screen.getByLabelText("project")).toHaveProperty("value", "project-c"));
});

// A variable changed by another client is not this editor's draft. Saving an
// unrelated field must leave it alone, not restore the stale opening value.
test("a concurrent variable update is not overwritten by another settings save", async () => {
  const list = savedList("google-vertex", { vars: { project: "project-a" } });
  const savedRow: InstanceEntry = list.instances[0]!;
  const { user, client } = setup(list);
  await choose(user, "Google Vertex");
  await user.click(screen.getByText("All connection settings"));
  const external: InstanceEntry = { ...savedRow, vars: { project: "project-external" } };
  scriptListing(client, { instances: [external], availableProviders: list.availableProviders });
  await act(async () => {
    await credentialsStore.getState().fetch();
  });
  await enterText(user, screen.getByLabelText("API key env"), "TEAM_KEY");

  const edited: InstanceEntry = { ...external, apiKeyEnv: "TEAM_KEY" };
  const editedListing: InstanceListResponse = { instances: [edited], availableProviders: list.availableProviders };
  scriptListing(client, editedListing);
  scriptEdit(client, editedListing);
  await user.click(screen.getByRole("button", { name: "Save settings" }));

  await waitFor(() => expect(editCalls(client)).toHaveLength(1));
  const params = editCalls(client)[0]!.params as Record<string, unknown>;
  expect(params.apiKeyEnv).toBe("TEAM_KEY");
  expect(params.vars).toBeUndefined();
});

// Whitespace is not a value: the hub trims it to empty, so the card must trim
// before computing values and clear flags or the existing override survives.
test("a whitespace-only API key env clears the override", async () => {
  const list = savedList("anthropic", { apiKeyEnv: "TEAM_KEY" });
  const { user, client } = setup(list);
  await choose(user);
  await user.click(screen.getByText("All connection settings"));
  const field = screen.getByLabelText("API key env");
  await user.clear(field);
  await user.type(field, "   ");

  const cleared: InstanceEntry = { ...list.instances[0]!, apiKeyEnv: undefined };
  const clearedListing: InstanceListResponse = { instances: [cleared], availableProviders: list.availableProviders };
  scriptListing(client, clearedListing);
  scriptEdit(client, clearedListing);
  await user.click(screen.getByRole("button", { name: "Save settings" }));

  await waitFor(() => expect(editCalls(client)).toHaveLength(1));
  const params = editCalls(client)[0]!.params as Record<string, unknown>;
  expect(params).toMatchObject({ name: "anthropic", clearApiKeyEnv: true });
  expect(params.apiKeyEnv).toBeUndefined();
});

test("read-only provider settings do not disable an existing credential store write", async () => {
  const { user, client, connected } = setup({ instances: [], availableProviders: catalogue, writesRefused: true });
  scriptSave(client);
  await choose(user);
  await user.type(screen.getByLabelText("API key"), "draft");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
});
test("switching to existing host access does not send the new key or claim authentication is disabled", async () => {
  const { user, client, connected } = setup(
    savedList("anthropic", { activeSource: "env:ANTHROPIC_API_KEY", hasStoredFile: false }),
  );
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "unused-draft");
  await openHostAccess(user);
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await user.click(screen.getByRole("button", { name: "Test connection" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
  expect(client.calls.some((c) => c.method === "evener/auth/apiKey/set" || c.method === "evener/auth/logout")).toBe(
    false,
  );
});

test("a new connection card does not inherit the previous card's host-access choice", async () => {
  const { user, client, connected } = setup(savedList("anthropic"));
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Replace" }));
  await openHostAccess(user);
  await user.click(screen.getByRole("button", { name: "Change provider" }));
  await choose(user, "Azure");
  await enterName(user, "openai-team");
  await enterText(user, screen.getByLabelText("Endpoint URL"), "https://team.openai.example/v1");
  const row = {
    ...catalogue[1]!.setup!,
    name: "openai-team",
    implicit: false,
    baseUrl: "https://team.openai.example/v1",
  };
  client.on("evener/instance/create", () => {
    client.on("evener/instance/list", () => ({ instances: [row], availableProviders: structuredClone(catalogue) }));
    return { instances: [row], availableProviders: structuredClone(catalogue) };
  });
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("openai-team"));
  // openai-team is a different connection with no host-access choice of its
  // own; inheriting anthropic's would make the card claim stored access it
  // never wrote and skip its credential submission.
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
});

test("review regression: a discarded cross-provider create reloads without the old credential draft", async () => {
  const { user, client } = setup();
  await choose(user, "Anthropic");
  await enterText(user, screen.getByLabelText("API key"), "anthropic-private-draft");
  await user.click(screen.getByRole("button", { name: "Change provider" }));
  await choose(user, "Azure");
  // The card that will create the other provider starts empty: the draft typed
  // for anthropic is not carried across.
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "");
  await enterName(user, "openai-team");
  await enterText(user, screen.getByLabelText("Endpoint URL"), "https://team.openai.example/v1");
  const pending = deferred<InstanceListResponse>();
  const row = { ...catalogue[1]!.setup!, name: "openai-team", implicit: false, baseUrl: "" };
  const listing: InstanceListResponse = { instances: [row], availableProviders: structuredClone(catalogue) };
  client.on("evener/instance/create", () => pending.promise);
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await act(async () => {
    await credentialsStore.getState().fetch();
    client.on("evener/instance/list", () => structuredClone(listing));
    pending.resolve(structuredClone(listing));
    await pending.promise;
  });
  expect(await screen.findByRole("button", { name: "Reload" })).toBeTruthy();
  client.on("evener/instance/list", () => ({
    instances: [{ ...row, baseUrl: "https://team.openai.example/v1" }],
    availableProviders: structuredClone(catalogue),
  }));
  await user.click(screen.getByRole("button", { name: "Reload" }));
  expect(await screen.findByRole("dialog", { name: "openai-team" })).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Add key" }));
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "");
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
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
  await enterName(user, "retained-name");
  await enterText(user, screen.getByLabelText("Endpoint URL"), "https://team.example/v1");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  // The create's own listing loses the race with the concurrent read, and the
  // confirming read lands a row with no resolved destination, so the flow
  // reaches its not-ready reload recovery rather than a usable connection.
  // A superseded create also schedules the store's debounced refetch. Run it
  // here, before the scripted reads below: left on the real clock it lands
  // whenever the test crosses the debounce, and if it lands inside the reload
  // step it takes that step's scripted response and holds the store's loading
  // flag, so the Reload click hits a disabled button and does nothing.
  const notReadyRow: InstanceEntry = {
    ...catalogue[0]!.setup!,
    name: "retained-name",
    implicit: false,
    baseUrl: "",
  };
  const notReadyList: InstanceListResponse = {
    instances: [notReadyRow],
    availableProviders: structuredClone(catalogue),
  };
  vi.useFakeTimers();
  await act(async () => {
    await credentialsStore.getState().fetch();
    client.on("evener/instance/list", () => structuredClone(notReadyList));
    pending.resolve(structuredClone(notReadyList));
    await pending.promise;
    await vi.runOnlyPendingTimersAsync();
  });
  vi.useRealTimers();
  expect(await screen.findByRole("button", { name: "Reload" })).toBeTruthy();
  // The older connection's listing still carries a row under this name. It is
  // hidden, so the flow stays in its recovery state rather than presenting it.
  const staleRow: InstanceEntry = {
    ...catalogue[0]!.setup!,
    name: "retained-name",
    implicit: false,
    hidden: true,
    baseUrl: "",
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
  const reload = screen.getByRole("button", { name: "Reload" });
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

  expect(await alertReason()).toBe("reload-failed");
  expect(screen.getByRole("button", { name: "Reload" })).toBeTruthy();
  expect(screen.queryByLabelText("API key")).toBeNull();
});

// A create whose own answer was superseded still carries the row it authored
// under the hub's write lock. The typed draft may be written only when the
// reconciled row proves that same destination; a different or missing
// fingerprint keeps the draft unwritten behind the key row.
test.each([
  { label: "a same-fingerprint", rowFp: "fp-authored", writes: true },
  { label: "a different-fingerprint", rowFp: "fp-other", writes: false },
  { label: "a missing-fingerprint", rowFp: undefined, writes: false },
])(
  "review regression: a superseded create writes its draft only to the authored destination after $label reconciliation",
  async ({ rowFp, writes }) => {
    const baselineList: InstanceListResponse = { instances: [], availableProviders: catalogue };
    const { user, client } = setup(baselineList);
    await startRecoveredCreate(user);
    const pending = deferred<InstanceListResponse>();
    const { authored, reconciled } = supersededCreateFixtures(rowFp);
    client.on("evener/instance/create", () => pending.promise);
    client.on("evener/auth/apiKey/set", () => ({ ...saved, provider: "recovered-team" }));
    client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    await user.click(screen.getByRole("button", { name: "Save and test" }));
    // The create's own response loses the race with the concurrent read, but
    // the reconciling read sees the authored row, so the flow adopts it with
    // the row's own destination rather than staying unresolved.
    await act(async () => {
      await credentialsStore.getState().fetch();
      client.on("evener/instance/list", () => structuredClone(reconciled));
      pending.resolve(structuredClone(authored));
      await pending.promise;
    });
    expect(await screen.findByRole("dialog", { name: "recovered-team" })).toBeTruthy();
    const sets = client.calls.filter((call) => call.method === "evener/auth/apiKey/set");
    expect(sets).toHaveLength(writes ? 1 : 0);
    if (writes) {
      expect(sets[0]!.params).toMatchObject({
        provider: "recovered-team",
        value: "add-card-key",
        expectedEndpointFingerprint: "fp-authored",
      });
    }
    expect(client.calls.some((call) => call.method === "evener/auth/test")).toBe(false);
    expect(
      client.calls.some(
        (call) =>
          call.method === "evener/auth/apiKey/set" &&
          (call.params as { value?: string } | undefined)?.value === "old-draft",
      ),
    ).toBe(false);
    expect(createCalls(client)).toHaveLength(1);
    if (!writes) {
      // The draft is retained behind the key row rather than discarded.
      await user.click(screen.getByRole("button", { name: "Add key" }));
      expect(screen.getByLabelText("API key")).toHaveProperty("value", "add-card-key");
    }
  },
);

// A create whose answer was superseded leaves its draft unclaimed when the
// reconciled row is a different destination. Replacing that whole value in one
// input event must still claim the row on screen, or the retype can never save.
test("a retained unclaimed draft can be replaced and saved against the row on screen", async () => {
  const baselineList: InstanceListResponse = { instances: [], availableProviders: catalogue };
  const { user, client } = setup(baselineList);
  await startRecoveredCreate(user);

  const pending = deferred<InstanceListResponse>();
  const { authored, reconciled } = supersededCreateFixtures("fp-other");
  client.on("evener/instance/create", () => pending.promise);
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await act(async () => {
    await credentialsStore.getState().fetch();
    client.on("evener/instance/list", () => structuredClone(reconciled));
    pending.resolve(structuredClone(authored));
    await pending.promise;
  });
  expect(await screen.findByRole("dialog", { name: "recovered-team" })).toBeTruthy();
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);

  await user.click(screen.getByRole("button", { name: "Add key" }));
  const field = screen.getByLabelText("API key");
  expect(field).toHaveProperty("value", "add-card-key");
  fireEvent.change(field, { target: { value: "retyped-key" } });
  scriptSave(client, reconciled);
  await user.click(screen.getByRole("button", { name: "Save" }));

  await waitFor(() => expect(client.calls.some((call) => call.method === "evener/auth/apiKey/set")).toBe(true));
  expect(client.calls.find((call) => call.method === "evener/auth/apiKey/set")?.params).toMatchObject({
    provider: "recovered-team",
    value: "retyped-key",
    expectedEndpointFingerprint: "fp-other",
  });
});

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
  await enterText(user, screen.getByLabelText("API key"), "sk-ant-draft");
  // The displayed URL does not move; the endpoint this name resolves to does.
  // The saved listing reports the source the save produced, so the review can
  // only come from the endpoint identity.
  scriptSave(client, fingerprintList("fp-2025", "store"));

  await user.click(screen.getByRole("button", { name: "Save and test" }));

  const review = await screen.findByTestId("access-review");
  // Both sanitized copies are the same text, so this pins that the review came
  // from the endpoint identity the fingerprint carries and not from what the
  // user reads.
  const destinations = [...review.querySelectorAll("[data-destination]")].map((line) => [
    line.getAttribute("data-destination"),
    line.textContent?.includes("https://anthropic.example/v1"),
  ]);
  expect(destinations).toEqual([
    ["previous", true],
    ["current", true],
  ]);
});

test("a credential draft is not saved to a destination that changed since it was typed", async () => {
  const { user, client } = setup(fingerprintList("fp-2024"));
  await choose(user, "Anthropic");
  await enterText(user, screen.getByLabelText("API key"), "sk-ant-draft");
  // Another client points the name at a different endpoint while the flow sits
  // idle with the draft: the draft was typed against the old destination, and
  // a credential only ever goes to an endpoint the user was shown.
  client.on("evener/instance/list", () => structuredClone(fingerprintList("fp-2025")));
  await act(async () => {
    await credentialsStore.getState().fetch();
  });

  await user.click(screen.getByRole("button", { name: "Save and test" }));

  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "");
  expect(await alertReason()).toBe("endpoint-changed");
});

// The created instance is a different connection. The draft typed for the
// previous destination must not survive into it, or a key meant for the
// endpoint the user was shown could be saved to the newly created one.
test("a created connection never receives the previous connection's credential draft", async () => {
  const { user, client, connected } = setup(fingerprintList("fp-original"));
  await choose(user, "Anthropic");
  await enterText(user, screen.getByLabelText("API key"), "old-endpoint-key");
  await user.click(screen.getByRole("button", { name: "Change provider" }));
  await choose(user, "Azure");
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "");

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
  client.on("evener/instance/create", () => {
    const listing: InstanceListResponse = { instances: [created], availableProviders: catalogue };
    client.on("evener/instance/list", () => structuredClone(listing));
    return structuredClone(listing);
  });
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));

  await enterName(user, "anthropic-team");
  await enterText(user, screen.getByLabelText("Endpoint URL"), "https://moved.example/v1");
  await user.click(screen.getByRole("button", { name: "Save and test" }));

  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic-team"));
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
  expect(createCalls(client)).toHaveLength(1);
});

// Two missing fingerprints are not evidence of the same destination. An
// unkeyable hub, or a created row the listing has not resolved yet, leaves
// nothing to compare, so a value typed for the previous connection must not
// survive into a connection nobody can describe (roborev finding:
// undefined === undefined must not be read as a match).
test("a previous draft does not match a created row with no fingerprints on either side", async () => {
  const { user, client } = setup(savedList("anthropic", { endpointFingerprint: undefined }));
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Replace" }));
  await enterText(user, screen.getByLabelText("API key"), "anthropic-private-draft");
  await user.click(screen.getByRole("button", { name: "Change provider" }));
  await choose(user, "Azure");
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "");

  const created: InstanceEntry = {
    ...catalogue[0]!.setup!,
    name: "anthropic-team",
    implicit: false,
    endpointFingerprint: undefined,
  };
  client.on("evener/instance/create", () => {
    const listing: InstanceListResponse = { instances: [created], availableProviders: structuredClone(catalogue) };
    client.on("evener/instance/list", () => structuredClone(listing));
    return structuredClone(listing);
  });

  await enterName(user, "anthropic-team");
  await enterText(user, screen.getByLabelText("Endpoint URL"), created.baseUrl ?? "");
  await user.click(screen.getByRole("button", { name: "Save and test" }));

  // The previous row and the created row both carry no fingerprint; missing
  // identity is not a match, so the old draft is never written.
  expect(await screen.findByRole("dialog", { name: "anthropic-team" })).toBeTruthy();
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
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
  await enterText(user, screen.getByLabelText("API key"), "sk-ant-draft");
  client.on("evener/auth/apiKey/set", () => {
    throw new WireError(
      "anthropic no longer resolves to the endpoint this form was opened on: review its destination and enter the credential again",
      -32013,
      { evenerErrorInfo: ErrorEndpointConflict },
    );
  });
  client.on("evener/instance/list", () => structuredClone(fingerprintList("fp-2025")));
  const before = credentialsStore.getState().selfRefresh;

  await user.click(screen.getByRole("button", { name: "Save and test" }));

  expect(await alertReason()).toBe("endpoint-changed");
  expect(credentialsStore.getState().selfRefresh).toBeGreaterThan(before);
  // Nothing is left mid-flight by the read that no longer resets the flow.
  expect(await screen.findByRole("button", { name: "Save and test" })).toHaveProperty("disabled", false);
});

test("the hub's endpoint refusal re-anchors the flow instead of saving to the moved destination", async () => {
  const { user, client } = setup(fingerprintList("fp-2024"));
  await choose(user, "Anthropic");
  await enterText(user, screen.getByLabelText("API key"), "sk-ant-draft");
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

  await user.click(screen.getByRole("button", { name: "Save and test" }));

  // The write asserted the endpoint this flow showed the user.
  const setCall = client.calls.find((call) => call.method === "evener/auth/apiKey/set");
  expect(setCall?.params).toMatchObject({ provider: "anthropic", expectedEndpointFingerprint: "fp-2024" });
  // The draft belonged to the endpoint that is gone: dropped, re-anchored, and
  // said so, rather than left as a save that silently went somewhere else.
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "");
  expect(await alertReason()).toBe("endpoint-changed");
});

// The probe must carry the destination this flow reviewed, so the hub can
// compare it where the request lands. Without the fingerprint on the wire the
// probe would only name the instance, and a name re-pointed between the refresh
// above and the call could send the stored credential to an endpoint the user
// never saw.
test("the check asserts the reviewed endpoint", async () => {
  const { user, client, connected } = setup(fingerprintList("fp-reviewed"));
  await choose(user, "Anthropic");
  await enterText(user, screen.getByLabelText("API key"), "sk-ant-draft");
  // The post-save listing carries the same destination as the baseline, so the
  // flow goes straight to the check rather than pausing on the review step.
  scriptSave(client, savedList("anthropic", { endpointFingerprint: "fp-reviewed" }));

  await user.click(screen.getByRole("button", { name: "Save and test" }));

  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
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
  await user.click(screen.getByRole("button", { name: "Test connection" }));
  expect(await alertReason()).toBe("check-fingerprint-unavailable");
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
  await enterText(user, screen.getByLabelText("API key"), "sk-ant-unkeyed");
  await user.click(screen.getByRole("button", { name: "Save and test" }));

  // Nothing reached the hub: no key write, no credential-JSON write, and no
  // probe against an endpoint the hub cannot describe.
  expect(client.calls.find((call) => call.method === "evener/auth/apiKey/set")).toBeUndefined();
  expect(client.calls.some((call) => call.method === "evener/auth/credentialJson/set")).toBe(false);
  expect(client.calls.some((call) => call.method === "evener/auth/test")).toBe(false);
  // The write refusal, not the check refusal.
  expect(await alertReason()).toBe("fingerprint-unavailable");
  // The phase stays idle: the same control is still offered rather than a
  // spinner left behind by a write that never happened.
  expect(screen.getByRole("button", { name: "Save and test" })).toHaveProperty("disabled", false);
});

// A refused endpoint assertion is the same change submit() reports: the
// destination moved under the check. It must reset the flow and say so, not
// masquerade as an unreachable endpoint that invites the user to retry against
// a destination that is gone.
test("a refused assertion is reported as a changed connection, not an endpoint failure", async () => {
  const { user, client } = setup(fingerprintList("fp-reviewed"));
  await choose(user, "Anthropic");
  await enterText(user, screen.getByLabelText("API key"), "sk-ant-draft");
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

  await user.click(screen.getByRole("button", { name: "Save and test" }));

  expect(await alertReason()).toBe("endpoint-changed");
  expect(reasonShown("check-failed")).toBe(false);
});

test("saving the endpoint row edits with the row's fingerprint and clears the test result", async () => {
  const { user, client } = setup(savedList());
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "unsupported", message: "" }));
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Test again" }));
  expect(await screen.findByRole("button", { name: "Continue without verification" })).toBeTruthy();

  const edited = { ...savedList().instances[0]!, baseUrl: "https://moved.example/v1" };
  client.on("evener/instance/edit", () => {
    const listing: InstanceListResponse = { instances: [edited], availableProviders: structuredClone(catalogue) };
    client.on("evener/instance/list", () => structuredClone(listing));
    return structuredClone(listing);
  });
  await user.click(screen.getByRole("button", { name: "Edit" }));
  const field = screen.getByLabelText("Endpoint URL");
  await user.clear(field);
  await enterText(user, field, "https://moved.example/v1");
  await user.click(screen.getByRole("button", { name: "Save" }));

  await waitFor(() => expect(editCalls(client)).toHaveLength(1));
  expect(editCalls(client)[0]!.params).toEqual({
    name: "anthropic",
    baseUrl: "https://moved.example/v1",
    expectedEndpointFingerprint: "fp-anthropic",
    originClientId: "test-tab",
  });
  // The result belonged to the old destination, so the card drops it rather
  // than showing a success it can no longer support.
  expect(screen.queryByRole("button", { name: "Continue without verification" })).toBeNull();
  expect(screen.getByText("https://moved.example/v1")).toBeTruthy();
});

test("saving an unchanged endpoint sends no edit", async () => {
  const { user, client } = setup(savedList());
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Edit" }));
  expect(screen.getByLabelText("Endpoint URL")).toHaveProperty("value", "https://anthropic.example/v1");

  // The editor was seeded from the sanitized listing. Saving it unchanged must
  // close the row rather than write that display value back, which would delete
  // the query parameters and userinfo the listing never showed.
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(screen.queryByLabelText("Endpoint URL")).toBeNull());
  expect(editCalls(client)).toHaveLength(0);
});

test("All connection settings saves protocol, surface, API key env and credential header through one edit", async () => {
  const { user, client } = setup(savedList());
  const edited: InstanceEntry = {
    ...savedList().instances[0]!,
    protocol: "anthropic",
    surface: "generic",
    apiKeyEnv: "TEAM_KEY",
    credentialHeader: "Authorization=Bearer $TEAM_KEY",
  };
  client.on("evener/instance/edit", () => {
    const listing: InstanceListResponse = { instances: [edited], availableProviders: structuredClone(catalogue) };
    client.on("evener/instance/list", () => structuredClone(listing));
    return structuredClone(listing);
  });
  await choose(user);
  await user.click(screen.getByText("All connection settings"));
  await user.selectOptions(screen.getByLabelText("Protocol"), "anthropic");
  await user.selectOptions(screen.getByLabelText("Surface"), "generic");
  await enterText(user, screen.getByLabelText("API key env"), "TEAM_KEY");
  await enterText(user, screen.getByLabelText("Credential header"), "Authorization=Bearer $TEAM_KEY");
  await user.click(screen.getByRole("button", { name: "Save settings" }));

  await waitFor(() => expect(editCalls(client)).toHaveLength(1));
  expect(editCalls(client)[0]!.params).toEqual({
    name: "anthropic",
    protocol: "anthropic",
    clearProtocol: false,
    surface: "generic",
    clearSurface: false,
    apiKeyEnv: "TEAM_KEY",
    clearApiKeyEnv: false,
    credentialHeader: "Authorization=Bearer $TEAM_KEY",
    clearCredentialHeader: false,
    expectedEndpointFingerprint: "fp-anthropic",
    originClientId: "test-tab",
  });
  await waitFor(() => expect(screen.queryByLabelText("Protocol")).toBeNull());
});

test("saving settings clears a typed draft when the destination moved under it", async () => {
  const { user, client } = setup(savedList());
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Replace" }));
  await enterText(user, screen.getByLabelText("API key"), "typed-for-anthropic");

  // The edit answers with the row it wrote, then another client repoints the
  // connection before the card's confirming read: the settings save adopts the
  // replacement row while the draft was typed for the old destination.
  const moved: InstanceEntry = {
    ...savedList().instances[0]!,
    baseUrl: "https://moved.example/v1",
    endpointFingerprint: "fp-moved",
  };
  client.on("evener/instance/edit", () => structuredClone(savedList()));
  client.on("evener/instance/list", () => ({
    instances: [moved],
    availableProviders: structuredClone(catalogue),
  }));

  await user.click(screen.getByText("All connection settings"));
  await user.click(screen.getByRole("button", { name: "Save settings" }));

  await waitFor(() => expect(editCalls(client)).toHaveLength(1));
  expect(await alertReason()).toBe("endpoint-changed");
  // The draft belongs to the endpoint it was typed for, so it is gone before a
  // later Save can pass the destination comparison and send it to the move.
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "");
  await user.click(screen.getByRole("button", { name: "Save" }));
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
});

test("the add card creates with the typed name and base URL, then writes the key and tests", async () => {
  const { user, client, connected } = setup();
  await choose(user, "Azure");
  const row = azureTeamRow();
  client.on("evener/instance/create", () => {
    client.on("evener/instance/list", () => ({
      instances: [row],
      availableProviders: structuredClone(catalogue),
    }));
    return { instances: [row], availableProviders: structuredClone(catalogue) };
  });
  client.on("evener/auth/apiKey/set", () => {
    row.activeSource = "store";
    row.hasStoredFile = true;
    return { ...saved, provider: "azure-team" };
  });
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));

  await enterName(user, "azure-team");
  await enterText(user, screen.getByLabelText("Endpoint URL"), "https://team.azure.example/v1");
  await enterText(user, screen.getByLabelText("API key"), "azure-secret");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("azure-team"));

  // The create owns the name and destination, the key is written against the
  // created row, and only then is the connection tested.
  expect(client.calls.filter((call) => call.method !== "evener/instance/list").map((call) => call.method)).toEqual([
    "evener/instance/create",
    "evener/auth/apiKey/set",
    "evener/auth/test",
  ]);
  expect(client.calls.find((call) => call.method === "evener/instance/create")?.params).toEqual({
    name: "azure-team",
    base: "azure",
    baseUrl: "https://team.azure.example/v1",
    originClientId: "test-tab",
  });
  expect(client.calls.find((call) => call.method === "evener/auth/apiKey/set")?.params).toEqual({
    provider: "azure-team",
    value: "azure-secret",
    expectedEndpointFingerprint: "fp-azure-team",
    originClientId: "test-tab",
  });
  expect(client.calls.find((call) => call.method === "evener/auth/test")?.params).toEqual({
    provider: "azure-team",
    expectedEndpointFingerprint: "fp-azure-team",
  });
});

test("manual retry after creating a connection keeps the stored source and does not review unchanged access", async () => {
  const { user, client, connected } = setup();
  await choose(user, "Azure");
  const created = azureTeamRow();
  let stored = false;
  // A fresh row per read keeps the pre-write baseline a "none" row even though
  // the key write is what turns the listing's copy into a stored one.
  client.on("evener/instance/list", () => ({
    instances: stored ? [{ ...created, activeSource: "store", hasStoredFile: true }] : [],
    availableProviders: structuredClone(catalogue),
  }));
  client.on("evener/instance/create", () => ({
    instances: [structuredClone(created)],
    availableProviders: structuredClone(catalogue),
  }));
  client.on("evener/auth/apiKey/set", () => {
    stored = true;
    return { ...saved, provider: "azure-team" };
  });
  let probes = 0;
  client.on("evener/auth/test", ({ provider }) => {
    probes += 1;
    return { provider, status: probes === 1 ? "auth_rejected" : "success", message: "" };
  });

  await enterName(user, "azure-team");
  await enterText(user, screen.getByLabelText("Endpoint URL"), "https://team.azure.example/v1");
  await enterText(user, screen.getByLabelText("API key"), "azure-secret");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  expect(await alertReason()).toBe("check-failed");
  // The create's own key write landed, so the failed verification is retried
  // against that stored source rather than reviewed as a changed destination.
  await user.click(screen.getByRole("button", { name: "Test again" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("azure-team"));
  expect(screen.queryByTestId("access-review")).toBeNull();
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(2);
});

test("an add card provider with template variables carries them on the created instance", async () => {
  const baseVertex = provider("google-vertex", "Google Vertex", ["credentialJson"]);
  const vertex: ProviderDescriptor = {
    ...baseVertex,
    setup: { ...baseVertex.setup!, baseUrl: "" },
    vars: { project: "GOOGLE_VERTEX_PROJECT", location: "GOOGLE_VERTEX_LOCATION" },
  };
  const { user, client, connected } = setup({ instances: [], availableProviders: [vertex] });
  await choose(user, "Google Vertex");
  await enterName(user, "vertex-team");
  await enterText(user, screen.getByLabelText("Endpoint URL"), "https://us-central1-aiplatform.googleapis.com/v1");
  await enterText(user, screen.getByLabelText("GOOGLE_VERTEX_PROJECT"), "my-project");
  await enterText(user, screen.getByLabelText("GOOGLE_VERTEX_LOCATION"), "us-central1");
  await enterText(user, screen.getByLabelText("Credential JSON"), '{"type":"service_account"}');

  const created: InstanceEntry = {
    ...vertex.setup!,
    name: "vertex-team",
    implicit: false,
    baseUrl: "https://us-central1-aiplatform.googleapis.com/v1",
    endpointFingerprint: "fp-vertex",
    vars: { project: "my-project", location: "us-central1" },
  };
  const listing: InstanceListResponse = { instances: [created], availableProviders: [vertex] };
  let createdNow = false;
  let stored = false;
  client.on("evener/instance/list", () =>
    structuredClone({
      instances: createdNow ? [{ ...created, activeSource: stored ? "store" : "none", hasStoredFile: stored }] : [],
      availableProviders: [vertex],
    }),
  );
  client.on("evener/instance/create", () => {
    createdNow = true;
    return structuredClone(listing);
  });
  client.on("evener/auth/credentialJson/set", () => {
    stored = true;
    return { ...saved, provider: "google-vertex" };
  });
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));

  await user.click(screen.getByRole("button", { name: "Save and test" }));

  await waitFor(() => expect(createCalls(client)).toHaveLength(1));
  expect(createCalls(client)[0]!.params).toMatchObject({
    name: "vertex-team",
    vars: { project: "my-project", location: "us-central1" },
  });
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("vertex-team"));
});

test("a discovery-only provider sends template variables from the settings editor", async () => {
  const baseVertex = provider("google-vertex", "Google Vertex", ["credentialJson"]);
  const vertex: ProviderDescriptor = {
    ...baseVertex,
    vars: { project: "GOOGLE_VERTEX_PROJECT" },
  };
  const { user, client } = setup({ instances: [], availableProviders: [vertex] });
  await choose(user, "Google Vertex");
  await user.click(screen.getByText("All connection settings"));
  await enterText(user, screen.getByLabelText("GOOGLE_VERTEX_PROJECT"), "my-project");

  const created: InstanceEntry = {
    ...vertex.setup!,
    name: "google-vertex",
    implicit: false,
    activeSource: "none",
    vars: { project: "my-project" },
  };
  const listing: InstanceListResponse = { instances: [created], availableProviders: [vertex] };
  client.on("evener/instance/create", () => {
    client.on("evener/instance/list", () => structuredClone(listing));
    return structuredClone(listing);
  });

  await user.click(screen.getByRole("button", { name: "Save settings" }));

  await waitFor(() => expect(createCalls(client)).toHaveLength(1));
  expect(createCalls(client)[0]!.params).toMatchObject({
    name: "google-vertex",
    vars: { project: "my-project" },
  });
});

test("a created endpoint with a query is confirmed by fingerprint, not by its sanitized URL", async () => {
  const { user, client, connected } = setup();
  await choose(user, "Azure");
  const row: InstanceEntry = {
    name: "query-team",
    providerId: "azure",
    base: "azure",
    // The listing sanitizes the authored destination's query away. The
    // fingerprint is the immutable identity of the endpoint the create wrote.
    baseUrl: "https://team.azure.example/v1",
    endpointFingerprint: "fp-query-team",
    protocol: "openai-chat",
    auth: "api-key",
    authModes: ["apiKey"],
    implicit: false,
    isDefault: false,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: true,
  };
  client.on("evener/instance/create", () => {
    const listing = { instances: [row], availableProviders: structuredClone(catalogue) };
    client.on("evener/instance/list", () => structuredClone(listing));
    return structuredClone(listing);
  });
  client.on("evener/auth/apiKey/set", () => {
    row.activeSource = "store";
    row.hasStoredFile = true;
    return { ...saved, provider: "query-team" };
  });
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));

  await enterName(user, "query-team");
  await enterText(user, screen.getByLabelText("Endpoint URL"), "https://team.azure.example/v1?tenant=a");
  await enterText(user, screen.getByLabelText("API key"), "query-secret");
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("query-team"));

  // The authored endpoint is the create's own row: its sanitized display URL
  // differs from what was typed (the query is stripped) but its fingerprint
  // proves the destination, so the key is written without a review detour.
  expect(screen.queryByTestId("access-review")).toBeNull();
  expect(client.calls.find((call) => call.method === "evener/auth/apiKey/set")?.params).toEqual({
    provider: "query-team",
    value: "query-secret",
    expectedEndpointFingerprint: "fp-query-team",
    originClientId: "test-tab",
  });
  expect(client.calls.find((call) => call.method === "evener/auth/test")?.params).toEqual({
    provider: "query-team",
    expectedEndpointFingerprint: "fp-query-team",
  });
});

test.each([
  {
    state: "an endpoint conflict",
    reason: "endpoint-changed",
    script(client: FakeClient) {
      client.on("evener/instance/edit", () => {
        throw new WireError(
          "anthropic no longer resolves to the endpoint this form was opened on: review its destination and enter the credential again",
          -32013,
          { evenerErrorInfo: ErrorEndpointConflict },
        );
      });
    },
  },
  {
    state: "a replaced connection",
    reason: "connection-replaced",
    script(_client: FakeClient) {
      act(() => credentialsStore.setState({ listingFromPreviousConnection: true }));
    },
  },
])("a refused endpoint edit under $state reports it and leaves no stale result", async ({ reason, script }) => {
  const { user, client } = setup(savedList());
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "unsupported", message: "" }));
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Test again" }));
  expect(await screen.findByRole("button", { name: "Continue without verification" })).toBeTruthy();
  script(client);

  await user.click(screen.getByRole("button", { name: "Edit" }));
  const field = screen.getByLabelText("Endpoint URL");
  await user.clear(field);
  await enterText(user, field, "https://moved.example/v1");
  await user.click(screen.getByRole("button", { name: "Save" }));

  expect(await alertReason()).toBe(reason);
  expect(screen.queryByRole("button", { name: "Continue without verification" })).toBeNull();
  expect(screen.getByRole("button", { name: "Save" })).toBeTruthy();
});

test("dismissing during the free-name read does not create the connection", async () => {
  const { user, client, close } = setup();
  await choose(user, "Azure");
  await enterName(user, "late-team");
  await enterText(user, screen.getByLabelText("Endpoint URL"), "https://late.example/v1");
  const read = deferred<InstanceListResponse>();
  client.on("evener/instance/list", () => read.promise);

  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  expect(close).toHaveBeenCalledTimes(1);

  await act(async () => {
    read.resolve({ instances: [], availableProviders: structuredClone(catalogue) });
    await read.promise;
  });
  expect(createCalls(client)).toHaveLength(0);
});

test("an unconfirmed create keeps its name and offers reload", async () => {
  const { user, client } = setup();
  await choose(user, "Azure");
  await enterName(user, "unconfirmed-team");
  await enterText(user, screen.getByLabelText("Endpoint URL"), "https://unconfirmed.example/v1");
  const empty: InstanceListResponse = { instances: [], availableProviders: structuredClone(catalogue) };
  client.on("evener/instance/create", () => {
    client.on("evener/instance/list", () => structuredClone(empty));
    return structuredClone(empty);
  });

  await user.click(screen.getByRole("button", { name: "Save and test" }));

  expect(await alertReason()).toBe("reload-failed");
  expect(screen.getByRole("button", { name: "Reload" })).toBeTruthy();
  expect(createCalls(client)).toHaveLength(1);
});

test("starting an endpoint save drops the previous unsupported result", async () => {
  const { user, client } = setup(savedList());
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "unsupported", message: "" }));
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Test again" }));
  expect(await screen.findByRole("button", { name: "Continue without verification" })).toBeTruthy();
  const save = deferred<InstanceListResponse>();
  client.on("evener/instance/edit", () => save.promise);
  await user.click(screen.getByRole("button", { name: "Edit" }));
  const field = screen.getByLabelText("Endpoint URL");
  await user.clear(field);
  await enterText(user, field, "https://moved.example/v1");

  await user.click(screen.getByRole("button", { name: "Save" }));

  expect(screen.queryByRole("button", { name: "Continue without verification" })).toBeNull();
  await act(async () => {
    save.resolve(savedList("anthropic", { baseUrl: "https://moved.example/v1" }));
    await save.promise;
  });
});

test("starting a settings save drops the previous unsupported result", async () => {
  const { user, client } = setup(savedList());
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "unsupported", message: "" }));
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Test again" }));
  expect(await screen.findByRole("button", { name: "Continue without verification" })).toBeTruthy();
  const save = deferred<InstanceListResponse>();
  client.on("evener/instance/edit", () => save.promise);
  await user.click(screen.getByText("All connection settings"));

  await user.click(screen.getByRole("button", { name: "Save settings" }));

  expect(screen.queryByRole("button", { name: "Continue without verification" })).toBeNull();
  await act(async () => {
    save.resolve(savedList());
    await save.promise;
  });
});

test("canceling a replacement restores the saved key row", async () => {
  const { user } = setup(savedList());
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Replace" }));
  await enterText(user, screen.getByLabelText("API key"), "replacement-draft");

  // The row's Cancel comes before the footer's in document order.
  await user.click(screen.getAllByRole("button", { name: "Cancel" })[0]!);

  expect(screen.getByTestId("credential-saved")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Test again" })).toBeTruthy();
});

test("typing a credential after choosing host access saves it", async () => {
  const { user, client, connected } = setup(
    savedList("anthropic", { activeSource: "env:ANTHROPIC_API_KEY", hasStoredFile: false }),
  );
  scriptSave(client);
  await choose(user);
  // A connection with no stored key opens its key row already.
  await openHostAccess(user);
  await enterText(user, screen.getByLabelText("API key"), "typed-key");

  await user.click(screen.getByRole("button", { name: /^(Save and test|Test connection)$/ }));

  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
  expect(client.calls.find((call) => call.method === "evener/auth/apiKey/set")?.params).toEqual({
    provider: "anthropic",
    value: "typed-key",
    expectedEndpointFingerprint: "fp-anthropic",
    originClientId: "test-tab",
  });
});

test("saving one settings field leaves an externally changed field alone", async () => {
  const { user, client } = setup(savedList());
  await choose(user);
  await user.click(screen.getByText("All connection settings"));
  // Another client changes the protocol while the disclosure sits open; the
  // untouched draft field must not overwrite it.
  const external = { ...savedList().instances[0]!, protocol: "anthropic" };
  client.on("evener/instance/list", () => ({
    instances: [external],
    availableProviders: structuredClone(catalogue),
  }));
  await act(async () => {
    await credentialsStore.getState().fetch();
  });
  const edited = { ...external, apiKeyEnv: "TEAM_KEY" };
  client.on("evener/instance/edit", () => {
    const listing: InstanceListResponse = {
      instances: [edited],
      availableProviders: structuredClone(catalogue),
    };
    client.on("evener/instance/list", () => structuredClone(listing));
    return structuredClone(listing);
  });
  await enterText(user, screen.getByLabelText("API key env"), "TEAM_KEY");

  await user.click(screen.getByRole("button", { name: "Save settings" }));

  await waitFor(() => expect(editCalls(client)).toHaveLength(1));
  expect(editCalls(client)[0]!.params).toEqual({
    name: "anthropic",
    apiKeyEnv: "TEAM_KEY",
    clearApiKeyEnv: false,
    expectedEndpointFingerprint: "fp-anthropic",
    originClientId: "test-tab",
  });
});

test("Enter in the endpoint editor saves that row's draft", async () => {
  const { user, client } = setup(savedList());
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Edit" }));
  const field = screen.getByLabelText("Endpoint URL");
  await user.clear(field);
  await enterText(user, field, "https://moved.example/v1");
  const save = deferred<InstanceListResponse>();
  client.on("evener/instance/edit", () => save.promise);

  await user.keyboard("{Enter}");

  await waitFor(() => expect(editCalls(client)).toHaveLength(1));
  expect(client.calls.some((call) => call.method === "evener/auth/test")).toBe(false);
  await act(async () => {
    save.resolve(savedList("anthropic", { baseUrl: "https://moved.example/v1" }));
    await save.promise;
  });
});

test("Enter in a connection setting saves the settings instead of testing the connection", async () => {
  const { user, client } = setup(savedList());
  await choose(user);
  await user.click(screen.getByText("All connection settings"));
  const edited = { ...savedList().instances[0]!, apiKeyEnv: "TEAM_KEY" };
  client.on("evener/instance/edit", () => {
    const listing: InstanceListResponse = {
      instances: [edited],
      availableProviders: structuredClone(catalogue),
    };
    client.on("evener/instance/list", () => structuredClone(listing));
    return structuredClone(listing);
  });
  const env = screen.getByLabelText("API key env");
  await enterText(user, env, "TEAM_KEY");

  await user.keyboard("{Enter}");

  // Enter belongs to the editor that has focus: it issues the settings edit
  // instead of testing the configuration still on the wire.
  await waitFor(() => expect(editCalls(client)).toHaveLength(1));
  expect(editCalls(client)[0]!.params).toMatchObject({
    name: "anthropic",
    apiKeyEnv: "TEAM_KEY",
  });
  expect(client.calls.some((call) => call.method === "evener/auth/test")).toBe(false);
});

test("Enter in the add card creates the connection", async () => {
  const { user, client } = setup();
  await choose(user, "Azure");
  await enterName(user, "enter-team");
  await enterText(user, screen.getByLabelText("Endpoint URL"), "https://enter.example/v1");
  const row: InstanceEntry = {
    ...catalogue[0]!.setup!,
    name: "enter-team",
    providerId: "azure",
    implicit: false,
    baseUrl: "https://enter.example/v1",
  };
  const listing: InstanceListResponse = { instances: [row], availableProviders: structuredClone(catalogue) };
  client.on("evener/instance/create", () => {
    client.on("evener/instance/list", () => structuredClone(listing));
    return structuredClone(listing);
  });
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));

  await user.keyboard("{Enter}");

  await waitFor(() => expect(createCalls(client)).toHaveLength(1));
});

test("a superseded create never writes its draft against the reconciled endpoint", async () => {
  const { user, client } = setup();
  await choose(user, "Azure");
  await enterName(user, "raced-team");
  await enterText(user, screen.getByLabelText("Endpoint URL"), "https://authored.example/v1");
  await enterText(user, screen.getByLabelText("API key"), "authored-key");

  // The create's own listing authored endpoint A. The same sanitized URL can
  // hide a different endpoint identity, so B differs only by fingerprint.
  const authored: InstanceEntry = {
    ...catalogue[0]!.setup!,
    name: "raced-team",
    providerId: "azure",
    implicit: false,
    baseUrl: "https://authored.example/v1",
    endpointFingerprint: "fp-authored",
    activeSource: "none",
  };
  const repointed: InstanceEntry = {
    ...authored,
    endpointFingerprint: "fp-repointed",
  };
  const authoredListing: InstanceListResponse = {
    instances: [authored],
    availableProviders: structuredClone(catalogue),
  };
  const repointedListing: InstanceListResponse = {
    instances: [repointed],
    availableProviders: structuredClone(catalogue),
  };
  const create = deferred<InstanceListResponse>();
  client.on("evener/instance/create", () => create.promise);

  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await waitFor(() => expect(client.calls.some((call) => call.method === "evener/instance/create")).toBe(true));
  // A concurrent read lands endpoint B and supersedes the create's answer.
  client.on("evener/instance/list", () => structuredClone(repointedListing));
  await act(async () => {
    await credentialsStore.getState().fetchSelf();
  });
  await act(async () => {
    create.resolve(structuredClone(authoredListing));
    await create.promise;
  });

  // The reconciliation read finds B. The card may show B, but it may not send
  // the key typed for A to B's fingerprint.
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
  expect(client.calls.some((call) => call.method === "evener/auth/test")).toBe(false);
  expect(await screen.findByRole("dialog", { name: "raced-team" })).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Add key" }));
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "authored-key");
});

test("a client replaced during the superseded-create confirmation cannot revive the old card", async () => {
  const { user, client } = setup();
  await choose(user, "Azure");
  await enterName(user, "stale-team");
  await enterText(user, screen.getByLabelText("Endpoint URL"), "https://stale.example/v1");

  const authored: InstanceEntry = {
    ...catalogue[0]!.setup!,
    name: "stale-team",
    providerId: "azure",
    implicit: false,
    baseUrl: "https://stale.example/v1",
    endpointFingerprint: "fp-stale",
    activeSource: "none",
  };
  const authoredListing: InstanceListResponse = {
    instances: [authored],
    availableProviders: structuredClone(catalogue),
  };
  const create = deferred<InstanceListResponse>();
  client.on("evener/instance/create", () => create.promise);
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await waitFor(() => expect(createCalls(client)).toHaveLength(1));

  // A concurrent read supersedes the create's own answer; the confirmation
  // read that follows is then held open.
  client.on("evener/instance/list", () => ({ instances: [], availableProviders: structuredClone(catalogue) }));
  await act(async () => {
    await credentialsStore.getState().fetch();
  });
  const confirming = deferred<InstanceListResponse>();
  client.on("evener/instance/list", () => confirming.promise);
  await act(async () => {
    create.resolve(structuredClone(authoredListing));
    await create.promise;
  });

  // The controller is replaced while that read is outstanding, then the read
  // lands. Its continuation belongs to the previous connection and must not
  // restore that connection's name or configured state.
  const replacement = new FakeClient("ready");
  replacement.on("evener/instance/list", () => ({ instances: [], availableProviders: structuredClone(catalogue) }));
  act(() => connectionStore.getState().connect(replacement));
  await act(async () => {
    confirming.resolve({ instances: [], availableProviders: structuredClone(catalogue) });
    await confirming.promise;
  });

  expect(reasonShown("reload-failed")).toBe(false);
  expect(screen.queryByText(/The connection was saved on the host/)).toBeNull();
});

// One class, every path. A credential draft is only ever written against the
// destination the card showed when the draft was typed. A foreign refresh can
// install a replacement while the draft is held; whichever action the card
// takes next - a key save, an endpoint save, or the settings save - must prove
// the destination first and must never carry the old draft to the replacement.
test.each([
  {
    path: "a credential save",
    async stage(user: ReturnType<typeof userEvent.setup>) {
      await choose(user);
      await user.click(screen.getByRole("button", { name: "Replace" }));
      await enterText(user, screen.getByLabelText("API key"), "typed-for-anthropic");
    },
    async attempt(user: ReturnType<typeof userEvent.setup>) {
      // The stored key the replacement sat in front of is still saved, so the
      // primary action is the check, not another save.
      await user.click(screen.getByRole("button", { name: "Test again" }));
    },
  },
  {
    path: "an endpoint save",
    async stage(user: ReturnType<typeof userEvent.setup>) {
      await choose(user);
      await user.click(screen.getByRole("button", { name: "Edit" }));
      const field = screen.getByLabelText("Endpoint URL");
      await user.clear(field);
      await enterText(user, field, "https://authored.example/v1");
    },
    async attempt(user: ReturnType<typeof userEvent.setup>) {
      await user.click(screen.getByRole("button", { name: "Save" }));
    },
  },
  {
    path: "a settings save",
    async stage(user: ReturnType<typeof userEvent.setup>) {
      await choose(user);
      await user.click(screen.getByRole("button", { name: "Replace" }));
      await enterText(user, screen.getByLabelText("API key"), "typed-for-anthropic");
    },
    async attempt(user: ReturnType<typeof userEvent.setup>) {
      await user.click(screen.getByText("All connection settings"));
      await user.click(screen.getByRole("button", { name: "Save settings" }));
      // The settings draft was authored against the replacement, so the save
      // may land there; the key drafted for the old destination must be gone
      // before anything can write it - including the key row's own Save.
      await user.click(screen.getByRole("button", { name: "Save" }));
    },
  },
])("destination integrity: $path never writes a draft to a replacement", async ({ stage, attempt }) => {
  const { user, client } = setup(savedList());
  await stage(user);

  const movedListing = movedList();
  client.on("evener/instance/list", () => structuredClone(movedListing));
  client.on("evener/instance/edit", () => structuredClone(movedListing));
  await act(async () => {
    await credentialsStore.getState().fetch();
  });

  await attempt(user);

  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
  const keyField = screen.queryByLabelText("API key");
  if (keyField) expect(keyField).toHaveProperty("value", "");
});

// The adoption race the review named: a self-marked read (another part of the
// page refreshing) installs B without the foreign-change watch standing up, so
// the card holds a key typed against A while its row already says B. Opening
// the settings disclosure now pins B, and an unchanged save compares B with B.
// The credential draft's own claim is the only thing that still knows it was
// typed for A, so adoption must consult it before advancing the baseline.
test("a settings adoption cannot retain a key typed for the replaced destination", async () => {
  const { user, client } = setup(savedList());
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Replace" }));
  await enterText(user, screen.getByLabelText("API key"), "typed-for-anthropic");

  const movedListing = movedList();
  client.on("evener/instance/list", () => structuredClone(movedListing));
  client.on("evener/instance/edit", () => structuredClone(movedListing));
  // Marked as this page's own read: the foreign-change watch stands aside, so
  // the draft survives until the save itself consults its claim.
  await act(async () => {
    await credentialsStore.getState().fetchSelf();
  });
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "typed-for-anthropic");

  await user.click(screen.getByText("All connection settings"));
  await user.click(screen.getByRole("button", { name: "Save settings" }));
  await waitFor(() => expect(editCalls(client)).toHaveLength(1));

  // The settings save adopted B. The next Save of the key row must not find a
  // baseline that matches the row and send A's key to B.
  await user.click(screen.getByRole("button", { name: "Save" }));
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "");
});

// The endpoint editor's Save asserts the destination the editor was opened on,
// not whatever row a foreign refresh installed while it sat idle. Otherwise a
// URL authored against A silently overwrites the replacement B.
test("an endpoint save asserts the destination the editor was opened on", async () => {
  const { user, client } = setup(savedList());
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Edit" }));
  const field = screen.getByLabelText("Endpoint URL");
  await user.clear(field);
  await enterText(user, field, "https://authored.example/v1");

  const movedListing = movedList();
  client.on("evener/instance/list", () => structuredClone(movedListing));
  await act(async () => {
    await credentialsStore.getState().fetch();
  });

  await user.click(screen.getByRole("button", { name: "Save" }));

  expect(editCalls(client)).toHaveLength(0);
  expect(await alertReason()).toBe("endpoint-changed");
  // The editor closed rather than keeping a draft that belonged to A as if it
  // were an edit for B.
  expect(screen.queryByLabelText("Endpoint URL")).toBeNull();
});

// Enter belongs to the editable field it was pressed in. A disclosure summary
// and a link are native controls: intercepting Enter there submits the
// connection form (and can start a provider check) instead of opening settings
// or following the link.
test("Enter on a disclosure or link keeps its native activation", async () => {
  const { user, client } = setup(savedList());
  await choose(user);
  await user.click(screen.getByText("All connection settings"));
  // Close it again so the summary is the control under test.
  await user.click(screen.getByText("All connection settings"));

  const summary = screen.getByText("All connection settings");
  fireEvent.keyDown(summary, { key: "Enter" });
  expect(editCalls(client)).toHaveLength(0);
  expect(client.calls.some((call) => call.method === "evener/auth/test")).toBe(false);

  const link = screen.getByRole("link", { name: "Get an API key" });
  fireEvent.keyDown(link, { key: "Enter" });
  expect(editCalls(client)).toHaveLength(0);
  expect(client.calls.some((call) => call.method === "evener/auth/test")).toBe(false);
});

// A replacement typed against the old destination is dropped when that
// destination moves, but the stored key the returned row still resolves is
// not: the card must show saved access and let the check run.
test("dropping a replacement draft restores the saved key state", async () => {
  const { user, client } = setup(savedList());
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Replace" }));
  await enterText(user, screen.getByLabelText("API key"), "replacement-for-anthropic");
  await user.click(screen.getByRole("button", { name: "Edit" }));
  const field = screen.getByLabelText("Endpoint URL");
  await user.clear(field);
  await enterText(user, field, "https://moved.example/v1");

  const moved = savedList("anthropic", {
    baseUrl: "https://moved.example/v1",
    endpointFingerprint: "fp-moved",
    activeSource: "store",
  });
  client.on("evener/instance/edit", () => {
    client.on("evener/instance/list", () => structuredClone(moved));
    return structuredClone(moved);
  });
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));

  await user.click(screen.getByRole("button", { name: "Save" }));

  // The replacement is gone; the stored key still resolves, so the card is
  // back to saved access and testing is not blocked on a missing credential.
  expect(await screen.findByRole("button", { name: "Test again" })).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Test again" }));
  await waitFor(() => expect(client.calls.some((call) => call.method === "evener/auth/test")).toBe(true));
  expect(client.calls.find((call) => call.method === "evener/auth/test")?.params).toMatchObject({
    provider: "anthropic",
    expectedEndpointFingerprint: "fp-moved",
  });
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
});

// An adoption that keeps a replacement because the destination did not move
// must not present it as saved: the next Save has to write the replacement,
// not test the stored key it sits in front of.
test("saving settings keeps a pending replacement unsaved", async () => {
  const { user, client } = setup(savedList());
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Replace" }));
  await enterText(user, screen.getByLabelText("API key"), "replacement-key");

  client.on("evener/instance/edit", () => structuredClone(savedList()));
  await user.click(screen.getByText("All connection settings"));
  await user.click(screen.getByRole("button", { name: "Save settings" }));
  await waitFor(() => expect(editCalls(client)).toHaveLength(1));

  scriptSave(client);
  await user.click(screen.getByRole("button", { name: "Save" }));

  await waitFor(() => expect(client.calls.some((call) => call.method === "evener/auth/apiKey/set")).toBe(true));
  expect(client.calls.find((call) => call.method === "evener/auth/apiKey/set")?.params).toMatchObject({
    provider: "anthropic",
    value: "replacement-key",
    expectedEndpointFingerprint: "fp-anthropic",
  });
});

// A successful credential write stays saved even when the listing refresh that
// follows it fails: canceling the replacement the card then offers must not
// derive saved=false from the stale row and ask for the key again.
test("canceling a replacement keeps a save whose refresh failed", async () => {
  const { user, client } = setup(savedList("anthropic", { activeSource: "none", hasStoredFile: false }));
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "first-key");

  client.on("evener/auth/apiKey/set", () => ({ ...saved, provider: "anthropic" }));
  client.on("evener/instance/list", () => {
    throw new Error("listing unavailable");
  });
  await user.click(screen.getByRole("button", { name: "Save and test" }));
  await waitFor(() => expect(client.calls.some((call) => call.method === "evener/auth/apiKey/set")).toBe(true));

  // The card kept the confirmed save and offers Replace; canceling it restores
  // saved access, so the next action refreshes and tests, not re-enters a key.
  await user.click(screen.getByRole("button", { name: "Replace" }));
  await user.click(screen.getAllByRole("button", { name: "Cancel" })[0]!);

  expect(await screen.findByRole("button", { name: "Test again" })).toBeTruthy();
  client.on("evener/instance/list", () =>
    structuredClone(savedList("anthropic", { activeSource: "store", hasStoredFile: true })),
  );
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await user.click(screen.getByRole("button", { name: "Test again" }));

  await waitFor(() => expect(client.calls.some((call) => call.method === "evener/auth/test")).toBe(true));
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(1);
});

// A refused replacement's recovery row can still report stored access. That
// adoption must clear the replacement's pending state synchronously, or the
// card asks for a key it already has and blocks the check.
test("a refused replacement whose recovery row has stored access keeps saved testing", async () => {
  const { user, client } = setup(fingerprintList("fp-2024", "store"));
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Replace" }));
  await enterText(user, screen.getByLabelText("API key"), "replacement");
  client.on("evener/auth/apiKey/set", () => {
    throw new WireError(
      "anthropic no longer resolves to the endpoint this form was opened on: review its destination and enter the credential again",
      -32013,
      { evenerErrorInfo: ErrorEndpointConflict },
    );
  });
  client.on("evener/instance/list", () => structuredClone(fingerprintList("fp-2024", "store")));

  await user.click(screen.getByRole("button", { name: "Save and test" }));

  expect(await screen.findByRole("button", { name: "Test again" })).toBeTruthy();
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await user.click(screen.getByRole("button", { name: "Test again" }));
  await waitFor(() => expect(client.calls.some((call) => call.method === "evener/auth/test")).toBe(true));
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(1);
});

// Enter during an IME composition confirms the composition, not the card's
// editor: the field may still be mid-composition and must not save.
test("Enter during IME composition does not save", async () => {
  const { user, client } = setup(savedList());
  await choose(user);
  await user.click(screen.getByRole("button", { name: "Edit" }));
  const field = screen.getByLabelText("Endpoint URL");
  await user.clear(field);
  await enterText(user, field, "https://composed.example/v1");

  fireEvent.keyDown(field, { key: "Enter", isComposing: true, keyCode: 229 });

  expect(editCalls(client)).toHaveLength(0);
});

// A native select keeps its own Enter behavior; the save shortcut is for text
// fields, not the Protocol or Surface picker.
test("Enter on a settings picker keeps native activation", async () => {
  const { user, client } = setup(savedList());
  await choose(user);
  await user.click(screen.getByText("All connection settings"));

  fireEvent.keyDown(screen.getByLabelText("Protocol"), { key: "Enter" });

  expect(editCalls(client)).toHaveLength(0);
  expect(client.calls.some((call) => call.method === "evener/auth/test")).toBe(false);
});

// A write whose answer a newer request superseded is not proof its listing is
// the one on screen. When no applied listing can confirm the row the edit
// wrote, the editor and its draft stay put rather than closing on the old
// listing and discarding the draft.
test("a superseded settings edit stays open until a listing confirms it", async () => {
  const { user, client } = setup(savedList());
  await choose(user);
  await user.click(screen.getByText("All connection settings"));
  await enterText(user, screen.getByLabelText("API key env"), "TEAM_KEY");

  const edit = deferred<InstanceListResponse>();
  client.on("evener/instance/edit", () => edit.promise);
  await user.click(screen.getByRole("button", { name: "Save settings" }));
  await waitFor(() => expect(editCalls(client)).toHaveLength(1));

  // A newer read starts before the edit answers, so the store discards the
  // edit's response. Every later read fails too: the card has no applied
  // listing it can adopt.
  const newer = deferred<InstanceListResponse>();
  let reads = 0;
  client.on("evener/instance/list", () => {
    reads += 1;
    if (reads === 1) return newer.promise;
    throw new Error("listing unavailable");
  });
  let pending!: Promise<boolean>;
  await act(async () => {
    pending = credentialsStore.getState().fetchSelf();
  });
  const edited = { ...savedList().instances[0]!, apiKeyEnv: "TEAM_KEY" };
  await act(async () => {
    edit.resolve({ instances: [edited], availableProviders: structuredClone(catalogue) });
    await edit.promise;
  });

  expect(await alertReason()).toBe("save-unconfirmed");
  // The draft and the editor are retained: the write may have landed, and the
  // card must not close on a listing that predates it.
  expect(screen.getByLabelText("API key env")).toHaveProperty("value", "TEAM_KEY");
  expect(screen.getByRole("button", { name: "Save settings" })).toBeTruthy();

  await act(async () => {
    newer.resolve(savedList());
    await pending.catch(() => false);
  });
});
