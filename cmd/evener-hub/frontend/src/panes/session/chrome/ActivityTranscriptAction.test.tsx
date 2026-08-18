import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import * as openTranscriptModule from "../transcript/openTranscript";
import { ActivityTranscriptAction } from "./ActivityTranscriptAction";

// vi.spyOn, not vi.mock: ActivityPanel.test.tsx statically imports this
// component's sibling (ActivityTree.tsx, via ActivityInspector.tsx) without
// ever mocking this module, so under a shared module registry the real
// openTranscript binding is already resolved by the time this file's tests
// run - see ActivityTree.test.tsx's identical comment for the full story.
// Spying on the real module's own export patches the one binding every
// importer actually shares, regardless of import order.
let openTranscript: typeof openTranscriptModule.openTranscript;
beforeEach(() => {
  openTranscript = vi.spyOn(openTranscriptModule, "openTranscript").mockImplementation(() => {});
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("ActivityTranscriptAction", () => {
  test("opens the transcript beside with parent context and renders the open-beside glyph", async () => {
    const user = userEvent.setup();
    render(<ActivityTranscriptAction transcriptRef="job:job_activity" parentRef="local:session" />);

    const button = screen.getByRole("button", { name: "Open transcript beside" });
    await user.click(button);

    expect(openTranscript).toHaveBeenCalledWith("job:job_activity", "local:session");
    expect(button.querySelector("svg[aria-hidden='true']")).toBeTruthy();
  });

  test("renders no button for absent or blank transcript refs", () => {
    const { rerender } = render(<ActivityTranscriptAction transcriptRef={undefined} />);
    expect(screen.queryByRole("button", { name: "Open transcript beside" })).toBeNull();

    rerender(<ActivityTranscriptAction transcriptRef="" />);
    expect(screen.queryByRole("button", { name: "Open transcript beside" })).toBeNull();

    rerender(<ActivityTranscriptAction transcriptRef="   " />);
    expect(screen.queryByRole("button", { name: "Open transcript beside" })).toBeNull();
  });

  test("stops propagation when clicked inside a row", async () => {
    const user = userEvent.setup();
    const onRowClick = vi.fn();
    render(
      <div role="treeitem" tabIndex={0} onClick={onRowClick} onKeyDown={() => {}}>
        <ActivityTranscriptAction transcriptRef="job:job_activity" parentRef="local:session" />
      </div>,
    );

    await user.click(screen.getByRole("button", { name: "Open transcript beside" }));

    expect(onRowClick).not.toHaveBeenCalled();
    expect(openTranscript).toHaveBeenCalledWith("job:job_activity", "local:session");
  });
});
