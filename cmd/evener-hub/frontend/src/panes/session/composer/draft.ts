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
const STORAGE_PREFIX = "evener.composer.draft.v1.";
const STRUCTURED_STORAGE_PREFIX = "evener.composer.draft.v2.";
const draftRevisions = new Map<string, number>();

// A composer draft: the textarea's text plus the canonical names of the
// skills selected for this request. skillNames are canonical catalog names
// (never prose): the submit boundary assembles them as {type: "skill", name}
// input items after the ordinary text/attachment items, and a chip change is
// a draft edit even when the text is byte-identical.
export interface ComposerDraft {
  text: string;
  skillNames: string[];
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

function isStoredComposerDraft(value: unknown): value is ComposerDraft {
  if (typeof value !== "object" || value === null) return false;
  const record = value as Partial<ComposerDraft>;
  return (
    typeof record.text === "string" &&
    Array.isArray(record.skillNames) &&
    record.skillNames.every((name) => typeof name === "string")
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
  try {
    const structured = localStorage.getItem(composerDraftStorageKey(ref));
    if (structured !== null) {
      let parsed: unknown;
      try {
        parsed = JSON.parse(structured);
      } catch {
        parsed = null;
      }
      if (isStoredComposerDraft(parsed)) return parsed;
    }
    return { text: localStorage.getItem(draftStorageKey(ref)) ?? "", skillNames: [] };
  } catch {
    return { text: "", skillNames: [] };
  }
}

// The text half of the structured draft, for callers that only compose prose
// (the dev harness's seeded panes, and tests reading back what they typed).
export function readDraft(ref: string): string {
  return readComposerDraft(ref).text;
}

// Writes the whole structured draft as ONE atomic v2 record, then removes any
// old v1 value (never the reverse order - a v2 write that crashed before the
// v1 removal still leaves one readable draft behind). Blank text with no
// selections removes the draft outright rather than storing an empty record -
// a draft that would never send is never persisted. A selection-only draft
// DOES send, so it persists even with blank text.
export function writeComposerDraft(ref: string, value: ComposerDraft): void {
  markDraftEdited(ref);
  try {
    if (value.text.trim() === "" && value.skillNames.length === 0) {
      localStorage.removeItem(composerDraftStorageKey(ref));
      localStorage.removeItem(draftStorageKey(ref));
    } else {
      localStorage.setItem(composerDraftStorageKey(ref), JSON.stringify(value));
      localStorage.removeItem(draftStorageKey(ref));
    }
  } catch {
    // Best-effort: a full quota or Safari private-mode must never be fatal
    // to the composer itself, only to draft persistence across reloads.
  }
}

// A text edit preserves the draft's selections - chips apply to the request
// independently of the prose, so editing words never drops a selected skill.
export function writeDraft(ref: string, value: string): void {
  writeComposerDraft(ref, { text: value, skillNames: readComposerDraft(ref).skillNames });
}

// clearDraft drops a ref's stored draft outright - called on every
// successful send/steer/queue/drain (never on failure), mirroring the
// legacy clearComposerDraftIfUnchanged convention.
export function clearDraft(ref: string): void {
  markDraftEdited(ref);
  clearPersistedDraft(ref);
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
