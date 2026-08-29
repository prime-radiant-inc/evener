import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { InstanceEntry } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { InstanceDetailSheet } from "./InstanceDetailSheet";

function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "type">): InstanceEntry {
  return {
    apiStyle: "",
    baseUrl: "",
    isDefault: false,
    activeSource: "absent",
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
    renderSheet(instance({ name: "openai-work", type: "openai", isDefault: true }));
    expect(screen.getByRole("dialog", { name: "openai-work" })).toBeTruthy();
    expect(screen.getByText(/default/i)).toBeTruthy();
  });

  test("the close button calls onClose", async () => {
    const user = userEvent.setup();
    const { onClose } = renderSheet(instance({ name: "a", type: "x" }));
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalled();
  });

  test("closes itself when the instance disappears from the store", async () => {
    const { onClose } = renderSheet(instance({ name: "a", type: "x" }));
    expect(screen.getByRole("dialog", { name: "a" })).toBeTruthy();
    credentialsStore.setState({ instances: [] });
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  test("the heading dot reads idle for a configured instance", () => {
    renderSheet(instance({ name: "a", type: "x", hasStoredFile: true, activeSource: "file" }));
    expect(screen.getByRole("img", { name: "Idle" })).toBeTruthy();
  });

  test("the heading dot reads ended when a required key is missing", () => {
    renderSheet(instance({ name: "a", type: "x", activeSource: "absent", credentialRequired: true }));
    expect(screen.getByRole("img", { name: "Ended" })).toBeTruthy();
  });
});

describe("credential display", () => {
  test("multiple layers show effective + shadowed chips", () => {
    renderSheet(
      instance({ name: "a", type: "openai", hasStoredOAuth: true, hasStoredFile: true, activeSource: "oauth" }),
    );
    expect(screen.getByText("effective")).toBeTruthy();
    expect(screen.getByText("shadowed")).toBeTruthy();
    expect(screen.getByText(/Configured via stored API key/)).toBeTruthy();
  });

  test("an oauth layer carries the stored email", () => {
    renderSheet(
      instance({ name: "a", type: "openai", hasStoredOAuth: true, storedEmail: "me@x.com", activeSource: "oauth" }),
    );
    expect(screen.getByText(/Configured via OAuth \(me@x\.com\)/)).toBeTruthy();
  });

  test("an unconfigured instance shows its label instead of layers", () => {
    renderSheet(instance({ name: "a", type: "x", activeSource: "absent" }));
    expect(screen.getByText("Not configured")).toBeTruthy();
    expect(screen.queryByText("effective")).toBeNull();
  });
});

describe("the meta table", () => {
  test("shows the instance type, and the API style/base row only when present", () => {
    renderSheet(
      instance({
        name: "a",
        type: "openai",
        apiStyle: "responses",
        baseUrl: "https://x",
        hasStoredFile: true,
        activeSource: "file",
      }),
    );
    expect(screen.getByText("Type")).toBeTruthy();
    expect(screen.getByText("openai")).toBeTruthy();
    expect(screen.getByText("responses · base https://x")).toBeTruthy();
  });

  test("omits the API row when neither apiStyle nor baseUrl is set", () => {
    renderSheet(instance({ name: "a", type: "x", hasStoredFile: true, activeSource: "file" }));
    expect(screen.queryByText("API")).toBeNull();
  });
});

describe("actions are conditionally rendered", () => {
  test("Set key only when authModes includes apiKey", () => {
    renderSheet(instance({ name: "a", type: "x", authModes: ["oauth"] }));
    expect(screen.queryByRole("button", { name: /set key|replace key/i })).toBeNull();
  });

  test("Sign in… only when authModes includes oauth", () => {
    renderSheet(instance({ name: "a", type: "x", authModes: ["apiKey"] }));
    expect(screen.queryByRole("button", { name: /sign in|refresh oauth/i })).toBeNull();
  });

  test("Clear only when activeSource is file or oauth", () => {
    renderSheet(instance({ name: "a", type: "x", activeSource: "env", envVar: "X_KEY" }));
    expect(screen.queryByRole("button", { name: "Clear" })).toBeNull();
  });

  test("Edit and Remove are always present", () => {
    renderSheet(instance({ name: "a", type: "x" }));
    expect(screen.getByRole("button", { name: "Edit" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Remove" })).toBeTruthy();
  });

  test("make default only when not already default", () => {
    renderSheet(instance({ name: "a", type: "x", isDefault: false }));
    expect(screen.getByRole("button", { name: /make default/i })).toBeTruthy();
  });

  test("make default hidden when already default", () => {
    renderSheet(instance({ name: "a", type: "x", isDefault: true }));
    expect(screen.queryByRole("button", { name: /make default/i })).toBeNull();
  });
});

describe("action labels follow stored state", () => {
  test("'Set key' when no file-sourced key exists", () => {
    renderSheet(instance({ name: "a", type: "x", authModes: ["apiKey"], hasStoredFile: false }));
    expect(screen.getByRole("button", { name: "Set key" })).toBeTruthy();
  });

  test("'Replace key' whenever a file-sourced key exists, even if a different source is currently effective", () => {
    renderSheet(
      instance({
        name: "a",
        type: "x",
        authModes: ["apiKey"],
        hasStoredFile: true,
        hasStoredOAuth: true,
        activeSource: "oauth",
      }),
    );
    expect(screen.getByRole("button", { name: "Replace key" })).toBeTruthy();
  });

  test("'Sign in…' when no OAuth is stored", () => {
    renderSheet(instance({ name: "a", type: "x", authModes: ["oauth"], hasStoredOAuth: false }));
    expect(screen.getByRole("button", { name: "Sign in…" })).toBeTruthy();
  });

  test("'Refresh OAuth' once signed in", () => {
    renderSheet(instance({ name: "a", type: "x", authModes: ["oauth"], hasStoredOAuth: true }));
    expect(screen.getByRole("button", { name: "Refresh OAuth" })).toBeTruthy();
  });
});

describe("action callbacks fire", () => {
  test("clicking each action calls its handler", async () => {
    const user = userEvent.setup();
    const { handlers } = renderSheet(
      instance({
        name: "a",
        type: "openai",
        authModes: ["apiKey", "oauth"],
        hasStoredFile: true,
        activeSource: "file",
      }),
    );
    await user.click(screen.getByRole("button", { name: "Replace key" }));
    expect(handlers.onSetApiKey).toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Sign in…" }));
    expect(handlers.onOAuthStart).toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Clear" }));
    expect(handlers.onClear).toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Edit" }));
    expect(handlers.onEdit).toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Remove" }));
    expect(handlers.onRemove).toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: /make default/i }));
    expect(handlers.onSetDefault).toHaveBeenCalled();
  });

  test("clicking Test credentials calls its handler", async () => {
    const user = userEvent.setup();
    const { handlers } = renderSheet(instance({ name: "a", type: "x" }));
    await user.click(screen.getByRole("button", { name: "Test credentials" }));
    expect(handlers.onTestCredentials).toHaveBeenCalledTimes(1);
  });

  test("pending verification disables only the Test credentials action", () => {
    renderSheet(instance({ name: "a", type: "x" }), { testCredentialsPending: true });
    expect((screen.getByRole("button", { name: "Testing credentials…" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Edit" }) as HTMLButtonElement).disabled).toBe(false);
    expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
  });

  test("a credential test result renders as a status line", () => {
    renderSheet(instance({ name: "a", type: "x" }), {
      testCredentialsResult: { provider: "a", status: "success", message: "Credentials verified." },
    });
    expect(screen.getByRole("status").textContent).toContain("Credentials verified.");
  });

  test("an unknown test status is sanitized to the endpoint-failure message", () => {
    renderSheet(instance({ name: "a", type: "x" }), {
      testCredentialsResult: { provider: "a", status: "garbage", message: "raw provider prose" },
    });
    const line = screen.getByRole("status");
    expect(line.textContent).toContain("The provider endpoint could not be reached.");
    expect(line.textContent).not.toContain("raw provider prose");
  });
});
