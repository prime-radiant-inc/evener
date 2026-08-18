import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test } from "vitest";
import { prefsStore, resetPrefsStoreForTests } from "../../../stores/prefs";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { DisplaySection } from "./display";

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

// Both default off: the per-turn cost segment Show estimated cost gates is
// opt-in transcript detail, and the session total in the footer strip shows
// regardless.
test("Enter sends and Show estimated cost both default OFF", () => {
  renderWithToasts();
  expect(screen.getByRole("switch", { name: "Enter sends" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.getByRole("switch", { name: "Show estimated cost" }).getAttribute("aria-checked")).toBe("false");
});

test("toggling Enter sends persists to the PINNED serf.prefs.enterToSend key ('1'/'0' encoding, matching W5's shipped reader) and toasts Settings saved", async () => {
  const user = userEvent.setup();
  renderWithToasts();

  await user.click(screen.getByRole("switch", { name: "Enter sends" }));

  expect(prefsStore.getState().enterToSend).toBe(true);
  expect(localStorage.getItem("serf.prefs.enterToSend")).toBe("1");
  expect(await screen.findByText("Settings saved")).toBeTruthy();
});

test("toggling Show estimated cost persists to the PINNED serf.prefs.showCost key independently of Enter sends", async () => {
  const user = userEvent.setup();
  renderWithToasts();

  await user.click(screen.getByRole("switch", { name: "Show estimated cost" }));

  expect(prefsStore.getState().showCost).toBe(true);
  expect(prefsStore.getState().enterToSend).toBe(false); // unaffected
  expect(localStorage.getItem("serf.prefs.showCost")).toBe("1");
});

test("Show estimated cost help text says the session total stays in the footer", () => {
  renderWithToasts();
  expect(screen.getByText(/The session's total cost always shows in the footer\./)).toBeTruthy();
});

test("Enter sends help text explains both keybind modes", () => {
  renderWithToasts();
  expect(screen.getByText(/Default off: ⌘\/Ctrl-Enter sends, Enter inserts a newline\./)).toBeTruthy();
});
