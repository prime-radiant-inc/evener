// MarketplaceSheet: the marketplace's editor (spec 2026-09-07 §3). Opens
// from a MarketplacesSection row tap and IS the edit surface: Name and the
// source (this sheet's own three-way picker) are prefilled form fields,
// Save in the footer lights up when either differs, and a changed source
// says it will re-fetch. Refresh and Remove - the two actions the row used
// to carry inline - sit below the meta table, Remove behind its
// ConfirmDialog. A wide right Sheet on desktop, a bottom Sheet on mobile.
//
// The entry is read from the store by name so cross-client changes land
// live; the draft reseeds when a different marketplace opens or this
// sheet's own save lands, never on an unrelated refresh. The sheet closes
// itself when its entry vanishes - except across its own rename, where the
// page re-selects the new name (onRenamed).
//
// Two consequences of that reseeding policy, both accepted. The draft is
// seeded in an effect, so the first commit after opening paints the title and
// a disabled Save with no body yet. And because a same-name store update
// deliberately does not reseed, another client's edit to the marketplace
// being edited here leaves this draft describing the OLD source: Save lights
// up untouched, and pressing it writes this form back over that edit. The
// alternative - reseeding on every store update - silently discards whatever
// the user is halfway through typing, which is worse.
import { type Dispatch, type SetStateAction, useEffect, useId, useRef, useState } from "react";
import { errorText } from "../../../../protocol/errors";
import type { MarketplaceEntry } from "../../../../protocol/types.gen";
import { useIsMobile } from "../../../../shell/useIsMobile";
import { directoryActions, extensionsStore, useExtensionsStore } from "../../../../stores/extensions";
import { Button, ConfirmDialog, FormRow, Input, PathField, RadioGroup, Sheet, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import {
  MARKETPLACE_SOURCE_OPTIONS,
  type MarketplaceDraft,
  type MarketplaceSourceKind,
  marketplaceDraftFor,
  marketplaceDraftIncomplete,
  marketplaceEditParams,
  marketplaceSourceTouched,
} from "./marketplaceEdit";
import styles from "./marketplacesPlugins.module.css";

const CLASS = {
  sheetForm: requireClass(styles.sheetForm, "marketplacesPlugins.module.css", "sheetForm"),
  sheetNote: requireClass(styles.sheetNote, "marketplacesPlugins.module.css", "sheetNote"),
  sheetError: requireClass(styles.sheetError, "marketplacesPlugins.module.css", "sheetError"),
  sheetActions: requireClass(styles.sheetActions, "marketplacesPlugins.module.css", "sheetActions"),
  sheetDivider: requireClass(styles.sheetDivider, "marketplacesPlugins.module.css", "sheetDivider"),
  metaList: requireClass(styles.metaList, "marketplacesPlugins.module.css", "metaList"),
  metaRow: requireClass(styles.metaRow, "marketplacesPlugins.module.css", "metaRow"),
  metaLabel: requireClass(styles.metaLabel, "marketplacesPlugins.module.css", "metaLabel"),
  metaValue: requireClass(styles.metaValue, "marketplacesPlugins.module.css", "metaValue"),
  rowMeta: requireClass(styles.rowMeta, "marketplacesPlugins.module.css", "rowMeta"),
};

export interface MarketplaceSheetProps {
  name: string | null;
  onClose: () => void;
  /** After a successful rename, with the new name: the page re-selects it
   * so the sheet stays open on the same marketplace. */
  onRenamed: (newName: string) => void;
  /** A Refresh or a Save on a marketplace BrowseSection currently has
   * expanded re-browses it at once, so the tree never shows a catalog the
   * write just invalidated. BrowseSection is a sibling of this sheet's
   * own section, which is why the set lives on the page. */
  expandedMarketplaces: Set<string>;
  /** Written only by a rename, which moves the entry's expansion to its new
   * name - the set is keyed by name. */
  setExpandedMarketplaces: Dispatch<SetStateAction<Set<string>>>;
}

function lastUpdatedText(seconds: number): string {
  return seconds > 0 ? new Date(seconds * 1000).toLocaleString() : "never";
}

export function MarketplaceSheet({
  name,
  onClose,
  onRenamed,
  expandedMarketplaces,
  setExpandedMarketplaces,
}: MarketplaceSheetProps) {
  const marketplaces = useExtensionsStore((s) => s.marketplaces);
  const isMobile = useIsMobile();
  const toasts = useToasts();
  const ids = useId();

  const entry = name === null || marketplaces === null ? undefined : marketplaces.find((m) => m.name === name);

  const [draft, setDraft] = useState<MarketplaceDraft | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [pendingRemove, setPendingRemove] = useState(false);
  const [removeBusy, setRemoveBusy] = useState(false);
  // Set for the span of a rename request: the old name vanishes from the
  // store when the response lands, and that vanish must not close the sheet.
  const pendingRename = useRef<string | null>(null);
  // The name the page has selected RIGHT NOW, readable from an async
  // continuation - an edit re-clones the repo, so seconds pass in which the
  // user can dismiss the sheet or open another marketplace.
  const liveName = useRef(name);
  // Synced in an effect, not during render: a render can be thrown away or
  // replayed under concurrent rendering, so a ref written there can end up
  // holding a name that was never committed. Only the async continuations
  // read it, and they run long after this commit.
  useEffect(() => {
    liveName.current = name;
  }, [name]);
  // The expansion set as it stands RIGHT NOW, for the same reason and synced
  // the same way: the tree is live while a save or a refresh runs, so the set
  // this sheet rendered with says nothing about what is on screen when the
  // response lands.
  const liveExpanded = useRef(expandedMarketplaces);
  useEffect(() => {
    liveExpanded.current = expandedMarketplaces;
  }, [expandedMarketplaces]);

  function seed(current: MarketplaceEntry): void {
    setDraft(marketplaceDraftFor(current));
    setFormError(null);
  }

  // biome-ignore lint/correctness/useExhaustiveDependencies: reseed only when a different marketplace opens; a refresh of the same one must not clobber in-progress edits
  useEffect(() => {
    // A Remove confirmation is about the marketplace it was opened for - it
    // names that one and its Confirm removes it - so it goes with the draft
    // rather than retargeting itself at whatever the page selects next. Its
    // busy flag belongs to the request that set it, for the same reason.
    setPendingRemove(false);
    setRemoveBusy(false);
    if (entry === undefined) {
      setDraft(null);
      setFormError(null);
      return;
    }
    seed(entry);
  }, [entry?.name]);

  // The rename is over once the page has re-selected under the new name, so
  // this effect exists precisely to fire on a changed `name` - which the
  // dependency rule reads as one dependency too many, because clearing a ref
  // is all the body does. It must clear BEFORE the close-on-vanish effect
  // reads the ref in the same commit: a rename the page has already
  // re-selected, onto a name a concurrent remove took back out of the list,
  // is a vanished entry that has to close.
  // biome-ignore lint/correctness/useExhaustiveDependencies: `name` changing is the whole trigger, not an extra dependency
  useEffect(() => {
    pendingRename.current = null;
  }, [name]);
  useEffect(() => {
    if (name !== null && marketplaces !== null && entry === undefined && pendingRename.current === null) onClose();
  }, [name, marketplaces, entry, onClose]);

  const open = name !== null && entry !== undefined;
  const params = entry !== undefined && draft !== null ? marketplaceEditParams(entry, draft) : null;
  const dirty = params !== null;
  // The re-fetch warning tracks the source the form SHOWS, not the request: it
  // belongs on screen from the moment a kind is picked, which is before that
  // kind's field has a value and so before the model reports a source change
  // at all. A source nobody touched promises nothing, even when its field sits
  // empty because the entry had no URL to seed it with.
  const sourceTouched = entry !== undefined && draft !== null && marketplaceSourceTouched(entry, draft);
  // A half-picked source is the one thing that blocks an otherwise valid save:
  // it would go out as a rename alone while the picker on screen says the
  // source moved too.
  const incomplete = draft !== null && marketplaceDraftIncomplete(draft);
  // The sheet's three mutations all name the marketplace as the sheet found
  // it, so only one may be in flight: a second sent while the first is
  // changing that name refreshes a marketplace that no longer answers to it,
  // or races the rename for the store lock.
  const busy = saving || refreshing || removeBusy;
  const canSave = dirty && !busy && !(sourceTouched && incomplete);

  function update(patch: Partial<MarketplaceDraft>): void {
    setDraft((current) => (current === null ? current : { ...current, ...patch }));
  }

  async function handleSave(): Promise<void> {
    if (entry === undefined || params === null || !canSave) return;
    setFormError(null);
    setSaving(true);
    if (params.newName !== undefined) pendingRename.current = params.newName;
    try {
      await extensionsStore.getState().editMarketplace(params);
      // Reported wherever the user has navigated to: the write landed, and a
      // sheet that moved on is no reason to leave a completed save unreported.
      toasts.push("success", `Saved ${params.newName ?? entry.name}`);
      // The expansion set is keyed by name, so a rename carries the entry over
      // before the re-browse below stores the catalog under the new name -
      // left alone, the set would render the renamed marketplace collapsed
      // over that fresh catalog and keep a name nothing answers to.
      const renamedTo = params.newName;
      if (renamedTo !== undefined) {
        setExpandedMarketplaces((current) => {
          if (!current.has(entry.name)) return current;
          const next = new Set(current);
          next.delete(entry.name);
          next.add(renamedTo);
          return next;
        });
      }
      // A save invalidates the catalog exactly as a Refresh does, so an
      // expanded tree node has to be re-requested or it sits on a spinner.
      // Keyed by the name the catalog will be stored under - the new one on a
      // rename - while the expansion set still holds the name the user
      // expanded. Unconditional, like Refresh's: the tree is the page's, not
      // this sheet's, so a user who has moved on still owes it a catalog.
      if (liveExpanded.current.has(entry.name)) {
        void extensionsStore.getState().browseMarketplace(params.newName ?? entry.name);
      }
      if (params.newName === undefined) {
        // The rename branch's guard applies here too: seeding is cosmetic - it
        // only normalizes whitespace - but on a sheet the page has moved to it
        // would show this marketplace's name and source under another one's
        // title, with Save armed to rename that one to this.
        if (liveName.current === entry.name) {
          const refreshed = extensionsStore.getState().marketplaces?.find((m) => m.name === entry.name);
          if (refreshed !== undefined) seed(refreshed);
        }
      } else if (liveName.current === entry.name) {
        onRenamed(params.newName);
      } else {
        // The page moved on while the rename was in flight: re-selecting the
        // new name here would re-open this sheet over whatever the user
        // navigated to. Drop the vanish suppression with it - the old name is
        // gone from the store and this sheet no longer owns it.
        pendingRename.current = null;
      }
    } catch (err) {
      pendingRename.current = null;
      const message = errorText(err);
      // Inline only while this sheet still shows the form that caused it; the
      // toast carries the failure either way.
      if (liveName.current === entry.name) setFormError(message);
      toasts.push("error", `Save failed: ${message}`);
    } finally {
      setSaving(false);
    }
  }

  async function handleRefresh(): Promise<void> {
    if (entry === undefined) return;
    setRefreshing(true);
    try {
      await extensionsStore.getState().refreshMarketplace(entry.name);
      if (liveExpanded.current.has(entry.name)) void extensionsStore.getState().browseMarketplace(entry.name);
      toasts.push("success", `Refreshed ${entry.name}`);
    } catch (err) {
      toasts.push("error", `Refresh failed: ${errorText(err)}`);
    } finally {
      setRefreshing(false);
    }
  }

  async function handleConfirmRemove(): Promise<void> {
    if (entry === undefined) return;
    setRemoveBusy(true);
    try {
      await extensionsStore.getState().removeMarketplace(entry.name);
      // Reported wherever the user has navigated to, like a save's: the
      // removal landed, and a sheet that moved on is no reason to leave it
      // unreported.
      toasts.push("success", `Removed marketplace ${entry.name}`);
      // The confirm this closes belongs to whatever marketplace the sheet
      // shows now, so on a sheet that moved on it would answer a question the
      // user has not answered yet.
      if (liveName.current === entry.name) setPendingRemove(false);
      // onClose fires via the entry-vanished effect once the store's updated
      // list lands - no explicit close here.
    } catch (err) {
      // The failure is reported wherever the user has navigated to; only the
      // flag is this sheet's to clear.
      toasts.push("error", `Remove marketplace failed: ${errorText(err)}`);
    } finally {
      if (liveName.current === entry.name) setRemoveBusy(false);
    }
  }

  return (
    <>
      <Sheet
        open={open}
        onClose={onClose}
        title={entry?.name ?? ""}
        side={isMobile ? "bottom" : "right"}
        size="wide"
        footer={
          entry !== undefined && (
            <Button onClick={() => void handleSave()} disabled={!canSave}>
              Save
            </Button>
          )
        }
      >
        {entry !== undefined && draft !== null && (
          <>
            {/* Save lives in the Sheet's footer, outside this form, so the
                form has no submit button and Enter is not a save path -
                consistently, for every source kind. The handler is here only
                because the local-path form's one text field is exactly the
                shape HTML submits implicitly, which without this would leave
                the page. */}
            <form
              className={CLASS.sheetForm}
              aria-label={`Edit ${entry.name}`}
              onSubmit={(event) => event.preventDefault()}
            >
              <FormRow label="Name" htmlFor={`${ids}-name`}>
                <Input
                  id={`${ids}-name`}
                  value={draft.name}
                  onChange={(event) => update({ name: event.target.value })}
                  disabled={saving}
                />
              </FormRow>
              <RadioGroup
                label="Source"
                value={draft.kind}
                onChange={(value) => update({ kind: value as MarketplaceSourceKind })}
                options={MARKETPLACE_SOURCE_OPTIONS}
                disabled={saving}
              />
              {draft.kind === "url" && (
                <FormRow label="Git URL" htmlFor={`${ids}-url`}>
                  <Input
                    id={`${ids}-url`}
                    value={draft.url}
                    onChange={(event) => update({ url: event.target.value })}
                    placeholder="https://github.com/owner/repo.git"
                    disabled={saving}
                  />
                </FormRow>
              )}
              {draft.kind === "github" && (
                <FormRow label="owner/repo" htmlFor={`${ids}-repo`}>
                  <Input
                    id={`${ids}-repo`}
                    value={draft.repo}
                    onChange={(event) => update({ repo: event.target.value })}
                    placeholder="owner/repo"
                    disabled={saving}
                  />
                </FormRow>
              )}
              {draft.kind === "directory" && (
                <FormRow label="Local path" htmlFor={`${ids}-path`}>
                  <PathField
                    ariaLabel="Local path"
                    directory={directoryActions}
                    id={`${ids}-path`}
                    value={draft.path}
                    onChange={(value) => update({ path: value })}
                    kind="dir"
                    complete={(prefix, includeFiles) => extensionsStore.getState().completePaths(prefix, includeFiles)}
                    placeholder="/absolute/path"
                    disabled={saving}
                  />
                </FormRow>
              )}
              {sourceTouched && (
                <p className={CLASS.sheetNote} role="status">
                  Saving re-fetches the marketplace. Installed plugins are unaffected.
                </p>
              )}
              {formError !== null && (
                <p className={CLASS.sheetError} role="alert">
                  {formError}
                </p>
              )}
            </form>
            <div className={CLASS.metaList}>
              <div className={CLASS.metaRow}>
                <span className={CLASS.metaLabel}>Install location</span>
                <span className={`${CLASS.metaValue} ${CLASS.rowMeta}`}>
                  {entry.installLocation || "not fetched yet"}
                </span>
              </div>
              <div className={CLASS.metaRow}>
                <span className={CLASS.metaLabel}>Last updated</span>
                <span className={`${CLASS.metaValue} ${CLASS.rowMeta}`}>{lastUpdatedText(entry.lastUpdated)}</span>
              </div>
            </div>
            <div className={CLASS.sheetActions}>
              <Button variant="quiet" onClick={() => void handleRefresh()} disabled={busy}>
                Refresh
              </Button>
            </div>
            <hr className={CLASS.sheetDivider} />
            <div className={CLASS.sheetActions}>
              <Button variant="danger" onClick={() => setPendingRemove(true)} disabled={busy}>
                Remove
              </Button>
            </div>
          </>
        )}
      </Sheet>
      <ConfirmDialog
        open={pendingRemove && entry !== undefined}
        title="Remove marketplace"
        confirmLabel="Remove"
        busy={removeBusy}
        onConfirm={() => void handleConfirmRemove()}
        onCancel={() => setPendingRemove(false)}
      >
        {entry !== undefined ? `Remove marketplace "${entry.name}"? Installed plugins from it are unaffected.` : ""}
      </ConfirmDialog>
    </>
  );
}
