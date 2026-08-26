import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import {
  makeTranscriptDisplayConfig,
  shippedDefault,
  type TranscriptDisplayConfigV1,
} from "../../../transcriptDisplay/config";
import { makeTranscriptPreviewModel } from "../../../transcriptDisplay/previewFixture";
import { TranscriptDisplayCard } from "./TranscriptDisplayCard";

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

afterEach(cleanup);

test("renders controls before a production-backed example and inventories shown and hidden categories", () => {
  renderCard();
  const card = screen.getByTestId("transcript-display-card-desktop");
  const controls = screen.getByTestId("transcript-display-controls-desktop");
  const preview = screen.getByTestId("transcript-display-preview-desktop");

  expect(card).toBeTruthy();
  expect(controls.compareDocumentPosition(preview) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(screen.getByText("Example only—not your data")).toBeTruthy();
  expect(card.textContent).toMatch(/Shown:.*User messages.*Agent messages.*Critical rows/s);
  expect(card.textContent).toMatch(/Hidden:.*Reasoning/);
  expect(preview.querySelector('[data-testid="transcript-preview-flow"]')).toBeTruthy();
  expect(preview.querySelector('[data-tool-name="read_file"]')).toBeTruthy();
});

test("offers one isolated Advanced disclosure and updates a controlled draft immediately", async () => {
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

  expect(screen.getAllByRole("button", { name: /Advanced/ })).toHaveLength(1);
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
  expect(screen.getByTestId("transcript-display-card-desktop").textContent).toMatch(/Current detail: Activity/);
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
