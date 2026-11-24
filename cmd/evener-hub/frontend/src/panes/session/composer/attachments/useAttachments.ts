// useAttachments orchestrates the composer's staged-image pipeline (parity-
// m5-composer.md §G, contracts §Attachments): validate against the package's
// attachmentLimits, reserve a marker + splice it into the composer text
// synchronously (before any async work - test-composer-image-markers.js's
// own "pending: true" contract), decode + encode one at a time via
// encodeImage.ts, then settle the item in place with its authoritative
// mediaType. By default the bag belongs to the mounted composer. A caller can
// supply an in-memory store to retain the bag and its encode continuations
// across remounts. Image bytes never go through localStorage.
import {
  admissionRejection,
  type ImageMarkerRange,
  insertMarker,
  MAX_ATTACHMENTS,
  MAX_SOURCE_BYTES,
  markerText,
  nextComposerImageMarker,
  stripMarker,
  stripMarkerAt,
  type TextSplice,
} from "@evener/appwire-client";
import { type SetStateAction, useMemo, useState } from "react";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { InputAttachment } from "../../../../stores/threads";
import { spelledImageMarkers } from "../submitProtocol";
import { encodeImage, ImageEncodeError } from "./encodeImage";

function preDecodeRejection(file: Pick<File, "name" | "type" | "size">, reservedCount: number): string | undefined {
  const name = file.name || "unknown";
  const admission = admissionRejection({ type: file.type }, reservedCount);
  if (admission === "notImage") return name;
  if (admission === "tooMany") return `${name} (maximum ${MAX_ATTACHMENTS} images)`;
  if (file.size > MAX_SOURCE_BYTES) return `${name} (maximum 25 MB)`;
  return undefined;
}

function encodeFailureMessage(fileName: string, error: unknown): string {
  const name = fileName || "unknown";
  if (error instanceof ImageEncodeError) {
    switch (error.code) {
      case "tooManyPixels":
        return `${name} (image too large; maximum 48 MP)`;
      case "tooLarge":
        return `${name} (maximum 8 MB)`;
      case "decodeFailed":
        return `${name} (image decode failed)`;
    }
  }
  return `${name} (image decode failed)`;
}

export interface PendingAttachment {
  marker: number;
  name: string;
  mediaType: string;
  width?: number;
  height?: number;
  data?: string; // base64; undefined while pending === true
  pending: boolean;
}

/** The one refusal every guarded press shows while a staged image encodes. */
const PENDING_ATTACHMENT_REFUSAL = "Image attachment is still processing";

// Markers identify text references and may change when recovery merges around
// a literal marker. Encode continuations instead own this stable item token.
const operationTokens = new WeakMap<PendingAttachment, symbol>();

function operationToken(item: PendingAttachment): symbol {
  let token = operationTokens.get(item);
  if (!token) {
    token = Symbol("pending attachment");
    operationTokens.set(item, token);
  }
  return token;
}

// TextEditor is the seam this hook uses to change the composer's text
// instead of ever touching a DOM node's `.value` directly: the textarea is
// a React-controlled input, and a direct `el.value = ...` mutation gets
// silently reverted by React's own controlled-input restoration pass when
// a "change"-family native event fires ANYWHERE in the tree (verified: the
// hidden file-picker input's own change event specifically, not a click or
// a paste event - caught by Composer.test.tsx's file-picker attach case
// actually failing, not by inspection). Composer.tsx is the one owner of
// `text` state, so it implements both halves: read() reports the CURRENT
// text and native cursor position (fresh, not cached, so sequential
// inserts in one ingestFiles() batch each see the previous insert's own
// result); write() applies a new value through setText() and separately
// restores the native cursor position once that commits (its own
// layout-effect, since only Composer.tsx has that lifecycle hook).
//
// Implementer's invariant, learned the hard way: read() must be equally
// "live" in EVERY field, not just some. A first Composer.tsx implementation
// read cursor live (off the DOM ref) but text from a plain per-render
// const - ingestFiles' own decode-failure .catch() can resume long after
// the render that registered it, so that stale `text` closure, fed back
// through write()'s (always-stable) setText, silently reverted the whole
// composer to however old that render's text was - discarding anything
// typed since. Composer.tsx's fix mirrors BOTH text and cursor through
// refs updated synchronously the instant write() runs, not waiting for a
// React commit - see its own textRef/cursorToRestoreRef comments. This
// hook's own tests use a fake TextEditor backed by one shared closure
// (see useAttachments.test.ts's makeFakeEditor), which is always-current
// by construction and therefore can't reproduce that specific staleness
// class - Composer.test.tsx's own regression test is what has to.
export type TextEditSource = "edit" | "submission" | "cleanup";

export interface TextEditor {
  /** Selection is the UTF-16 range replaced by an accepted attachment marker. */
  read(): { text: string; cursor: number; selection: { start: number; end: number } };
  /** `splice`, when supplied, is the exact span in the CURRENT text this write
   * replaced and how much text went in its place. It is what makes the write
   * unambiguous: a gesture that inserts a marker immediately before an
   * existing one produces a before/after pair that shares "[image " with the
   * untouched marker, and inferring the edit from that pair would put the
   * insertion INSIDE it and retire its provenance. Callers that know their own
   * coordinates should always say so. */
  write(text: string, cursor: number, source?: TextEditSource, splice?: TextSplice): void;
  /** Returns the tracked staged occurrence range for `marker` (if still in
   * text) and whether `marker` ever had positional provenance. Used so tile
   * removal and cleanup delete only the staged occurrence, preserving literals. */
  stagedRangeFor?(marker: number): { range?: ImageMarkerRange; positioned: boolean };
}

export interface UseAttachmentsResult {
  items: PendingAttachment[];
  /** Replaces the staged items with already-encoded durable attachments
   * restored into Composer state. Their marker high-water mark remains
   * authoritative for attachments added afterward. */
  replaceWithSettled(items: PendingAttachment[]): void;
  /** Moves every staged item whose marker `text` spells to a free number,
   * leaving `text` exactly as it arrived. A retained composer keeps its tiles
   * across the draft another pane replaced, so a tile's number can name an
   * occurrence the incoming text owns as ordinary prose; leaving the tile
   * there would pair that literal with this tile's bytes at Send. The tile's
   * identity (its bytes and its encode continuation) moves with it - only the
   * number changes. `reserved` names numbers this move must also skip. */
  renumberOff(text: string, reserved?: readonly number[]): void;
  /** Clears every staged item, restarts marker numbering, and invalidates all
   * encode continuations already in flight. The editor text is owned by the
   * caller and is deliberately left untouched. */
  reset(): void;
  /** True while any item is still mid-encode - gates submit (parity §A: "a
   * staged attachment is still mid-encode" blocks send/steer alike). */
  hasPending: boolean;
  /** Why submitting or restoring now must be refused - the shared message for
   * a staged item that is still mid-encode - or undefined when none is. */
  refusalReason(): string | undefined;
  /** Validates + stages every file, synchronously splicing one "[image N]"
   * marker per accepted file into the composer text, replacing the selection,
   * then encodes each serially via encodeImage. Every rejection reason from
   * this one call is combined into a single onRejected call (never one per
   * file) - the caller (Composer.tsx) wires this straight to
   * useToasts().push("error", ...), per the wave's failure-feedback
   * convention; this hook has no toast/DOM-banner opinion of its own. */
  ingestFiles(files: File[], onRejected: (message: string) => void): void;
  /** Removes one item by its stable marker (not array index, which shifts
   * as items are added/removed) and strips its "[image N]" text from the
   * composer. Never resets the marker counter - only clearSubmitted does,
   * and only when the result is empty (parity: removing the highest
   * marker still never reuses it). */
  removeItem(marker: number): void;
  /** Maps settled items to the InputAttachment shape stores/threads.ts
   * expects - marker/mediaType/data/name, dropping the UI-only width/height/
   * pending fields. The marker travels so the store can pair text and
   * attachment by identity (see InputAttachment's own doc comment); the wire
   * never sees it. Callers should only invoke this once hasPending is false
   * (nothing here defends against an in-flight item; the composer's own
   * submit-gating on hasPending is what prevents that). */
  toInputAttachments(): InputAttachment[];
  /** Removes exactly the items whose marker is in `submittedMarkers` -
   * called after a successful send/steer/queue/drain with the marker set
   * from the snapshot taken at submit time, so anything staged WHILE that
   * request was in flight (not part of the snapshot) survives untouched.
   * Also strips each submitted marker's own "[image N]" text from the
   * editor (same as removeItem) - without this, a concurrent edit that
   * diverged the composer's text from the submitted snapshot would leave
   * an orphaned marker with no backing item behind (harmless when the
   * caller ALSO blanks the whole textarea alongside this call, the common
   * case, but a real, silent leak otherwise). Resets the marker counter to
   * restart at 1 only when the result is empty (mirrors composer-
   * attachments.js's resetMarkerCounter, called only when
   * clearSubmittedComposerItems empties the list). */
  clearSubmitted(submittedMarkers: Set<number>): void;
}

export interface QueuedImageEncode {
  generation: number;
  token: symbol;
  start: () => Promise<void>;
}

export function createAttachmentStore() {
  return createStore(() => ({
    items: [] as PendingAttachment[],
    nextMarkerRef: { current: 0 },
    removedWhilePendingRef: { current: new Set<symbol>() },
    generationRef: { current: 0 },
    encodeQueueRef: { current: [] as QueuedImageEncode[] },
    encodeInFlightRef: { current: false },
    // Numbers a staged image may not take: an adoption record this composer
    // carries (a draft it read but cannot back with bytes) still names them.
    // The composer source fills this from its own provenance; a store nothing
    // fills reserves nothing, which is the mounted-composer-only path's
    // pre-existing behavior.
    reservedMarkersRef: { current: [] as number[] },
  }));
}

export type AttachmentStore = ReturnType<typeof createAttachmentStore>;

export function createAttachmentOperations(editor: TextEditor, store: AttachmentStore): UseAttachmentsResult {
  const setItems = (next: SetStateAction<PendingAttachment[]>) => {
    store.setState((state) => ({ items: typeof next === "function" ? next(state.items) : next }));
  };
  const stripTrackedMarker = (text: string, cursor: number | undefined, marker: number) => {
    if (editor.stagedRangeFor) {
      const tracked = editor.stagedRangeFor(marker);
      if (tracked.range) return stripMarkerAt(text, cursor, tracked.range);
      if (tracked.positioned) return { value: text, cursor };
    }
    return stripMarker(text, cursor, marker);
  };
  // The marker high-water mark is deliberately NOT derived from items.length
  // (which shrinks on removal) - a plain ref persists across renders without
  // re-triggering one, exactly mirroring composer-attachments.js's own
  // pendingState.__nextMarker bookkeeping.
  const {
    nextMarkerRef,
    removedWhilePendingRef,
    generationRef,
    encodeQueueRef,
    encodeInFlightRef,
    reservedMarkersRef,
  } = store.getState();
  // kata kt4j: operation tokens removed via removeItem while decode was still
  // in flight. The browser's Image/canvas pipeline has no cancellation hook,
  // so an encodeImage call started before removal keeps running and still
  // settles (success or failure) afterward - the queue continuation checks
  // below check this set before touching the editor or calling onRejected,
  // so a decode failure for something the user already discarded is
  // silently swallowed instead of toasted. Populated SYNCHRONOUSLY inside
  // removeItem, not derived from `items` at settle time - a render-cycle-
  // timed read is the wrong tool for an async continuation that can resume
  // arbitrarily later (the same lesson TextEditor's read()/write() above
  // learned the hard way, see this file's own header comment). Drained by
  // whichever settle branch runs first, so it only ever holds markers
  // currently removed-while-pending.
  // A reset cannot cancel browser image/canvas work already in flight, so each
  // continuation captures this generation and becomes a no-op if replacement
  // increments it before the encode settles.

  const replaceWithSettled = (nextItems: PendingAttachment[]) => {
    nextMarkerRef.current = nextItems.reduce((highest, item) => Math.max(highest, item.marker), 0);
    // A recovery merge carries existing staged objects alongside new items.
    // Preserve operation identity when the merge remaps a staged item's marker.
    // Current attachments remain the ordered prefix of the merged composition.
    const previous = store.getState().items;
    setItems(
      nextItems.map((item, index) => {
        const original = previous.includes(item) ? item : previous[index];
        let token: symbol | undefined;
        if (
          original &&
          original.name === item.name &&
          original.mediaType === item.mediaType &&
          original.data === item.data &&
          original.pending === item.pending
        ) {
          token = operationToken(original);
        }
        const retained = previous.includes(item) ? item : { ...item };
        operationTokens.set(retained, token ?? operationToken(retained));
        return retained;
      }),
    );
  };

  const reset = () => {
    generationRef.current += 1;
    removedWhilePendingRef.current.clear();
    encodeQueueRef.current.length = 0;
    nextMarkerRef.current = 0;
    setItems([]);
  };

  const drainEncodeQueue = () => {
    if (encodeInFlightRef.current) return;
    while (encodeQueueRef.current.length > 0) {
      const next = encodeQueueRef.current.shift();
      if (!next) continue;
      if (generationRef.current !== next.generation) continue;
      if (removedWhilePendingRef.current.delete(next.token)) continue;
      encodeInFlightRef.current = true;
      void next.start().finally(() => {
        encodeInFlightRef.current = false;
        drainEncodeQueue();
      });
      return;
    }
  };

  const renumberOff = (text: string, reserved: readonly number[] = []) => {
    // A marker the text spells is that text's own occurrence - whoever wrote
    // it, it is not an anchor this composer staged for this text.
    const spelled = new Set(spelledImageMarkers(text));
    if (spelled.size === 0) return;
    const blocked = [...new Set([...reserved, ...reservedMarkersRef.current])];
    const items = store.getState().items;
    let highWater = nextMarkerRef.current;
    // Nothing moved means the text spells none of this composer's numbers, which
    // is the ordinary rebind and must stay a no-op.
    let moved = false;
    // The accumulating list starts as the current tiles and takes each new
    // number as it is assigned, so `nextComposerImageMarker` sees both the
    // unmoved tiles' numbers and the ones this same call already handed out.
    const assigned: PendingAttachment[] = [...items];
    for (const [index, item] of items.entries()) {
      if (!spelled.has(item.marker)) continue;
      // Each assignment starts above the previous one, so two moved tiles never
      // land on the same number and no move can land on a tile that is staying.
      const marker = nextComposerImageMarker(text, assigned, highWater, blocked);
      highWater = marker;
      assigned[index] = { ...item, marker };
      moved = true;
    }
    if (!moved) return;
    // replaceWithSettled matches each item against the one at its own index, so
    // a moved tile keeps its operation token and therefore its in-flight encode
    // (and, once settled, its bytes).
    replaceWithSettled(assigned);
    // The assigned numbers are this high-water mark now; a later gesture starts
    // above them.
    nextMarkerRef.current = highWater;
  };

  const ingestFiles = (files: File[], onRejected: (message: string) => void) => {
    const generation = generationRef.current;
    const rejections: string[] = [];
    const accepted: { file: File; marker: number; token: symbol }[] = [];
    // Reserved count starts at the CURRENT pending total and increments
    // per accepted file within this same batch (matching
    // reserveAttachmentItems's own running `reserved` counter) - a drop
    // of 8 files at once must reject the 9th within that single batch,
    // not just across separate gestures.
    let reservedCount = store.getState().items.length;
    for (const file of files) {
      const reason = preDecodeRejection(file, reservedCount);
      if (reason) {
        rejections.push(reason);
        continue;
      }
      reservedCount++;
      nextMarkerRef.current = nextComposerImageMarker(
        editor.read().text,
        store.getState().items,
        nextMarkerRef.current,
        reservedMarkersRef.current,
      );
      accepted.push({ file, marker: nextMarkerRef.current, token: Symbol("pending attachment") });
    }

    if (accepted.length > 0) {
      // Spliced synchronously, in order, before any async decode - each
      // sibling marker in the SAME batch chains off the cursor position
      // the previous insertMarker call already advanced to.
      let {
        text,
        selection: { start, end },
      } = editor.read();
      // One write reaches the editor for the whole batch, so its splice is the
      // batch's own: the selection the gesture replaced, and the markers put
      // there. Reported as exact coordinates rather than left to be inferred -
      // an insertion directly before an adopted marker shares "[image " with
      // it, and the inference would retire that marker.
      const splice: TextSplice = { start, end, insertedLength: 0 };
      const newItems: PendingAttachment[] = accepted.map(({ file, marker, token }) => {
        const literal = markerText(marker);
        const edit = insertMarker(text, start, end, literal);
        text = edit.value;
        start = end = edit.cursor;
        splice.insertedLength += literal.length;
        const item = { marker, name: file.name, mediaType: "image/png", pending: true };
        operationTokens.set(item, token);
        return item;
      });
      setItems((prev) => [...prev, ...newItems]);
      // The text and the tile that backs its marker are one gesture, so the
      // persisted record is written once, AFTER the tile exists: the write
      // carries the marker in stagedImageMarkers because the bytes are in the
      // store by then. Writing the text first would persist an anchor with no
      // provenance, and the store's own change notification would then depend
      // on a second write to repair it - which is the window a reload, or an
      // encode settling late, could read the wrong draft through.
      editor.write(text, start, undefined, splice);

      for (const { file, token } of accepted) {
        encodeQueueRef.current.push({
          generation,
          token,
          start: async () => {
            try {
              const { data, mediaType, width, height } = await encodeImage(file);
              if (generationRef.current !== generation) return;
              // Discarded before it settled (kata kt4j) - nothing left to update.
              if (removedWhilePendingRef.current.delete(token)) return;
              setItems((prev) =>
                prev.map((item) =>
                  operationToken(item) === token ? { ...item, data, mediaType, width, height, pending: false } : item,
                ),
              );
            } catch (error) {
              if (generationRef.current !== generation) return;
              // Same discard check as above - the user already removed this
              // attachment, so its eventual decode failure is nothing to
              // strip from the editor or tell them about.
              if (removedWhilePendingRef.current.delete(token)) return;
              const currentItem = store.getState().items.find((item) => operationToken(item) === token);
              if (currentItem) {
                // The tile leaves the store before the strip's own write, so the
                // save that write performs never names this marker as staged
                // (see removeItem).
                setItems((prev) => prev.filter((item) => operationToken(item) !== token));
                const current = editor.read();
                const stripped = stripTrackedMarker(current.text, current.cursor, currentItem.marker);
                editor.write(stripped.value, stripped.cursor ?? current.cursor, "cleanup", stripped.splice);
              }
              onRejected(encodeFailureMessage(file.name, error));
            }
          },
        });
      }
      drainEncodeQueue();
    }

    if (rejections.length > 0) {
      onRejected(
        rejections.length === 1
          ? `Couldn't attach ${rejections[0]}`
          : `Couldn't attach ${rejections.length} files: ${rejections.join(", ")}`,
      );
    }
  };

  const removeItem = (marker: number) => {
    // Recorded unconditionally, whether or not this item is still pending
    // (kata kt4j) - cheap and self-cleaning either way: a pending item's
    // own decode drains this entry the instant it settles (see
    // ingestFiles' .then/.catch above), and an already-settled item's
    // removal adds an entry no in-flight decode will ever consult again.
    const removed = store.getState().items.find((item) => item.marker === marker);
    if (removed) removedWhilePendingRef.current.add(operationToken(removed));
    // The tile leaves the store BEFORE the text write, so whichever save that
    // write performs already sees a composition without it. A save taken while
    // the tile was still there would name the marker's number as staged, and
    // the literal this strip leaves behind - a separate spelling the person
    // typed - would come back after a reload as a missing reference: a draft
    // needing no repair at all, refused.
    setItems((prev) => prev.filter((item) => item.marker !== marker));
    const current = editor.read();
    const stripped = stripTrackedMarker(current.text, current.cursor, marker);
    editor.write(stripped.value, stripped.cursor ?? current.cursor, undefined, stripped.splice);
  };

  const clearSubmitted = (submittedMarkers: Set<number>) => {
    // Same ordering rule as removeItem: the items leave the store first, so the
    // writes below persist a composition that no longer carries them. A save
    // that still saw them would name their numbers as staged even though the
    // text it records no longer holds those occurrences.
    setItems((prev) => {
      const next = prev.filter((item) => !submittedMarkers.has(item.marker));
      if (next.length === 0) {
        // Restarting numbering at 1 lets a later attachment reuse marker 1.
        // Any retired marker still recorded in removedWhilePendingRef must not
        // survive that reuse: removeItem records EVERY removed marker (a
        // settled one's entry is never drained by a continuation), so a reused
        // marker's successful encode would be discarded as "already removed"
        // and the item would stay pending forever. Clear the retired set, and
        // retire the continuations that referenced it by advancing the
        // generation, so neither an old removal nor an old settle can touch
        // the reused marker. Older invalidation (reset) does the same.
        nextMarkerRef.current = 0;
        removedWhilePendingRef.current.clear();
        encodeQueueRef.current.length = 0;
        generationRef.current += 1;
      }
      return next;
    });
    if (submittedMarkers.size > 0) {
      // Each write describes one exact splice, so atoms between removed
      // markers retain their identities. Live editor refs update synchronously.
      // A marker already removed by a concurrent edit remains a safe no-op.
      let { text, cursor } = editor.read();
      for (const marker of submittedMarkers) {
        const stripped = stripTrackedMarker(text, cursor, marker);
        text = stripped.value;
        cursor = stripped.cursor ?? cursor;
        editor.write(text, cursor, "submission", stripped.splice);
      }
    }
  };

  const toInputAttachments = (): InputAttachment[] => {
    return store
      .getState()
      .items.filter((item): item is PendingAttachment & { data: string } => item.data !== undefined)
      .map((item) => ({ marker: item.marker, mediaType: item.mediaType, data: item.data, name: item.name }));
  };

  const refusalReason = (): string | undefined =>
    store.getState().items.some((item) => item.pending) ? PENDING_ATTACHMENT_REFUSAL : undefined;

  return {
    get items() {
      return store.getState().items;
    },
    replaceWithSettled,
    renumberOff,
    reset,
    get hasPending() {
      return refusalReason() !== undefined;
    },
    refusalReason,
    ingestFiles,
    removeItem,
    toInputAttachments,
    clearSubmitted,
  };
}

export function useAttachments(editor: TextEditor, backingStore?: AttachmentStore): UseAttachmentsResult {
  const [localStore] = useState(() => backingStore ?? createAttachmentStore());
  const store = backingStore ?? localStore;
  const items = useStore(store, (state) => state.items);
  const operations = useMemo(() => createAttachmentOperations(editor, store), [editor, store]);
  return { ...operations, items };
}
