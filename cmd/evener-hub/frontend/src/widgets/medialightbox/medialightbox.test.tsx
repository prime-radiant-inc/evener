import { fireEvent, render, screen, within } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { Button } from "../button";
import dialogStyles from "../dialog/dialog.module.css";
import { MediaLightbox } from "./index";

test("the image renders in Dialog's large size with its caption", () => {
  render(
    <MediaLightbox
      open
      onClose={() => {}}
      title="Image 1 of 2"
      src="/s/ref/images/a"
      alt="Image 1 of 2"
      caption="hero-a.png"
      captionTestId="caption"
      imageTestId="image"
    />,
  );
  const dialog = screen.getByRole("dialog");
  expect(within(dialog).getByTestId("image").getAttribute("src")).toBe("/s/ref/images/a");
  expect(within(dialog).getByTestId("caption").textContent).toBe("hero-a.png");
  expect(dialog.className.split(/\s+/)).toContain(dialogStyles.dialogVariantLarge);
});

test("no caption element renders when there is no caption", () => {
  render(<MediaLightbox open onClose={() => {}} title="t" src="/a" alt="a" captionTestId="caption" />);
  expect(screen.queryByTestId("caption")).toBeNull();
});

test("caller controls render below the image", () => {
  render(
    <MediaLightbox open onClose={() => {}} title="t" src="/a" alt="a">
      <Button onClick={() => {}}>Next</Button>
    </MediaLightbox>,
  );
  expect(screen.getByRole("button", { name: "Next" })).toBeTruthy();
});

test("the dialog's close control closes it", () => {
  const onClose = vi.fn();
  render(<MediaLightbox open onClose={onClose} title="t" src="/a" alt="a" />);
  fireEvent.click(screen.getByRole("button", { name: "Close" }));
  expect(onClose).toHaveBeenCalledTimes(1);
});
