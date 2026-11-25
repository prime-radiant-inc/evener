import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import { ImageThumbnail } from "./index";

test("the image is one button that opens the larger view", async () => {
  const user = userEvent.setup();
  const onOpen = vi.fn();
  render(<ImageThumbnail src="data:," alt="Chart" onOpen={onOpen} />);
  await user.click(screen.getByRole("button", { name: "Chart" }));
  expect(onOpen).toHaveBeenCalledTimes(1);
});

test("a label names the trigger when the image's alt text is decorative", () => {
  render(<ImageThumbnail src="data:," alt="" label="View shot.png" onOpen={() => {}} />);
  expect(screen.getByRole("button", { name: "View shot.png" })).toBeTruthy();
});

test("the caption renders inside the trigger", () => {
  render(
    <ImageThumbnail
      src="data:,"
      alt="Chart"
      caption="shot.png"
      captionTestId="caption"
      triggerTestId="thumb"
      onOpen={() => {}}
    />,
  );
  const button = screen.getByTestId("thumb");
  expect(button.tagName).toBe("BUTTON");
  expect(button.querySelector('[data-testid="caption"]')?.textContent).toBe("shot.png");
});

test("the frame and the trigger carry their own test ids", () => {
  render(<ImageThumbnail src="data:," alt="Chart" frameTestId="tile" triggerTestId="thumb" onOpen={() => {}} />);
  expect(screen.getByTestId("tile").contains(screen.getByTestId("thumb"))).toBe(true);
});

test("a placeholder replaces the trigger while the overlay keeps its node", () => {
  const { rerender } = render(
    <ImageThumbnail placeholder={<div data-testid="slot" />} overlay={<span data-testid="overlay" />} />,
  );
  const overlay = screen.getByTestId("overlay");
  expect(screen.getByTestId("slot")).toBeTruthy();

  rerender(<ImageThumbnail src="data:," alt="Chart" overlay={<span data-testid="overlay" />} onOpen={() => {}} />);

  expect(screen.getByRole("button", { name: "Chart" })).toBeTruthy();
  expect(screen.getByTestId("overlay")).toBe(overlay);
});

test("width and height reserve the box only when both are given", () => {
  const { rerender } = render(<ImageThumbnail src="data:," alt="Chart" width={96} height={96} onOpen={() => {}} />);
  let img = screen.getByRole("button", { name: "Chart" }).querySelector("img");
  expect(img?.getAttribute("width")).toBe("96");
  expect(img?.getAttribute("height")).toBe("96");

  rerender(<ImageThumbnail src="data:," alt="Chart" onOpen={() => {}} />);
  img = screen.getByRole("button", { name: "Chart" }).querySelector("img");
  expect(img?.hasAttribute("width")).toBe(false);
  expect(img?.hasAttribute("height")).toBe(false);
});

test("an image that fails to load reports it instead of throwing", () => {
  const onImageError = vi.fn();
  render(<ImageThumbnail src="data:," alt="Chart" onOpen={() => {}} onImageError={onImageError} />);
  const img = screen.getByRole("button", { name: "Chart" }).querySelector("img");
  if (!img) throw new Error("no image rendered");
  fireEvent.error(img);
  expect(onImageError).toHaveBeenCalledTimes(1);
});
