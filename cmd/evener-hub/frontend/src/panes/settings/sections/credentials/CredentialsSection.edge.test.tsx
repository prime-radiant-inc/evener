// Edge cases for CredentialsSection.tsx uncovered lines:
// - handleConfirmedAction clear failure error toast
// - handleConfirmedAction remove failure error toast
// - findInstance returns undefined for apiKey dialog when instance is gone
// - findInstance returns undefined for edit dialog when instance is gone

import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { InstanceEntry, InstanceListResponse } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { Toast } from "../../../../widgets";
import { resetToastStoreForTests } from "../../../../widgets/toast/store";
import { CredentialsSection } from "./CredentialsSection";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

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

const WORK = instance({
  name: "work",
  providerId: "anthropic",
  authModes: ["apiKey"],
  isDefault: true,
  hasStoredFile: true,
  activeSource: "store",
});
const PERSONAL = instance({
  name: "personal",
  providerId: "openai-codex",
  auth: "oauth-openai-codex",
  authModes: ["oauth"],
});
const LIST: InstanceListResponse = { instances: [WORK, PERSONAL], availableProviders: [] };

// Same detail-sheet navigation path as CredentialsSection.test.tsx: every
// per-instance action is reached through the row's inspector.
async function openSheet(user: ReturnType<typeof userEvent.setup>, name: string): Promise<HTMLElement> {
  await user.click(screen.getByRole("button", { name: new RegExp(name) }));
  return screen.findByRole("dialog", { name });
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

describe("CredentialsSection edge cases", () => {
  test("clear failure shows error toast", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/auth/logout", () => {
      throw new Error("logout denied");
    });
    render(
      <>
        <Toast />
        <CredentialsSection sectionId="credentials" />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    // WORK has a stored key → its sheet offers Clear.
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: "Clear" }));
    const dialog = screen.getByRole("dialog", { name: "Clear credentials" });
    await user.click(within(dialog).getByRole("button", { name: "Clear" }));
    await screen.findByText("Clear failed: Something went wrong.");
    expect(screen.getByRole("dialog", { name: "Clear credentials" })).toBeTruthy();
  });

  test("remove failure shows error toast", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/instance/remove", () => {
      throw new Error("remove denied");
    });
    render(
      <>
        <Toast />
        <CredentialsSection sectionId="credentials" />
      </>,
    );
    await screen.findByText("personal");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    await user.click(within(inspector).getByRole("button", { name: "Remove" }));
    const dialog = screen.getByRole("dialog", { name: "Remove instance" });
    await user.click(within(dialog).getByRole("button", { name: "Remove" }));
    await screen.findByText("Remove failed: Something went wrong.");
    expect(screen.getByRole("dialog", { name: "Remove instance" })).toBeTruthy();
  });

  // An open API-key editor stops rendering if its instance disappears.
  test("an API key dialog closes when a refreshed list removes its instance", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: /replace key/i }));
    await screen.findByRole("dialog", { name: "Set API key for work" });

    act(() => credentialsStore.setState({ instances: [PERSONAL] }));

    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Set API key for work" })).toBeNull());
  });

  // An open edit dialog stops rendering if its instance disappears.
  test("an edit dialog closes when a refreshed list removes its instance", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: "Edit" }));
    await screen.findByRole("dialog", { name: "Edit work" });

    act(() => credentialsStore.setState({ instances: [PERSONAL] }));

    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Edit work" })).toBeNull());
  });
});
