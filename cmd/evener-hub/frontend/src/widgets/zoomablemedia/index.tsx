import { type ButtonHTMLAttributes, forwardRef, type ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./zoomablemedia.module.css";

export interface ZoomableMediaProps
  extends Omit<
    ButtonHTMLAttributes<HTMLButtonElement>,
    "type" | "onClick" | "className" | "aria-label" | "children" | "disabled"
  > {
  /** The button's accessible name ("Zoom image", "View shot.png"). Omit it
   * only when the media's own text alternative (an img alt, plus any
   * caption inside) already names what opens. */
  label?: string;
  onOpen: () => void;
  /** Fill the container's height too: for a fixed-size thumbnail tile. Off,
   * the trigger is as tall as its media. */
  fill?: boolean;
  /** The media itself: an image, a rendered diagram, a thumbnail and its
   * caption. */
  children: ReactNode;
}

const CLASS = {
  media: requireClass(styles.zoomableMedia, "zoomablemedia.module.css", "zoomableMedia"),
};

/**
 * Media that opens a larger view of itself: the whole image or diagram is
 * one block button (no control chrome, zoom-in cursor, inset focus ring so a
 * clipping tile cannot hide it). The larger view is the caller's - a single
 * image's Dialog, a gallery's stepping lightbox, the diagram viewer - so this
 * owns only the trigger. Fills its container's width; with `fill`, its
 * height too (a fixed-size thumbnail tile).
 */
export const ZoomableMedia = forwardRef<HTMLButtonElement, ZoomableMediaProps>(function ZoomableMedia(
  { label, onOpen, fill = false, children, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      {...rest}
      type="button"
      aria-label={label}
      data-fill={fill || undefined}
      className={CLASS.media}
      onClick={onOpen}
    >
      {children}
    </button>
  );
});
