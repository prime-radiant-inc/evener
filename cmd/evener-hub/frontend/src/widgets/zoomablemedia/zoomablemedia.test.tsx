import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef } from "react";
import { expect, test, vi } from "vitest";
import { ZoomableMedia } from "./index";

test("the media is one button that opens the larger view", async () => {
  const user = userEvent.setup();
  const onOpen = vi.fn();
  render(
    <ZoomableMedia label="Zoom image" onOpen={onOpen}>
      <img src="data:," alt="chart.png" />
    </ZoomableMedia>,
  );
  await user.click(screen.getByRole("button", { name: "Zoom image" }));
  expect(onOpen).toHaveBeenCalledTimes(1);
});

test("Enter and Space open it from the keyboard", async () => {
  const user = userEvent.setup();
  const onOpen = vi.fn();
  render(
    <ZoomableMedia label="Open diagram fullscreen" onOpen={onOpen}>
      <svg aria-hidden="true" />
    </ZoomableMedia>,
  );
  screen.getByRole("button", { name: "Open diagram fullscreen" }).focus();
  await user.keyboard("{Enter}");
  await user.keyboard(" ");
  expect(onOpen).toHaveBeenCalledTimes(2);
});

test("without a label the media's own text alternative names the button", () => {
  render(
    <ZoomableMedia onOpen={() => {}}>
      <img src="data:," alt="Release chart" />
    </ZoomableMedia>,
  );
  expect(screen.getByRole("button", { name: "Release chart" })).toBeTruthy();
});

test("data attributes and the ref reach the button", () => {
  const ref = createRef<HTMLButtonElement>();
  render(
    <ZoomableMedia ref={ref} label="View shot.png" onOpen={() => {}} data-testid="thumb">
      <img src="data:," alt="" />
    </ZoomableMedia>,
  );
  const button = screen.getByRole("button", { name: "View shot.png" });
  expect(screen.getByTestId("thumb")).toBe(button);
  expect(ref.current).toBe(button);
});

test("it never submits an enclosing form", async () => {
  const user = userEvent.setup();
  const onSubmit = vi.fn((event: SubmitEvent) => event.preventDefault());
  render(
    <form onSubmit={(event) => onSubmit(event.nativeEvent as SubmitEvent)}>
      <ZoomableMedia label="View shot.png" onOpen={() => {}}>
        <img src="data:," alt="" />
      </ZoomableMedia>
    </form>,
  );
  await user.click(screen.getByRole("button", { name: "View shot.png" }));
  expect(onSubmit).not.toHaveBeenCalled();
});
