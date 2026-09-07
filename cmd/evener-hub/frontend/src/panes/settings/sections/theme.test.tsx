import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, describe, expect, test } from "vitest";
import { prefsStore, resetPrefsStoreForTests } from "../../../stores/prefs";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { ThemeSection } from "./theme";

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
  document.documentElement.removeAttribute("data-theme");
  delete document.body.dataset.phoneDensity;
  delete document.body.dataset.fontSize;
  delete document.body.dataset.transcriptMeasure;
  resetPrefsStoreForTests();
  resetToastStoreForTests();
});

afterEach(cleanup);

function renderWithToasts() {
  render(
    <>
      <ThemeSection />
      <Toast />
    </>,
  );
}

describe("Color theme", () => {
  test("defaults to the System option checked", () => {
    renderWithToasts();
    expect(screen.getByRole("radio", { name: "System" }).getAttribute("aria-checked")).toBe("true");
  });

  test("choosing Light persists the pref, applies data-theme, and toasts 'Theme: light'", async () => {
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("radio", { name: "Light" }));

    expect(prefsStore.getState().theme).toBe("light");
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    expect(await screen.findByText("Theme: light")).toBeTruthy();
  });

  test("choosing System after a concrete theme removes data-theme and does not toast a stale value", async () => {
    const user = userEvent.setup();
    renderWithToasts();
    await user.click(screen.getByRole("radio", { name: "Dark" }));
    await screen.findByText("Theme: dark");

    await user.click(screen.getByRole("radio", { name: "System" }));

    expect(prefsStore.getState().theme).toBe("system");
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
    expect(await screen.findByText("Theme: system")).toBeTruthy();
  });
});

describe("Phone density", () => {
  test("defaults to Compact and does not toast on change (unlike Color theme)", async () => {
    const user = userEvent.setup();
    renderWithToasts();
    expect(screen.getByRole("radio", { name: "Compact" }).getAttribute("aria-checked")).toBe("true");

    await user.click(screen.getByRole("radio", { name: "Comfortable" }));

    expect(prefsStore.getState().phoneDensity).toBe("comfortable");
    expect(document.body.dataset.phoneDensity).toBe("comfortable");
    expect(screen.queryByText(/Settings saved/)).toBeNull();
  });

  // The help copy must name the gate that's actually shipped in tokens.css
  // (@media (max-width: 900px), matching useIsMobile's own breakpoint) -
  // not a stale number that names a different, unimplemented gate.
  test("the help copy states the shipped 900px density gate", () => {
    renderWithToasts();
    expect(screen.getByText(/phones \(≤900px\)/)).toBeTruthy();
  });
});

describe("Font size", () => {
  test("defaults to M and updates the pref plus document.body.dataset.fontSize", async () => {
    const user = userEvent.setup();
    renderWithToasts();
    expect(screen.getByRole("radio", { name: "M" }).getAttribute("aria-checked")).toBe("true");

    await user.click(screen.getByRole("radio", { name: "XL" }));

    expect(prefsStore.getState().fontSize).toBe("xl");
    expect(document.body.dataset.fontSize).toBe("xl");
  });
});

describe("Transcript width", () => {
  test("defaults to Reading and updates the pref plus document.body.dataset.transcriptMeasure", async () => {
    const user = userEvent.setup();
    renderWithToasts();
    expect(screen.getByRole("radio", { name: "Reading" }).getAttribute("aria-checked")).toBe("true");

    await user.click(screen.getByRole("radio", { name: "Wide" }));

    expect(prefsStore.getState().transcriptMeasure).toBe("wide");
    expect(document.body.dataset.transcriptMeasure).toBe("wide");
  });
});
