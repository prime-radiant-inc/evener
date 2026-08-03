import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
import { openTranscript } from "../transcript/openTranscript";
import { ActivityTranscriptAction } from "./ActivityTranscriptAction";

vi.mock("../transcript/openTranscript", () => ({
  openTranscript: vi.fn(),
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
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
      <div onClick={onRowClick}>
        <ActivityTranscriptAction transcriptRef="job:job_activity" parentRef="local:session" />
      </div>,
    );

    await user.click(screen.getByRole("button", { name: "Open transcript beside" }));

    expect(onRowClick).not.toHaveBeenCalled();
    expect(openTranscript).toHaveBeenCalledWith("job:job_activity", "local:session");
  });
});
