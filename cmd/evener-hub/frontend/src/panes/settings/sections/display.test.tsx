import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test } from "vitest";
import { prefsStore, resetPrefsStoreForTests } from "../../../stores/prefs";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { DisplaySection } from "./display";

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
  // @ts-expect-error MemoryStorage is the deterministic browser storage seam.
  globalThis.localStorage = new MemoryStorage();
});

beforeEach(() => {
  localStorage.clear();
  resetPrefsStoreForTests();
  resetToastStoreForTests();
});

afterEach(cleanup);

function renderWithToasts() {
  render(
    <>
      <DisplaySection />
      <Toast />
    </>,
  );
}

test("Enter sends remains the only Display setting and defaults off", () => {
  renderWithToasts();
  expect(screen.getByRole("switch", { name: "Enter sends" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.queryByRole("switch", { name: "Show estimated cost" })).toBeNull();
  expect(screen.queryByText(/estimated cost/i)).toBeNull();
});

test("toggling Enter sends persists to the pinned key and toasts Settings saved", async () => {
  const user = userEvent.setup();
  renderWithToasts();

  await user.click(screen.getByRole("switch", { name: "Enter sends" }));

  expect(prefsStore.getState().enterToSend).toBe(true);
  expect(localStorage.getItem("evener.prefs.enterToSend")).toBe("1");
  expect(await screen.findByText("Settings saved")).toBeTruthy();
});

test("Enter sends help text explains both keybind modes", () => {
  renderWithToasts();
  expect(screen.getByText(/Default off: ⌘\/Ctrl-Enter sends, Enter inserts a newline\./)).toBeTruthy();
});
