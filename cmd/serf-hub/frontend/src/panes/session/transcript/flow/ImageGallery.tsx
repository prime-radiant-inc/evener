// ImageGallery: thumbnails for a set of images, each already carrying a
// resolved src plus whatever name/path/source the wire attached (see this
// module's own report for the adoption line messages/tools import to
// actually render item.images/item.outputImages through this), with a
// shared Dialog-based lightbox (prev/next - by click, or the Left/Right
// arrow keys, kata b4xf - wraps at the ends, Esc/backdrop-close via Dialog's
// own contract, size="large" so it fills almost the whole viewport rather
// than Dialog's default compact width). ItemModel.images/outputImages are
// already display-ready ItemImage objects by the time they reach here
// (reducer.ts's imagesToItemImages/outputImagesToItemImages resolve the
// wire's InputItem/OutputImage objects to one src each, preferring the
// sha-addressed `/s/{ref}/images/{sha}` route - cmd/serf-hub/
// web_workspace.go, handleSessionImage - while keeping name/path/source
// alongside instead of discarding them, kata byq2) - this component does no
// URL construction of its own, only rendering src and a caption built from
// whichever of those three survives (captionFor, below).
//
// "Adjacent" (kata b4xf's arrow-key ask) means the other images passed in
// the SAME `images` prop - i.e. the same tool call's outputImages
// (ToolCallItem.tsx) or the same user message's images (UserMessageItem.tsx)
// - never images from a different item elsewhere in the transcript. That's
// already how the Previous/Next buttons group images; the arrow keys use the
// exact same step() below, not a new grouping.
import { useCallback, useEffect, useState } from "react";
import type { ItemImage } from "../../../../protocol/model";
import { Button, Dialog } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./imagegallery.module.css";

export interface ImageGalleryProps {
  images: ItemImage[] | undefined;
}

const CLASS = {
  strip: requireClass(styles.strip, "imagegallery.module.css", "strip"),
  thumb: requireClass(styles.thumb, "imagegallery.module.css", "thumb"),
  thumbImg: requireClass(styles.thumbImg, "imagegallery.module.css", "thumbImg"),
  caption: requireClass(styles.caption, "imagegallery.module.css", "caption"),
  lightboxImg: requireClass(styles.lightboxImg, "imagegallery.module.css", "lightboxImg"),
  lightboxCaption: requireClass(styles.lightboxCaption, "imagegallery.module.css", "lightboxCaption"),
  nav: requireClass(styles.nav, "imagegallery.module.css", "nav"),
};

function altFor(index: number, total: number): string {
  return `Image ${index + 1} of ${total}`;
}

// The one label left once url/path/name have already been spent resolving
// src (see this file's top comment): name is the most recognizable to a
// reader, path the next most, source - an OutputImage-only, coarse
// provenance tag ("written-file", "read-file", "shell-path", "tool-result") -
// the last resort rather than no caption at all. Mockup 20 held a caption
// constant across all four multi-image alternatives it explored; undefined
// here means none of the three survived, so the caller renders no caption
// line at all.
function captionFor(image: ItemImage): string | undefined {
  return image.name ?? image.path ?? image.source;
}

export function ImageGallery({ images }: ImageGalleryProps) {
  // null = closed; a number is the open lightbox's current index.
  const [openIndex, setOpenIndex] = useState<number | null>(null);
  // Computed before the `images` early return below, and hooks ordered
  // ahead of it too (Rules of Hooks: every hook here must run on every
  // render, regardless of whether this instance ends up rendering nothing).
  const total = images?.length ?? 0;
  const isOpen = openIndex !== null;

  const step = useCallback(
    (delta: number) => {
      setOpenIndex((current) => (current === null ? current : (current + delta + total) % total));
    },
    [total],
  );

  // Left/Right arrow-key navigation while the lightbox is open (kata b4xf),
  // gated on the same total>1 the Previous/Next buttons use below - with
  // only one image there's nothing to step to, so no listener is attached
  // at all. A window listener, not a handler on some element inside the
  // dialog: initial focus can land on Dialog's own close button
  // (OverlayPanel.tsx), which sits outside this component's own subtree, so
  // a listener scoped to an element in here could miss it. Always removed
  // on close (isOpen flips false) or unmount, so a closed gallery's
  // keydowns never leak into whatever opens next.
  useEffect(() => {
    if (!isOpen || total <= 1) return;
    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === "ArrowLeft") step(-1);
      else if (event.key === "ArrowRight") step(1);
    }
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [isOpen, total, step]);

  if (!images || images.length === 0) return null;
  // noUncheckedIndexedAccess means images[openIndex] is ItemImage|undefined
  // even though openIndex (set only via a valid map index, or step()'s own
  // modulo) is always in range - activeImage narrows that once, for both the
  // lightbox img and its caption below.
  const activeImage = openIndex !== null ? images[openIndex] : undefined;
  const activeCaption = activeImage !== undefined ? captionFor(activeImage) : undefined;

  return (
    <div className={CLASS.strip}>
      {images.map((image, i) => {
        const caption = captionFor(image);
        return (
          // image.src is a content-addressed URL (/s/{ref}/images/{sha}, see
          // this file's own top comment) - already unique per distinct image
          // on its own; +i here is belt-and-suspenders against a literal
          // duplicate (two entries hashing the same, which would collide as
          // a bare-src key), not standing in for a missing real id.
          <button
            // biome-ignore lint/suspicious/noArrayIndexKey: src is already content-stable, see above
            key={image.src + i}
            type="button"
            data-testid="image-gallery-thumb"
            className={CLASS.thumb}
            onClick={() => setOpenIndex(i)}
          >
            <img className={CLASS.thumbImg} src={image.src} alt={altFor(i, total)} />
            {caption !== undefined && (
              <span className={CLASS.caption} data-testid="image-gallery-caption">
                {caption}
              </span>
            )}
          </button>
        );
      })}

      {openIndex !== null && activeImage !== undefined && (
        <Dialog open onClose={() => setOpenIndex(null)} title={altFor(openIndex, total)} size="large">
          <img
            data-testid="image-gallery-lightbox-img"
            className={CLASS.lightboxImg}
            src={activeImage.src}
            alt={altFor(openIndex, total)}
          />
          {activeCaption !== undefined && (
            <div className={CLASS.lightboxCaption} data-testid="image-gallery-lightbox-caption">
              {activeCaption}
            </div>
          )}
          {total > 1 && (
            <div className={CLASS.nav}>
              <Button variant="quiet" size="sm" data-testid="image-gallery-prev" onClick={() => step(-1)}>
                Previous
              </Button>
              <Button variant="quiet" size="sm" data-testid="image-gallery-next" onClick={() => step(1)}>
                Next
              </Button>
            </div>
          )}
        </Dialog>
      )}
    </div>
  );
}
