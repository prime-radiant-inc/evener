import type { ReactNode } from "react";
import { ZoomableMedia } from "../zoomablemedia";

export interface ImageThumbnailProps {
  /** The image to draw. Omit only while the media is not ready, passing
   * `placeholder` instead so the frame and its overlay keep their positions. */
  src?: string;
  /** The image's text alternative. Decorative when the trigger's label or a
   * caption already names it. */
  alt?: string;
  /** Opens the larger view. The caller owns the lightbox (pair it with
   * MediaLightbox). */
  onOpen?: () => void;
  /** The trigger's accessible name. Omit when the image's alt text and any
   * caption already name what opens. */
  label?: string;
  /** The frame element's class: the caller's size, background, overflow, and
   * hover skin, composed with the shared edge/radius frame. */
  frameClassName?: string;
  /** The image's class: the caller's rendered size and fit. */
  imageClassName?: string;
  /** A text-alternative caption drawn inside the trigger. */
  caption?: ReactNode;
  captionClassName?: string;
  captionTestId?: string;
  /** Intrinsic box attributes for a thumbnail that reserves its box before
   * decode. Omit both to let the image keep its own aspect ratio. */
  width?: number;
  height?: number;
  loading?: "lazy" | "eager";
  decoding?: "async" | "sync" | "auto";
  /** A source the browser could not load, so the caller can drop it. */
  onImageError?: () => void;
  /** Content drawn in the frame after the trigger: dimensions, a remove
   * control. Rendered in every state so those nodes keep their identity. */
  overlay?: ReactNode;
  /** Replaces the trigger while the image is not ready. Keeps the frame and
   * `overlay` at the same positions across the pending-to-settled transition,
   * so a focused overlay control is updated rather than unmounted. */
  placeholder?: ReactNode;
  /** The frame element's test id (the tile itself). */
  frameTestId?: string;
  /** The trigger's test id (the open button). */
  triggerTestId?: string;
  /** Fill the frame's height too: the default for a fixed-size thumbnail
   * tile. Off, the trigger is as tall as its image. */
  fill?: boolean;
  /** Round and clip the trigger with the caller's tile radius, for a frame
   * that stops clipping at touch size. */
  radius?: "control" | "inherit";
}

/**
 * One image in a framed tile: the frame element, the ZoomableMedia trigger,
 * the image, and an optional caption, with slots for a caller-owned
 * placeholder and overlay (a remove control, dimensions). The larger view is
 * the caller's, so a thumbnail that opens a lightbox pairs this with
 * MediaLightbox. The transcript gallery and the composer's staged attachment
 * both compose this rather than rebuilding the tile markup apiece.
 */
export function ImageThumbnail({
  src,
  alt = "",
  onOpen,
  label,
  frameClassName,
  imageClassName,
  caption,
  captionClassName,
  captionTestId,
  width,
  height,
  loading,
  decoding,
  onImageError,
  overlay,
  placeholder,
  frameTestId,
  triggerTestId,
  fill = true,
  radius,
}: ImageThumbnailProps) {
  return (
    <div className={frameClassName} data-testid={frameTestId}>
      {placeholder ??
        (src !== undefined && onOpen !== undefined && (
          <ZoomableMedia label={label} fill={fill} radius={radius} onOpen={onOpen} data-testid={triggerTestId}>
            <img
              className={imageClassName}
              src={src}
              alt={alt}
              width={width}
              height={height}
              loading={loading}
              decoding={decoding}
              onError={onImageError}
            />
            {caption !== undefined && (
              <span className={captionClassName} data-testid={captionTestId}>
                {caption}
              </span>
            )}
          </ZoomableMedia>
        ))}
      {overlay}
    </div>
  );
}
