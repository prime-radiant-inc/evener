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
import { canonicalSkillNames, type ImageMarkerRange, validMarkerRanges } from "@evener/appwire-client";
import { ownClientId } from "../../../stores/mutationClientIdentity";
import { createSecureUUID } from "../../../stores/secureUUID";
import { patchSelectionText, type SkillEditorValue } from "./skillDocument";

const STORAGE_PREFIX = "evener.composer.draft.v1.";
const STRUCTURED_STORAGE_PREFIX = "evener.composer.draft.v2.";
const COMPOSER_SEAT_PREFIX = "evener.composer.seat.v1.";
const draftRevisions = new Map<string, number>();

// A composer draft: the textarea's text plus the canonical names of the
// skills selected for this request. skillNames are canonical catalog names
// (never prose): the submit boundary assembles them as {type: "skill", name}
// input items after the ordinary text/attachment items, and a chip change is
// a draft edit even when the text is byte-identical.
export interface ComposerDraft extends SkillEditorValue {
  /** App Group delivery already merged into this draft, including after a WebKit reload. */
  shareId?: string;
  // Exact identity for this persisted structured snapshot. Older records have
  // no identity and keep their existing recovery behavior.
  revisionId?: string;
  // Wall-clock stamp of the last write, so two refs' drafts can be ordered when
  // one is carried to the other (see carryComposerDraft). Absent on records an
  // older build wrote.
  editedAt?: number;
  // The image markers the writer staged WITH THIS REVISION: a marker enters
  // this list only when a staging gesture wrote "[image N]" into the text and
  // appended the tile that holds its bytes. Typed or pasted marker text never
  // enters it, which is what lets a reader tell an orphaned anchor (this
  // composer has the text, not the bytes) from literal prose. Bytes never
  // enter localStorage, so a marker named here is one whose bytes are
  // guaranteed absent to whoever reads this record next. Absent on records an
  // older build wrote: no provenance, so its markers stay prose.
  stagedImageMarkers?: number[];
  // WHERE those staged anchors are. With two identical spellings of one
  // number - a literal the person typed before an anchor the writer staged -
  // the number list cannot say which occurrence has no image behind it, and a
  // reloading reader would mark both. Each range must still name exactly
  // markerText(marker) when the record is read, or it is discarded: a range
  // that no longer matches its text would delete the wrong bytes.
  stagedImageRanges?: ImageMarkerRange[];
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

function isStoredComposerDraft(value: unknown): value is ComposerDraft {
  if (typeof value !== "object" || value === null) return false;
  const record = value as Partial<ComposerDraft>;
  return (
    typeof record.text === "string" &&
    Array.isArray(record.skillNames) &&
    record.skillNames.every((name) => typeof name === "string") &&
    (record.commandNames === undefined ||
      (Array.isArray(record.commandNames) && record.commandNames.every((name) => typeof name === "string"))) &&
    (record.editedAt === undefined || typeof record.editedAt === "number") &&
    (record.revisionId === undefined || typeof record.revisionId === "string") &&
    (record.stagedImageMarkers === undefined ||
      (Array.isArray(record.stagedImageMarkers) &&
        record.stagedImageMarkers.every((marker) => Number.isSafeInteger(marker) && marker > 0))) &&
    (record.stagedImageRanges === undefined || Array.isArray(record.stagedImageRanges)) &&
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
  // Both fields describe this snapshot's IDENTITY and ownership, not the
  // draft's content: revisionId says which write produced it, stagedImageMarkers
  // says which of its markers a writer with bytes staged. Callers that need
  // them read readComposerDraftSnapshot; a caller composing or rendering a
  // draft gets only what the person sees.
  const {
    shareId: _shareId,
    revisionId: _revisionId,
    stagedImageMarkers: _staged,
    stagedImageRanges: _stagedRanges,
    ...draft
  } = readComposerDraftSnapshot(ref);
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
        // A stored range is re-validated against the text it names: a
        // hand-edited or stale record must not hand a repair an offset that
        // no longer holds markerText(marker).
        const stagedImageRanges = validMarkerRanges(parsed.text, parsed.stagedImageRanges);
        return {
          text: parsed.text,
          skillNames: canonicalSkillNames(parsed.skillNames),
          ...(parsed.commandNames ? { commandNames: canonicalSkillNames(parsed.commandNames) } : {}),
          ...(parsed.mentions ? { mentions: parsed.mentions } : {}),
          ...(parsed.revisionId ? { revisionId: parsed.revisionId } : {}),
          ...(typeof parsed.shareId === "string" ? { shareId: parsed.shareId } : {}),
          ...(parsed.stagedImageMarkers ? { stagedImageMarkers: parsed.stagedImageMarkers } : {}),
          ...(stagedImageRanges.length > 0 ? { stagedImageRanges } : {}),
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
export function writeComposerDraft(ref: string, value: ComposerDraft): string | undefined {
  markDraftEdited(ref);
  try {
    if (isBlankDraft(value)) {
      localStorage.removeItem(composerDraftStorageKey(ref));
      localStorage.removeItem(draftStorageKey(ref));
      return undefined;
    } else {
      // The one choke point every persisted draft edit goes through, so the stamp
      // is the draft's actual last-write time.
      const revisionId = createSecureUUID();
      localStorage.setItem(
        composerDraftStorageKey(ref),
        JSON.stringify({ ...value, revisionId, editedAt: Date.now() }),
      );
      try {
        localStorage.removeItem(draftStorageKey(ref));
      } catch {
        // The structured snapshot is committed and takes precedence on read.
      }
      return revisionId;
    }
  } catch {
    // Best-effort: a full quota or Safari private-mode must never be fatal
    // to the composer itself, only to draft persistence across reloads.
    return undefined;
  }
}

export function readComposerDraftRevisionId(ref: string): string | undefined {
  return readComposerDraftSnapshot(ref).revisionId;
}

// Explicit atoms follow their visible labels across text edits. Names-only
// drafts retain their selections independently of the prose.
export function writeDraft(ref: string, value: string): void {
  // The snapshot, not readComposerDraft: a text-only edit must not drop the
  // provenance its markers carry, or the next reader would send them as prose.
  const current = readComposerDraftSnapshot(ref);
  writeComposerDraft(ref, current.mentions ? patchSelectionText(current, value) : { ...current, text: value });
}

// clearDraft drops a ref's stored draft outright - called on every
// successful send/steer/queue/drain (never on failure), mirroring the
// legacy clearComposerDraftIfUnchanged convention.
export function clearDraft(ref: string): void {
  markDraftEdited(ref);
  clearPersistedDraft(ref);
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
  // The snapshots, not readComposerDraft: the destination composer stages no
  // tiles, so every carried marker's bytes are already gone and its provenance
  // must travel with the text or the new composer would send the anchor as
  // prose.
  const from = readComposerDraftSnapshot(fromRef);
  if (isBlankDraft(from)) return;
  const to = readComposerDraftSnapshot(toRef);
  if (!isBlankDraft(to) && readDraftEditedAt(fromRef) <= readDraftEditedAt(toRef)) return;
  writeComposerDraft(toRef, from);
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
