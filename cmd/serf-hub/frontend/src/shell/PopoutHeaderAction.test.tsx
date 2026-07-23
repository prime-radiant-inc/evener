import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { IDockviewHeaderActionsProps } from "dockview-core";
import { afterEach, expect, test, vi } from "vitest";

// popOutPane is the imperative entry point PopoutHeaderAction drives; mock the
// whole module (same idiom as DocPane.test.tsx mocking docContent) so the
// affordance test asserts the wiring, not dockview's real popout mechanics
// (those are paneActions.test.ts's job).
vi.mock("./paneActions", () => ({ popOutPane: vi.fn() }));

import { PopoutHeaderAction } from "./PopoutHeaderAction";
import { popOutPane } from "./paneActions";

const mockPopOut = vi.mocked(popOutPane);

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

// A minimal IDockviewHeaderActionsProps stand-in: PopoutHeaderAction reads only
// activePanel and location?.type off the (much wider) real props, so a loose
// literal cast through unknown is enough - same technique paneActions.test.ts
// uses for DockviewApi (the real DockviewGroupLocation carries a getWindow()
// etc. that this component never touches).
function headerProps(over: {
  activePanel?: { id: string };
  location?: { type: "grid" | "floating" | "popout" };
}): IDockviewHeaderActionsProps {
  return over as unknown as IDockviewHeaderActionsProps;
}

test("renders a 'Pop out' affordance that pops out the group's focused pane", async () => {
  const user = userEvent.setup();
  render(<PopoutHeaderAction {...headerProps({ activePanel: { id: "pane_doc_1" } })} />);

  const button = screen.getByRole("button", { name: "Pop out" });
  await user.click(button);
  expect(mockPopOut).toHaveBeenCalledWith("pane_doc_1");
});

test("renders nothing when the group has no active panel - there is nothing to pop out", () => {
  const { container } = render(<PopoutHeaderAction {...headerProps({ activePanel: undefined })} />);
  expect(container.firstChild).toBeNull();
  expect(screen.queryByRole("button", { name: "Pop out" })).toBeNull();
});

test("renders nothing for a group already in its own popout window - no re-popout", () => {
  render(<PopoutHeaderAction {...headerProps({ activePanel: { id: "pane_doc_1" }, location: { type: "popout" } })} />);
  expect(screen.queryByRole("button", { name: "Pop out" })).toBeNull();
});

test("renders nothing for a floating group - popout promotes a docked pane, not a floating one", () => {
  render(
    <PopoutHeaderAction {...headerProps({ activePanel: { id: "pane_doc_1" }, location: { type: "floating" } })} />,
  );
  expect(screen.queryByRole("button", { name: "Pop out" })).toBeNull();
});
