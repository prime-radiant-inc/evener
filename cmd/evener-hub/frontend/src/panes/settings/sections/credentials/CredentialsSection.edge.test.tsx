// Edge cases for CredentialsSection.tsx uncovered lines:
// - handleConfirmedAction clear failure error toast (lines 153-154)
// - handleConfirmedAction remove failure error toast (lines 153-154)
// - findInstance returns undefined for apiKey dialog when instance is gone (line 223)
// - findInstance returns undefined for edit dialog when instance is gone (line 224)

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

const WORK = instance({
  name: "work",
  type: "anthropic",
  authModes: ["apiKey"],
  isDefault: true,
  hasStoredFile: true,
  activeSource: "file",
});
const PERSONAL = instance({ name: "personal", type: "openai", authModes: ["apiKey", "oauth"] });
const LIST: InstanceListResponse = { instances: [WORK, PERSONAL], availableTypes: ["anthropic", "openai"] };

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

// Lines 153-154: handleConfirmedAction clear failure error toast
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
    // Click Clear on the work instance (has stored file → Clear available)
    const clearButtons = screen.getAllByRole("button", { name: "Clear" });
    await user.click(clearButtons[0]!);
    const dialog = screen.getByRole("dialog");
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
    const removeButtons = screen.getAllByRole("button", { name: "Remove" });
    await user.click(removeButtons[1]!);
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Remove" }));
    await screen.findByText("Remove failed: Something went wrong.");
    expect(screen.getByRole("dialog", { name: "Remove instance" })).toBeTruthy();
  });

  // Lines 194, 223-224: an open API-key editor stops rendering if its instance disappears.
  test("an API key dialog closes when a refreshed list removes its instance", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("work");
    const user = userEvent.setup();
    const setKeyButtons = screen.getAllByRole("button", { name: /replace key/i });
    await user.click(setKeyButtons[0]!);
    await screen.findByRole("dialog", { name: "Set API key for work" });

    act(() => credentialsStore.setState({ instances: [PERSONAL] }));

    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Set API key for work" })).toBeNull());
  });

  // Lines 196, 228-229: an open edit dialog stops rendering if its instance disappears.
  test("an edit dialog closes when a refreshed list removes its instance", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("work");
    const user = userEvent.setup();
    const editButtons = screen.getAllByRole("button", { name: "Edit" });
    await user.click(editButtons[0]!);
    await screen.findByRole("dialog", { name: "Edit work" });

    act(() => credentialsStore.setState({ instances: [PERSONAL] }));

    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Edit work" })).toBeNull());
  });
});
