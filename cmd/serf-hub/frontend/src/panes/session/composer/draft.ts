// Per-ref sticky composer drafts (parity-m5-composer.md §F, contracts
// §Drafts): unsent textarea text survives a reload, keyed by session ref.
//
// Cross-session leak guard, dropped (verified, not assumed): the legacy
// composer (drafts.js) needed `isOtherSessionsDraft`/a "bound session"
// check because ONE DOM form element was morphed in place across an htmx
// session swap, so stale text from session A's textarea could still be
// sitting there when the SAME element got rebound to session B. Under
// dockview, that can't happen: shell/paneRegistry.ts registers "session" as
// NOT a singleton pane type ("distinct refs are distinct panes" - see
// panes/session/index.tsx's own comment), and shell/DockHost.tsx's PaneHost
// unmounts a pane's whole React tree when its tab isn't active and mounts a
// fresh one when it becomes active again (never re-parents an existing
// Composer instance onto a different ref). So a mounted Composer's `ref`
// prop is fixed for that instance's entire lifetime, and every mount starts
// from React's own empty initial state - there is no "leftover text from a
// different ref" for a fresh mount to ever see. Restoring this ref's draft
// on mount (Composer.tsx) is therefore unconditional, not guarded.
const STORAGE_PREFIX = "serf.composer.draft.v1.";

export function draftStorageKey(ref: string): string {
  return `${STORAGE_PREFIX}${ref}`;
}

// Every localStorage access is guarded: private-mode/disabled/full storage
// degrades silently to "no draft" rather than ever breaking the composer,
// same convention as shell/rail/Rail.tsx's own collapsed-state persistence.
export function readDraft(ref: string): string {
  try {
    return localStorage.getItem(draftStorageKey(ref)) ?? "";
  } catch {
    return "";
  }
}

// Blank/whitespace-only content removes the key rather than storing an
// empty string - a draft that would never send is never persisted.
export function writeDraft(ref: string, value: string): void {
  try {
    if (value.trim() === "") {
      localStorage.removeItem(draftStorageKey(ref));
    } else {
      localStorage.setItem(draftStorageKey(ref), value);
    }
  } catch {
    // Best-effort: a full quota or Safari private-mode must never be fatal
    // to the composer itself, only to draft persistence across reloads.
  }
}

// clearDraft drops a ref's stored draft outright - called on every
// successful send/steer/queue/drain (never on failure), mirroring the
// legacy clearComposerDraftIfUnchanged convention.
export function clearDraft(ref: string): void {
  try {
    localStorage.removeItem(draftStorageKey(ref));
  } catch {
    // See writeDraft's own comment.
  }
}
