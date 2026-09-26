import type { InstanceEntry, InstanceListResponse, ProviderDescriptor } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { urlToPane } from "../../../../shell/routing";
import { connectionStore } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { setMutationClientIdentityForTests } from "../../../../stores/mutationClientIdentity";
import { resetToastStoreForTests } from "../../../../widgets/toast/store";
import { ConnectProviderDialog } from "./ConnectProviderDialog";
import { CredentialsSection } from "./CredentialsSection";

// This file covers the lazy chunk's entry component: the guided connect flow it
// renders (ProviderConnection has its own file for the flow's own behavior), the
// listing-level recovery around it, and the one handoff out of the flow - the
// settings pane's credentials section, where existing connections are managed.
function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "providerId">): InstanceEntry {
  const row: InstanceEntry = {
    protocol: "openai-chat",
    auth: "bearer",
    implicit: true,
    isDefault: false,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: true,
    ...overrides,
  };
  // A production row with a destination carries an endpoint fingerprint (the
  // hub keys one unless it cannot): these fixtures build reachable
  // destinations, so stand one in unless the test names its own.
  if ((row.baseUrl ?? "") !== "" && (row.endpointFingerprint ?? "") === "") {
    row.endpointFingerprint = "fp-fixture";
  }
  return row;
}

function provider(id: string, name: string, setup?: InstanceEntry): ProviderDescriptor {
  return {
    id,
    name,
    protocol: "openai-chat",
    auth: "bearer",
    implicit: true,
    authModes: setup?.authModes ?? ["apiKey"],
    ...(setup ? { setup } : {}),
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

/** The dialog as the app mounts it: `onClose` is what makes it go away, so the
 * harness owns that state and the test can see the dialog actually leave. */
function Harness({ onClose }: { onClose(): void }) {
  const [open, setOpen] = useState(true);
  if (!open) return null;
  return (
    <ConnectProviderDialog
      onClose={() => {
        setOpen(false);
        onClose();
      }}
      onConnected={() => {}}
    />
  );
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
  resetToastStoreForTests();
  // The auth mutations carry the page's identity on the wire; pinning it keeps
  // a recorded call's params from varying per run.
  setMutationClientIdentityForTests("test-tab");
});

afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  window.history.pushState({}, "", "/");
  vi.restoreAllMocks();
});

test("the picker offers the connect choices and hands existing connections to the provider settings", async () => {
  const row = instance({ name: "anthropic", providerId: "anthropic", authModes: ["apiKey"] });
  const fake = new FakeClient("ready");
  fake.on("evener/instance/list", () => ({
    instances: [row],
    availableProviders: [provider("anthropic", "Anthropic", row)],
  }));
  connectionStore.getState().connect(fake);
  const close = vi.fn();
  render(<Harness onClose={close} />);
  const user = userEvent.setup();

  // The guided discovery surface, and nothing of the management view it used to
  // grow: no instance rows, no per-row actions, no second settings dialog.
  expect(await screen.findByRole("button", { name: "Anthropic" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Full provider settings" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Add provider instance" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Test connection" })).toBeNull();

  await user.click(screen.getByText("Already configured access on this host?"));
  await user.click(screen.getByRole("button", { name: "Manage existing connections" }));

  // The affordance is a handoff, not a second editor: the real provider
  // settings are where an existing connection is listed, edited and tested, so
  // the dialog closes itself and the route lands on that pane's credentials
  // section - never a dialog left standing over the pane it hands off to.
  // Both the emitted path and the pane it resolves to are pinned: urlToPane
  // alone would accept any legacy alias that reaches the same section.
  expect(window.location.pathname).toBe("/settings/credentials");
  expect(urlToPane(window.location.pathname)).toEqual({ type: "settings", params: { section: "credentials" } });
  expect(close).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("the dialog still renders the guided flow and hands the connected name to its caller", async () => {
  const setup = instance({
    name: "anthropic",
    providerId: "anthropic",
    baseUrl: "https://api.anthropic.example/v1",
    authModes: ["apiKey"],
  });
  const fake = new FakeClient("ready");
  let saved = false;
  fake.on("evener/instance/list", () => ({
    instances: [saved ? { ...setup, activeSource: "store", hasStoredFile: true } : setup],
    availableProviders: [provider("anthropic", "Anthropic", setup)],
  }));
  fake.on("evener/auth/apiKey/set", () => {
    saved = true;
    return {
      provider: "anthropic",
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
      hasStoredFile: true,
    };
  });
  fake.on("evener/auth/test", () => ({ provider: "anthropic", status: "success", message: "" }));
  connectionStore.getState().connect(fake);
  const onConnected = vi.fn();
  render(<ConnectProviderDialog onClose={() => {}} onConnected={onConnected} />);
  const user = userEvent.setup();

  await user.click(await screen.findByRole("button", { name: "Anthropic" }));
  await user.type(screen.getByLabelText("API key"), "sk-guided");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await user.click(await screen.findByRole("button", { name: "Continue" }));

  expect(onConnected).toHaveBeenCalledWith("anthropic");
});

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
        availableProviders: [provider("anthropic", "Anthropic")],
      }));
      fake.emitReady();
      old.resolve({ instances: [], availableProviders: [] });
      await old.promise;
    });
    expect(
      view === "onboarding"
        ? await screen.findByRole("button", { name: "Anthropic" })
        : await screen.findByText("recovered-provider"),
    ).toBeTruthy();
    expect(credentialsStore.getState().loading).toBe(false);
  },
);

test("a registry load failure can be retried without closing the dialog", async () => {
  let attempts = 0;
  const fake = new FakeClient("ready");
  fake.on("evener/instance/list", () => {
    attempts += 1;
    if (attempts === 1) throw new Error("registry unavailable");
    return { instances: [], availableProviders: [provider("anthropic", "Anthropic")] };
  });
  connectionStore.getState().connect(fake);
  const close = vi.fn();
  render(<ConnectProviderDialog onClose={close} onConnected={() => {}} />);

  await screen.findByRole("alert");
  await userEvent.setup().click(screen.getByRole("button", { name: "Retry" }));
  expect(await screen.findByRole("button", { name: "Anthropic" })).toBeTruthy();
  expect(attempts).toBe(2);
  expect(close).not.toHaveBeenCalled();
});

// The dialog is a CONTROLLER-scoped editor: every instance/auth mutation it
// issues goes to the controller, so it must read the controller's listing --
// never a remote host's. Component 07b keeps a remote host's listing in its
// own store partition, which is what makes the mount-time fetch() here
// harmless to the spawn form's remote view.
test("mounting the dialog reads the controller's list and issues no proxied call", async () => {
  const fake = new FakeClient("ready");
  fake.on("evener/instance/list", () => ({
    instances: [instance({ name: "controller-only", providerId: "anthropic", activeSource: "store" })],
    availableProviders: [provider("anthropic", "Anthropic")],
  }));
  fake.on("evener/host/request", () => {
    throw new Error("the controller-scoped dialog must never route through the proxy");
  });
  connectionStore.getState().connect(fake);

  render(<ConnectProviderDialog onClose={() => {}} onConnected={() => {}} />);

  expect(await screen.findByRole("button", { name: "Anthropic" })).toBeTruthy();
  expect(fake.calls.some((call) => call.method === "evener/host/request")).toBe(false);
  expect(fake.calls.filter((call) => call.method === "evener/instance/list")).toHaveLength(1);
});
