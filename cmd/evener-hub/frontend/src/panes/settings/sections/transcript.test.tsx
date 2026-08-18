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

// Every toggle that governs a line the transcript does not otherwise draw
// defaults OFF, except Round timings and "Prompt loaded". Prompt loaded
// governs the system-prompt scaffold and the per-load notice, both of which
// the transcript renders unconditionally today, so defaulting it off would
// delete a visible feature for everyone who never opened this pane. Round
// timings ships ON per the five-participant study (docs/web-ui/
// ux-plan-2026-07.md): its absence was the most-repeated complaint,
// independently, from four of the five participants.
test("the toggles that add a new line default off, except round timings; the one that governs existing items defaults on", () => {
  renderWithToasts();
  expect(screen.getByRole("switch", { name: "Round timings" }).getAttribute("aria-checked")).toBe("true");
  expect(screen.getByRole("switch", { name: "Token counts" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.getByRole("switch", { name: "Hook exits (all)" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.getByRole("switch", { name: "Hook exits (normal only)" }).getAttribute("aria-checked")).toBe("false");
  // Sentence-case fix: legacy copy is "Prompt Loaded" (Title Case), which
  // breaks the pattern the other 3 labels in this same section follow -
  // normalized here per this wave's sentence-case gate.
  expect(screen.getByRole("switch", { name: "Prompt loaded" }).getAttribute("aria-checked")).toBe("true");
});

test("toggling Round timings persists independently of the others and toasts Settings saved", async () => {
  const user = userEvent.setup();
  // Round timings ships on; start from off so the click below exercises the
  // same on-transition (and the same persisted key) this test always meant to.
  prefsStore.getState().setTranscriptStatus("roundTimings", false);
  renderWithToasts();

  await user.click(screen.getByRole("switch", { name: "Round timings" }));

  expect(prefsStore.getState().transcript).toEqual({
    roundTimings: true,
    tokenCounts: false,
    hookExitsAll: false,
    hookExitsNormal: false,
    promptLoaded: true,
  });
  expect(await screen.findByText("Settings saved")).toBeTruthy();
});

// Token counts and Round timings gate two SEPARATE segments of the per-turn
// transcript meta line, so neither may imply the other.
test("toggling Token counts persists under its own key without disturbing Round timings", async () => {
  const user = userEvent.setup();
  renderWithToasts();

  await user.click(screen.getByRole("switch", { name: "Token counts" }));

  expect(prefsStore.getState().transcript.tokenCounts).toBe(true);
  // Untouched by this click, so it stays at its shipped default (on).
  expect(prefsStore.getState().transcript.roundTimings).toBe(true);
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

// This one starts ON (see the defaults test above), so the interesting
// direction is turning it OFF - and the persisted "0" is what has to survive a
// reload, since an absent key reads back as the ON default.
test("toggling Prompt loaded off persists under its own key", async () => {
  const user = userEvent.setup();
  renderWithToasts();

  await user.click(screen.getByRole("switch", { name: "Prompt loaded" }));

  expect(prefsStore.getState().transcript.promptLoaded).toBe(false);
  expect(localStorage.getItem("serf.prefs.transcriptPromptLoaded")).toBe("0");

  await user.click(screen.getByRole("switch", { name: "Prompt loaded" }));

  expect(prefsStore.getState().transcript.promptLoaded).toBe(true);
  expect(localStorage.getItem("serf.prefs.transcriptPromptLoaded")).toBe("1");
});

// The intro copy dates from when all four toggles gated one "system status"
// blob. Round timings and Token counts annotate a turn rather than show a
// system event, so that framing now misdescribes half the section.
test("the intro describes the section as optional transcript detail, not system status items", () => {
  renderWithToasts();
  expect(screen.getByText(/Optional transcript detail\./)).toBeTruthy();
  expect(screen.queryByText(/System status items/)).toBeNull();
});
