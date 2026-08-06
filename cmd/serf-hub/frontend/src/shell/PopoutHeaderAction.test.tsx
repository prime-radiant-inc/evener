import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { IDockviewHeaderActionsProps } from "dockview-core";
import { afterAll, afterEach, beforeEach, expect, test, vi } from "vitest";
import { PopoutHeaderAction } from "./PopoutHeaderAction";
import * as paneActions from "./paneActions";

// popOutPane is the imperative entry point PopoutHeaderAction drives; stub it
// so the affordance test asserts the wiring, not dockview's real popout
// mechanics (those are paneActions.test.ts's job).
//
// A hoisted vi.mock("./paneActions", () => ({ popOutPane: vi.fn() })) used to
// sit here, replacing the WHOLE module (dropping openBeside and
// inheritOpenerTheme entirely) in the shared module registry - under
// isolate:false that registry is shared by every file in the worker, so this
// would poison every other file that imports paneActions.ts for the rest of
// the worker's life, not just while this file's own test runs. vi.spyOn
// mutates only the one property this file cares about, on the SAME shared
// module object every other file also reads from, and mockRestore() in
// afterAll hands the real popOutPane back for whatever file runs next.
//
// Re-spied in beforeEach below, not just once here: some other file sharing
// this worker calling the GLOBAL vi.restoreAllMocks() would silently hand the
// real popOutPane back before this file's own test ever runs (see
// shell/palette/commands.test.ts's own comment on the same hazard).
let mockPopOut = vi.spyOn(paneActions, "popOutPane").mockImplementation(() => {});

beforeEach(() => {
  mockPopOut = vi.spyOn(paneActions, "popOutPane").mockImplementation(() => {});
});

afterEach(() => {
  cleanup();
  mockPopOut.mockClear();
});

afterAll(() => {
  mockPopOut.mockRestore();
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
