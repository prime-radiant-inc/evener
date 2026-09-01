import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import type { ThreadModel } from "../../../protocol/model";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import { connectionStore } from "../../../stores/connection";
import { threadsStore } from "../../../stores/threads";
import {
  makeTranscriptDisplayConfig,
  shippedDefault,
  type TranscriptDisplayConfigV1,
} from "../../../transcriptDisplay/config";
import { makeTranscriptPreviewModel } from "../../../transcriptDisplay/previewFixture";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { TranscriptDisplayCard } from "./TranscriptDisplayCard";

const capturedPreviewBodies = vi.hoisted(
  () => [] as Array<{ model: ThreadModel; disclosureScope: string; sessionRef: string | undefined; surface: string }>,
);

vi.mock("../../session/transcript/TranscriptBody", async () => {
  const actual = await vi.importActual<typeof import("../../session/transcript/TranscriptBody")>(
    "../../session/transcript/TranscriptBody",
  );
  return {
    ...actual,
    TranscriptBody: (props: Parameters<typeof actual.TranscriptBody>[0]) => {
      capturedPreviewBodies.push({
        model: props.model,
        disclosureScope: props.disclosureScope,
        sessionRef: props.sessionRef,
        surface: props.surface,
      });
      return <actual.TranscriptBody {...props} />;
    },
  };
});

const confirmed = shippedDefault("desktop");

function renderCard(overrides: Partial<React.ComponentProps<typeof TranscriptDisplayCard>> = {}): void {
  render(
    <TranscriptDisplayCard
      layout="desktop"
      confirmed={confirmed}
      saveState="idle"
      onChange={vi.fn()}
      onRetry={vi.fn()}
      {...overrides}
    />,
  );
}

afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  capturedPreviewBodies.length = 0;
  resetDisclosureStoreForTests();
  vi.restoreAllMocks();
});

test("renders controls before a production-backed example and inventories shown and hidden categories", () => {
  renderCard();
  const card = screen.getByTestId("transcript-display-card-desktop");
  const controls = screen.getByTestId("transcript-display-controls-desktop");
  const canvas = screen.getByTestId("transcript-display-preview-canvas-desktop");
  const preview = screen.getByTestId("transcript-display-preview-desktop");
  const cardHeading = card.querySelector("h2");
  const exampleHeading = card.querySelector("h3");

  expect(card).toBeTruthy();
  expect(card.querySelector(":scope > div")).toBeTruthy();
  expect(card.querySelectorAll("h2")).toHaveLength(1);
  expect(card.querySelectorAll("h3")).toHaveLength(1);
  expect((card.textContent ?? "").match(/Hub revision/g)).toHaveLength(1);
  if (cardHeading === null || exampleHeading === null) throw new Error("card headings were not rendered");
  expect(cardHeading.compareDocumentPosition(exampleHeading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(card.textContent).not.toContain("Current detail");
  expect(screen.getAllByRole("radio", { checked: true })).toHaveLength(1);
  expect(controls.compareDocumentPosition(preview) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(canvas.contains(preview)).toBe(true);
  expect(preview.getAttribute("style")).toBeNull();
  expect(preview.style.width).toBe("");
  expect(screen.getByText("Example only—not your data")).toBeTruthy();
  expect(card.textContent).toMatch(/Shown:.*User messages.*Agent messages.*Critical rows/s);
  expect(card.textContent).toMatch(/Hidden:.*Reasoning/);
  expect(preview.querySelector('[data-testid="transcript-preview-flow"]')).toBeTruthy();
  expect(preview.querySelector('[data-tool-name="read_file"]')).toBeTruthy();
});

test("offers one isolated disclosure and updates a controlled draft immediately", async () => {
  const user = userEvent.setup();
  let value = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const onChange = vi.fn((next: TranscriptDisplayConfigV1) => {
    value = next;
  });
  const { rerender } = render(
    <TranscriptDisplayCard
      layout="desktop"
      confirmed={confirmed}
      draft={value}
      saveState="idle"
      onChange={onChange}
      onRetry={vi.fn()}
    />,
  );

  expect(screen.getAllByRole("group")).toHaveLength(1);
  expect(screen.getAllByRole("radio", { checked: true })).toHaveLength(1);
  await user.click(screen.getByRole("radio", { name: "Activity" }));
  expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ content: { kind: "preset", level: "activity" } }));
  rerender(
    <TranscriptDisplayCard
      layout="desktop"
      confirmed={confirmed}
      draft={value}
      saveState="idle"
      onChange={onChange}
      onRetry={vi.fn()}
    />,
  );
  expect(screen.getByRole("radio", { name: "Activity" }).getAttribute("aria-checked")).toBe("true");
});

test("makes the Mobile outer canvas the phone-width owner", () => {
  renderCard({ layout: "mobile", confirmed: shippedDefault("mobile") });
  const canvas = screen.getByTestId("transcript-display-preview-canvas-mobile");
  const host = screen.getByTestId("transcript-display-preview-mobile");

  expect(canvas.contains(host)).toBe(true);
  expect(host.className).not.toContain("mobile");
  expect(screen.getByText("Example only—not your data")).toBeTruthy();
});

test("isolates preview disclosures and never consults the live threads store", async () => {
  const rpcClient = new FakeClient("ready");
  const rpc = vi.spyOn(rpcClient, "request").mockImplementation(() => {
    throw new Error("preview must not use RPC");
  });
  connectionStore.setState({ state: "ready", client: rpcClient });
  const getState = vi.spyOn(threadsStore, "getState");
  const getInitialState = vi.spyOn(threadsStore, "getInitialState");
  const subscribe = vi.spyOn(threadsStore, "subscribe");
  capturedPreviewBodies.length = 0;
  const user = userEvent.setup();
  render(
    <>
      <TranscriptDisplayCard
        layout="desktop"
        confirmed={confirmed}
        saveState="idle"
        onChange={vi.fn()}
        onRetry={vi.fn()}
      />
      <TranscriptDisplayCard
        layout="mobile"
        confirmed={shippedDefault("mobile")}
        draft={makeTranscriptDisplayConfig({ kind: "preset", level: "tools" })}
        saveState="idle"
        onChange={vi.fn()}
        onRetry={vi.fn()}
      />
    </>,
  );

  const previewBodies = capturedPreviewBodies.filter((body) => body.surface === "preview");
  expect(previewBodies).toHaveLength(2);
  expect(previewBodies[0]?.model).not.toBe(previewBodies[1]?.model);
  expect(previewBodies.map((body) => body.disclosureScope)).toEqual([
    "settings:transcript-display:desktop",
    "settings:transcript-display:mobile",
  ]);
  expect(previewBodies.map((body) => body.sessionRef)).toEqual(["settings-preview:desktop", "settings-preview:mobile"]);
  expect(rpc).not.toHaveBeenCalled();

  const desktopPreview = screen.getByTestId("transcript-display-preview-desktop");
  const mobilePreview = screen.getByTestId("transcript-display-preview-mobile");
  // At tools level, the intent trigger (tool-row-trigger) controls the body
  // directly (legacy behavior — summaryOpen defaults true, no separate
  // body trigger). Use tool-row-trigger for body disclosure assertions.
  const desktopTrigger = [...desktopPreview.querySelectorAll<HTMLElement>('[data-testid="tool-row-trigger"]')].find(
    (trigger) => trigger.closest('[data-tool-name="read_file"]') !== null,
  );
  const mobileTrigger = [...mobilePreview.querySelectorAll<HTMLElement>('[data-testid="tool-row-trigger"]')].find(
    (trigger) => trigger.closest('[data-tool-name="read_file"]') !== null,
  );
  if (!(desktopTrigger instanceof HTMLElement) || !(mobileTrigger instanceof HTMLElement)) {
    throw new Error("preview read_file trigger was not rendered");
  }
  expect(desktopTrigger.getAttribute("aria-expanded")).toBe("false");
  expect(mobileTrigger.getAttribute("aria-expanded")).toBe("false");
  expect(getState).not.toHaveBeenCalled();
  expect(getInitialState).not.toHaveBeenCalled();
  expect(subscribe).not.toHaveBeenCalled();
  expect(rpc).not.toHaveBeenCalled();

  await user.click(desktopTrigger);
  expect(desktopTrigger.getAttribute("aria-expanded")).toBe("true");
  expect(mobileTrigger.getAttribute("aria-expanded")).toBe("false");
  expect(getState).not.toHaveBeenCalled();
  expect(getInitialState).not.toHaveBeenCalled();
  expect(subscribe).not.toHaveBeenCalled();
});

test("shows local override, saving, and retryable failure without claiming success", () => {
  const onRetry = vi.fn();
  renderCard({
    localOverride: makeTranscriptDisplayConfig({ kind: "preset", level: "full" }),
    saveState: "saving",
  });
  expect(screen.getByText(/browser-local live view is overriding this hub default/i)).toBeTruthy();
  expect(screen.getByRole("status").textContent).toMatch(/Saving/);

  cleanup();
  renderCard({ saveState: "error", error: "revision conflict", onRetry });
  expect(screen.getByRole("alert").textContent).toContain("revision conflict");
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  screen.getByRole("button", { name: "Retry" }).click();
  expect(onRetry).toHaveBeenCalledTimes(1);
  expect(screen.queryByText("Settings saved")).toBeNull();
});

test("uses a fresh deterministic preview model for the card", () => {
  const first = makeTranscriptPreviewModel();
  const second = makeTranscriptPreviewModel();
  expect(first).not.toBe(second);
  expect(first.turns[0]?.items[0]?.startedAt).toBe(second.turns[0]?.items[0]?.startedAt);
});
