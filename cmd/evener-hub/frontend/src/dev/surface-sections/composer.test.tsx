import { cleanup, fireEvent, render, within } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { resetAskDockStoreForTests } from "../../panes/session/composer/askDock/askDockStore";
import { resetThreadsStoreForTests } from "../../stores/threads";
import ComposerSurfaceSection from "./composer";

afterEach(() => {
  cleanup();
  resetThreadsStoreForTests();
  resetAskDockStoreForTests();
  localStorage.clear();
});

test("each themed composer has a dedicated pane-width fixture and retains editable drafts and question selection", () => {
  const { container } = render(<ComposerSurfaceSection />);
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
    input.focus();
    fireEvent.change(input, { target: { value: "Keep this editable" } });
    expect(input.value).toBe("Keep this editable");
    expect(document.activeElement).toBe(input);
    expect(within(drafted).getByRole("button", { name: /^Send$/ })).toBeTruthy();
    const choice = within(pending).getByRole("radio", { name: /No, main only/ }) as HTMLInputElement;
    fireEvent.click(choice);
    expect(choice.checked).toBe(true);
  }
});
