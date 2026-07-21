// ImageGallery: thumbnails for a set of already-resolved image URLs (see
// this module's own report for the adoption line messages/tools import to
// actually render item.images/item.outputImages through this), with a
// shared Dialog-based lightbox (prev/next, wraps at the ends, Esc/backdrop-
// close via Dialog's own contract). ItemModel.images/outputImages are
// already display-ready strings by the time they reach here (reducer.ts's
// imagesToStrings/outputImagesToStrings resolve the wire's InputItem/
// OutputImage objects down to one URL each, preferring the sha-addressed
// `/s/{ref}/images/{sha}` route - cmd/serf-hub/web_workspace.go,
// handleSessionImage) - this component does no URL construction of its own,
// only rendering.
import { useState } from "react";
import { Button, Dialog } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./imagegallery.module.css";

export interface ImageGalleryProps {
  images: string[] | undefined;
}

const CLASS = {
  strip: requireClass(styles.strip, "imagegallery.module.css", "strip"),
  thumb: requireClass(styles.thumb, "imagegallery.module.css", "thumb"),
  thumbImg: requireClass(styles.thumbImg, "imagegallery.module.css", "thumbImg"),
  lightboxImg: requireClass(styles.lightboxImg, "imagegallery.module.css", "lightboxImg"),
  nav: requireClass(styles.nav, "imagegallery.module.css", "nav"),
};

function altFor(index: number, total: number): string {
  return `Image ${index + 1} of ${total}`;
}

export function ImageGallery({ images }: ImageGalleryProps) {
  // null = closed; a number is the open lightbox's current index.
  const [openIndex, setOpenIndex] = useState<number | null>(null);

  if (!images || images.length === 0) return null;
  const total = images.length;

  function step(delta: number) {
    setOpenIndex((current) => (current === null ? current : (current + delta + total) % total));
  }

  return (
    <div className={CLASS.strip}>
      {images.map((src, i) => (
        <button
          key={src + i}
          type="button"
          data-testid="image-gallery-thumb"
          className={CLASS.thumb}
          onClick={() => setOpenIndex(i)}
        >
          <img className={CLASS.thumbImg} src={src} alt={altFor(i, total)} />
        </button>
      ))}

      {openIndex !== null && (
        <Dialog open onClose={() => setOpenIndex(null)} title={altFor(openIndex, total)}>
          <img data-testid="image-gallery-lightbox-img" className={CLASS.lightboxImg} src={images[openIndex]} alt={altFor(openIndex, total)} />
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
