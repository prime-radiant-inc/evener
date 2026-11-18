// useAttachments orchestrates the composer's staged-image pipeline (parity-
// m5-composer.md §G, contracts §Attachments): validate against the package's
// attachmentLimits, reserve a marker + splice it into the composer text
// synchronously (before any async work - test-composer-image-markers.js's
// own "pending: true" contract), re-encode to PNG via encodePng.ts, then
// settle the item in place. By default the bag belongs to the mounted
// composer. A caller can supply an in-memory store to retain the bag and its
// encode continuations across remounts. Image bytes never go through
// localStorage.
import {
  insertMarker,
  markerText,
  nextComposerImageMarker,
  rejectionReason,
  stripMarker,
} from "@evener/appwire-client";
import { type SetStateAction, useMemo, useState } from "react";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { createSecureUUID } from "../../../../stores/secureUUID";
import type { InputAttachment } from "../../../../stores/threads";
import { reencodeToPng } from "./encodePng";

export interface PendingAttachment {
  /** This image's identity, stable from the paste that creates it until the
   * send that consumes it. A marker number is a text reference and the merge
   * renumbers it, so every per-image fact (foreign, restored) keys on this and
   * never on `marker`. */
  id: string;
  marker: number;
  name: string;
  mediaType: string;
  width?: number;
  height?: number;
  data?: string; // base64; undefined while pending === true
  pending: boolean;
}

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
  write(text: string, cursor: number, source?: TextEditSource): void;
}

export interface UseAttachmentsResult {
  items: PendingAttachment[];
  /** Replaces the staged items with already-encoded durable attachments
   * restored into Composer state. Their marker high-water mark remains
   * authoritative for attachments added afterward. */
  replaceWithSettled(items: PendingAttachment[]): void;
  /** Clears every staged item, restarts marker numbering, and invalidates all
   * encode continuations already in flight. The editor text is owned by the
   * caller and is deliberately left untouched. */
  reset(): void;
  /** True while any item is still mid-encode - gates submit (parity §A: "a
   * staged attachment is still mid-encode" blocks send/steer alike). */
  hasPending: boolean;
  /** Validates + stages every file, synchronously splicing one "[image N]"
   * marker per accepted file into the composer text, replacing the selection,
   * then re-encodes each to PNG asynchronously. Every rejection reason from
   * this one call is combined into a single onRejected call (never one per
   * file) - the caller (Composer.tsx) wires this straight to
   * useToasts().push("error", ...), per the wave's failure-feedback
   * convention; this hook has no toast/DOM-banner opinion of its own. */
  ingestFiles(files: File[], onRejected: (message: string) => void): void;
  /** Removes one item by its stable marker (not array index, which shifts
   * as items are added/removed) and strips its "[image N]" text from the
   * composer. Never resets the marker counter - only clearSubmitted does,
   * and only when the result is empty (parity: removing the highest
   * marker still never reuses it).
   *
   * `dropReference` says whether the text marker belongs to this tile. A tile
   * retained from the composition an adopted draft replaced does not own the
   * number it now bears, so removing it must leave the adopted text alone:
   * stripping that reference would edit prose the person did not write and
   * would clear a missing-image refusal as though they had deleted it. */
  removeItem(marker: number, options?: { dropReference?: boolean }): void;
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
  clearSubmitted(retire: Set<number>, strip?: Set<number>): void;
}

export function createAttachmentStore() {
  return createStore(() => ({
    items: [] as PendingAttachment[],
    nextMarkerRef: { current: 0 },
    removedWhilePendingRef: { current: new Set<symbol>() },
    generationRef: { current: 0 },
  }));
}

export type AttachmentStore = ReturnType<typeof createAttachmentStore>;

/**
 * Whether the composition in the editor still owns this attachment's reference.
 *
 * A rebind can adopt a peer's text while this pane still holds its own tile, so
 * a completion that started under one composition can settle under another. The
 * text then belongs to somebody else's image, and a cleanup that edits it erases
 * prose or a missing-image warning the person never asked it to touch. Identity
 * answers this, never the number: the peer's "[image 1]" and this pane's image 1
 * are two different things that share a glyph.
 *
 * Absent, a tile is assumed to own its reference - the shape a composer that
 * never rebinds is in.
 */
export type ReferenceOwnership = (item: { id: string; marker: number }) => boolean;

export function createAttachmentOperations(
  editor: TextEditor,
  store: AttachmentStore,
  ownsReference?: ReferenceOwnership,
): UseAttachmentsResult {
  const setItems = (next: SetStateAction<PendingAttachment[]>) => {
    store.setState((state) => ({ items: typeof next === "function" ? next(state.items) : next }));
  };
  // The marker high-water mark is deliberately NOT derived from items.length
  // (which shrinks on removal) - a plain ref persists across renders without
  // re-triggering one, exactly mirroring composer-attachments.js's own
  // pendingState.__nextMarker bookkeeping.
  const { nextMarkerRef, removedWhilePendingRef, generationRef } = store.getState();
  // kata kt4j: operation tokens removed via removeItem while decode was still
  // in flight. The browser's Image/canvas pipeline has no cancellation hook,
  // so a reencodeToPng call started before removal keeps running and still
  // settles (success or failure) afterward - ingestFiles' own .then/.catch
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
    const previous = store.getState().items;
    setItems(
      nextItems.map((item) => {
        // By IDENTITY, not by array position. A recovery replacement can exclude
        // a tile from the merge and renumber what stays, so an item that is
        // still decoding can move to a different index; matching it by position
        // would hand its decode token to whatever now sits there, and the tile
        // that actually owns the token would never settle - staying `pending`
        // and blocking submission until the person removed it.
        const original = previous.includes(item) ? item : previous.find((candidate) => candidate.id === item.id);
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
    nextMarkerRef.current = 0;
    setItems([]);
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
      const reason = rejectionReason({ type: file.type, size: file.size, name: file.name }, reservedCount);
      if (reason) {
        rejections.push(reason);
        continue;
      }
      reservedCount++;
      nextMarkerRef.current = nextComposerImageMarker(
        editor.read().text,
        store.getState().items,
        nextMarkerRef.current,
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
      const newItems: PendingAttachment[] = accepted.map(({ file, marker, token }) => {
        const edit = insertMarker(text, start, end, markerText(marker));
        text = edit.value;
        start = end = edit.cursor;
        const item = { id: createSecureUUID(), marker, name: file.name, mediaType: "image/png", pending: true };
        operationTokens.set(item, token);
        return item;
      });
      // Bytes first, then the text that names them. The editor
      // write is the commit point: a source persists its draft on that call,
      // recording exactly the markers its attachment store holds at that
      // instant. Writing the marker first would persist an anchor the record
      // could not attribute to an image, and an adopting reader would then
      // send it as literal text with no image behind it.
      setItems((prev) => [...prev, ...newItems]);
      editor.write(text, start);

      for (const { file, token } of accepted) {
        reencodeToPng(file)
          .then(({ data, width, height }) => {
            if (generationRef.current !== generation) return;
            // Discarded before it settled (kata kt4j) - nothing left to update.
            if (removedWhilePendingRef.current.delete(token)) return;
            setItems((prev) =>
              prev.map((item) =>
                operationToken(item) === token ? { ...item, data, width, height, pending: false } : item,
              ),
            );
          })
          .catch(() => {
            if (generationRef.current !== generation) return;
            // Same discard check as above - the user already removed this
            // attachment, so its eventual decode failure is nothing to
            // strip from the editor or tell them about.
            if (removedWhilePendingRef.current.delete(token)) return;
            const currentItem = store.getState().items.find((item) => operationToken(item) === token);
            if (currentItem) {
              // Edit the text only when this tile still OWNS the reference it
              // bears. Counting tiles by number is not that question: a pane
              // that rebinds to a peer draft spelling "[image 1]" while its own
              // image 1 is still decoding leaves that tile the only one on the
              // number, and a count would then call it the owner and strip the
              // peer's reference out of the adopted text - erasing their prose
              // or the missing-image warning that tells them it will not send.
              // Ownership is a property of the attachment, so the id answers.
              if (!ownsReference || ownsReference(currentItem)) {
                const current = editor.read();
                const stripped = stripMarker(current.text, current.cursor, currentItem.marker);
                editor.write(stripped.value, stripped.cursor ?? current.cursor, "cleanup");
              }
              setItems((prev) => prev.filter((item) => operationToken(item) !== token));
            }
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
  };

  const removeItem = (marker: number, options?: { dropReference?: boolean }) => {
    // Recorded unconditionally, whether or not this item is still pending
    // (kata kt4j) - cheap and self-cleaning either way: a pending item's
    // own decode drains this entry the instant it settles (see
    // ingestFiles' .then/.catch above), and an already-settled item's
    // removal adds an entry no in-flight decode will ever consult again.
    const removed = store.getState().items.find((item) => item.marker === marker);
    if (removed) removedWhilePendingRef.current.add(operationToken(removed));
    if (options?.dropReference === false) {
      // The number is not this tile's, so the text's reference describes
      // something else. Drop only the bytes.
      setItems((prev) => prev.filter((item) => item.marker !== marker));
      return;
    }
    const current = editor.read();
    const stripped = stripMarker(current.text, current.cursor, marker);
    // Same ordering as ingestFiles, for the same reason: the persisted record
    // this write produces must not cite bytes this source just dropped.
    setItems((prev) => prev.filter((item) => item.marker !== marker));
    editor.write(stripped.value, stripped.cursor ?? current.cursor);
  };

  /** Retire submitted tiles, editing the text only for references this
   * composition still owns.
   *
   * `retire` is every tile this press is done with. `strip` is the subset whose
   * `[image N]` the text should lose - a tile the composition kept after a
   * rebind is staged here but its number in the new text is the adopted draft's
   * reference, so stripping it would erase prose the person never wrote and
   * clear a missing-image tag. Retirement is identity's business; the text edit
   * is the composition's. */
  const clearSubmitted = (retire: Set<number>, strip: Set<number> = retire) => {
    if (strip.size > 0) {
      // Each write describes one exact splice, so atoms between removed
      // markers retain their identities. Live editor refs update synchronously.
      // A marker already removed by a concurrent edit remains a safe no-op.
      let { text, cursor } = editor.read();
      for (const marker of strip) {
        const stripped = stripMarker(text, cursor, marker);
        text = stripped.value;
        cursor = stripped.cursor ?? cursor;
        editor.write(text, cursor, "submission");
      }
    }
    setItems((prev) => {
      const next = prev.filter((item) => !retire.has(item.marker));
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
        generationRef.current += 1;
      }
      return next;
    });
  };

  const toInputAttachments = (): InputAttachment[] => {
    return store
      .getState()
      .items.filter((item): item is PendingAttachment & { data: string } => item.data !== undefined)
      .map((item) => ({ marker: item.marker, mediaType: item.mediaType, data: item.data, name: item.name }));
  };

  return {
    get items() {
      return store.getState().items;
    },
    replaceWithSettled,
    reset,
    get hasPending() {
      return store.getState().items.some((item) => item.pending);
    },
    ingestFiles,
    removeItem,
    toInputAttachments,
    clearSubmitted,
  };
}

export function useAttachments(
  editor: TextEditor,
  backingStore?: AttachmentStore,
  ownsReference?: ReferenceOwnership,
): UseAttachmentsResult {
  const [localStore] = useState(() => backingStore ?? createAttachmentStore());
  const store = backingStore ?? localStore;
  const items = useStore(store, (state) => state.items);
  const operations = useMemo(
    () => createAttachmentOperations(editor, store, ownsReference),
    [editor, store, ownsReference],
  );
  return { ...operations, items, hasPending: items.some((item) => item.pending) };
}
