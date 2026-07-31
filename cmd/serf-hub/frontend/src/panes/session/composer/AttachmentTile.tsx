// AttachmentTile: one staged composer attachment, from the moment it is
// pasted to the moment it is sent.
//
// ONE SHAPE FOR BOTH STATES (kata 39xe). A staged attachment is a tile
// whether or not its PNG re-encode has landed: an empty slot while it is
// encoding, the decoded thumbnail once it settles. That is a visual call
// (Jesse, 2026-07-30) and it is also what makes the remove button safe.
// Pending used to render a text <Chip> and settled a raw <div> at the same
// list position; React reconciles by element TYPE at a position, so it
// unmounted the chip's remove button rather than updating it, and a user
// holding tab-focus on that button when the decode landed had focus fall to
// <body> - reproduced, and pinned by Composer.test.tsx's own regression
// test. Here the outer element and the remove button never change type or
// position, so React updates them across the transition and focus stays put.
// Keep it that way: any future state must vary what FILLS the tile, never
// what the tile or its controls ARE.
//
// EVERY ITEM IS AN IMAGE, so there is exactly one branch and no
// "attachment that isn't an image" fallback to drift out of sync with this
// one. Both doors enforce it: attachments/limits.ts's rejectionReason
// refuses any file whose type is not image/*, and recovery/recoveryDraft.ts
// only lifts input items of type "image" out of a recovered mutation.
import { useState } from "react";
import { Dialog } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import type { PendingAttachment } from "./attachments/useAttachments";
import styles from "./composer.module.css";

export interface AttachmentTileProps {
  item: PendingAttachment;
  onRemove: () => void;
}

const CLASS = {
  imageTile: requireClass(styles.imageTile, "composer.module.css", "imageTile"),
  imagePending: requireClass(styles.imagePending, "composer.module.css", "imagePending"),
  imageOpenButton: requireClass(styles.imageOpenButton, "composer.module.css", "imageOpenButton"),
  imageThumbnail: requireClass(styles.imageThumbnail, "composer.module.css", "imageThumbnail"),
  dimensionsOverlay: requireClass(styles.dimensionsOverlay, "composer.module.css", "dimensionsOverlay"),
  removeImageButton: requireClass(styles.removeImageButton, "composer.module.css", "removeImageButton"),
  lightboxImage: requireClass(styles.lightboxImage, "composer.module.css", "lightboxImage"),
};

function RemoveIcon() {
  return (
    <svg viewBox="0 0 12 12" width="10" height="10" aria-hidden="true">
      <path d="M2 2 L10 10 M10 2 L2 10" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
    </svg>
  );
}

export function AttachmentTile({ item, onRemove }: AttachmentTileProps) {
  const [lightboxOpen, setLightboxOpen] = useState(false);
  // `data` is the one thing that decides whether there is a picture to draw.
  // A recovered attachment (recovery/recoveryDraft.ts) arrives with data and
  // pending:false but no dimensions, so `pending` and "has dimensions" are
  // both the wrong question here - and each answers its own smaller one
  // below.
  const source = item.data === undefined ? undefined : `data:${item.mediaType};base64,${item.data}`;

  return (
    <div className={CLASS.imageTile}>
      {source === undefined ? (
        // Named, so a screen reader hears which attachment is holding
        // things up. Not a live region: eight of those announcing at once
        // is noise, and the state resolves on its own in a few frames.
        <div className={CLASS.imagePending} role="img" aria-label={`${item.name} (still processing)`} />
      ) : (
        <button
          type="button"
          className={CLASS.imageOpenButton}
          aria-label={`View ${item.name}`}
          onClick={() => setLightboxOpen(true)}
        >
          {/* Decorative: the button around it already carries the name, and
              a second announcement of the same filename is just noise. */}
          <img className={CLASS.imageThumbnail} src={source} alt="" />
        </button>
      )}
      {item.width !== undefined && item.height !== undefined && (
        <div className={CLASS.dimensionsOverlay}>
          {item.width}×{item.height}
        </div>
      )}
      {/* Last, always, in both states - see this file's header comment. */}
      <button type="button" className={CLASS.removeImageButton} aria-label={`Remove ${item.name}`} onClick={onRemove}>
        <RemoveIcon />
      </button>
      {source !== undefined && lightboxOpen && (
        <Dialog open onClose={() => setLightboxOpen(false)} title={item.name} size="large">
          <img className={CLASS.lightboxImage} src={source} alt={item.name} />
        </Dialog>
      )}
    </div>
  );
}
