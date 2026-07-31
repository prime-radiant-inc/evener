import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import dialogStyles from "../../../../widgets/dialog/dialog.module.css";
import { ImageGallery } from "./ImageGallery";

afterEach(() => {
  cleanup();
  // The keydown-listener tests below spy on window.addEventListener/
  // removeEventListener; vi.spyOn's call history otherwise survives across
  // tests in this file (nothing here uses vitest's restoreMocks config),
  // which would let an earlier test's recorded "keydown" call leak into a
  // later test's "was anything added" check.
  vi.restoreAllMocks();
});

test("renders nothing when images is undefined", () => {
  const { container } = render(<ImageGallery images={undefined} />);
  expect(container.firstChild).toBeNull();
});

test("renders nothing when images is an empty array", () => {
  const { container } = render(<ImageGallery images={[]} />);
  expect(container.firstChild).toBeNull();
});

test("renders one thumbnail button per image, each a real focusable button", () => {
  render(<ImageGallery images={[{ src: "/s/ref/images/aaa" }, { src: "/s/ref/images/bbb" }]} />);
  const thumbs = screen.getAllByTestId("image-gallery-thumb");
  expect(thumbs).toHaveLength(2);
  for (const thumb of thumbs) expect(thumb.tagName).toBe("BUTTON");
});

test("each thumbnail's <img> src is the resolved URL passed in, with distinguishing alt text", () => {
  render(<ImageGallery images={[{ src: "/s/ref/images/aaa" }, { src: "/s/ref/images/bbb" }]} />);
  const imgs = screen.getAllByTestId("image-gallery-thumb").map((btn) => btn.querySelector("img")!);
  expect(imgs[0]!.getAttribute("src")).toBe("/s/ref/images/aaa");
  expect(imgs[1]!.getAttribute("src")).toBe("/s/ref/images/bbb");
  expect(imgs[0]!.getAttribute("alt")).not.toBe(imgs[1]!.getAttribute("alt"));
});

test("no lightbox is open before any thumbnail is clicked", () => {
  render(<ImageGallery images={[{ src: "/s/ref/images/aaa" }]} />);
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("clicking a thumbnail opens the lightbox showing that image", () => {
  render(<ImageGallery images={[{ src: "/s/ref/images/aaa" }, { src: "/s/ref/images/bbb" }]} />);

  fireEvent.click(screen.getAllByTestId("image-gallery-thumb")[1]!);

  const dialog = screen.getByRole("dialog");
  const lightboxImg = within(dialog).getByTestId("image-gallery-lightbox-img");
  expect(lightboxImg.getAttribute("src")).toBe("/s/ref/images/bbb");
});

test("the lightbox uses Dialog's large size variant, not the compact default (kata b4xf)", () => {
  render(<ImageGallery images={[{ src: "/s/ref/images/aaa" }]} />);
  fireEvent.click(screen.getByTestId("image-gallery-thumb"));

  // Exact token membership, not substring containment: dialogVariantLarge
  // contains "dialogVariant" as a literal prefix, so a substring check would
  // pass even if the wrong (default) class were applied.
  const dialogClassTokens = screen.getByRole("dialog").className.split(/\s+/);

  expect(dialogClassTokens).toContain(dialogStyles.dialogVariantLarge);
  expect(dialogClassTokens).not.toContain(dialogStyles.dialogVariant);
});

test("a single-image set shows no prev/next controls in the lightbox", () => {
  render(<ImageGallery images={[{ src: "/s/ref/images/aaa" }]} />);
  fireEvent.click(screen.getByTestId("image-gallery-thumb"));

  expect(screen.queryByTestId("image-gallery-prev")).toBeNull();
  expect(screen.queryByTestId("image-gallery-next")).toBeNull();
});

test("a multi-image set shows prev/next controls that step across the set", () => {
  render(
    <ImageGallery images={[{ src: "/s/ref/images/a" }, { src: "/s/ref/images/b" }, { src: "/s/ref/images/c" }]} />,
  );
  fireEvent.click(screen.getAllByTestId("image-gallery-thumb")[0]!);
  expect(screen.getByTestId("image-gallery-lightbox-img").getAttribute("src")).toBe("/s/ref/images/a");

  fireEvent.click(screen.getByTestId("image-gallery-next"));
  expect(screen.getByTestId("image-gallery-lightbox-img").getAttribute("src")).toBe("/s/ref/images/b");

  fireEvent.click(screen.getByTestId("image-gallery-prev"));
  expect(screen.getByTestId("image-gallery-lightbox-img").getAttribute("src")).toBe("/s/ref/images/a");
});

test("next wraps from the last image back to the first", () => {
  render(<ImageGallery images={[{ src: "/s/ref/images/a" }, { src: "/s/ref/images/b" }]} />);
  fireEvent.click(screen.getAllByTestId("image-gallery-thumb")[1]!); // start at b (last)

  fireEvent.click(screen.getByTestId("image-gallery-next"));

  expect(screen.getByTestId("image-gallery-lightbox-img").getAttribute("src")).toBe("/s/ref/images/a");
});

test("prev wraps from the first image back to the last", () => {
  render(<ImageGallery images={[{ src: "/s/ref/images/a" }, { src: "/s/ref/images/b" }]} />);
  fireEvent.click(screen.getAllByTestId("image-gallery-thumb")[0]!); // start at a (first)

  fireEvent.click(screen.getByTestId("image-gallery-prev"));

  expect(screen.getByTestId("image-gallery-lightbox-img").getAttribute("src")).toBe("/s/ref/images/b");
});

// --- keyboard navigation (kata b4xf: left/right arrows step between
// adjacent images, same as the Previous/Next buttons; only wired up while
// there's more than one image to step between) -----------------------------

test("ArrowRight steps to the next image, same as clicking Next", async () => {
  const user = userEvent.setup();
  render(
    <ImageGallery images={[{ src: "/s/ref/images/a" }, { src: "/s/ref/images/b" }, { src: "/s/ref/images/c" }]} />,
  );
  fireEvent.click(screen.getAllByTestId("image-gallery-thumb")[0]!);
  expect(screen.getByTestId("image-gallery-lightbox-img").getAttribute("src")).toBe("/s/ref/images/a");

  await user.keyboard("{ArrowRight}");

  expect(screen.getByTestId("image-gallery-lightbox-img").getAttribute("src")).toBe("/s/ref/images/b");
});

test("ArrowLeft steps to the previous image, same as clicking Previous", async () => {
  const user = userEvent.setup();
  render(
    <ImageGallery images={[{ src: "/s/ref/images/a" }, { src: "/s/ref/images/b" }, { src: "/s/ref/images/c" }]} />,
  );
  fireEvent.click(screen.getAllByTestId("image-gallery-thumb")[2]!);
  expect(screen.getByTestId("image-gallery-lightbox-img").getAttribute("src")).toBe("/s/ref/images/c");

  await user.keyboard("{ArrowLeft}");

  expect(screen.getByTestId("image-gallery-lightbox-img").getAttribute("src")).toBe("/s/ref/images/b");
});

test("arrow keys wrap at the ends, same as the buttons", async () => {
  const user = userEvent.setup();
  render(<ImageGallery images={[{ src: "/s/ref/images/a" }, { src: "/s/ref/images/b" }]} />);
  fireEvent.click(screen.getAllByTestId("image-gallery-thumb")[1]!); // start at b (last)

  await user.keyboard("{ArrowRight}");

  expect(screen.getByTestId("image-gallery-lightbox-img").getAttribute("src")).toBe("/s/ref/images/a");
});

test("arrow keys do nothing with only one image open (no Previous/Next controls either)", async () => {
  const user = userEvent.setup();
  render(<ImageGallery images={[{ src: "/s/ref/images/a" }]} />);
  fireEvent.click(screen.getByTestId("image-gallery-thumb"));
  expect(screen.queryByTestId("image-gallery-prev")).toBeNull();
  expect(screen.queryByTestId("image-gallery-next")).toBeNull();

  await user.keyboard("{ArrowRight}");
  await user.keyboard("{ArrowLeft}");

  expect(screen.getByTestId("image-gallery-lightbox-img").getAttribute("src")).toBe("/s/ref/images/a");
});

test("closing the lightbox removes its keydown listener (no leaked listener)", () => {
  const addSpy = vi.spyOn(window, "addEventListener");
  const removeSpy = vi.spyOn(window, "removeEventListener");

  render(<ImageGallery images={[{ src: "/s/ref/images/a" }, { src: "/s/ref/images/b" }]} />);
  fireEvent.click(screen.getAllByTestId("image-gallery-thumb")[0]!);

  const keydownCall = addSpy.mock.calls.find(([type]) => type === "keydown");
  expect(keydownCall).toBeTruthy();

  fireEvent.click(screen.getByRole("button", { name: "Close" }));

  expect(removeSpy).toHaveBeenCalledWith("keydown", keydownCall![1]);

  // The listener is really gone, not just "removeEventListener was called
  // with the right args" - firing the same key on window after close must
  // not reopen or otherwise touch the (now-closed) dialog.
  fireEvent.keyDown(window, { key: "ArrowRight" });
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("no keydown listener is attached at all for a single-image gallery", () => {
  const addSpy = vi.spyOn(window, "addEventListener");

  render(<ImageGallery images={[{ src: "/s/ref/images/a" }]} />);
  fireEvent.click(screen.getByTestId("image-gallery-thumb"));

  expect(addSpy.mock.calls.some(([type]) => type === "keydown")).toBe(false);
});

test("Escape closes the lightbox (via Dialog's own contract)", async () => {
  const user = userEvent.setup();
  render(<ImageGallery images={[{ src: "/s/ref/images/a" }]} />);
  fireEvent.click(screen.getByTestId("image-gallery-thumb"));
  expect(screen.getByRole("dialog")).toBeTruthy();

  await user.keyboard("{Escape}");

  expect(screen.queryByRole("dialog")).toBeNull();
});

test("the dialog's own close button closes the lightbox", () => {
  render(<ImageGallery images={[{ src: "/s/ref/images/a" }]} />);
  fireEvent.click(screen.getByTestId("image-gallery-thumb"));

  fireEvent.click(screen.getByRole("button", { name: "Close" }));

  expect(screen.queryByRole("dialog")).toBeNull();
});

test("reopening after closing starts at the newly-clicked thumbnail, not the previously-closed index", () => {
  render(<ImageGallery images={[{ src: "/s/ref/images/a" }, { src: "/s/ref/images/b" }]} />);
  fireEvent.click(screen.getAllByTestId("image-gallery-thumb")[1]!);
  fireEvent.click(screen.getByRole("button", { name: "Close" }));

  fireEvent.click(screen.getAllByTestId("image-gallery-thumb")[0]!);

  expect(screen.getByTestId("image-gallery-lightbox-img").getAttribute("src")).toBe("/s/ref/images/a");
});

// --- captions (kata byq2: name/path/source now survive the reducer, so
// there's finally something to caption an image WITH) ----------------------

test("a thumbnail shows a caption when its image carries a name", () => {
  render(<ImageGallery images={[{ src: "/s/ref/images/aaa", name: "screenshot.png" }]} />);
  const thumb = screen.getByTestId("image-gallery-thumb");
  expect(within(thumb).getByTestId("image-gallery-caption").textContent).toBe("screenshot.png");
});

test("a thumbnail shows no caption when its image carries neither name, path, nor source", () => {
  render(<ImageGallery images={[{ src: "/s/ref/images/aaa" }]} />);
  const thumb = screen.getByTestId("image-gallery-thumb");
  expect(within(thumb).queryByTestId("image-gallery-caption")).toBeNull();
});

test("a thumbnail's caption prefers name, then path, then source, never leaving one blank while a later field is shown", () => {
  render(
    <ImageGallery
      images={[
        { src: "/s/ref/images/a", name: "photo.jpg", path: "uploads/photo.jpg", source: "written-file" },
        { src: "/s/ref/images/b", path: "uploads/other.jpg", source: "written-file" },
        { src: "/s/ref/images/c", source: "written-file" },
      ]}
    />,
  );
  const captions = screen.getAllByTestId("image-gallery-caption").map((el) => el.textContent);
  expect(captions).toEqual(["photo.jpg", "uploads/other.jpg", "written-file"]);
});

test("the lightbox shows the same caption as the thumbnail that opened it", () => {
  render(
    <ImageGallery
      images={[
        { src: "/s/ref/images/a", name: "hero-a.png" },
        { src: "/s/ref/images/b", name: "hero-b.png" },
      ]}
    />,
  );
  fireEvent.click(screen.getAllByTestId("image-gallery-thumb")[1]!);

  const dialog = screen.getByRole("dialog");
  expect(within(dialog).getByTestId("image-gallery-lightbox-caption").textContent).toBe("hero-b.png");
});

test("the lightbox shows no caption element when the active image has none", () => {
  render(<ImageGallery images={[{ src: "/s/ref/images/a" }]} />);
  fireEvent.click(screen.getByTestId("image-gallery-thumb"));

  const dialog = screen.getByRole("dialog");
  expect(within(dialog).queryByTestId("image-gallery-lightbox-caption")).toBeNull();
});

test("stepping the lightbox to a new image updates its caption", () => {
  render(
    <ImageGallery
      images={[
        { src: "/s/ref/images/a", name: "hero-a.png" },
        { src: "/s/ref/images/b", name: "hero-b.png" },
      ]}
    />,
  );
  fireEvent.click(screen.getAllByTestId("image-gallery-thumb")[0]!);
  expect(screen.getByTestId("image-gallery-lightbox-caption").textContent).toBe("hero-a.png");

  fireEvent.click(screen.getByTestId("image-gallery-next"));

  expect(screen.getByTestId("image-gallery-lightbox-caption").textContent).toBe("hero-b.png");
});
