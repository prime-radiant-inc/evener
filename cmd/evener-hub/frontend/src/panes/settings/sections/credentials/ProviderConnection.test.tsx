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
  expect(screen.getByTestId("billing-note")).toBeTruthy();
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

test("changing base in the full form continues with the actual created provider rather than old vendor help", async () => {
  const { user, client } = setup();
  await choose(user, "Azure");
  await user.click(screen.getByRole("button", { name: "Configure provider" }));
  await user.selectOptions(screen.getByLabelText("Base provider"), "openai");
  await enterText(user, screen.getByLabelText("Name"), "openai-team");
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
  await act(async () => {
    fireEvent.click(screen.getByText("Advanced settings"));
  });
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

  expect(screen.getByRole("dialog", { name: "Connect Anthropic" })).toBeTruthy();
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
  await enterText(user, screen.getByLabelText("Name"), "retained-name");
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
    await user.click(screen.getByRole("button", { name: "Save and check" }));
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
    expect(screen.queryByRole("button", { name: "Use reviewed access and check" })).toBeNull();
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

test("a cloud choice opens the complete preselected configuration editor", async () => {
  const { user } = setup();
  await user.click(await revealAllProviders());
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
  await user.click(await revealAllProviders());
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
  await user.click(screen.getByRole("button", { name: "Save and check" }));

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
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  expect(document.activeElement).toBe(screen.getByLabelText("API key"));
  expect(screen.getByRole("alert").id).toBe(screen.getByLabelText("API key").getAttribute("aria-describedby"));
  expect(client.calls.filter((c) => c.method !== "evener/instance/list")).toEqual([]);
});
test("saves directly to the real public ID then reloads and checks before completing with its name", async () => {
  const { user, client, connected } = setup();
  scriptSave(client);
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
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
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  expect(await screen.findByRole("alert")).toBe(document.activeElement);
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "fixture-key");
  expect(screen.queryByText(/secret-failure-body/)).toBeNull();
  expect(client.calls.some((c) => c.method === "evener/auth/test")).toBe(false);
  scriptSave(client);
  await user.click(screen.getByRole("button", { name: "Save and check" }));
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
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await screen.findByRole("button", { name: "Checking model list…" });
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
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await screen.findByRole("button", { name: "Checking model list…" });

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
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await screen.findByRole("button", { name: "Checking model list…" });

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
  await user.click(screen.getByRole("button", { name: "Retry check" }));
  expect(screen.getByRole("button", { name: "Checking model list…" })).toHaveProperty("disabled", true);

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
  await user.click(screen.getByRole("button", { name: "Retry check" }));
  expect(await screen.findByTestId("access-review")).toBeTruthy();
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);
  await user.click(screen.getByRole("button", { name: "Use reviewed access and check" }));
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
  await user.click(screen.getByRole("button", { name: "Retry check" }));
  await screen.findByRole("button", { name: "Checking model list…" });

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
  await user.click(screen.getByRole("button", { name: "Use reviewed access and check" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
  expect(probes).toBe(2);
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
});

test("saved OAuth access awaiting an automatic retry does not invite another sign-in", async () => {
  const { user, client } = setup(savedList("openai-codex", { activeSource: "oauth", hasStoredOAuth: true }));
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "endpoint_failure", message: "" }));
  await choose(user, "OpenAI Codex");
  expect(screen.getByRole("button", { name: "Sign in" })).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Retry check" }));

  await waitFor(() => expect(reasonShown("check-failed")).toBe(true));
  const scheduledNotice = document.querySelector<HTMLElement>('[data-retry="scheduled"]');
  expect(scheduledNotice).not.toBeNull();
  expect(scheduledNotice?.getAttribute("role")).toBe("status");
  expect(scheduledNotice?.textContent?.trim()).not.toBe("");
  // Recovery is already running on stored access: offering a fresh acquisition
  // would invite a needless sign-in.
  expect(screen.queryByRole("button", { name: "Sign in" })).toBeNull();
  // Manual retry and dismissal are still the person's.
  expect(screen.getByRole("button", { name: "Retry check" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Close, access stays saved" })).toBeTruthy();
});

test("a manual retry cancels the scheduled one so checks do not overlap", async () => {
  const { user, client, connected } = setup();
  scriptSave(client);
  const first = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => first.promise);
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await screen.findByRole("button", { name: "Checking model list…" });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    first.resolve({ provider: "anthropic", status: "endpoint_failure", message: "" });
    await first.promise;
  });

  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Retry check" }));
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
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await screen.findByRole("button", { name: "Checking model list…" });
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
  await mounted.user.click(screen.getByRole("button", { name: "Save and check" }));
  await screen.findByRole("button", { name: "Checking model list…" });
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
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await screen.findByRole("button", { name: "Checking model list…" });
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
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await screen.findByRole("button", { name: "Checking model list…" });
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
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await screen.findByRole("button", { name: "Checking model list…" });
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
  expect(screen.getByTestId("credential-saved")).toBeTruthy();
  expect(screen.getByRole("button", { name: /^Close, access stays saved$/i })).toBeTruthy();
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Retry check" }));
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
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await screen.findByRole("button", { name: "Checking model list…" });
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
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await screen.findByRole("button", { name: "Refreshing access…" });
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
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await screen.findByRole("button", { name: "Checking model list…" });
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
  expect(screen.getByRole("button", { name: "Checking model list…" })).toHaveProperty("disabled", true);
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

test("opening another-instance configuration retires a scheduled retry", async () => {
  const { user, client, connected } = setup();
  scriptSave(client);
  const first = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => first.promise);
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await screen.findByRole("button", { name: "Checking model list…" });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    first.resolve({ provider: "anthropic", status: "endpoint_failure", message: "" });
    await first.promise;
  });
  await act(async () => {
    fireEvent.click(screen.getByText("Advanced settings"));
  });
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Configure another instance" }));
  });
  expect(screen.getByRole("dialog", { name: "Add provider instance" })).toBeTruthy();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(60000);
  });
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);
  expect(connected).not.toHaveBeenCalled();
  expect(screen.getByRole("dialog", { name: "Add provider instance" })).toBeTruthy();
});

test("switching to host access retires a scheduled retry for the credential path", async () => {
  const { user, client, connected } = setup();
  scriptSave(client);
  const first = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => first.promise);
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "fixture-key");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await screen.findByRole("button", { name: "Checking model list…" });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  await act(async () => {
    first.resolve({ provider: "anthropic", status: "endpoint_failure", message: "" });
    await first.promise;
  });
  await act(async () => {
    fireEvent.click(screen.getByText("Advanced settings"));
  });
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Use existing host access" }));
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
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await screen.findByRole("button", { name: "Checking model list…" });
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
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await screen.findByRole("button", { name: "Refreshing access…" });
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
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await screen.findByRole("button", { name: "Checking model list…" });
  await user.click(screen.getByRole("button", { name: "Change provider" }));
  client.on("evener/instance/list", () => ({ instances: [], availableProviders: structuredClone(catalogue) }));
  await choose(user, "OpenAI");
  expect(screen.getByRole("dialog", { name: "Connect OpenAI" })).toBeTruthy();
  await act(async () => {
    pending.resolve({ provider: "anthropic", status: "success", message: "" });
    await pending.promise;
  });
  expect(connected).not.toHaveBeenCalled();
  expect(screen.getByRole("dialog", { name: "Connect OpenAI" })).toBeTruthy();
});

test("a check shows one progress state instead of sign-in plus a competing check action", async () => {
  const { user, client } = setup();
  await choose(user, "OpenAI Codex");
  const pending = deferred<{ provider: string; status: string; message: string }>();
  client.on("evener/auth/test", () => pending.promise);
  await user.click(screen.getByRole("button", { name: "Check connection" }));
  expect(screen.queryByRole("button", { name: "Sign in" })).toBeNull();
  expect(screen.getByRole("button", { name: "Checking model list…" })).toHaveProperty("disabled", true);
  await act(async () => {
    pending.resolve({ provider: "openai-codex", status: "endpoint_failure", message: "" });
    await pending.promise;
  });
  expect(await screen.findByRole("button", { name: "Retry check" })).toHaveProperty("disabled", false);
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
  expect(screen.getByTestId("credential-saved")).toBeTruthy();
  expect(screen.getByRole("button", { name: /^Close, access stays saved$/i })).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Retry check" }));
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
  expect(screen.getByTestId("credential-saved")).toBeTruthy();
  expect(screen.getByRole("button", { name: /^Close, access stays saved$/i })).toBeTruthy();
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Retry check" }));
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
  await user.click(screen.getByRole("button", { name: "Save and check" }));
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
  await first.user.click(screen.getByRole("button", { name: "Save and check" }));
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
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "");
  await user.click(screen.getByRole("button", { name: "Retry check" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
  expect(first.client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(1);
  reopened.unmount();
});
test("editing reopened stored access can be dismissed without a write while the original stays saved", async () => {
  const { user, client, close } = setup(savedList());
  await choose(user);
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
  await enterText(user, screen.getByLabelText("API key"), "replacement-draft");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
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
    await user.click(screen.getByRole("button", { name: "Save and check" }));
    expect(await screen.findByRole("button", { name: "Use reviewed access and check" })).toBeTruthy();
    expect(screen.getByTestId("credential-saved")).toBeTruthy();
    expect(client.calls.some((c) => c.method === "evener/auth/test")).toBe(false);
    await user.click(screen.getByRole("button", { name: "Use reviewed access and check" }));
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
  await user.click(screen.getByRole("button", { name: "Check connection" }));
  await user.click(await screen.findByRole("button", { name: "Use reviewed access and check" }));
  expect(await alertReason()).toBe("check-failed");
  await user.click(screen.getByRole("button", { name: "Retry check" }));
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
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(client.calls.some((c) => c.method === "evener/auth/test")).toBe(false);
  scriptSave(client);
  client.on("evener/instance/list", () => savedList());
  await user.click(screen.getByRole("button", { name: "Retry check" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
  expect(client.calls.filter((c) => c.method === "evener/auth/apiKey/set")).toHaveLength(1);
});
test("optional local access checks the host without clearing or requiring a key", async () => {
  const { user, client, connected } = setup();
  await choose(user, "Local endpoint");
  expect(screen.getByLabelText("API key")).toHaveProperty("required", false);
  expect(screen.getByText("http://localhost:8080/v1")).toBeTruthy();
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await user.click(screen.getByRole("button", { name: "Check connection" }));
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
  await user.click(screen.getByRole("button", { name: "Save and check" }));
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
  await enterText(user, screen.getByLabelText("Name"), "azure-team");
  await user.type(screen.getByLabelText("AZURE_RESOURCE"), "team");
  await user.click(screen.getByRole("button", { name: "Create" }));
  await enterText(user, await screen.findByLabelText("API key"), "retained-draft");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(screen.getByTestId("connection-configured").textContent).toContain("azure-team");
  client.on("evener/auth/apiKey/set", () => {
    client.on("evener/instance/list", () => ({
      ...created,
      instances: [{ ...row, activeSource: "store", hasStoredFile: true }],
    }));
    return { ...saved, provider: "azure-team" };
  });
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await user.click(screen.getByRole("button", { name: "Save and check" }));
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
  await user.click(screen.getByRole("button", { name: "Save and check" }));
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
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  expect(screen.getByRole("button", { name: "Checking model list…" })).toHaveProperty("disabled", true);
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
  await user.click(screen.getByRole("button", { name: "Retry check" }));
  expect(screen.getByRole("button", { name: "Checking model list…" })).toHaveProperty("disabled", true);

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
  await user.click(screen.getByRole("button", { name: "Retry check" }));
  expect(screen.getByRole("button", { name: "Checking model list…" })).toHaveProperty("disabled", true);

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
  await user.click(screen.getByRole("button", { name: "Retry check" }));
  expect(screen.getByRole("button", { name: "Checking model list…" })).toHaveProperty("disabled", true);

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
  fireEvent.click(screen.getByRole("button", { name: "Save and check" }));
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
  fireEvent.click(screen.getByRole("button", { name: "Save and check" }));
  // Flush the save/refresh microtask chain to the pending check without
  // advancing the clock (0ms drains microtasks, fires no timer).
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(screen.getByRole("button", { name: "Checking model list…" })).toHaveProperty("disabled", true);
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
  const { user, client, connected } = setup({ instances: [], availableProviders: catalogue, writesRefused: true });
  scriptSave(client);
  await choose(user);
  await user.type(screen.getByLabelText("API key"), "draft");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
});
test("switching to existing host access does not send the new key or claim authentication is disabled", async () => {
  const { user, client, connected } = setup(
    savedList("anthropic", { activeSource: "env:ANTHROPIC_API_KEY", hasStoredFile: false }),
  );
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "unused-draft");
  await user.click(screen.getByText("Advanced settings"));
  await user.click(screen.getByRole("button", { name: "Use existing host access" }));
  client.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  await user.click(screen.getByRole("button", { name: "Check connection" }));
  await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("anthropic"));
  expect(client.calls.some((c) => c.method === "evener/auth/apiKey/set" || c.method === "evener/auth/logout")).toBe(
    false,
  );
});

test("adopting a created instance drops a prior host-access choice instead of inheriting it", async () => {
  const { user, client } = setup(savedList("anthropic"));
  await choose(user);
  await enterText(user, screen.getByLabelText("API key"), "unused-draft");
  await user.click(screen.getByText("Advanced settings"));
  await user.click(screen.getByRole("button", { name: "Use existing host access" }));
  expect(screen.queryByLabelText("API key")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Configure another instance" }));
  await user.selectOptions(screen.getByLabelText("Base provider"), "openai");
  await enterText(user, screen.getByLabelText("Name"), "openai-team");
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
  await enterText(user, screen.getByLabelText("API key"), "anthropic-private-draft");
  await user.click(screen.getByText("Advanced settings"));
  await user.click(screen.getByRole("button", { name: "Configure another instance" }));
  await user.selectOptions(screen.getByLabelText("Base provider"), "openai");
  await enterText(user, screen.getByLabelText("Name"), "openai-team");
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
  await enterText(user, screen.getByLabelText("Name"), "retained-name");
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

  expect(await alertReason()).toBe("reload-failed");
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
    await enterText(user, screen.getByLabelText("API key"), "anthropic-private-draft");
    await user.click(screen.getByText("Advanced settings"));
    await user.click(screen.getByRole("button", { name: "Configure another instance" }));
    await user.selectOptions(screen.getByLabelText("Base provider"), base);
    await enterText(user, screen.getByLabelText("Name"), "recovered-team");
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
  await enterText(user, screen.getByLabelText("API key"), "sk-ant-draft");
  // The displayed URL does not move; the endpoint this name resolves to does.
  // The saved listing reports the source the save produced, so the review can
  // only come from the endpoint identity.
  scriptSave(client, fingerprintList("fp-2025", "store"));

  await user.click(screen.getByRole("button", { name: "Save and check" }));

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

  await user.click(screen.getByRole("button", { name: "Save and check" }));

  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
  expect(screen.getByLabelText("API key")).toHaveProperty("value", "");
  expect(await alertReason()).toBe("endpoint-changed");
});

// The adopted instance is a different connection. The draft typed for the
// previous destination must not survive the adoption, or a key meant for the
// endpoint the user was shown could be saved to the newly created one.
test("adopting a created instance clears a credential draft typed for the previous endpoint", async () => {
  const { user, client } = setup(fingerprintList("fp-original"));
  await choose(user, "Anthropic");
  await enterText(user, screen.getByLabelText("API key"), "old-endpoint-key");

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
  await enterText(user, screen.getByLabelText("Name"), "anthropic-team");
  await enterText(user, screen.getByLabelText(/base url/i), "https://moved.example/v1");
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
  await enterText(user, screen.getByLabelText("API key"), "anthropic-private-draft");

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
  await enterText(user, screen.getByLabelText("Name"), "anthropic-team");
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

  await user.click(screen.getByRole("button", { name: "Save and check" }));

  expect(await alertReason()).toBe("endpoint-changed");
  expect(credentialsStore.getState().selfRefresh).toBeGreaterThan(before);
  // Nothing is left mid-flight by the read that no longer resets the flow.
  expect(await screen.findByRole("button", { name: "Save and check" })).toHaveProperty("disabled", false);
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

  await user.click(screen.getByRole("button", { name: "Save and check" }));

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

  await user.click(screen.getByRole("button", { name: "Save and check" }));

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
  await user.click(screen.getByRole("button", { name: "Check connection" }));
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
  await user.click(screen.getByRole("button", { name: "Save and check" }));

  // Nothing reached the hub: no key write, no credential-JSON write, and no
  // probe against an endpoint the hub cannot describe.
  expect(client.calls.find((call) => call.method === "evener/auth/apiKey/set")).toBeUndefined();
  expect(client.calls.some((call) => call.method === "evener/auth/credentialJson/set")).toBe(false);
  expect(client.calls.some((call) => call.method === "evener/auth/test")).toBe(false);
  // The write refusal, not the check refusal.
  expect(await alertReason()).toBe("fingerprint-unavailable");
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

  await user.click(screen.getByRole("button", { name: "Save and check" }));

  expect(await alertReason()).toBe("endpoint-changed");
  expect(reasonShown("check-failed")).toBe(false);
});
