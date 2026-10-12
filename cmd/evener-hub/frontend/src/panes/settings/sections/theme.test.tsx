import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { prefsStore, resetPrefsStoreForTests } from "../../../stores/prefs";
import { Toast } from "../../../widgets";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { ThemeSection } from "./theme";

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
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

/** Waits for a success toast naming the theme value just chosen. */
async function themeToasted(value: string): Promise<void> {
  await waitFor(() => expect(getToasts().at(-1)).toMatchObject({ kind: "success" }));
  expect(getToasts().at(-1)?.text).toContain(value);
}

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
    await themeToasted("light");
  });

  test("choosing System after a concrete theme removes data-theme and does not toast a stale value", async () => {
    const user = userEvent.setup();
    renderWithToasts();
    await user.click(screen.getByRole("radio", { name: "Dark" }));
    await themeToasted("dark");

    await user.click(screen.getByRole("radio", { name: "System" }));

    expect(prefsStore.getState().theme).toBe("system");
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
    await themeToasted("system");
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
    expect(getToasts()).toEqual([]);
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
