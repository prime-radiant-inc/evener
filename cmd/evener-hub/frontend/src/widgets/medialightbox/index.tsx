import type { ReactNode } from "react";
import { Dialog } from "../dialog";
import { requireClass } from "../internal/requireClass";
import styles from "./medialightbox.module.css";

export interface MediaLightboxProps {
  open: boolean;
  onClose: () => void;
  /** The dialog's accessible title. */
  title: string;
  src: string;
  alt: string;
  /** A text caption below the image. */
  caption?: ReactNode;
  captionTestId?: string;
  /** The image's test id. */
  imageTestId?: string;
  /** Extra controls below the image, such as a gallery's Previous/Next. */
  children?: ReactNode;
}

const CLASS = {
  image: requireClass(styles.image, "medialightbox.module.css", "image"),
  caption: requireClass(styles.caption, "medialightbox.module.css", "caption"),
};

/**
 * The one-image lightbox shared by ImageThumbnail's callers: Dialog's
 * size="large" frame around a whole image (the shared lightbox image size),
 * with an optional caption and caller-supplied controls. The trigger and any
 * stepping between images stay with the caller.
 */
export function MediaLightbox({
  open,
  onClose,
  title,
  src,
  alt,
  caption,
  captionTestId,
  imageTestId,
  children,
}: MediaLightboxProps) {
  return (
    <Dialog open={open} onClose={onClose} title={title} size="large">
      <img data-testid={imageTestId} className={CLASS.image} src={src} alt={alt} />
      {caption !== undefined && (
        <div className={CLASS.caption} data-testid={captionTestId}>
          {caption}
        </div>
      )}
      {children}
    </Dialog>
  );
}
