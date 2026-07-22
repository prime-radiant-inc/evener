import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { prefsStore, resetPrefsStoreForTests } from "../../../stores/prefs";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { NotificationsSection } from "./notifications";

// See shell/rail/Rail.test.tsx's identical comment: Node 26 shadows jsdom's
// real window.localStorage with its own (non-functional under vitest)
// global, so every test file that touches localStorage needs this same
// small in-memory stand-in. Scoped to this file only.
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
});

// jsdom does not implement the Notification API at all - a minimal
// controllable stand-in, scoped to this file, mirroring the MemoryStorage
// precedent above. A plain object (not a class - Biome's own
// noStaticOnlyClass rejects a static-members-only class) with the same two
// members tests drive directly: real Notification.permission is a static
// getter, requestPermission() a static method.
const FakeNotification = {
  permission: "default" as NotificationPermission,
  requestPermission: vi.fn<() => Promise<NotificationPermission>>(),
};

beforeEach(() => {
  localStorage.clear();
  resetPrefsStoreForTests();
  resetToastStoreForTests();
  FakeNotification.permission = "default";
  FakeNotification.requestPermission = vi.fn<() => Promise<NotificationPermission>>();
  // @ts-expect-error test stand-in, see FakeNotification's own comment
  globalThis.Notification = FakeNotification;
});

afterEach(() => {
  cleanup();
  // @ts-expect-error undo the stand-in so other test files see no Notification global (matches jsdom's own default)
  delete globalThis.Notification;
});

function renderWithToasts() {
  render(
    <>
      <NotificationsSection />
      <Toast />
    </>,
  );
}

test("all 4 toggles default unchecked and Loud for defaults to Questions & errors (code-wins discrepancy resolution)", () => {
  renderWithToasts();
  expect(screen.getByRole("switch", { name: "Title bar count" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.getByRole("switch", { name: "Favicon dot" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.getByRole("switch", { name: "OS notification" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.getByRole("switch", { name: "Sound" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.getByRole("radio", { name: "Questions & errors" }).getAttribute("aria-checked")).toBe("true");
});

test("intro copy matches the code-wins defaults (does not claim title/favicon are on by default)", () => {
  renderWithToasts();
  expect(screen.queryByText(/Title and favicon default on/)).toBeNull();
});

describe("Title bar count / Favicon dot / Sound - plain toggles, no permission gate", () => {
  test("Title bar count persists independently and toasts Settings saved", async () => {
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("switch", { name: "Title bar count" }));

    expect(prefsStore.getState().notifications).toEqual({ title: true, favicon: false, os: false, sound: false });
    expect(await screen.findByText("Settings saved")).toBeTruthy();
  });

  test("Sound persists independently", async () => {
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("switch", { name: "Sound" }));

    expect(prefsStore.getState().notifications.sound).toBe(true);
    expect(prefsStore.getState().notifications.title).toBe(false);
  });
});

describe("OS notification - permission-gated toggle", () => {
  test("turning ON while permission is 'default' requests permission; granted commits true and toasts success", async () => {
    FakeNotification.permission = "default";
    FakeNotification.requestPermission.mockResolvedValue("granted");
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("switch", { name: "OS notification" }));

    expect(FakeNotification.requestPermission).toHaveBeenCalledTimes(1);
    expect(await screen.findByText("Settings saved")).toBeTruthy();
    expect(prefsStore.getState().notifications.os).toBe(true);
  });

  test("turning ON while permission is 'default' and the user denies: reverts to false, warning toast, no commit", async () => {
    FakeNotification.permission = "default";
    FakeNotification.requestPermission.mockResolvedValue("denied");
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("switch", { name: "OS notification" }));

    expect(await screen.findByText("Browser denied notification permission")).toBeTruthy();
    expect(prefsStore.getState().notifications.os).toBe(false);
    expect(screen.getByRole("switch", { name: "OS notification" }).getAttribute("aria-checked")).toBe("false");
  });

  test("if requestPermission() itself rejects, reverts to false but shows NO toast at all", async () => {
    FakeNotification.permission = "default";
    FakeNotification.requestPermission.mockRejectedValue(new Error("blocked"));
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("switch", { name: "OS notification" }));

    // Let the rejected promise's handler run before asserting silence.
    await vi.waitFor(() => expect(prefsStore.getState().notifications.os).toBe(false));
    expect(screen.queryByText("Settings saved")).toBeNull();
    expect(screen.queryByText("Browser denied notification permission")).toBeNull();
  });

  test("turning ON when permission is already 'granted' commits unconditionally without prompting again", async () => {
    FakeNotification.permission = "granted";
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("switch", { name: "OS notification" }));

    expect(FakeNotification.requestPermission).not.toHaveBeenCalled();
    expect(prefsStore.getState().notifications.os).toBe(true);
    expect(await screen.findByText("Settings saved")).toBeTruthy();
  });

  test("turning ON when permission is already 'denied' commits unconditionally (turning on never re-prompts)", async () => {
    FakeNotification.permission = "denied";
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("switch", { name: "OS notification" }));

    expect(FakeNotification.requestPermission).not.toHaveBeenCalled();
    expect(prefsStore.getState().notifications.os).toBe(true);
  });

  test("turning OFF never checks or re-requests permission, and commits unconditionally", async () => {
    prefsStore.getState().setNotification("os", true);
    FakeNotification.permission = "granted";
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("switch", { name: "OS notification" }));

    expect(FakeNotification.requestPermission).not.toHaveBeenCalled();
    expect(prefsStore.getState().notifications.os).toBe(false);
    expect(await screen.findByText("Settings saved")).toBeTruthy();
  });

  test("if the Notification API doesn't exist in this browser at all, turning on just commits (no crash)", async () => {
    // @ts-expect-error simulate a browser with no Notification global
    delete globalThis.Notification;
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("switch", { name: "OS notification" }));

    expect(prefsStore.getState().notifications.os).toBe(true);
  });
});

describe("Loud for", () => {
  test("choosing Everything needing me persists and toasts Settings saved", async () => {
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("radio", { name: "Everything needing me" }));

    expect(prefsStore.getState().notificationsLoudScope).toBe("all");
    expect(await screen.findByText("Settings saved")).toBeTruthy();
  });
});
