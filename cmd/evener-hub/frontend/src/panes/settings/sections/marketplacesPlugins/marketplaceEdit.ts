// marketplaceEdit.ts: the marketplace sheet's form model - the draft, how it
// is seeded from a MarketplaceEntry, and how a dirty draft becomes the
// smallest evener/marketplace/edit request. Pure, so the rules (the kind's
// own field is the only one that counts; a same-kind edit keeps the fields
// the form has no input for except the ref and sha pins, and a source whose
// kind the picker does not offer
// shows its URL under Git URL and keeps that kind until the user picks a
// different one; stored padding is not an edit; a blanked field is an
// unfinished edit, not a change) are pinned without rendering anything.
import type { MarketplaceEditParams, MarketplaceEntry, MarketplaceSourceInput } from "../../../../protocol/types.gen";

export type MarketplaceSourceKind = "url" | "github" | "directory";

/** The same three kinds, in the same order, the add form offers. git-subdir
 * is not offered: the picker would need a second field, and the add form
 * never had one. */
export const MARKETPLACE_SOURCE_OPTIONS: { value: MarketplaceSourceKind; label: string }[] = [
  { value: "url", label: "Git URL" },
  { value: "github", label: "owner/repo" },
  { value: "directory", label: "Local path" },
];

export interface MarketplaceDraft {
  name: string;
  kind: MarketplaceSourceKind;
  url: string;
  repo: string;
  path: string;
}

function isOfferedKind(kind: string): kind is MarketplaceSourceKind {
  return MARKETPLACE_SOURCE_OPTIONS.some((option) => option.value === kind);
}

export function marketplaceDraftFor(entry: MarketplaceEntry): MarketplaceDraft {
  const { source } = entry;
  const kind: MarketplaceSourceKind = isOfferedKind(source.kind) ? source.kind : "url";
  return {
    name: entry.name,
    kind,
    url: kind === "url" ? (source.url ?? "") : "",
    repo: kind === "github" ? (source.repo ?? "") : "",
    path: kind === "directory" ? (source.path ?? "") : "",
  };
}

/** The one field the draft's kind reads. The others hold whatever the user
 * last typed under another kind and are ignored. */
function draftValue(draft: MarketplaceDraft): string {
  if (draft.kind === "github") return draft.repo.trim();
  if (draft.kind === "directory") return draft.path.trim();
  return draft.url.trim();
}

/** The source without the pins, for re-pointing it at another repository. A
 * ref names a branch or tag and a sha a commit, each in the one repository it
 * was set for, so neither can follow the source somewhere else. */
function unpinned(source: MarketplaceSourceInput): MarketplaceSourceInput {
  const copy = { ...source };
  delete copy.ref;
  delete copy.sha;
  return copy;
}

function sourceFromDraft(entry: MarketplaceEntry, draft: MarketplaceDraft): MarketplaceSourceInput {
  const value = draftValue(draft);
  // A same-kind edit changes the one field the picker shows and keeps the
  // other fields the form has no input for, minus the pins: the field it
  // changes IS the repository, and a git-subdir's path is the catalog's place
  // inside whatever repository that is, while a ref or sha names something in
  // the old one. A rename alone sends no source at all, so it leaves a pinned
  // marketplace's pins where they are. A kind the picker cannot offer shows
  // its URL under Git URL, which makes `url` its own kind here - dropping to a
  // plain url would change which catalog the marketplace reads. Only picking a
  // different kind replaces the source wholesale.
  const sameKind = draft.kind === entry.source.kind || (draft.kind === "url" && !isOfferedKind(entry.source.kind));
  const kept: MarketplaceSourceInput = sameKind ? unpinned(entry.source) : { kind: draft.kind };
  if (draft.kind === "github") return { ...kept, repo: value };
  if (draft.kind === "directory") return { ...kept, path: value };
  return { ...kept, url: value };
}

/** Whether the draft still describes the entry's own source. The stored value
 * is trimmed alongside the draft's: the form shows stored padding verbatim, and
 * an untouched field is not an edit. */
function sourceUnchanged(entry: MarketplaceEntry, draft: MarketplaceDraft): boolean {
  const { source } = entry;
  const next = sourceFromDraft(entry, draft);
  if (next.kind !== source.kind) return false;
  if (next.kind === "github") return next.repo === (source.repo ?? "").trim();
  if (next.kind === "directory") return next.path === (source.path ?? "").trim();
  return next.url === (source.url ?? "").trim();
}

/** Whether the draft's own kind has no value yet. Save stays disabled on an
 * incomplete draft: a picked-but-empty kind is no source change, so a draft
 * whose name ALSO changed would otherwise save as a rename alone while the
 * picker on screen says the source moved too. */
export function marketplaceDraftIncomplete(draft: MarketplaceDraft): boolean {
  return draftValue(draft) === "";
}

/** Whether the user has moved the source away from the draft the entry seeds -
 * a different kind, or a different value in that kind's own field. Wider than
 * marketplaceEditParams reporting a source (a kind picked but not filled in is
 * touched and not yet a change) and narrower than an empty field (a source
 * whose kind this frontend cannot represent, and which carries no URL to show,
 * seeds an empty field nobody touched). */
export function marketplaceSourceTouched(entry: MarketplaceEntry, draft: MarketplaceDraft): boolean {
  const seeded = marketplaceDraftFor(entry);
  return draft.kind !== seeded.kind || draftValue(draft) !== draftValue(seeded);
}

/** The request carrying exactly what changed, or null when nothing did. An
 * emptied field is nothing changed rather than a change to nothing: the
 * server ignores an empty newName and cannot fetch an empty source. */
export function marketplaceEditParams(entry: MarketplaceEntry, draft: MarketplaceDraft): MarketplaceEditParams | null {
  const params: MarketplaceEditParams = { name: entry.name };
  let changed = false;
  const name = draft.name.trim();
  // Trimmed on both sides: a stored name shown with its padding is not a
  // rename to its own trimmed form. What goes over the wire is the trimmed
  // draft either way.
  if (name && name !== entry.name.trim()) {
    params.newName = name;
    changed = true;
  }
  if (draftValue(draft) && !sourceUnchanged(entry, draft)) {
    params.source = sourceFromDraft(entry, draft);
    changed = true;
  }
  return changed ? params : null;
}
