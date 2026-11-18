// Per-ref sticky composer drafts (parity-m5-composer.md §F, contracts
// §Drafts): unsent textarea text survives a reload, keyed by session ref.
//
// No cross-session leak guard is needed (verified, not assumed): a draft can
// never bleed from one session into another because a Composer element is
// never reused across refs. shell/paneRegistry.ts registers "session" as
// NOT a singleton pane type ("distinct refs are distinct panes" - see
// panes/session/index.tsx's own comment), and shell/DockHost.tsx's PaneHost
// unmounts a pane's whole React tree when its tab isn't active and mounts a
// fresh one when it becomes active again (never re-parents an existing
// Composer instance onto a different ref). So a mounted Composer's `ref`
// prop is fixed for that instance's entire lifetime, and every mount starts
// from React's own empty initial state - there is no "leftover text from a
// different ref" for a fresh mount to ever see. Restoring this ref's draft
// on mount (Composer.tsx) is therefore unconditional, not guarded.
//
// Drafts are STRUCTURED (2026-09 skills lifecycle): one v2 record holds
// {text, skillNames} as a single atomic localStorage value, so a draft's
// canonical skill selections survive a reload next to its text. The v1 key
// held plain text only; see readComposerDraft for the approved transition.
import { canonicalSkillNames, markerPattern } from "@evener/appwire-client";
import { ownClientId } from "../../../stores/mutationClientIdentity";
import { createSecureUUID } from "../../../stores/secureUUID";
import { patchSelectionText, type SkillEditorValue } from "./skillDocument";

const STORAGE_PREFIX = "evener.composer.draft.v1.";
const STRUCTURED_STORAGE_PREFIX = "evener.composer.draft.v2.";
const COMPOSER_SEAT_PREFIX = "evener.composer.seat.v1.";
// Missing-image provenance for a ref's draft, kept beside the draft record
// rather than inside it. A recovery row takes ownership of the draft TEXT into
// IndexedDB and the localStorage record is then cleared, so a fact that rode
// only that record would be lost exactly when the person reopens the draft
// This side record outlives the draft record for the
// same reason the recovery row does: it describes the ref's composition, not
// one storage location's copy of it.
const IMAGE_PROVENANCE_PREFIX = "evener.composer.imageProvenance.v1.";
const draftRevisions = new Map<string, number>();

// A composer draft: the textarea's text plus the canonical names of the
// skills selected for this request. skillNames are canonical catalog names
// (never prose): the submit boundary assembles them as {type: "skill", name}
// input items after the ordinary text/attachment items, and a chip change is
// a draft edit even when the text is byte-identical.
export interface ComposerDraft extends SkillEditorValue {
  // Exact identity for this persisted structured snapshot. Older records have
  // no identity and keep their existing recovery behavior.
  revisionId?: string;
  // Wall-clock stamp of the last write, so two refs' drafts can be ordered when
  // one is carried to the other (see carryComposerDraft). Absent on records an
  // older build wrote.
  editedAt?: number;
}

// The marker numbers whose bytes were staged when this record was written.
//
// Kept OUT of ComposerDraft's public shape on purpose: it is persistence
// provenance, not draft content, and every reader of the draft (chips, the
// editor, the carry) has no use for it. It exists so an adopting composer can
// tell an anchor that HAD bytes - and lost them, because localStorage never
// holds image bytes - from literal "[image 1]" prose the person typed. Only
// the former is unbacked, so only the former refuses a send
// (sourceState's unbackedImageMarkers). Absent on records an older build
// wrote, and those adopt no unbacked markers: an old record cannot prove an
// anchor had bytes, and refusing on a guess would refuse legitimate prose.
interface StoredComposerDraft extends ComposerDraft {
  imageMarkers?: number[];
}

// A remount inherits the same draft revision. Editing or replacing its
// contents changes ownership even when the resulting text is identical.
export function readDraftRevision(ref: string): number {
  return draftRevisions.get(ref) ?? 0;
}

export function markDraftEdited(ref: string): void {
  draftRevisions.set(ref, readDraftRevision(ref) + 1);
}

export function draftStorageKey(ref: string): string {
  return `${STORAGE_PREFIX}${ref}`;
}

export function composerDraftStorageKey(ref: string): string {
  return `${STRUCTURED_STORAGE_PREFIX}${ref}`;
}

function imageProvenanceStorageKey(ref: string): string {
  return `${IMAGE_PROVENANCE_PREFIX}${ref}`;
}

// A conversation seat outlives the pane id a mobile reload reallocates - but
// only the per-pane record below survives a desktop layout save/restore. It
// belongs to this tab alone and never to the shared layout.
function paneSeatKey(paneId: string): string {
  return `${COMPOSER_SEAT_PREFIX}${encodeURIComponent(ownClientId())}.pane.${encodeURIComponent(paneId)}`;
}

function storedSeat(key: string): string | undefined {
  try {
    const value = localStorage.getItem(key);
    return value === null || value === "" ? undefined : value;
  } catch {
    return undefined;
  }
}

// The stored pane mapping without validating its conversation. A desktop
// cascade restore checks continuity through the cascade path instead of the
// exact leaf: another tab may have drilled the shared layout deeper since
// this tab saved its seat.
export function readPaneComposerSeatEntry(paneId: string): { seat: string; ref: string } | undefined {
  const raw = storedSeat(paneSeatKey(paneId));
  if (raw === undefined) return undefined;
  try {
    const parsed = JSON.parse(raw) as { seat?: unknown; ref?: unknown };
    if (typeof parsed.seat !== "string" || typeof parsed.ref !== "string") return undefined;
    return { seat: parsed.seat, ref: parsed.ref };
  } catch {
    // A pre-identity mapping cannot prove which conversation it belongs to.
    return undefined;
  }
}

// Clears a pane-id seat record. A layout save rewrites the entries for the
// panes it holds, so a cleared entry only matters for panes a save will not
// revisit.
export function clearPaneComposerSeat(paneId: string): void {
  try {
    localStorage.removeItem(paneSeatKey(paneId));
  } catch {
    // Best-effort, like every other composer persistence.
  }
}

export function writePaneComposerSeat(paneId: string, ref: string, seat: string): void {
  try {
    localStorage.setItem(paneSeatKey(paneId), JSON.stringify({ ref, seat }));
  } catch {
    // Best-effort, like every other composer persistence.
  }
}

function isStoredComposerDraft(value: unknown): value is StoredComposerDraft {
  if (typeof value !== "object" || value === null) return false;
  const record = value as Partial<StoredComposerDraft>;
  return (
    typeof record.text === "string" &&
    Array.isArray(record.skillNames) &&
    record.skillNames.every((name) => typeof name === "string") &&
    (record.commandNames === undefined ||
      (Array.isArray(record.commandNames) && record.commandNames.every((name) => typeof name === "string"))) &&
    (record.editedAt === undefined || typeof record.editedAt === "number") &&
    (record.revisionId === undefined || typeof record.revisionId === "string") &&
    (record.imageMarkers === undefined ||
      (Array.isArray(record.imageMarkers) &&
        record.imageMarkers.every((marker) => Number.isSafeInteger(marker) && marker > 0))) &&
    (record.mentions === undefined ||
      (Array.isArray(record.mentions) &&
        record.mentions.every(
          (mention) =>
            mention !== null &&
            typeof mention === "object" &&
            (mention.kind === "skill" || mention.kind === "command") &&
            typeof mention.name === "string" &&
            Number.isInteger(mention.offset) &&
            mention.offset >= 0,
        )))
  );
}

// Every localStorage access is guarded: private-mode/disabled/full storage
// degrades silently to "no draft" rather than ever breaking the composer,
// same convention as shell/rail/Rail.tsx's own collapsed-state persistence.
//
// Existing-draft transition (approved): when the v2 record is absent, the old
// v1 PLAIN-TEXT value is read as literal text with skillNames: []. The v1
// value is never JSON-decoded to guess selections and no slash mention in it
// is ever inferred as a selection - an old draft is exactly the text it was.
export function readComposerDraft(ref: string): ComposerDraft {
  const { revisionId: _revisionId, ...draft } = readComposerDraftSnapshot(ref);
  return draft;
}

// Recovery ownership needs content and identity from the same stored snapshot.
export function readComposerDraftSnapshot(ref: string): ComposerDraft {
  try {
    const structured = localStorage.getItem(composerDraftStorageKey(ref));
    if (structured !== null) {
      let parsed: unknown;
      try {
        parsed = JSON.parse(structured);
      } catch {
        parsed = null;
      }
      // The draft is the one selection source the composer reads verbatim, so
      // it is also the one place a corrupted or hand-edited v2 record could
      // hand padded, empty or repeated names straight to the chips. Canonical
      // names are the contract everywhere else; enforce it here too.
      if (isStoredComposerDraft(parsed)) {
        return {
          text: parsed.text,
          skillNames: canonicalSkillNames(parsed.skillNames),
          ...(parsed.commandNames ? { commandNames: canonicalSkillNames(parsed.commandNames) } : {}),
          ...(parsed.mentions ? { mentions: parsed.mentions } : {}),
          ...(parsed.revisionId ? { revisionId: parsed.revisionId } : {}),
        };
      }
    }
    return { text: localStorage.getItem(draftStorageKey(ref)) ?? "", skillNames: [] };
  } catch {
    return { text: "", skillNames: [] };
  }
}

// A draft with nothing to send: no prose and no selections. The one predicate
// the writer and the carry share, so an empty draft is never persisted and a
// blank source never moves.
function isBlankDraft(value: ComposerDraft): boolean {
  return value.text.trim() === "" && value.skillNames.length === 0 && !value.commandNames?.length;
}

// The text half of the structured draft, for callers that only compose prose
// (the dev harness's seeded panes, and tests reading back what they typed).
export function readDraft(ref: string): string {
  return readComposerDraft(ref).text;
}

// A persisted draft's last-write stamp, 0 when it carries none (an older build
// wrote it, or there is no record). Kept out of readComposerDraft's own value:
// the stamp is persistence metadata the carry needs to order two refs, not part
// of the draft's content, and returning it there would change the draft's public
// shape for every reader.
export function readDraftEditedAt(ref: string): number {
  try {
    const structured = localStorage.getItem(composerDraftStorageKey(ref));
    if (structured === null) return 0;
    const parsed: unknown = JSON.parse(structured);
    if (!isStoredComposerDraft(parsed)) return 0;
    return parsed.editedAt ?? 0;
  } catch {
    return 0;
  }
}

// Writes the whole structured draft as ONE atomic v2 record, then removes any
// old v1 value (never the reverse order - a v2 write that crashed before the
// v1 removal still leaves one readable draft behind). Blank text with no
// selections removes the draft outright rather than storing an empty record -
// a draft that would never send is never persisted. A selection is only ever
// staged next to its own visible `/<name>` reference, so a record with names
// and no text is not something the composer produces; one that arrives from an
// older build is normalized on restore rather than trusted here.
export function writeComposerDraft(
  ref: string,
  value: ComposerDraft,
  imageMarkers?: readonly number[],
): string | undefined {
  markDraftEdited(ref);
  // Three outcomes, and they differ: a stored draft, a deliberate blank
  // removal (provenance must go too), and a failed write (provenance must
  // stay describing the draft that is still there). Collapsing the last two
  // into "undefined" is what let a smaller marker list overwrite the old one
  // while the old text survived - a reload then paired text with markers from
  // a write that never happened.
  const outcome = (() => {
    try {
      if (isBlankDraft(value)) {
        localStorage.removeItem(composerDraftStorageKey(ref));
        localStorage.removeItem(draftStorageKey(ref));
        return { kind: "removed" } as const;
      }
      // The one choke point every persisted draft edit goes through, so the stamp
      // is the draft's actual last-write time.
      const revisionId = createSecureUUID();
      localStorage.setItem(
        composerDraftStorageKey(ref),
        JSON.stringify({
          ...value,
          revisionId,
          editedAt: Date.now(),
          ...(imageMarkers && imageMarkers.length > 0 ? { imageMarkers: [...imageMarkers] } : {}),
        }),
      );
      try {
        localStorage.removeItem(draftStorageKey(ref));
      } catch {
        // The structured snapshot is committed and takes precedence on read.
      }
      return { kind: "stored", revisionId } as const;
    } catch {
      // Best-effort: a full quota or Safari private-mode must never be fatal
      // to the composer itself, only to draft persistence across reloads.
      return { kind: "failed" } as const;
    }
  })();
  // Provenance describes the revision that actually committed. A failed write
  // changes nothing here, so the previous draft keeps the markers that
  // describe it, and a removal takes them with the text.
  if (imageMarkers !== undefined) {
    if (outcome.kind === "stored") writeComposerImageMarkers(ref, imageMarkers);
    else if (outcome.kind === "removed") clearComposerImageMarkers(ref);
  }
  return outcome.kind === "stored" ? outcome.revisionId : undefined;
}

export function readComposerDraftRevisionId(ref: string): string | undefined {
  return readComposerDraftSnapshot(ref).revisionId;
}

/**
 * The marker numbers a persisted draft recorded as having image bytes behind
 * them, in the same record readComposerDraftSnapshot just returned.
 *
 * This is the one fact an adopting composer needs to honour the invariant
 * that a marker and its bytes are committed together: a marker listed here
 * had an image, localStorage kept no bytes, so the adopter owns an anchor
 * with nothing behind it and must refuse to send it. A marker NOT listed here
 * is literal prose the person typed, which sends as text.
 *
 * The draft record is authoritative for its OWN text, and the side record is
 * consulted only when there is no draft record to describe. Text and its
 * marker list are one fact, so a write that fails to store the text must not
 * leave a newer list describing the older text: reading both from the same
 * record is what keeps a failed replacement from enabling a stale anchor.
 * The side record exists for the one case the draft record cannot cover - a
 * text that has moved into a recovery row and cleared its local copy.
 */
export function readComposerDraftImageMarkers(ref: string): number[] {
  try {
    const structured = localStorage.getItem(composerDraftStorageKey(ref));
    if (structured !== null) {
      const parsed: unknown = JSON.parse(structured);
      if (isStoredComposerDraft(parsed)) return parsed.imageMarkers ?? [];
    }
  } catch {
    // Fall through to the side record, which is also best-effort.
  }
  return readImageProvenance(ref) ?? [];
}

// ── The composition image record ────────────────────────────────────────────
//
// THE INVARIANT. An image's marker number is a NAME THE TEXT USES, not an
// identity and not a durable key. Numbers are reassigned on every merge,
// every rebind and every activation, so any fact stored against a number can
// be read later against a different image - or against prose that merely
// spells the same glyph. This record therefore keys every fact by ATTACHMENT
// IDENTITY and stores the number only as the label the committed text
// currently uses.
//
// It is ONE record per composition, written with the composition it describes
// and read as a whole, because the facts are only meaningful together:
//
//   anchors  - marker numbers the committed text references that have NO
//              bytes. These are what refuse a send. Stored by number because
//              an anchor has no attachment to be keyed by: its whole nature is
//              that no tile carries it.
//   tiles    - per OWNED attachment: its id, the marker the committed text
//              gives it, whether it is eligible for Send, and whether it
//              still holds bytes. Eligible and hasBytes are different facts:
//              a released tile (reference deleted, tile kept) keeps its bytes
//              so a reload restores it, and is not eligible so the reload
//              cannot send it.
//
// It is keyed by the COMPOSITION'S OWN identity - the recovery row's client
// mutation id - and never by ref. A retarget moves a row to another session
// while keeping its id, so a ref-keyed record is orphaned by exactly the event
// that should carry it. It also means two panes on one ref cannot overwrite
// each other's facts.
//
// Everything the composer decides about images reads this record instead of
// re-deriving from `[image N]` spellings: reserved numbers come from the
// composition's text plus its attached markers, the sendable subset is a
// projection of `tiles`, and a reopen restores both lists or neither.
export type CompositionTileFacts = {
  /** Attachment identity: minted at tile creation, stable through renumbering. */
  id: string;
  /** The label the committed text gives this tile right now. */
  marker: number;
  /** Whether a send of this composition may carry this tile's bytes. */
  eligible: boolean;
  /** Whether this tile still holds bytes (false: a markerless restored image). */
  hasBytes: boolean;
};

export type CompositionImageRecord = {
  anchors: number[];
  tiles: CompositionTileFacts[];
};

// The row's own id is the whole key. See THE INVARIANT above.
function compositionImageRecordKey(clientMutationId: string): string {
  return `${IMAGE_PROVENANCE_PREFIX}recovery.${clientMutationId}`;
}

/** The image facts recorded with this composition, or undefined when none has been written. */
export function readCompositionImageRecord(clientMutationId: string): CompositionImageRecord | undefined {
  try {
    const raw = localStorage.getItem(compositionImageRecordKey(clientMutationId));
    if (raw === null) return undefined;
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null) return undefined;
    const record = parsed as Partial<CompositionImageRecord>;
    const anchors = Array.isArray(record.anchors)
      ? record.anchors.filter((marker): marker is number => Number.isSafeInteger(marker) && marker > 0)
      : [];
    const tiles = Array.isArray(record.tiles)
      ? record.tiles.filter(
          (tile): tile is CompositionTileFacts =>
            typeof tile === "object" &&
            tile !== null &&
            typeof (tile as CompositionTileFacts).id === "string" &&
            Number.isSafeInteger((tile as CompositionTileFacts).marker),
        )
      : [];
    return { anchors, tiles };
  } catch {
    return undefined;
  }
}

export function writeCompositionImageRecord(clientMutationId: string, record: CompositionImageRecord): void {
  try {
    const next: CompositionImageRecord = {
      anchors: [...new Set(record.anchors)].sort((left, right) => left - right),
      tiles: record.tiles.map((tile) => ({
        id: tile.id,
        marker: tile.marker,
        eligible: tile.eligible,
        hasBytes: tile.hasBytes,
      })),
    };
    localStorage.setItem(compositionImageRecordKey(clientMutationId), JSON.stringify(next));
  } catch {
    // Best-effort, like every other composer persistence.
  }
}

// A stored marker list, or undefined when the key holds nothing usable.
// Undefined and [] differ: [] is a record that says this composition has no
// images, which is what retires a reference the person removed.
function readMarkerList(key: string): number[] | undefined {
  try {
    const raw = localStorage.getItem(key);
    if (raw === null) return undefined;
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed) || !parsed.every((marker) => Number.isSafeInteger(marker) && (marker as number) > 0))
      return undefined;
    return parsed as number[];
  } catch {
    return undefined;
  }
}

// The ref's missing-image markers as recorded by the most recent draft write.
function readImageProvenance(ref: string): number[] | undefined {
  return readMarkerList(imageProvenanceStorageKey(ref));
}

/** Records this ref's missing-image markers beside its draft.
 *
 * Every draft write calls this, including the ones that clear the draft
 * record: a composition whose images are gone must keep saying so even when
 * its text moves to a recovery row, or reopening it would restore the
 * anchors untagged. An empty list is written, not omitted - it is how the
 * removal of a reference becomes permanent. */
export function writeComposerImageMarkers(ref: string, markers: readonly number[]): void {
  try {
    const next = [...new Set(markers)].sort((left, right) => left - right);
    localStorage.setItem(imageProvenanceStorageKey(ref), JSON.stringify(next));
  } catch {
    // Best-effort, like every other composer persistence: losing this only
    // costs the refusal, never the person's text.
  }
}

export function clearComposerImageMarkers(ref: string): void {
  try {
    localStorage.removeItem(imageProvenanceStorageKey(ref));
  } catch {
    // See writeComposerImageMarkers.
  }
}

// The recorded markers a new text still spells. Provenance travels only with
// the anchor it describes: a marker whose "[image N]" the text no longer
// contains has no image reference left to lose.
function markersStillSpelled(text: string, markers: readonly number[]): number[] {
  if (markers.length === 0) return [];
  const spelled = new Set(Array.from(text.matchAll(markerPattern()), (match) => Number(match[1])));
  return markers.filter((marker) => spelled.has(marker));
}

// Explicit atoms follow their visible labels across text edits. Names-only
// drafts retain their selections independently of the prose.
export function writeDraft(ref: string, value: string): void {
  const current = readComposerDraft(ref);
  writeComposerDraft(
    ref,
    current.mentions ? patchSelectionText(current, value) : { ...current, text: value },
    markersStillSpelled(value, readComposerDraftImageMarkers(ref)),
  );
}

// clearDraft drops a ref's stored draft outright - called on every
// successful send/steer/queue/drain (never on failure), mirroring the
// legacy clearComposerDraftIfUnchanged convention.
export function clearDraft(ref: string): void {
  markDraftEdited(ref);
  clearPersistedDraft(ref);
  clearComposerImageMarkers(ref);
}

// A store-driven resume can return a different identity, and the pane follows
// it. The draft the old-ref composer held is stored under the OLD ref
// (persistDraft keys by ref), while the incoming pane's composer starts on the
// NEW ref's own draft - so without this the old ref's draft would be dropped at
// the ref change. The carry must preserve whichever draft is genuinely NEWER,
// not assume the source is: the notice's Resume button hands over whatever sat
// in the old-ref composer, so a draft typed in another pane on the resumed ref
// can be newer. The persisted wall-clock stamp orders the two across refs (the
// per-ref revision counter cannot). A blank or absent destination always takes
// the source; otherwise the source wins only when strictly newer. Ties, and the
// migration case of two unstamped records, keep the destination - conservative,
// and transient until drafts are re-edited. The source copy under the old ref is
// left in place either way (returning to that identity still finds it). Staged
// image attachments are NOT part of the carried draft: they are per-mount
// sourceState and the new pane starts with none.
export function carryComposerDraft(fromRef: string, toRef: string): void {
  if (fromRef === toRef) return;
  const from = readComposerDraft(fromRef);
  if (isBlankDraft(from)) return;
  const to = readComposerDraft(toRef);
  if (!isBlankDraft(to) && readDraftEditedAt(fromRef) <= readDraftEditedAt(toRef)) return;
  // The carried text keeps its anchors' provenance: the new pane holds none of
  // the bytes (this function's own note above), so a marker it names is an
  // anchor with no image and must refuse rather than send as literal text.
  writeComposerDraft(toRef, from, markersStillSpelled(from.text, readComposerDraftImageMarkers(fromRef)));
}

// Recovery persistence owns the text in IndexedDB. Removing its redundant
// localStorage copy does not represent a new edit of that draft.
export function clearPersistedDraft(ref: string): void {
  try {
    localStorage.removeItem(composerDraftStorageKey(ref));
    localStorage.removeItem(draftStorageKey(ref));
  } catch {
    // See writeComposerDraft's own comment.
  }
}

// A recovery continuation may clear only the precise local snapshot its
// transaction incorporated. A replacement edit writes a new ID first.
export function clearPersistedDraftIfRevisionMatches(ref: string, revisionId: string): boolean {
  if (readComposerDraftRevisionId(ref) !== revisionId) return false;
  clearPersistedDraft(ref);
  return true;
}
