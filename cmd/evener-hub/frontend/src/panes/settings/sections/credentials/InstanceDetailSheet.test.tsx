import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { InstanceEntry } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { InstanceDetailSheet } from "./InstanceDetailSheet";

function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "providerId">): InstanceEntry {
  return {
    protocol: "openai-chat",
    auth: "bearer",
    implicit: false,
    isDefault: false,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: true,
    ...overrides,
  };
}

function noopHandlers() {
  return {
    onTestCredentials: vi.fn(),
    onSetApiKey: vi.fn(),
    onOAuthStart: vi.fn(),
    onEdit: vi.fn(),
    onClear: vi.fn(),
    onRemove: vi.fn(),
    onSetDefault: vi.fn(),
  };
}

function renderSheet(inst: InstanceEntry | null, extra: Partial<Parameters<typeof InstanceDetailSheet>[0]> = {}) {
  const handlers = noopHandlers();
  const onClose = vi.fn();
  credentialsStore.setState({ instances: inst === null ? [] : [inst] });
  render(<InstanceDetailSheet name={inst?.name ?? null} onClose={onClose} {...handlers} {...extra} />);
  return { handlers, onClose };
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("visibility", () => {
  test("renders nothing when name is null", () => {
    renderSheet(null);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  test("renders a dialog named after the instance, with the default chip when set", () => {
    renderSheet(instance({ name: "openai-work", providerId: "openai", isDefault: true }));
    expect(screen.getByRole("dialog", { name: "openai-work" })).toBeTruthy();
    expect(screen.getByText(/default/i)).toBeTruthy();
  });

  test("the close button calls onClose", async () => {
    const user = userEvent.setup();
    const { onClose } = renderSheet(instance({ name: "a", providerId: "x" }));
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalled();
  });

  test("closes itself when the instance disappears from the store", async () => {
    const { onClose } = renderSheet(instance({ name: "a", providerId: "x" }));
    expect(screen.getByRole("dialog", { name: "a" })).toBeTruthy();
    credentialsStore.setState({ instances: [] });
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  test("the heading dot reads idle for a configured instance", () => {
    renderSheet(instance({ name: "a", providerId: "x", hasStoredFile: true, activeSource: "store" }));
    expect(screen.getByRole("img", { name: "Idle" })).toBeTruthy();
  });

  test("the heading dot reads ended when a required key is missing", () => {
    renderSheet(instance({ name: "a", providerId: "x", activeSource: "none", credentialRequired: true }));
    expect(screen.getByRole("img", { name: "Ended" })).toBeTruthy();
  });
});

describe("credential display", () => {
  test("a stored key behind a higher-ranked source shows effective + shadowed chips", () => {
    renderSheet(
      instance({
        name: "a",
        providerId: "anthropic",
        hasStoredFile: true,
        activeSource: "api_key",
      }),
    );
    expect(screen.getByText("effective")).toBeTruthy();
    expect(screen.getByText("shadowed")).toBeTruthy();
    expect(screen.getByText(/Configured via providers\.toml/)).toBeTruthy();
    expect(screen.getByText(/Configured via stored API key/)).toBeTruthy();
  });

  test("an oauth layer carries the stored email", () => {
    renderSheet(
      instance({
        name: "a",
        providerId: "openai-codex",
        auth: "oauth-openai-codex",
        hasStoredOAuth: true,
        storedEmail: "me@x.com",
        activeSource: "oauth",
      }),
    );
    expect(screen.getByText(/Configured via OAuth \(me@x\.com\)/)).toBeTruthy();
  });

  test("an unconfigured instance shows its label instead of layers", () => {
    renderSheet(instance({ name: "a", providerId: "x", activeSource: "none" }));
    expect(screen.getByText("Not configured")).toBeTruthy();
    expect(screen.queryByText("effective")).toBeNull();
  });
});

describe("the meta table", () => {
  test("shows the instance's provider and its protocol/base URL", () => {
    renderSheet(
      instance({
        name: "a",
        providerId: "openai",
        protocol: "openai-responses",
        baseUrl: "https://x",
        hasStoredFile: true,
        activeSource: "store",
      }),
    );
    expect(screen.getByText("Provider")).toBeTruthy();
    expect(screen.getByText("openai")).toBeTruthy();
    expect(screen.getByText("openai-responses · base https://x")).toBeTruthy();
  });

  // protocol has no omitempty on the wire, so the API row always has
  // something to show.
  test("the API row shows the protocol alone when no base URL is set", () => {
    renderSheet(
      instance({ name: "a", providerId: "x", protocol: "openai-chat", hasStoredFile: true, activeSource: "store" }),
    );
    expect(screen.getByText("openai-chat")).toBeTruthy();
  });
});

describe("actions are conditionally rendered", () => {
  test("Set key only when authModes includes apiKey", () => {
    renderSheet(instance({ name: "a", providerId: "x", authModes: ["oauth"] }));
    expect(screen.queryByRole("button", { name: /set key|replace key/i })).toBeNull();
  });

  test("Sign in… only when authModes includes oauth", () => {
    renderSheet(instance({ name: "a", providerId: "x", authModes: ["apiKey"] }));
    expect(screen.queryByRole("button", { name: /sign in|refresh oauth/i })).toBeNull();
  });

  test("Clear only when activeSource is store or oauth", () => {
    renderSheet(instance({ name: "a", providerId: "x", activeSource: "env:X_API_KEY", envVar: "X_API_KEY" }));
    expect(screen.queryByRole("button", { name: "Clear" })).toBeNull();
  });

  test("Clear once a stored key is what resolves", () => {
    renderSheet(instance({ name: "a", providerId: "x", activeSource: "store", hasStoredFile: true }));
    expect(screen.getByRole("button", { name: "Clear" })).toBeTruthy();
  });

  test("Edit and Remove are both offered for a non-implicit instance", () => {
    renderSheet(instance({ name: "a", providerId: "x" }));
    expect(screen.getByRole("button", { name: "Edit" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Remove" })).toBeTruthy();
  });

  // Removing an implicit instance is refused server-side (spec §11.3), so
  // the sheet must not even offer the button; Edit stays, since editing an
  // implicit instance writes a shadow rather than changing it.
  test("an implicit instance offers Edit but no Remove", () => {
    renderSheet(instance({ name: "groq", providerId: "groq", implicit: true }));
    expect(screen.getByRole("button", { name: "Edit" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Remove" })).toBeNull();
  });

  // The danger zone is Clear + Remove under a divider; an implicit instance
  // with nothing stored offers neither, so the divider must go too rather
  // than trailing an empty section.
  test("no danger-zone divider when the instance offers neither Clear nor Remove", () => {
    renderSheet(instance({ name: "groq", providerId: "groq", implicit: true, activeSource: "none" }));
    expect(document.querySelectorAll("hr").length).toBe(0);
  });

  test("the danger-zone divider stays when Clear alone is offered", () => {
    renderSheet(
      instance({ name: "groq", providerId: "groq", implicit: true, activeSource: "store", hasStoredFile: true }),
    );
    expect(document.querySelectorAll("hr").length).toBe(1);
  });

  test("make default only when not already default", () => {
    renderSheet(instance({ name: "a", providerId: "x", isDefault: false }));
    expect(screen.getByRole("button", { name: /make default/i })).toBeTruthy();
  });

  test("make default hidden when already default", () => {
    renderSheet(instance({ name: "a", providerId: "x", isDefault: true }));
    expect(screen.queryByRole("button", { name: /make default/i })).toBeNull();
  });
});

describe("action labels follow stored state", () => {
  test("'Set key' when no stored key exists", () => {
    renderSheet(instance({ name: "a", providerId: "x", authModes: ["apiKey"], hasStoredFile: false }));
    expect(screen.getByRole("button", { name: "Set key" })).toBeTruthy();
  });

  test("'Replace key' whenever a stored key exists, and the sheet says that key is shadowed", () => {
    renderSheet(
      instance({
        name: "a",
        providerId: "x",
        authModes: ["apiKey"],
        hasStoredFile: true,
        activeSource: "api_key",
      }),
    );
    expect(screen.getByRole("button", { name: "Replace key" })).toBeTruthy();
    // Replacing a key that providers.toml outranks changes nothing the
    // instance actually uses, so the shadowed chip has to be on screen next
    // to the offer.
    expect(screen.getByText("shadowed")).toBeTruthy();
  });

  test("'Sign in…' when no OAuth is stored", () => {
    renderSheet(
      instance({
        name: "a",
        providerId: "openai-codex",
        auth: "oauth-openai-codex",
        authModes: ["oauth"],
        hasStoredOAuth: false,
      }),
    );
    expect(screen.getByRole("button", { name: "Sign in…" })).toBeTruthy();
  });

  test("'Refresh OAuth' once signed in", () => {
    renderSheet(
      instance({
        name: "a",
        providerId: "openai-codex",
        auth: "oauth-openai-codex",
        authModes: ["oauth"],
        hasStoredOAuth: true,
        activeSource: "oauth",
      }),
    );
    expect(screen.getByRole("button", { name: "Refresh OAuth" })).toBeTruthy();
  });

  // authModesFor maps each auth scheme to a fixed, non-overlapping set, so a
  // bearer instance is never oauth-capable however its credential is stored.
  test("a bearer-auth instance never offers Sign in, even with a stored key", () => {
    renderSheet(
      instance({
        name: "a",
        providerId: "openai",
        auth: "bearer",
        authModes: ["apiKey"],
        hasStoredFile: true,
        activeSource: "store",
      }),
    );
    expect(screen.queryByRole("button", { name: /sign in|refresh oauth/i })).toBeNull();
  });
});

describe("action callbacks fire", () => {
  test("clicking each action calls its handler", async () => {
    const user = userEvent.setup();
    const { handlers } = renderSheet(
      instance({
        name: "a",
        providerId: "anthropic",
        authModes: ["apiKey"],
        hasStoredFile: true,
        activeSource: "store",
      }),
    );
    await user.click(screen.getByRole("button", { name: "Replace key" }));
    expect(handlers.onSetApiKey).toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Clear" }));
    expect(handlers.onClear).toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Edit" }));
    expect(handlers.onEdit).toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Remove" }));
    expect(handlers.onRemove).toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: /make default/i }));
    expect(handlers.onSetDefault).toHaveBeenCalled();
  });

  test("clicking Sign in calls its handler", async () => {
    const user = userEvent.setup();
    const { handlers } = renderSheet(
      instance({ name: "a", providerId: "openai-codex", auth: "oauth-openai-codex", authModes: ["oauth"] }),
    );
    await user.click(screen.getByRole("button", { name: "Sign in…" }));
    expect(handlers.onOAuthStart).toHaveBeenCalled();
  });

  test("clicking Test credentials calls its handler", async () => {
    const user = userEvent.setup();
    const { handlers } = renderSheet(instance({ name: "a", providerId: "x" }));
    await user.click(screen.getByRole("button", { name: "Test credentials" }));
    expect(handlers.onTestCredentials).toHaveBeenCalledTimes(1);
  });

  test("pending verification disables only the Test credentials action", () => {
    renderSheet(instance({ name: "a", providerId: "x" }), { testCredentialsPending: true });
    expect((screen.getByRole("button", { name: "Testing credentials…" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Edit" }) as HTMLButtonElement).disabled).toBe(false);
    expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
  });

  test("a credential test result renders as a status line", () => {
    renderSheet(instance({ name: "a", providerId: "x" }), {
      testCredentialsResult: { provider: "a", status: "success", message: "Credentials verified." },
    });
    expect(screen.getByRole("status").textContent).toContain("Credentials verified.");
  });

  test("an unknown test status is sanitized to the endpoint-failure message", () => {
    renderSheet(instance({ name: "a", providerId: "x" }), {
      testCredentialsResult: { provider: "a", status: "garbage", message: "raw provider prose" },
    });
    const line = screen.getByRole("status");
    expect(line.textContent).toContain("The provider endpoint could not be reached.");
    expect(line.textContent).not.toContain("raw provider prose");
  });
});

// writesRefused is the wire's "providers.toml cannot be written" flag
// (InstanceListResponse, spec §11.3): it gates the evener/instance/* writes
// only. Set key/Sign in/Clear/Test credentials write the credentials store
// or an OAuth record, never providers.toml, so they stay live.
describe("writesRefused disables instance-CRUD actions only", () => {
  test("disables Edit, Remove, and make default", () => {
    renderSheet(instance({ name: "a", providerId: "x", isDefault: false }), { writesRefused: true });
    expect((screen.getByRole("button", { name: "Edit" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: /make default/i }) as HTMLButtonElement).disabled).toBe(true);
  });

  test("leaves Test credentials, Set/Replace key, and Clear enabled", () => {
    renderSheet(
      instance({
        name: "a",
        providerId: "x",
        authModes: ["apiKey"],
        hasStoredFile: true,
        activeSource: "store",
      }),
      { writesRefused: true },
    );
    expect((screen.getByRole("button", { name: "Test credentials" }) as HTMLButtonElement).disabled).toBe(false);
    expect((screen.getByRole("button", { name: "Replace key" }) as HTMLButtonElement).disabled).toBe(false);
    expect((screen.getByRole("button", { name: "Clear" }) as HTMLButtonElement).disabled).toBe(false);
  });

  test("an implicit instance under writesRefused still has no Remove button at all", () => {
    renderSheet(instance({ name: "groq", providerId: "groq", implicit: true }), { writesRefused: true });
    expect(screen.queryByRole("button", { name: "Remove" })).toBeNull();
  });
});
