// ImageGallery: thumbnails for a set of images, each already carrying a
// resolved src plus whatever name/path/source the wire attached (see this
// module's own report for the adoption line messages/tools import to
// actually render item.images/item.outputImages through this), with a
// shared Dialog-based lightbox (prev/next, wraps at the ends, Esc/backdrop-
// close via Dialog's own contract). ItemModel.images/outputImages are
// already display-ready ItemImage objects by the time they reach here
// (reducer.ts's imagesToItemImages/outputImagesToItemImages resolve the
// wire's InputItem/OutputImage objects to one src each, preferring the
// sha-addressed `/s/{ref}/images/{sha}` route - cmd/serf-hub/
// web_workspace.go, handleSessionImage - while keeping name/path/source
// alongside instead of discarding them, kata byq2) - this component does no
// URL construction of its own, only rendering src and a caption built from
// whichever of those three survives (captionFor, below).
import { useState } from "react";
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
// provenance tag ("written-file", "shell-path", "tool-result") - the last
// resort rather than no caption at all. Mockup 20 held a caption constant
// across all four multi-image alternatives it explored; undefined here means
// none of the three survived, so the caller renders no caption line at all.
function captionFor(image: ItemImage): string | undefined {
  return image.name ?? image.path ?? image.source;
}

export function ImageGallery({ images }: ImageGalleryProps) {
  // null = closed; a number is the open lightbox's current index.
  const [openIndex, setOpenIndex] = useState<number | null>(null);

  if (!images || images.length === 0) return null;
  const total = images.length;
  // noUncheckedIndexedAccess means images[openIndex] is ItemImage|undefined
  // even though openIndex (set only via a valid map index, or step()'s own
  // modulo) is always in range - activeImage narrows that once, for both the
  // lightbox img and its caption below.
  const activeImage = openIndex !== null ? images[openIndex] : undefined;
  const activeCaption = activeImage !== undefined ? captionFor(activeImage) : undefined;

  function step(delta: number) {
    setOpenIndex((current) => (current === null ? current : (current + delta + total) % total));
  }

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
        <Dialog open onClose={() => setOpenIndex(null)} title={altFor(openIndex, total)}>
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
