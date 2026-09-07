// Turn-failure end-cap: the diagnostic that closes a failed turn (parity-m4
// §9:237, renderer.js:4259-4278 diagnostic card + 4428-4521 recovery actions).
// It reads TurnModel.error (the wire's TurnError, reducer.ts:216) and renders a
// taxonomy chip + the error message + an optional hint, plus a recovery action
// that re-issues the turn.
//
// Design system: the failure "colour" is carried entirely by the danger Chip
// (its tone is allowlisted in token-contract.test.ts); this component's own CSS
// module stays on neutral ink/edge tokens, because a non-widget stylesheet may
// not reference --danger (the same posture as tools/sandboxEscalation). The
// recovery button is NOT danger-toned - re-issuing a turn is not destructive, so
// spending danger on it would misread under color-is-attention.
//
// The recovery action needs the session ref, which reaches TurnBlock only once
// Session.tsx (a controller-owned chokepoint) passes it down. Until that
// one-line wiring lands, the diagnostic still renders in full - only the action
// button is withheld (see .superpowers/sdd/w8-t3-report.md).

import { useMemo, useState } from "react";
import { sessionActionError } from "../../../protocol/errors";
import type { ItemImage, ItemModel, TurnModel } from "../../../protocol/model";
import type { TurnError } from "../../../protocol/types.gen";
import { translateAttachmentMarkers } from "../../../stores/attachmentMarkers";
import { type InputAttachment, threadsStore, useThreadsStore } from "../../../stores/threads";
import { Button, Chip, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { classifyTurnError } from "./turnFailure";
import styles from "./turnfailure.module.css";

const CLASS = {
  cap: requireClass(styles.cap, "turnfailure.module.css", "cap"),
  head: requireClass(styles.head, "turnfailure.module.css", "head"),
  message: requireClass(styles.message, "turnfailure.module.css", "message"),
  hint: requireClass(styles.hint, "turnfailure.module.css", "hint"),
  hintSummary: requireClass(styles.hintSummary, "turnfailure.module.css", "hintSummary"),
};

// The turn that failed opened with the user's own input as its first item
// (EventUserInput opens a turn, then inserts the userMessage, before the
// assistant works in that same turn - appwire_projection.go:131-168), so it
// is the honest thing to re-issue on retry. Absent (an empty or item-less
// turn), there is nothing to retry.
export interface RetryInput {
  // Composer-style text with "[image N]" anchors, reconstructed from the
  // stored translated prose (see planRetryImages): this is what send()
  // expects as composer text, so a retry that itself fails into recovery
  // rebuilds a composer whose tiles still have their anchors.
  text: string;
  attachments?: InputAttachment[];
  // How many images the originating item carried, including ones whose bytes
  // did not survive to the model (a sha-routed src with no inline data) and
  // therefore could not become attachments. The retry clicker reports the
  // difference as dropped; it must come from the ORIGINATING item, which for
  // a reloaded failure sits in an earlier turn than the failed one.
  sourceImageCount: number;
}

// OriginatingInput is what the lookback for a failed turn's originating input
// found: either something re-issuable, or an image-only(ish) input whose bytes
// did not survive (a sha-routed src with no inline data, e.g. a reloaded
// turn). The latter STOPS the lookback rather than falling through to an
// older, unrelated text prompt: re-issuing that would answer a question the
// reader did not ask.
export type OriginatingInput =
  | { kind: "retry"; input: RetryInput }
  | { kind: "images-unavailable"; sourceImageCount: number };

interface ImageOccurrence {
  marker: number;
  name?: string;
  start: number;
  end: number;
}

// parseOccurrences finds the "(attached image N[: name])" prose the submit
// boundary wrote (attachmentMarkers.ts). A filename may itself contain ")",
// so the name is not simply "up to the first closing paren": after the
// "N: " prefix, the longest known attachment name followed by ")" wins; only
// when no known name fits does the span fall back to the first ")". An
// absent name (or an empty one) is undefined, matching the wire shape where
// an unnamed attachment's marker translates with no name clause.
function parseOccurrences(text: string, knownNames: (string | undefined)[]): ImageOccurrence[] {
  const names = knownNames.filter((name): name is string => name !== undefined && name !== "");
  const prefix = /\(attached image (\d+)/g;
  const occurrences: ImageOccurrence[] = [];
  let match = prefix.exec(text);
  while (match !== null) {
    const marker = Number(match[1]);
    const cursor = match.index + match[0].length;
    const rest = text.slice(cursor);
    if (rest.startsWith(")")) {
      occurrences.push({ marker, start: match.index, end: cursor + 1 });
      prefix.lastIndex = cursor + 1;
    } else if (rest.startsWith(":")) {
      const afterColon = rest.slice(1).startsWith(" ") ? rest.slice(2) : rest.slice(1);
      const base = cursor + (rest.length - afterColon.length);
      const fitting = names
        .filter((name) => afterColon.startsWith(`${name})`))
        .sort((left, right) => right.length - left.length);
      const best = fitting[0];
      if (best !== undefined) {
        occurrences.push({ marker, name: best, start: match.index, end: base + best.length + 1 });
        prefix.lastIndex = base + best.length + 1;
      } else {
        const close = afterColon.indexOf(")");
        if (close === -1) {
          prefix.lastIndex = match.index + 1;
        } else {
          const rawName = afterColon.slice(0, close);
          occurrences.push({
            marker,
            ...(rawName ? { name: rawName } : {}),
            start: match.index,
            end: base + close + 1,
          });
          prefix.lastIndex = base + close + 1;
        }
      }
    } else {
      prefix.lastIndex = match.index + 1;
    }
    match = prefix.exec(text);
  }
  return occurrences;
}

// planRetryImages recovers the originating input's image bytes from the
// model's display-ready ItemImage shape (reducer.ts's imagesToItemImages
// resolves the wire's inline mediaType+data bytes to a data: URI src, which is
// exactly what a live userMessage item carries). Each byte-carrying image
// becomes an InputAttachment the send path already knows how to wire
// (threads.ts's buildInput). Images whose bytes are unavailable (a sha-routed
// src with no inline data) are dropped: resending a name with no bytes would
// fabricate an attachment.
//
// Marker pairing is by attachment NAME first: markers are stable composer ids
// while the attachment array is acceptance order, so positional pairing
// corrupts identity when the user reordered markers in the text. Occurrences
// without a name match fall back to positional pairing over the leftovers,
// but only when their markers run in the same order as the images: an
// out-of-order (or repeated) unnamed marker sequence means the true
// marker-to-bytes identity was lost, and the pairing refuses rather than
// misassigning bytes. Images with no occurrence keep a 1-based positional
// marker. A named occurrence that matches no image at all is foreign
// (user-typed prose, not a translated marker) and is never consumed: pairing
// it would steal a real image under a marker it was never staged with.
//
// The returned text rewrites each resolvable occurrence back to its
// "[image N]" composer anchor: send() re-translates anchors to prose on the
// wire (an exact round-trip - see the tests), while the outbox record keeps
// anchor text as composerText, so a failed retry recovers tiled images
// instead of orphaned prose. An occurrence whose image has no bytes is left
// verbatim: rewriting it would put a raw marker on the wire (kata 6nmz).
//
// ambiguous is true when the pairing cannot be uniquely determined, and the
// caller must refuse the images rather than guess: a name claimed under
// several DIFFERENT markers, several attachments matching one name, or two
// attachments sharing one ASSIGNED marker means the true marker-to-bytes
// identity was lost on the wire. Repeated copies of one "(marker: name)"
// mention over a single attachment are the same claim restated, not a
// conflict, and stay safe. It also covers the belt-and-braces cases below:
// anchor text that does not re-translate back to the stored prose
// byte-for-byte, which proves the pairing corrupted something, and duplicate
// assigned markers, which the round-trip cannot see for unnamed images (every
// "[image N]" re-translates to identical "(attached image N)" prose).
function planRetryImages(
  images: ItemImage[] | undefined,
  text: string,
): { attachments: InputAttachment[]; anchorText: string; ambiguous: boolean } {
  const items = images ?? [];
  const occurrences = parseOccurrences(
    text,
    items.map((image) => image.name),
  );
  const decoded = items.map((image) => {
    const dataUri = /^data:([^;,]+)?;base64,(.*)$/s.exec(image.src);
    if (!dataUri) return undefined;
    return { mediaType: dataUri[1] || "image/png", data: dataUri[2] ?? "", name: image.name };
  });
  const markerForImage: (number | undefined)[] = items.map(() => undefined);
  const rewriteOccurrence: boolean[] = occurrences.map(() => false);
  const claimedImage = items.map(() => false);
  const byName = new Map<string, number[]>();
  items.forEach((image, index) => {
    if (image.name === undefined || image.name === "") return;
    const list = byName.get(image.name) ?? [];
    list.push(index);
    byName.set(image.name, list);
  });
  // Only names participating on both sides can conflict: one name under
  // several markers, or several attachments under one name. Anything else -
  // foreign prose, unnamed images, a lone repeated mention - pairs exactly.
  const markersByName = new Map<string, Set<number>>();
  occurrences.forEach((occurrence) => {
    if (occurrence.name === undefined || !byName.has(occurrence.name)) return;
    const markers = markersByName.get(occurrence.name) ?? new Set<number>();
    markers.add(occurrence.marker);
    markersByName.set(occurrence.name, markers);
  });
  let ambiguous = false;
  for (const [name, markers] of markersByName) {
    if (markers.size > 1 || (byName.get(name)?.length ?? 0) > 1) {
      ambiguous = true;
      break;
    }
  }
  const pendingOccurrences: number[] = [];
  occurrences.forEach((occurrence, occurrenceIndex) => {
    if (occurrence.name !== undefined && !byName.has(occurrence.name)) return;
    const candidate =
      occurrence.name === undefined
        ? undefined
        : (byName.get(occurrence.name) ?? []).find((index) => !claimedImage[index]);
    if (candidate === undefined) {
      pendingOccurrences.push(occurrenceIndex);
      return;
    }
    claimedImage[candidate] = true;
    markerForImage[candidate] = occurrence.marker;
    if (decoded[candidate] !== undefined) rewriteOccurrence[occurrenceIndex] = true;
  });
  const unpairedImages: number[] = [];
  items.forEach((_, index) => {
    if (!claimedImage[index]) unpairedImages.push(index);
  });
  // Unnamed occurrences pair positionally with the leftover images in order:
  // the nth unnamed mention takes the nth unclaimed image. That pairing is
  // only sound when the markers run in the same order as the images — an
  // out-of-order (or gapped) marker sequence means the true marker-to-bytes
  // identity was lost, and guessing would attach the wrong bytes to a tile.
  // A repeated NAMED mention whose image is already claimed is the same claim
  // restated, not a new pairing: it stays unpaired (verbatim prose) rather
  // than consuming an unrelated leftover image under a duplicate marker.
  // Images left without an occurrence keep a fallback marker below, which is
  // always safe (fresh numbers outside the used set).
  let positionalAmbiguous = false;
  const pendingUnnamed: number[] = [];
  pendingOccurrences.forEach((occurrenceIndex) => {
    const occurrence = occurrences[occurrenceIndex];
    if (occurrence !== undefined && occurrence.name === undefined) pendingUnnamed.push(occurrenceIndex);
  });
  const unnamedMarkers = pendingUnnamed.map(
    (occurrenceIndex) => (occurrences[occurrenceIndex] as ImageOccurrence).marker,
  );
  unnamedMarkers.forEach((marker, position) => {
    if (position > 0 && unnamedMarkers[position - 1] !== undefined && marker <= (unnamedMarkers[position - 1] ?? 0)) {
      positionalAmbiguous = true;
    }
  });
  if (positionalAmbiguous) {
    ambiguous = true;
  } else {
    pendingUnnamed.forEach((occurrenceIndex, position) => {
      const imageIndex = unpairedImages[position];
      if (imageIndex === undefined) return;
      const occurrence = occurrences[occurrenceIndex];
      if (occurrence === undefined) return;
      claimedImage[imageIndex] = true;
      markerForImage[imageIndex] = occurrence.marker;
      if (decoded[imageIndex] !== undefined) rewriteOccurrence[occurrenceIndex] = true;
    });
  }
  const attachments: InputAttachment[] = [];
  // Fallback markers come from the unused set, never positional indexes: an
  // index+1 can collide with a real marker when earlier markers were removed
  // or reordered, shadowing an attachment in the translation map and failing
  // the round-trip check below (dropping every attachment, valid ones too).
  // Raw "[image N]" anchors already in the stored text are reserved too: the
  // user may have typed one literally, and a fallback reusing its number
  // would capture it at send translation time, corrupting their prose.
  const usedMarkers = new Set<number>();
  occurrences.forEach((occurrence) => {
    usedMarkers.add(occurrence.marker);
  });
  Array.from(text.matchAll(/\[image (\d+)\]/g)).forEach((match) => {
    usedMarkers.add(Number(match[1]));
  });
  const allocFallbackMarker = (): number => {
    let marker = 1;
    while (usedMarkers.has(marker)) marker += 1;
    usedMarkers.add(marker);
    return marker;
  };
  items.forEach((_, index) => {
    const bytes = decoded[index];
    if (bytes === undefined) return;
    attachments.push({
      marker: markerForImage[index] ?? allocFallbackMarker(),
      mediaType: bytes.mediaType,
      data: bytes.data,
      ...(bytes.name ? { name: bytes.name } : {}),
    });
  });
  let anchorText = "";
  let cursor = 0;
  occurrences.forEach((occurrence, occurrenceIndex) => {
    anchorText += text.slice(cursor, occurrence.start);
    anchorText += rewriteOccurrence[occurrenceIndex]
      ? `[image ${occurrence.marker}]`
      : text.slice(occurrence.start, occurrence.end);
    cursor = occurrence.end;
  });
  anchorText += text.slice(cursor);
  if (!ambiguous && attachments.length > 0) {
    const assignedMarkers = new Set<number>();
    let duplicateMarker = false;
    items.forEach((_, index) => {
      const marker = markerForImage[index];
      if (marker === undefined || decoded[index] === undefined) return;
      if (assignedMarkers.has(marker)) duplicateMarker = true;
      assignedMarkers.add(marker);
    });
    if (duplicateMarker || translateAttachmentMarkers(anchorText, attachments) !== text) {
      ambiguous = true;
    }
  }
  return { attachments, anchorText, ambiguous };
}

function retryItem(turn: TurnModel): ItemModel | undefined {
  return turn.items.find((it) => it.type === "userMessage");
}

function originFromItem(item: ItemModel): OriginatingInput | undefined {
  const storedText = item.text;
  const text = storedText.trim();
  const sourceImageCount = item.images?.length ?? 0;
  const plan = planRetryImages(item.images, text);
  // An image-only input is retryable: buildInput and the server both accept
  // empty text with attachments (parity-m5-composer §B). Text is required
  // only when there is nothing else to send. An ambiguous pairing (duplicate
  // names the wire cannot disambiguate) refuses the images instead of
  // guessing: text still retries with the dropped-image warning below, while
  // an image-only input degrades to the explicit re-attach state.
  if (text || plan.attachments.length > 0) {
    if (plan.ambiguous) {
      if (!text) return { kind: "images-unavailable", sourceImageCount };
      return { kind: "retry", input: { text, sourceImageCount } };
    }
    return {
      kind: "retry",
      input: {
        text: plan.anchorText,
        ...(plan.attachments.length > 0 ? { attachments: plan.attachments } : {}),
        sourceImageCount,
      },
    };
  }
  // Effectively empty text with images whose bytes are gone: the originating
  // input exists but cannot be re-issued. Report it (stopping the lookback)
  // rather than skipping on to an older prompt.
  if (sourceImageCount > 0) return { kind: "images-unavailable", sourceImageCount };
  return undefined;
}

/**
 * The input that opened the exchange `turnId` ended, searched backwards from
 * that turn.
 *
 * A LIVE failure keeps the input in its own turn, where the search finds it
 * immediately. A RELOADED one does not: one persisted transcript entry
 * becomes one turn (apptranscript.go's ProjectTurn), so a failure entry is a
 * turn holding only the failure, and the input sits in an earlier one. Retry
 * was therefore offered while a reader watched a failure happen and withheld
 * from the reader who came back to it - the same failure, the same recovery,
 * present or absent on nothing but when you looked.
 *
 * The search stops AT the failed turn: a later prompt is a different
 * exchange, and re-issuing it would answer a question the reader did not ask.
 *
 * findOriginatingItem is the shared lookback: it returns the originating item
 * itself (never building the payload), so the store selector below can hold
 * the ITEM reference — stable across unrelated updates by the reducer's
 * immutable folds — and compute the full payload from it in render.
 */
export function originatingInput(turns: TurnModel[], turnId: string): OriginatingInput | undefined {
  const item = findOriginatingItem(turns, turnId);
  return item && originFromItem(item);
}

function findOriginatingItem(turns: TurnModel[], turnId: string): ItemModel | undefined {
  const found = turns.findIndex((t) => t.id === turnId);
  const from = found === -1 ? turns.length - 1 : found;
  for (let i = from; i >= 0; i--) {
    const turn = turns[i];
    const item = turn && retryItem(turn);
    if (!item) continue;
    // A turn carrying the user's input always decides the lookback: a
    // retryable input returns, and an image-only input with lost bytes stops
    // it (images-unavailable) rather than yielding to an older prompt. Only
    // a turn with no usable input at all (whitespace-only text, no images)
    // keeps looking backwards. MUST match originFromItem's usable/empty
    // verdict below — a hot-path copy that never builds the payload.
    if (item.text.trim() || (item.images?.length ?? 0) > 0) return item;
  }
  return undefined;
}

export function TurnFailureEndCap({
  error,
  turn,
  sessionRef,
}: {
  error: TurnError;
  turn: TurnModel;
  sessionRef?: string;
}) {
  const info = classifyTurnError(error);
  const toasts = useToasts();
  const [hintOpen, setHintOpen] = useState(false);
  // Selected by ITEM reference, never by payload value: computing the retry
  // payload copies every image's base64 bytes, which would freeze the UI if it
  // ran on each unrelated store update. The reducer's immutable folds hand
  // back the same item reference for untouched turns (reducer.ts's mapItem),
  // and zustand's default Object.is equality then skips the re-render, so the
  // payload is recomputed only when the originating item itself changes.
  const priorItem = useThreadsStore((s) =>
    sessionRef === undefined ? undefined : findOriginatingItem(s.threads.get(sessionRef)?.turns ?? [], turn.id),
  );
  const priorOrigin = useMemo(() => (priorItem === undefined ? undefined : originFromItem(priorItem)), [priorItem]);
  const ownItem = retryItem(turn);
  const ownOrigin = useMemo(
    () => (ownItem === undefined ? undefined : originFromItem(ownItem)),
    // ownItem is derived from the turn prop, not the store: its identity
    // already changes exactly when the failed turn re-renders with new items.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [ownItem],
  );
  // A blank item on the failed turn (whitespace text, no images) yields no
  // origin: fall back to the lookback, exactly as originatingInput skips
  // unusable inputs. An explicit images-unavailable on the failed turn still
  // wins — it names the exchange's own lost bytes, not an older prompt.
  const origin = ownOrigin === undefined ? priorOrigin : ownOrigin;
  const input = origin?.kind === "retry" ? origin.input : undefined;
  const canRetry = sessionRef !== undefined && input !== undefined;
  const unavailableCount = origin?.kind === "images-unavailable" ? origin.sourceImageCount : 0;
  const showReattachNote = unavailableCount > 0;

  // Recovery re-issues the turn's originating input via the existing
  // threadsStore.send action (turn/start), images included: the originating
  // userMessage item still carries the bytes the first send projected
  // (projectUserInputImages), so a retry that resent text alone would answer
  // a different question than the one asked. Bytes that did not survive to
  // the model (a sha-routed src with no inline data, e.g. a reloaded turn)
  // cannot be re-issued; those are dropped and named in a warning toast so
  // the silent text-only resend this fixes never recurs. For a
  // connection-class failure the hub's auto-resume layer transparently
  // relaunches a dead daemon, so a single call serves both the "Retry" and
  // "Reconnect & retry" labels; a failed re-issue surfaces on the shared
  // toast singleton, never a silent swallow.
  async function retry() {
    if (sessionRef === undefined || input === undefined) return;
    try {
      const dropped = input.sourceImageCount - (input.attachments?.length ?? 0);
      await threadsStore.getState().send(sessionRef, input.text, input.attachments);
      if (dropped > 0) {
        toasts.push(
          "warning",
          `Retried without ${dropped === 1 ? "an attached image" : `${dropped} attached images`} - re-attach ${dropped === 1 ? "it" : "them"} to ask about ${dropped === 1 ? "it" : "them"} again.`,
        );
      }
    } catch (e) {
      toasts.push("error", sessionActionError(`${info.recoveryLabel} failed`, e));
    }
  }

  return (
    <div className={CLASS.cap} data-testid="turn-failure" data-turn-error="true">
      <div className={CLASS.head}>
        <Chip tone="danger">{info.badge}</Chip>
        <span className={CLASS.message}>{info.message}</span>
        {info.hint && (
          <button
            type="button"
            className={CLASS.hintSummary}
            aria-expanded={hintOpen}
            onClick={() => setHintOpen((o) => !o)}
          >
            What can I do?
          </button>
        )}
        {hintOpen && info.hint && <div className={CLASS.hint}>{info.hint}</div>}
        {canRetry && (
          <Button variant="primary" size="sm" onClick={() => void retry()}>
            {info.recoveryLabel}
          </Button>
        )}
        {showReattachNote && (
          <span className={CLASS.hint}>
            {unavailableCount === 1
              ? "Attached image unavailable — re-attach the image to retry."
              : `Attached images unavailable — re-attach ${unavailableCount} images to retry.`}
          </span>
        )}
      </div>
    </div>
  );
}
