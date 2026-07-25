import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, describe, expect, test } from "vitest";
import { prefsStore, resetPrefsStoreForTests } from "../../../stores/prefs";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { TranscriptSection } from "./transcript";

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
      <TranscriptSection />
      <Toast />
    </>,
  );
}

test("every toggle defaults to unchecked", () => {
  renderWithToasts();
  expect(screen.getByRole("switch", { name: "Round timings" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.getByRole("switch", { name: "Token counts" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.getByRole("switch", { name: "Hook exits (all)" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.getByRole("switch", { name: "Hook exits (normal only)" }).getAttribute("aria-checked")).toBe("false");
  // Sentence-case fix: legacy copy is "Prompt Loaded" (Title Case), which
  // breaks the pattern the other 3 labels in this same section follow -
  // normalized here per this wave's sentence-case gate.
  expect(screen.getByRole("switch", { name: "Prompt loaded" }).getAttribute("aria-checked")).toBe("false");
});

test("toggling Round timings persists independently of the others and toasts Settings saved", async () => {
  const user = userEvent.setup();
  renderWithToasts();

  await user.click(screen.getByRole("switch", { name: "Round timings" }));

  expect(prefsStore.getState().transcript).toEqual({
    roundTimings: true,
    tokenCounts: false,
    hookExitsAll: false,
    hookExitsNormal: false,
    promptLoaded: false,
  });
  expect(await screen.findByText("Settings saved")).toBeTruthy();
});

// Token counts and Round timings gate two SEPARATE segments of the per-turn
// transcript meta line, so neither may imply the other.
test("toggling Token counts persists under its own key without switching Round timings on", async () => {
  const user = userEvent.setup();
  renderWithToasts();

  await user.click(screen.getByRole("switch", { name: "Token counts" }));

  expect(prefsStore.getState().transcript.tokenCounts).toBe(true);
  expect(prefsStore.getState().transcript.roundTimings).toBe(false);
  expect(localStorage.getItem("serf.prefs.transcriptTokenCounts")).toBe("1");
  expect(await screen.findByText("Settings saved")).toBeTruthy();
});

describe("Hook exits (all) / Hook exits (normal only)", () => {
  test("both can be independently on - all is not exclusive with normal-only", async () => {
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("switch", { name: "Hook exits (all)" }));
    await user.click(screen.getByRole("switch", { name: "Hook exits (normal only)" }));

    expect(prefsStore.getState().transcript.hookExitsAll).toBe(true);
    expect(prefsStore.getState().transcript.hookExitsNormal).toBe(true);
  });

  test("copy clarifies hookExitsAll is a superset of hookExitsNormal", () => {
    renderWithToasts();
    expect(screen.getByText(/The all-hooks setting includes these too\./)).toBeTruthy();
  });
});

test("toggling Prompt loaded persists under its own key", async () => {
  const user = userEvent.setup();
  renderWithToasts();

  await user.click(screen.getByRole("switch", { name: "Prompt loaded" }));

  expect(prefsStore.getState().transcript.promptLoaded).toBe(true);
  expect(localStorage.getItem("serf.prefs.transcriptPromptLoaded")).toBe("1");
});
