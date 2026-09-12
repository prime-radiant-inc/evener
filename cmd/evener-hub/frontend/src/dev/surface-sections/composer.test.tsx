import { act, cleanup, fireEvent, render, within } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { resetAskDockStoreForTests } from "../../panes/session/composer/askDock/askDockStore";
import { flushPendingTurnsProjectionForTests } from "../../panes/session/composer/queue/pendingTurnsStore";
import { resetThreadsStoreForTests } from "../../stores/threads";
import ComposerSurfaceSection from "./composer";

afterEach(() => {
  cleanup();
  resetThreadsStoreForTests();
  resetAskDockStoreForTests();
  localStorage.clear();
});

test("each themed composer has a dedicated pane-width fixture and retains editable drafts and question selection", async () => {
  const { container } = render(<ComposerSurfaceSection />);
  // The section's mount effect seeds threadsStore with three real composer
  // fixtures; that schedules the pending-turns projection, which publishes
  // asynchronously. Own its tracked completion before the test body ends.
  await flushPendingTurnsProjectionForTests();
  const panes = container.querySelectorAll("[data-theme]");
  expect(panes).toHaveLength(2);
  for (const pane of panes) {
    const resting = within(pane as HTMLElement).getByText("resting").parentElement;
    const drafted = within(pane as HTMLElement).getByText("drafted").parentElement;
    const pending = within(pane as HTMLElement).getByText("ask pending (transcript trailing row)").parentElement;
    for (const fixture of [resting, drafted, pending]) {
      expect(fixture?.className).toMatch(/paneFixture/);
    }
    if (!drafted || !pending) throw new Error("missing gallery fixture");
    const input = within(drafted).getByRole("textbox") as HTMLTextAreaElement;
    expect(input.value).toContain("CHANGELOG");
    // focus() blurs the previously focused pane's textarea; the Composer's
    // blur handler (handleTextareaBlur) updates state in response, so the
    // raw DOM focus call must be owned.
    act(() => input.focus());
    fireEvent.change(input, { target: { value: "Keep this editable" } });
    expect(input.value).toBe("Keep this editable");
    expect(document.activeElement).toBe(input);
    expect(within(drafted).getByRole("button", { name: /^Send$/ })).toBeTruthy();
    const choice = within(pending).getByRole("radio", { name: /No, main only/ }) as HTMLInputElement;
    fireEvent.click(choice);
    expect(choice.checked).toBe(true);
  }
});
