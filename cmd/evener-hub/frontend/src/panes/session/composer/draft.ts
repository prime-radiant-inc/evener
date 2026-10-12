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
import { canonicalSkillNames } from "@evener/appwire-client";
import type { SkillEditorValue } from "./skillDocument";

const STORAGE_PREFIX = "evener.composer.draft.v1.";
const STRUCTURED_STORAGE_PREFIX = "evener.composer.draft.v2.";
const draftRevisions = new Map<string, number>();

// A composer draft: the textarea's text plus the canonical names of the
// skills selected for this request. skillNames are canonical catalog names
// (never prose): the submit boundary assembles them as {type: "skill", name}
// input items after the ordinary text/attachment items, and a chip change is
// a draft edit even when the text is byte-identical.
export interface ComposerDraft extends SkillEditorValue {}

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
    record.skillNames.every((name) => typeof name === "string") &&
    (record.commandNames === undefined ||
      (Array.isArray(record.commandNames) && record.commandNames.every((name) => typeof name === "string"))) &&
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

function decodeStructuredComposerDraft(structured: string | null): ComposerDraft | null {
  if (structured === null) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(structured);
  } catch {
    return null;
  }
  if (!isStoredComposerDraft(parsed)) return null;
  return {
    text: parsed.text,
    skillNames: canonicalSkillNames(parsed.skillNames),
    ...(parsed.commandNames ? { commandNames: canonicalSkillNames(parsed.commandNames) } : {}),
    ...(parsed.mentions ? { mentions: parsed.mentions } : {}),
  };
}

export function decodeSupportedComposerDraft(structured: string | null, legacy: string | null): ComposerDraft {
  return decodeStructuredComposerDraft(structured) ?? { text: legacy ?? "", skillNames: [] };
}

// Every localStorage access is guarded: private-mode/disabled/full storage
// degrades silently to "no draft" rather than ever breaking the composer,
// same convention as shell/rail/Rail.tsx's own collapsed-state persistence.
//
// Existing-draft transition (approved): when the v2 record is absent, the old
// v1 PLAIN-TEXT value is read as literal text with skillNames: []. The v1
// value is never JSON-decoded to guess selections and no slash mention in it
// is ever inferred as a selection - an old draft is exactly the text it was.
// The runtime read projection: no durable write, one live value per ref,
// published by the source that currently owns the visible draft. The owner id
// lets a retiring source avoid erasing a newer same-ref source's value.
const runtimeDrafts = new Map<string, { value: ComposerDraft; ownerId: string }>();

export function publishRuntimeDraft(ref: string, value: ComposerDraft, ownerId: string): void {
  runtimeDrafts.set(ref, { value, ownerId });
}

export function clearRuntimeDraft(ref: string, ownerId: string): void {
  const entry = runtimeDrafts.get(ref);
  if (entry !== undefined && entry.ownerId === ownerId) runtimeDrafts.delete(ref);
}

export function readComposerDraft(ref: string): ComposerDraft {
  // A live source's projection is authoritative while it exists: it is the
  // draft currently visible in the composer, updated from durable loads and
  // accepted edits without a localStorage write.
  const projected = runtimeDrafts.get(ref);
  if (projected !== undefined) return projected.value;
  try {
    const structured = localStorage.getItem(composerDraftStorageKey(ref));
    const decoded = decodeStructuredComposerDraft(structured);
    if (decoded !== null) return decoded;
    return decodeSupportedComposerDraft(null, localStorage.getItem(draftStorageKey(ref)));
  } catch {
    return { text: "", skillNames: [] };
  }
}

// The text half of the structured draft, for callers that only compose prose
// (the dev harness's seeded panes, and tests reading back what they typed).
export function readDraft(ref: string): string {
  return readComposerDraft(ref).text;
}
