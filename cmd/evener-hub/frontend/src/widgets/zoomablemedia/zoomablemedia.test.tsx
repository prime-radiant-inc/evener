import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef } from "react";
import { expect, test, vi } from "vitest";
import { readModuleCss } from "../../styles/cssBlock";
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

// The props type omits aria-label, onClick and type, but a caller spreading a
// loosely typed object can still pass them; label and onOpen must win.
test("a stray aria-label or onClick arriving via untyped spread cannot replace label or onOpen", async () => {
  const user = userEvent.setup();
  const onOpen = vi.fn();
  const stray = vi.fn();
  const loose = { "aria-label": "Stray name", onClick: stray } as object;
  render(
    <ZoomableMedia label="Zoom image" onOpen={onOpen} {...loose}>
      <img src="data:," alt="" />
    </ZoomableMedia>,
  );
  await user.click(screen.getByRole("button", { name: "Zoom image" }));
  expect(onOpen).toHaveBeenCalledTimes(1);
  expect(stray).not.toHaveBeenCalled();
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

// Height filling is opt-in: a fixed-size thumbnail tile wants the trigger to
// fill it, but in a flowing pane (the doc viewer) a filled height would
// stretch the trigger to the pane instead of the image.
test("only fill=true marks the trigger to fill its container's height", () => {
  const { rerender } = render(
    <ZoomableMedia label="Zoom image" onOpen={() => {}}>
      <img src="data:," alt="" />
    </ZoomableMedia>,
  );
  expect(screen.getByRole("button", { name: "Zoom image" }).hasAttribute("data-fill")).toBe(false);
  rerender(
    <ZoomableMedia label="Zoom image" onOpen={() => {}} fill>
      <img src="data:," alt="" />
    </ZoomableMedia>,
  );
  expect(screen.getByRole("button", { name: "Zoom image" }).hasAttribute("data-fill")).toBe(true);
});

test("the base trigger keeps automatic height; only the fill form sets 100%", () => {
  const css = readModuleCss(import.meta.url, "zoomablemedia.module.css");
  const base = /\.zoomableMedia\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(base).not.toMatch(/(^|[;\s])height\s*:/);
  const fill = /\.zoomableMedia\[data-fill\]\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(fill).toMatch(/height:\s*100%/);
});
