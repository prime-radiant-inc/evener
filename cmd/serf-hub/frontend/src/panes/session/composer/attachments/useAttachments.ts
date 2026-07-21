// useAttachments orchestrates the composer's staged-image pipeline (parity-
// m5-composer.md §G, contracts §Attachments): validate against limits.ts,
// reserve a marker + insert it into the textarea synchronously (before any
// async work - test-composer-image-markers.js's own "pending: true"
// contract), re-encode to PNG via encodePng.ts, then settle the item in
// place. Component state (not a store): per Composer.tsx's own header
// comment and the wave plan's binding constraints, only DURABLE state
// (drafts, queue, pending-optimistic entries) needs to survive a dockview
// tab-switch remount - staged, not-yet-sent attachments are exactly the
// kind of in-progress, undurable UI state the legacy composer ALSO never
// persisted past its own DOM lifetime, and base64-heavy image bytes would
// blow past localStorage's practical quota (up to 8 * 8MB) if it tried.
import { type RefObject, useCallback, useRef, useState } from "react";
import type { InputAttachment } from "../../../../stores/threads";
import { reencodeToPng } from "./encodePng";
import { rejectionReason } from "./limits";
import { insertAtCursor, markerText, stripMarker } from "./textareaMarkers";

export interface PendingAttachment {
  marker: number;
  name: string;
  mediaType: string;
  width?: number;
  height?: number;
  data?: string; // base64; undefined while pending === true
  pending: boolean;
}

export interface UseAttachmentsResult {
  items: PendingAttachment[];
  /** True while any item is still mid-encode - gates submit (parity §A: "a
   * staged attachment is still mid-encode" blocks send/steer alike). */
  hasPending: boolean;
  /** Validates + stages every file, synchronously inserting one "[image N]"
   * marker per accepted file at the textarea's current cursor, then
   * re-encodes each to PNG asynchronously. Every rejection reason from this
   * one call is combined into a single onRejected call (never one per
   * file) - the caller (Composer.tsx) wires this straight to
   * useToasts().push("error", ...), per the wave's failure-feedback
   * convention; this hook has no toast/DOM-banner opinion of its own. */
  ingestFiles(files: File[], onRejected: (message: string) => void): void;
  /** Removes one item by its stable marker (not array index, which shifts
   * as items are added/removed) and strips its "[image N]" text from the
   * textarea. Never resets the marker counter - only clearSubmitted does,
   * and only when the result is empty (parity: removing the highest
   * marker still never reuses it). */
  removeItem(marker: number): void;
  /** Maps settled items to the wire-facing shape stores/threads.ts expects
   * - mediaType/data/name only, dropping the UI-only width/height/marker/
   * pending fields. Callers should only invoke this once hasPending is
   * false (nothing here defends against an in-flight item; the composer's
   * own submit-gating on hasPending is what prevents that). */
  toInputAttachments(): InputAttachment[];
  /** Removes exactly the items whose marker is in `submittedMarkers` -
   * called after a successful send/steer/queue/drain with the marker set
   * from the snapshot taken at submit time, so anything staged WHILE that
   * request was in flight (not part of the snapshot) survives untouched.
   * Resets the marker counter to restart at 1 only when the result is
   * empty (mirrors composer-attachments.js's resetMarkerCounter, called
   * only when clearSubmittedComposerItems empties the list). */
  clearSubmitted(submittedMarkers: Set<number>): void;
}

export function useAttachments(textareaRef: RefObject<HTMLTextAreaElement | null>): UseAttachmentsResult {
  const [items, setItems] = useState<PendingAttachment[]>([]);
  // The marker high-water mark is deliberately NOT derived from items.length
  // (which shrinks on removal) - a plain ref persists across renders without
  // re-triggering one, exactly mirroring composer-attachments.js's own
  // pendingState.__nextMarker bookkeeping.
  const nextMarkerRef = useRef(0);

  const ingestFiles = useCallback(
    (files: File[], onRejected: (message: string) => void) => {
      const rejections: string[] = [];
      const accepted: { file: File; marker: number }[] = [];
      // Reserved count starts at the CURRENT pending total and increments
      // per accepted file within this same batch (matching
      // reserveAttachmentItems's own running `reserved` counter) - a drop
      // of 8 files at once must reject the 9th within that single batch,
      // not just across separate gestures.
      let reservedCount = items.length;
      for (const file of files) {
        const reason = rejectionReason({ type: file.type, size: file.size, name: file.name }, reservedCount);
        if (reason) {
          rejections.push(reason);
          continue;
        }
        reservedCount++;
        nextMarkerRef.current += 1;
        accepted.push({ file, marker: nextMarkerRef.current });
      }

      if (accepted.length > 0) {
        const el = textareaRef.current;
        const newItems: PendingAttachment[] = accepted.map(({ file, marker }) => {
          // Inserted synchronously, in order, before any async decode - a
          // sibling marker in the SAME batch chains off the cursor position
          // the previous insertAtCursor call already advanced to.
          if (el) insertAtCursor(el, markerText(marker));
          return { marker, name: file.name, mediaType: "image/png", pending: true };
        });
        setItems((prev) => [...prev, ...newItems]);

        for (const { file, marker } of accepted) {
          reencodeToPng(file)
            .then(({ data, width, height }) => {
              setItems((prev) =>
                prev.map((item) => (item.marker === marker ? { ...item, data, width, height, pending: false } : item)),
              );
            })
            .catch(() => {
              stripMarker(textareaRef.current, marker);
              setItems((prev) => prev.filter((item) => item.marker !== marker));
              onRejected(`${file.name || "unknown"} (image decode failed)`);
            });
        }
      }

      if (rejections.length > 0) {
        onRejected(
          rejections.length === 1
            ? `Couldn't attach ${rejections[0]}`
            : `Couldn't attach ${rejections.length} files: ${rejections.join(", ")}`,
        );
      }
    },
    [items, textareaRef],
  );

  const removeItem = useCallback(
    (marker: number) => {
      stripMarker(textareaRef.current, marker);
      setItems((prev) => prev.filter((item) => item.marker !== marker));
    },
    [textareaRef],
  );

  const clearSubmitted = useCallback((submittedMarkers: Set<number>) => {
    setItems((prev) => {
      const next = prev.filter((item) => !submittedMarkers.has(item.marker));
      if (next.length === 0) nextMarkerRef.current = 0;
      return next;
    });
  }, []);

  const toInputAttachments = useCallback((): InputAttachment[] => {
    return items
      .filter((item): item is PendingAttachment & { data: string } => item.data !== undefined)
      .map((item) => ({ mediaType: item.mediaType, data: item.data, name: item.name }));
  }, [items]);

  return {
    items,
    hasPending: items.some((item) => item.pending),
    ingestFiles,
    removeItem,
    toInputAttachments,
    clearSubmitted,
  };
}
