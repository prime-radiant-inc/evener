import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import DisclosureGallerySection from "./disclosure";

afterEach(cleanup);

test("each ThemeFlip pane shows disabled collapsed store and open controlled disclosures", () => {
  render(<DisclosureGallerySection />);
  const gallery = screen.getByTestId("disclosure-gallery");
  const panes = gallery.querySelectorAll('[data-theme="dark"], [data-theme="light"]');
  expect(panes).toHaveLength(2);

  for (const pane of panes) {
    const view = within(pane as HTMLElement);
    const collapsed = view.getByText("Disabled collapsed store");
    const open = view.getByText("Disabled open controlled");
    expect(collapsed.closest("summary")?.getAttribute("aria-disabled")).toBe("true");
    expect(collapsed.closest("summary")?.getAttribute("tabindex")).toBe("-1");
    expect(open.closest("summary")?.getAttribute("aria-disabled")).toBe("true");
    expect(open.closest("summary")?.getAttribute("tabindex")).toBe("-1");
    expect(view.getByText("Disabled controlled body")).toBeTruthy();
    expect(open.closest("details")?.hasAttribute("open")).toBe(true);
  }
});
