import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { prefsStore, resetPrefsStoreForTests } from "../../../stores/prefs";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { toastKinds } from "../../../widgets/toast/toastTestUtils";
import { NotificationsSection } from "./notifications";

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

// jsdom does not implement the Notification API at all - a minimal
// controllable stand-in, scoped to this file. A plain object (not a class - Biome's own
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

test.each([
  [false, false],
  [true, false],
  [false, true],
  [true, true],
])("notification scope is available only for enabled channels, sound=%s desktop=%s", (sound, os) => {
  prefsStore.getState().setNotification("sound", sound);
  prefsStore.getState().setNotification("os", os);
  renderWithToasts();
  expect(screen.queryAllByRole("radio").length > 0).toBe(sound || os);
});

test("hiding and restoring sound options preserves the chosen notification scope", async () => {
  prefsStore.getState().setNotification("sound", true);
  renderWithToasts();
  const user = userEvent.setup();
  await user.click(screen.getByRole("radio", { name: "Everything needing me" }));
  await user.click(screen.getByRole("switch", { name: "Sound" }));
  expect(screen.queryAllByRole("radio")).toEqual([]);
  expect(prefsStore.getState().notificationsLoudScope).toBe("all");
  await user.click(screen.getByRole("switch", { name: "Sound" }));
  expect(screen.getByRole("radio", { name: "Everything needing me" }).getAttribute("aria-checked")).toBe("true");
});

test("title bar count defaults checked; favicon/OS/sound default unchecked; Loud for defaults to Questions, reply requests, approvals & errors", () => {
  renderWithToasts();
  expect(screen.getByRole("switch", { name: "Title bar count" }).getAttribute("aria-checked")).toBe("true");
  expect(screen.getByRole("switch", { name: "Browser tab indicator" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.getByRole("switch", { name: "Desktop notifications" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.getByRole("switch", { name: "Sound" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.queryAllByRole("radio")).toEqual([]);
  expect(prefsStore.getState().notificationsLoudScope).toBe("asks");
});

describe("Title bar count / Browser tab indicator / Sound - plain toggles, no permission gate", () => {
  // Title bar count now defaults ON, so the first click turns it OFF - the
  // opposite direction of every other toggle in this file, but the same
  // persist-and-toast behavior.
  test("Title bar count persists independently and toasts Settings saved", async () => {
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("switch", { name: "Title bar count" }));

    expect(prefsStore.getState().notifications).toEqual({ title: false, favicon: false, os: false, sound: false });
    await waitFor(() => expect(toastKinds()).toEqual(["success"]));
  });

  test("Sound persists independently", async () => {
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("switch", { name: "Sound" }));

    expect(prefsStore.getState().notifications.sound).toBe(true);
    // title stays at its shipped default (on) - untouched by this toggle.
    expect(prefsStore.getState().notifications.title).toBe(true);
  });
});

describe("Desktop notifications - permission-gated toggle", () => {
  test("turning ON while permission is 'default' requests permission; granted commits true and toasts success", async () => {
    FakeNotification.permission = "default";
    FakeNotification.requestPermission.mockResolvedValue("granted");
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("switch", { name: "Desktop notifications" }));

    expect(FakeNotification.requestPermission).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(toastKinds()).toEqual(["success"]));
    expect(prefsStore.getState().notifications.os).toBe(true);
  });

  test("turning ON while permission is 'default' and the user denies: reverts to false, warning toast, no commit", async () => {
    FakeNotification.permission = "default";
    FakeNotification.requestPermission.mockResolvedValue("denied");
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("switch", { name: "Desktop notifications" }));

    await waitFor(() => expect(toastKinds()).toEqual(["warning"]));
    expect(prefsStore.getState().notifications.os).toBe(false);
    expect(screen.getByRole("switch", { name: "Desktop notifications" }).getAttribute("aria-checked")).toBe("false");
  });

  test("if requestPermission() itself rejects, reverts to false but shows NO toast at all", async () => {
    FakeNotification.permission = "default";
    FakeNotification.requestPermission.mockRejectedValue(new Error("blocked"));
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("switch", { name: "Desktop notifications" }));

    // Let the rejected promise's handler run before asserting silence.
    await vi.waitFor(() => expect(prefsStore.getState().notifications.os).toBe(false));
    expect(toastKinds()).toEqual([]);
  });

  test("turning ON when permission is already 'granted' commits unconditionally without prompting again", async () => {
    FakeNotification.permission = "granted";
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("switch", { name: "Desktop notifications" }));

    expect(FakeNotification.requestPermission).not.toHaveBeenCalled();
    expect(prefsStore.getState().notifications.os).toBe(true);
    await waitFor(() => expect(toastKinds()).toEqual(["success"]));
  });

  test("turning ON when permission is already 'denied' commits unconditionally (turning on never re-prompts)", async () => {
    FakeNotification.permission = "denied";
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("switch", { name: "Desktop notifications" }));

    expect(FakeNotification.requestPermission).not.toHaveBeenCalled();
    expect(prefsStore.getState().notifications.os).toBe(true);
  });

  test("turning OFF never checks or re-requests permission, and commits unconditionally", async () => {
    prefsStore.getState().setNotification("os", true);
    FakeNotification.permission = "granted";
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("switch", { name: "Desktop notifications" }));

    expect(FakeNotification.requestPermission).not.toHaveBeenCalled();
    expect(prefsStore.getState().notifications.os).toBe(false);
    await waitFor(() => expect(toastKinds()).toEqual(["success"]));
  });

  test("if the Notification API doesn't exist in this browser at all, turning on just commits (no crash)", async () => {
    // @ts-expect-error simulate a browser with no Notification global
    delete globalThis.Notification;
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("switch", { name: "Desktop notifications" }));

    expect(prefsStore.getState().notifications.os).toBe(true);
  });
});

describe("Loud for", () => {
  test("choosing Everything needing me persists and toasts Settings saved", async () => {
    prefsStore.getState().setNotification("sound", true);
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("radio", { name: "Everything needing me" }));

    expect(prefsStore.getState().notificationsLoudScope).toBe("all");
    await waitFor(() => expect(toastKinds()).toEqual(["success"]));
  });
});
