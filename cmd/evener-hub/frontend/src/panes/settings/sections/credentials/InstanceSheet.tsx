// InstanceSheet: the provider instance's editor (spec 2026-09-07 §2). Opens
// from an InstanceRow tap and IS the edit surface: the authored fields are
// form inputs prefilled from the instance, Save in the sheet footer lights
// up when any differs, and renaming is editing the Name field. Below the
// form sit the layered credential display and the actions the inspector
// this replaced already had (test, set/replace key or credential JSON,
// sign in/refresh OAuth, make default) and the danger zone; the secret
// entry and OAuth flows stay dialogs because they carry write-only values
// or several steps. A wide right Sheet on desktop, a bottom Sheet on mobile
// (useIsMobile, the shell's own source).
//
// The instance is read from the store by name so cross-client changes land
// live. The draft is seeded when a different instance opens and again
// after this sheet's own save lands, never on an unrelated refresh, so
// in-progress edits survive another client's change. The sheet closes
// itself when its instance disappears - except across its own rename,
// where the section re-selects the new name (onRenamed) and the vanish is
// the rename landing, not a removal. Across that vanish the sheet goes on
// showing the instance the rename went out for (renamingFrom), so it never
// unmounts itself mid-rename. A save whose answer arrives after the user has
// dismissed the sheet or picked another row still counts as a write, and is
// toasted as one, but stops there (shownName): re-selecting or reseeding then
// would drag the sheet back to a save the user has walked away from. And a
// save the store discarded as superseded steers nothing on its own answer -
// the store's current list is the only thing that can say what landed.
//
// Owns the one mutation it edits (evener/instance/edit); the section still
// owns what every other action DOES (opening an editor, a confirm, or
// calling the store), the same division of labor as before.

import type { AuthTestResponse, InstanceEditParams, InstanceEntry } from "@evener/appwire-client";
import {
  CONNECTION_REPLACED_ERROR,
  credentialLayers,
  errorText,
  friendlyErrorMessage,
  fromEnvironment,
  isEndpointConflict,
  isInstanceRenamePersisted,
  keylessByDesign,
  renameLeavesEnvironmentRow,
  safeCredentialTestMessage,
  safeCredentialTestResult,
  unconfiguredLabel,
} from "@evener/appwire-client";
import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { useIsMobile } from "../../../../shell/useIsMobile";
import { credentialsStore, isStaleListingRefusal, useCredentialsStore } from "../../../../stores/credentials";
import { Button, Chip, FormRow, Input, Select, Sheet, StatusDot, Switch, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./InstanceSheet.module.css";
import {
  draftFor,
  type InstanceDraft,
  instanceEditParams,
  PROTOCOL_OPTIONS,
  SURFACE_OPTIONS,
  varRows,
} from "./instanceEdit";
import {
  baseCarriedByRename,
  changedFields,
  fieldValue,
  renamedInstanceLanded,
  supersededSaveLanded,
  untouchedIdentityMatches,
} from "./instanceLanding";
import { confirmListingState } from "./reconcileListing";

const CLASS = {
  headingRow: requireClass(styles.headingRow, "InstanceSheet.module.css", "headingRow"),
  form: requireClass(styles.form, "InstanceSheet.module.css", "form"),
  formError: requireClass(styles.formError, "InstanceSheet.module.css", "formError"),
  layers: requireClass(styles.layers, "InstanceSheet.module.css", "layers"),
  layer: requireClass(styles.layer, "InstanceSheet.module.css", "layer"),
  unconfigured: requireClass(styles.unconfigured, "InstanceSheet.module.css", "unconfigured"),
  metaRow: requireClass(styles.metaRow, "InstanceSheet.module.css", "metaRow"),
  metaLabel: requireClass(styles.metaLabel, "InstanceSheet.module.css", "metaLabel"),
  metaValue: requireClass(styles.metaValue, "InstanceSheet.module.css", "metaValue"),
  actionRows: requireClass(styles.actionRows, "InstanceSheet.module.css", "actionRows"),
  fullRow: requireClass(styles.fullRow, "InstanceSheet.module.css", "fullRow"),
  divider: requireClass(styles.divider, "InstanceSheet.module.css", "divider"),
  testResult: requireClass(styles.testResult, "InstanceSheet.module.css", "testResult"),
};

const LITERAL_HEADER_ERROR =
  "Credential header must reference a $VARIABLE or run a $(command), never a literal secret.";
const EMPTY_NAME_ERROR = "Name cannot be empty.";
// The save landed, but its response was superseded, so nothing reseeded the
// form: the toast says what the sheet is showing - the user's own draft - and
// how to get the current state.
const STALE_SAVE_WARNING =
  "Saved, but the list changed underneath; your edits were kept — refresh to see the current state";
// The instance under the sheet was replaced by a different one at the same
// name, so the retained draft belongs to an instance that is no longer there.
// The form is reset to the instance now on screen rather than saving stale
// edits onto its replacement.
const CHANGED_INSTANCE_ERROR =
  "This instance was replaced under the same name; the form was reset to the instance now on screen.";
// The rename persisted but the listing cannot yet confirm the destination row,
// so the sheet still shows the held original while the section's selection names
// the destination. A second Save would resubmit the completed rename against
// the old name, which the config no longer carries; the form says so rather than
// sending it.
const RENAME_PENDING_SAVE_ERROR =
  "This rename is still being confirmed; wait for the renamed instance to appear before saving again.";
// The same gap for every other per-instance action: the sheet's callbacks target
// the section's selected name, so firing one now would act on whatever occupies
// the destination, not the instance on screen.
const RENAME_PENDING_ACTION_MESSAGE =
  "This rename is still being confirmed; wait for the renamed instance to appear before acting on it.";
// The hub refused the endpoint this form was seeded from: the name moved since
// the draft was read, so the save was not applied. The draft is kept for a retry
// once the listing on screen shows the destination now in effect.
const ENDPOINT_CHANGED_SAVE_ERROR =
  "This instance changed to a different endpoint since the form was opened. Review its destination and save again.";

// The immutable identity of the entry a draft belongs to: the name plus the
// fields no edit through this sheet changes. providerId/base/auth pick the
// provider and scheme; endpointFingerprint identifies the complete resolved
// endpoint even when the displayed baseUrl is byte-identical (a query-only
// change is invisible in baseUrl). Provenance (implicit) is deliberately not
// here: the guided flow legitimately re-anchors a draft from a curated setup row
// to the authored row created from it, and the removal path that could hand a
// name to an environment-supplied row closes its sheet instead (see
// CredentialsSection's confirmed removal). The fields a draft edits - baseUrl,
// protocol, surface, vars, apiKeyEnv, credentialHeader - are deliberately not
// here: a change to one of them is this instance edited, not a different
// instance under the same name.
const DRAFT_IDENTITY_FIELDS = ["name", "providerId", "base", "auth", "endpointFingerprint"] as const;

/** The identity a draft was seeded from, so a listing change that leaves the
 * name but puts a different instance under it cannot silently re-target the
 * draft: the save is refused and the form re-anchored to the entry now on
 * screen instead of writing edits typed for the old instance onto its
 * replacement. */
function draftIdentity(entry: InstanceEntry): string {
  const fields = DRAFT_IDENTITY_FIELDS.map((field) => fieldValue(entry, field));
  // A row the hub cannot key serves no fingerprint, and two such rows would
  // otherwise share an identity: fall back to the endpoint the user can see, so
  // a same-name replacement at a different destination is still a different
  // instance even while fingerprinting is unavailable.
  if (fieldValue(entry, "endpointFingerprint") === "") fields.push(fieldValue(entry, "baseUrl"));
  return fields.join("\u0000");
}

/** The same identity with the endpoint fingerprint left out - the fields
 * draftIdentity carries minus the fingerprint. Two rows that match on this are
 * the same instance, one re-pointed; a row that differs here (or is absent) is
 * a replacement wearing the name, and a draft typed for the old instance must
 * not follow it. */
function draftIdentityWithoutEndpoint(entry: InstanceEntry): string {
  return DRAFT_IDENTITY_FIELDS.filter((field) => field !== "endpointFingerprint")
    .map((field) => fieldValue(entry, field))
    .join("\u0000");
}

/** The note under Name. A rename always leaves the old name behind in launch
 * config and past sessions; on an instance the environment supplies it also
 * leaves the instance itself, because the variable that makes it exist is not
 * the row's to move, so the rename authors a second instance beside it. That
 * includes a stored key the rename moves away from a set variable it was
 * shadowing (renameLeavesEnvironmentRow). */
function renameNote(instance: InstanceEntry): string {
  const keepsOldName = `Launch config and past sessions that reference "${instance.name}" keep the old name.`;
  return renameLeavesEnvironmentRow(instance)
    ? `Renaming adds a new instance and leaves this one in place, because the environment supplies it. ${keepsOldName}`
    : keepsOldName;
}

export interface InstanceSheetProps {
  name: string | null;
  onClose: () => void;
  /** After a successful rename, with the new name: the section re-selects
   * it so the sheet stays open on the same instance. */
  onRenamed: (newName: string) => void;
  onSetApiKey: () => void;
  onSetCredentialJson: () => void;
  onOAuthStart: () => void;
  onClear: () => void;
  onClearStoredKey: () => void;
  onRemove: () => void;
  onSetDefault: () => void;
  onTestCredentials: () => void;
  /** Flips one model row's disabled flag; owned by the section like every
   * other non-edit action. */
  onToggleModel: (model: string, disabled: boolean) => void;
  /** Re-fetches this instance's live listing; owned by the section like
   * every other non-edit action. */
  onRefreshModels: () => void;
  /** A live-model refresh is in flight for this sheet: the Models section
   * says so while the catalog rows stay interactive. */
  modelsRefreshing?: boolean;
  /** Pending toggle keys (`instance/model`, see the section owner):
   * the matching switch is disabled until its write settles. */
  pendingToggles?: ReadonlySet<string>;
  testCredentialsPending?: boolean;
  testCredentialsResult?: AuthTestResponse;
  /** Disables Save/Remove/make default while providers.toml cannot be
   * written (InstanceListResponse.writesRefused, spec §11.3) - Set key/Sign
   * in/Clear/Clear stored key/Test credentials are unaffected: they write
   * the credentials store or an OAuth record, never providers.toml. */
  writesRefused?: boolean;
}

export function InstanceSheet({
  name,
  onClose,
  onRenamed,
  onSetApiKey,
  onSetCredentialJson,
  onOAuthStart,
  onClear,
  onClearStoredKey,
  onRemove,
  onSetDefault,
  onTestCredentials,
  onToggleModel,
  onRefreshModels,
  modelsRefreshing = false,
  pendingToggles,
  testCredentialsPending = false,
  testCredentialsResult,
  writesRefused = false,
}: InstanceSheetProps) {
  const instances = useCredentialsStore((s) => s.instances);
  const availableProviders = useCredentialsStore((s) => s.availableProviders);
  const isMobile = useIsMobile();
  const toast = useToasts();
  const ids = useId();

  const [initial, setInitial] = useState<InstanceDraft | null>(null);
  const [draft, setDraft] = useState<InstanceDraft | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // The rename of this sheet's own that went out, held with the request that
  // carries its destination. The old name leaves the store when the response
  // lands, a beat before the section re-selects the new one, so for that beat
  // the sheet's subject is in neither place; keeping the entry is what carries
  // the sheet across, and an `open` that dips false would unmount the panel,
  // replaying its slide-in from off-screen and throwing focus out of the form.
  // The held entry is also the identity the listing's row at the destination
  // must answer to: the destination name is not reserved, so a row there that
  // is not this rename's own (an implicit curated row the environment
  // re-derived, another instance that took the freed name) must not become the
  // sheet's subject - the held entry stays until the expected row appears.
  const [renamingFrom, setRenamingFrom] = useState<{ entry: InstanceEntry; params: InstanceEditParams } | undefined>(
    undefined,
  );
  // The name the section has the sheet on right now, readable from a save
  // still in flight: handleSave captured `name` from the render it ran in, and
  // the user is free to dismiss the sheet or pick another row before the
  // response lands. Compared against the instance the save went out for, this
  // is what says whether the answer is still this sheet's to act on.
  const shownName = useRef(name);
  // The identity the current draft was seeded from. A listing change can put a
  // different instance under the same name without the name-keyed effect
  // above noticing, so handleSave checks the draft still belongs to the
  // instance it is about to write to.
  const seededIdentity = useRef<string | null>(null);
  // The endpoint the draft was seeded from, sent with the save as the atomic
  // assertion the client-side identity check above cannot be: the hub refuses
  // the edit when another client has re-pointed the name since the draft was
  // read. Undefined when the row carried no fingerprint - nothing to assert.
  const seededFingerprint = useRef<string | undefined>(undefined);

  const stored = name === null ? undefined : instances.find((i) => i.name === name);
  // The section is on this rename's destination. The listing's row there is
  // this rename's own only when it passes renamedInstanceLanded's identity
  // checks; while it does not - absent, an impostor, or an implicit
  // re-derivation - the held entry stays the subject so edits never target the
  // wrong configuration.
  const atRenameDestination = renamingFrom !== undefined && name === renamingFrom.params.newName;
  /** The listing row that is this rename's own landing, judged at render time:
   * authored, and carrying every identity field the rename left alone. No
   * mutation capture is available here, so this is the identity check alone -
   * the save path proves the destination with the captured endpointFingerprint
   * (renamedInstanceLanded). */
  function renameLandingRow(from: { entry: InstanceEntry; params: InstanceEditParams }): InstanceEntry | undefined {
    const listed = instances.find((i) => i.name === from.params.newName);
    if (listed === undefined || listed.implicit) return undefined;
    return untouchedIdentityMatches(from.entry, listed, from.params, baseCarriedByRename) ? listed : undefined;
  }
  const renameLandedHere =
    atRenameDestination && renamingFrom !== undefined && renameLandingRow(renamingFrom) !== undefined;
  // The reconciliation gap: the section has selected this rename's destination,
  // but the listing's row there is not this rename's own (absent, implicit, or a
  // different instance). The sheet's subject stays the held original while every
  // callback the section passes targets the destination name - so any action
  // fired now would act on whatever occupies that name, not the instance shown.
  const renameGap = atRenameDestination && !renameLandedHere;
  const instance = renameGap ? renamingFrom.entry : (stored ?? renamingFrom?.entry);
  const template = instance === undefined ? undefined : availableProviders.find((p) => p.id === instance.providerId);

  function seed(inst: InstanceEntry): void {
    const seeded = draftFor(
      inst,
      credentialsStore.getState().availableProviders.find((p) => p.id === inst.providerId),
    );
    setInitial(seeded);
    setDraft(seeded);
    setFormError(null);
    seededIdentity.current = draftIdentity(inst);
    seededFingerprint.current = inst.endpointFingerprint;
  }

  // biome-ignore lint/correctness/useExhaustiveDependencies: reseed only when a different instance opens; a refresh of the same instance must not clobber in-progress edits
  useEffect(() => {
    if (instance === undefined) {
      setInitial(null);
      setDraft(null);
      setFormError(null);
      return;
    }
    seed(instance);
  }, [instance?.name]);

  // A detail sheet is only as alive as its subject: the instance can vanish
  // under an open sheet (its own Remove completing, or another client's
  // change), and an editor for a thing that no longer exists closes itself
  // rather than offering actions on a ghost. Its own rename is the one
  // vanish that is not a removal, and renamingFrom is what tells them apart:
  // across a rename `instance` is still the held one, so this stays quiet.
  useEffect(() => {
    if (name !== null && instance === undefined) onClose();
  }, [name, instance, onClose]);
  // The section moved the selection: the held entry has done its job once the
  // listing can stand in for it, and holding it longer would keep a ghost on
  // screen. On this rename's destination that means the listing's row must be
  // this rename's own row (renamedInstanceLanded): an impostor - implicit,
  // stale, or a different instance wearing the freed name - or an absent row
  // keeps the held entry as the sheet's subject. Anywhere else the held entry
  // is released once the store carries the selected name; it is kept while the
  // rename is still in flight under the held name, which is the gap between the
  // write landing and the section re-selecting.
  useEffect(() => {
    if (renamingFrom === undefined) return;
    if (name === null) {
      setRenamingFrom(undefined);
      return;
    }
    if (name === renamingFrom.params.newName) {
      const listed = instances.find((i) => i.name === renamingFrom.params.newName);
      if (
        listed !== undefined &&
        !listed.implicit &&
        untouchedIdentityMatches(renamingFrom.entry, listed, renamingFrom.params, baseCarriedByRename)
      ) {
        setRenamingFrom(undefined);
      }
      return;
    }
    if (name !== renamingFrom.entry.name && stored !== undefined) setRenamingFrom(undefined);
  }, [name, stored, instances, renamingFrom]);
  // A layout effect, not the passive one above: a response can land between
  // the commit that dismissed the sheet and a passive effect, and a mirror
  // that is one beat stale lets exactly the save this guards slip through.
  // useEditorLifetime keeps the dialogs' equivalent flag on layout timing for
  // the same reason.
  useLayoutEffect(() => {
    shownName.current = name;
  }, [name]);

  const open = name !== null && instance !== undefined;

  const params = initial !== null && draft !== null ? instanceEditParams(initial, draft) : null;
  // An emptied Name is an edit the request cannot carry - an empty newName
  // means "unchanged" on the wire, so the diff leaves it out and `params` can
  // come back null with the field visibly cleared. It still counts as dirty:
  // a Save the user cannot press answers the mistake with nothing at all,
  // where a pressable one answers with the reason.
  const emptiedName = draft !== null && draft.name.trim() === "";
  const dirty = params !== null || emptiedName;

  function update(patch: Partial<InstanceDraft>): void {
    setDraft((current) => (current === null ? current : { ...current, ...patch }));
  }
  function updateVar(key: string, value: string): void {
    setDraft((current) => (current === null ? current : { ...current, vars: { ...current.vars, [key]: value } }));
  }
  // A per-instance action is refused while the rename gap persists: the section
  // callback it would run targets the selected destination, not the held
  // instance on screen. A toast, not a dead control: Button drops the click
  // entirely for aria-disabled/disabled (widgets/button), so a disabled control
  // would refuse in silence where this explains the wait.
  function refuseDuringRename(): void {
    toast.push("warning", RENAME_PENDING_ACTION_MESSAGE);
  }

  async function handleSave(): Promise<void> {
    // The action carries its own write gate rather than borrowing the Save
    // button's disabled state: the form submits too, and a refused or
    // in-flight write must not go out through that door either.
    if (busy || writesRefused) return;
    if (instance === undefined) return;
    // The rename persisted but the listing has not confirmed its destination:
    // the form still holds the user's dirty rename draft, and resubmitting it
    // would send the completed rename against the old name, which is gone.
    // Refuse with the reason (the file's pressable-refusal precedent) and send
    // nothing - the button and the form's own submit door both land here.
    if (renameGap) {
      setFormError(RENAME_PENDING_SAVE_ERROR);
      return;
    }
    // The draft belongs to the instance it was seeded from. A removal and
    // recreation under the freed name, or another instance renamed onto it,
    // leaves the name the sheet is on while handing it a different instance:
    // saving then writes edits typed against the old endpoint onto the
    // replacement. Refuse, re-anchor to the entry now on screen, and say so -
    // the same refusal the guided flow makes when its destination moves.
    if (draftIdentity(instance) !== seededIdentity.current) {
      seed(instance);
      setFormError(CHANGED_INSTANCE_ERROR);
      return;
    }
    // Ahead of the `params === null` guard, not behind it: an emptied Name is
    // exactly the edit that leaves params null, so a refusal below would
    // never be reached. Refused rather than sent because the wire reads an
    // empty newName as "unchanged" - the request would succeed and rename
    // nothing while the toast claimed a save.
    if (emptiedName) {
      setFormError(EMPTY_NAME_ERROR);
      return;
    }
    if (params === null) return;
    if (params.credentialHeader !== undefined && !params.credentialHeader.includes("$")) {
      setFormError(LITERAL_HEADER_ERROR);
      return;
    }
    // The endpoint the draft was seeded from travels with the save, so the hub
    // can refuse an edit whose name another client has re-pointed since - the
    // atomic counterpart of this sheet's own identity check. An empty
    // fingerprint asserts nothing.
    if (seededFingerprint.current !== undefined) {
      params.expectedEndpointFingerprint = seededFingerprint.current;
    }
    setFormError(null);
    setBusy(true);
    if (params.newName !== undefined) setRenamingFrom({ entry: instance, params });
    try {
      // The store's verdict, not the response, says what landed: a refresh
      // that started after this save answers first, and the store discards
      // this response as superseded. Its listing is then a document nothing
      // holds, so a toast naming it, a reseed from it, or a steer onto a name
      // it alone reports would all show the user a state that is not there.
      // The discarded answer is still the hub's authoritative post-write row
      // for this instance, though, so keep it to compare against the listing
      // now held (see supersededSaveLanded).
      let authoritative: InstanceEntry | undefined;
      const authoritativeName = params.newName ?? instance.name;
      const applied = await credentialsStore.getState().edit(params, {
        onSuperseded: (response) => {
          authoritative = response.instances.find((entry) => entry.name === authoritativeName);
        },
      });
      let listedInstances = credentialsStore.getState().instances;
      if (!applied) {
        // The verdict says a newer read won the ordering race. That read was
        // issued after this write, but its response arrives AFTER this write's
        // (evener/instance/list is not a concurrent method - it runs inline on
        // the connection's serial worker), so the listing sampled right now is
        // still the PRE-save one. Settle a read that started after the write
        // before comparing the listing against the mutation's own captured row.
        // The read is non-fatal - a torn-down connection must not be reported as
        // a failed save - but its RESULT matters: `edit` already scheduled the
        // store's own debounced refetch, and that read can start later and
        // supersede this one, resolving false without applying anything. Re-read
        // until a read applies (bounded), so the listing sampled is a post-write
        // one.
        for (let attempt = 0; attempt < 3; attempt += 1) {
          const settled = await credentialsStore
            .getState()
            .fetch()
            .catch(() => false);
          if (settled) break;
        }
        listedInstances = credentialsStore.getState().instances;
      }
      // Except when the store's own list holds this save's rename: the same
      // instance, now wearing the name it was given. Holding the name is not
      // enough on its own - a rename frees a name that any other instance can
      // take - so the entry is checked against the instance this save renamed.
      const listedRename = renamedInstanceLanded(listedInstances, instance, params, authoritative);
      if (applied || listedRename !== undefined) toast.push("success", `Saved ${params.newName ?? instance.name}`);
      // The sheet may have moved on while the request was in flight: dismissed,
      // or pointed at another row. The write stands and the toast above is
      // owed either way, but what follows steers the sheet - onRenamed asks
      // the section to select the new name, and the reseed replaces the draft.
      // Applied late, either one takes a sheet the user has moved somewhere
      // else and drags it back to this save: a dismissed sheet re-opens, a
      // freshly picked row loses the selection or shows another instance's
      // values under its own title.
      if (shownName.current !== instance.name) return;
      if (!applied) {
        if (listedRename !== undefined) {
          onRenamed(listedRename);
        } else {
          // The store's listing may already hold this plain save's own change:
          // a refresh that started after the save answers first, and the store
          // discards the response as superseded while its entry carries what
          // this save declared. The draft is still the user's, but the seeded
          // identity anchor predates the change, so re-anchor it to the entry
          // the save landed as — without reseeding, which would discard the
          // draft. The next Save then compares like against like instead of
          // refusing an instance that was never replaced.
          const landed = supersededSaveLanded(listedInstances, instance, params, authoritative);
          if (landed !== undefined) {
            seededIdentity.current = draftIdentity(landed);
            // The assertion travels with the save, so it is re-anchored with the
            // identity: the landed row's endpointFingerprint is the destination
            // the next save must assert. Leaving the pre-save fingerprint here
            // would have the hub refuse the next save for a destination that
            // moved only because this save moved it.
            seededFingerprint.current = landed.endpointFingerprint;
            // Rebase the diff baseline to what landed, keeping the draft: "draft
            // equals landed" is then correctly not dirty, and a user reverting a
            // field to its pre-save value becomes correctly dirty and writable
            // (the pre-save baseline alone left params null and Save disabled).
            const landedTemplate = credentialsStore
              .getState()
              .availableProviders.find((p) => p.id === landed.providerId);
            const landedDraft = draftFor(landed, landedTemplate);
            setInitial(landedDraft);
            // Rebase the baseline to what landed, but carry the DRAFT forward
            // only where this save declared the field. Every other field takes
            // the landed value, so a value another client changed under the
            // draft - or a derived field like the resolved Base URL - cannot
            // masquerade as a pending overwrite; the fields the user did edit
            // keep their draft values, so a revert stays correctly dirty.
            const declared = changedFields(params);
            setDraft((current) =>
              current === null
                ? current
                : {
                    name: landedDraft.name,
                    baseUrl: declared.has("baseUrl") ? current.baseUrl : landedDraft.baseUrl,
                    protocol: declared.has("protocol") ? current.protocol : landedDraft.protocol,
                    surface: declared.has("surface") ? current.surface : landedDraft.surface,
                    // `vars` is a map, not a scalar: the draft only keeps the
                    // keys THIS save declared, and takes every other key from
                    // the landed row - otherwise a variable another client
                    // changed under the draft resurfaces as a pending overwrite.
                    vars: Object.fromEntries([
                      ...Object.entries(landedDraft.vars),
                      ...Object.entries(params.vars ?? {}).map(([key]) => [key, current.vars[key] ?? ""] as const),
                    ]),
                    apiKeyEnv: declared.has("apiKeyEnv") ? current.apiKeyEnv : landedDraft.apiKeyEnv,
                    credentialHeader: declared.has("credentialHeader")
                      ? current.credentialHeader
                      : landedDraft.credentialHeader,
                  },
            );
          } else {
            // The save was superseded and its landing could not be confirmed.
            // The fields a draft edits are deliberately excluded from
            // draftIdentity, so a same-name, same-endpoint replacement whose
            // surface, variables or credentials differ would pass the next
            // pre-write check and then be overwritten by the retained draft.
            // Mark the draft stale so that next Save refuses and re-seeds.
            seededIdentity.current = null;
          }
          setRenamingFrom(undefined);
          toast.push("warning", STALE_SAVE_WARNING);
        }
        return;
      }
      if (params.newName !== undefined) {
        onRenamed(params.newName);
      } else {
        const refreshed = credentialsStore.getState().instances.find((i) => i.name === instance.name);
        if (refreshed !== undefined) seed(refreshed);
      }
    } catch (err) {
      // The store's own refusal of a write issued from the previous
      // connection's listing (stores/credentials.ts's requireWritableClient):
      // the draft was seeded from rows that connection read, so nothing was
      // sent. Keep the draft, name the change, and ask for this connection's
      // listing - its arrival is what makes the retry land. Reported as a save
      // failure it would tell the user their edit could not be saved when no
      // save ever went out.
      if (isStaleListingRefusal(err)) {
        void credentialsStore
          .getState()
          .fetch()
          .catch(() => {});
        if (shownName.current === instance.name) {
          setRenamingFrom(undefined);
          setFormError(CONNECTION_REPLACED_ERROR);
        }
        toast.push("warning", CONNECTION_REPLACED_ERROR);
        return;
      }
      if (isEndpointConflict(err)) {
        // The hub refused the asserted destination: the name moved since the
        // draft was seeded, so this save was not applied. Keep the draft, say
        // why, and re-read the listing so a retry asserts the destination now on
        // screen.
        if (shownName.current === instance.name) {
          setRenamingFrom(undefined);
          setFormError(ENDPOINT_CHANGED_SAVE_ERROR);
        }
        toast.push("error", ENDPOINT_CHANGED_SAVE_ERROR);
        // The retry this message asks for can only land once the sheet asserts
        // the destination the name now resolves to, so the read is awaited
        // rather than left in flight: the re-anchor below has to see a listing
        // the store actually applied, never the stale row the refusal
        // described. A read that never applied leaves today's refusal in place.
        const listed = await confirmListingState(() => true);
        if (!listed || shownName.current !== instance.name) return;
        const refreshed = credentialsStore.getState().instances.find((i) => i.name === instance.name);
        // Re-anchor only when the refreshed row is the same instance re-pointed
        // - an identity match that ignores the endpoint fingerprint. Then the
        // draft is left untouched and the next Save carries the user's edits and
        // the destination now on screen. A replacement (a different
        // provider/auth/base) or an absent row keeps handleSave's identity guard
        // as the authority: it reseeds and refuses, because these edits must not
        // land on a stranger.
        if (refreshed === undefined) return;
        if (draftIdentityWithoutEndpoint(refreshed) !== draftIdentityWithoutEndpoint(instance)) return;
        seededIdentity.current = draftIdentity(refreshed);
        seededFingerprint.current = refreshed.endpointFingerprint;
        return;
      }
      // A rename that stood but could not carry the instance's OAuth record
      // comes back carrying the hub's own discriminator for it
      // (isInstanceRenamePersisted). providers.toml already names the new
      // instance, so the save is not a failure: follow the instance to its new
      // name, surfacing the hub's message - it names the credential left behind
      // - as a warning rather than a plain failure. The discriminator is the
      // authoritative fact here, so the steer does not depend on the refreshed
      // listing: the hub's registry can still be a fallback listing that omits
      // the new row (its own reload or rollback failed), and the config naming
      // the new instance is enough to follow it. The listing is still refreshed
      // - the store should catch up, and the bounded retry gives the registry a
      // chance to - but its verdict is not a gate, and its failure must not
      // replace the steer with a form error on an instance the config no longer
      // carries.
      if (params.newName !== undefined && isInstanceRenamePersisted(err)) {
        // The read is there to settle the listing, not to gate the steer below:
        // the discriminator is the authoritative fact, and the mutation's
        // captured row is out of scope in this catch, so the predicate is the
        // render-time identity check alone.
        await confirmListingState((rows) => {
          const listed = rows.find((i) => i.name === params.newName);
          return (
            listed !== undefined &&
            !listed.implicit &&
            untouchedIdentityMatches(instance, listed, params, baseCarriedByRename)
          );
        });
        if (shownName.current === instance.name) {
          onRenamed(params.newName);
        }
        toast.push("warning", friendlyErrorMessage(err));
        return;
      }
      const message = errorText(err);
      // The toast is owed wherever the user has gone - they asked for a write
      // that did not happen. The form's error line is not: it belongs to the
      // instance the save went out for, and written into a sheet since
      // pointed elsewhere it blames one instance for another's failure. Same
      // for releasing the rename guard, which only this sheet's save set.
      if (shownName.current === instance.name) {
        setRenamingFrom(undefined);
        setFormError(message);
      }
      toast.push("error", `Save failed: ${message}`);
    } finally {
      setBusy(false);
    }
  }

  const supportsApiKey = instance !== undefined && (instance.authModes ?? []).includes("apiKey");
  const supportsCredentialJson = instance !== undefined && (instance.authModes ?? []).includes("credentialJson");
  const supportsOAuth = instance !== undefined && (instance.authModes ?? []).includes("oauth");
  const showClear = instance !== undefined && (instance.activeSource === "store" || instance.activeSource === "oauth");
  // showClearStoredKey: a stray stored key sits shadowed behind whatever IS
  // active (the same condition credentialLayers uses to render that second,
  // non-effective layer above) - true for an oauth/adc login with a leftover
  // credentials.toml entry, and just as much for a signed-out Codex row a
  // previous Clear left stranded (Clear's Codex branch removes the OAuth
  // record, not the file, when one is active; issue #713). This action
  // always targets the store layer only, so it is safe to offer regardless
  // of what is effective.
  const showClearStoredKey = instance?.hasStoredFile && instance.activeSource !== "store";
  // The danger zone is Clear + Clear stored key + Remove under a divider; an
  // implicit instance with nothing stored offers none of them, and a divider
  // over nothing reads as a rendering bug.
  const showDangerZone = instance !== undefined && (showClear || showClearStoredKey || !fromEnvironment(instance));
  const layers = instance === undefined ? [] : credentialLayers(instance);
  const unconfigured = instance === undefined ? null : unconfiguredLabel(instance);
  // The sheet's per-model toggles read the registry's own inventory. The
  // Models section always renders - with its Refresh button - so an
  // instance whose live listing has not arrived yet still offers a manual
  // re-fetch; only the toggles need rows.
  const models = instance?.models ?? [];
  const safeTestResult = testCredentialsResult
    ? safeCredentialTestResult(name ?? "", testCredentialsResult)
    : undefined;
  const clearingBaseUrl =
    instance !== undefined && draft !== null && Boolean(instance.baseUrl) && draft.baseUrl.trim() === "";
  // Non-empty and trimmed on both sides, exactly as instanceEditParams decides
  // whether the request carries a newName: the note and the request must agree
  // on what counts as a rename, and an emptied Name is not one.
  const renaming =
    initial !== null && draft !== null && draft.name.trim() !== "" && draft.name.trim() !== initial.name.trim();
  const nameHelp = instance !== undefined && renaming ? renameNote(instance) : undefined;

  return (
    <Sheet
      open={open}
      onClose={onClose}
      title={instance?.name ?? ""}
      side={isMobile ? "bottom" : "right"}
      size="wide"
      footer={
        instance !== undefined && (
          <Button onClick={() => void handleSave()} aria-disabled={busy} disabled={!dirty || writesRefused}>
            Save
          </Button>
        )
      }
    >
      {instance !== undefined && (
        <>
          <div className={CLASS.headingRow}>
            <StatusDot state={layers.length > 0 || keylessByDesign(instance) ? "idle" : "ended"} />
            {instance.isDefault && <Chip>★ default</Chip>}
            {fromEnvironment(instance) && <Chip>from environment</Chip>}
          </div>
          {draft !== null && (
            <form
              className={CLASS.form}
              aria-label={`Edit ${instance.name}`}
              onSubmit={(event) => {
                event.preventDefault();
                void handleSave();
              }}
            >
              <FormRow label="Name" htmlFor={`${ids}-name`} help={nameHelp}>
                <Input
                  id={`${ids}-name`}
                  value={draft.name}
                  onChange={(event) => update({ name: event.target.value })}
                  disabled={busy}
                />
              </FormRow>
              <div className={CLASS.metaRow}>
                <span className={CLASS.metaLabel}>Base provider</span>
                <span className={CLASS.metaValue}>{instance.providerId}</span>
              </div>
              <FormRow
                label="Base URL"
                htmlFor={`${ids}-baseurl`}
                help={clearingBaseUrl ? "Resets the endpoint to the provider's default." : undefined}
              >
                <Input
                  id={`${ids}-baseurl`}
                  value={draft.baseUrl}
                  onChange={(event) => update({ baseUrl: event.target.value })}
                  placeholder="https://…"
                  disabled={busy}
                />
              </FormRow>
              <FormRow label="Protocol" htmlFor={`${ids}-protocol`}>
                <Select
                  id={`${ids}-protocol`}
                  value={draft.protocol}
                  onChange={(event) => update({ protocol: event.target.value })}
                  options={PROTOCOL_OPTIONS}
                  disabled={busy}
                />
              </FormRow>
              <FormRow label="Surface" htmlFor={`${ids}-surface`}>
                <Select
                  id={`${ids}-surface`}
                  value={draft.surface}
                  onChange={(event) => update({ surface: event.target.value })}
                  options={SURFACE_OPTIONS}
                  disabled={busy}
                />
              </FormRow>
              {varRows(draft, template).map(({ key, label }) => (
                <FormRow key={key} label={label} htmlFor={`${ids}-var-${key}`}>
                  <Input
                    id={`${ids}-var-${key}`}
                    value={draft.vars[key] ?? ""}
                    onChange={(event) => updateVar(key, event.target.value)}
                    disabled={busy}
                  />
                </FormRow>
              ))}
              <FormRow label="API key environment variable" htmlFor={`${ids}-apikeyenv`}>
                <Input
                  id={`${ids}-apikeyenv`}
                  value={draft.apiKeyEnv}
                  onChange={(event) => update({ apiKeyEnv: event.target.value })}
                  placeholder="e.g. PORTKEY_KEY"
                  disabled={busy}
                />
              </FormRow>
              <FormRow
                label="Credential header"
                htmlFor={`${ids}-credentialheader`}
                help="NAME=VALUE; the value must reference a $VARIABLE or run a $(command), never a literal secret."
              >
                <Input
                  id={`${ids}-credentialheader`}
                  value={draft.credentialHeader}
                  onChange={(event) => update({ credentialHeader: event.target.value })}
                  placeholder="Authorization=Bearer $VAR"
                  disabled={busy}
                />
              </FormRow>
              {formError !== null && (
                <p className={CLASS.formError} role="alert">
                  {formError}
                </p>
              )}
            </form>
          )}
          {unconfigured !== null ? (
            <p className={CLASS.unconfigured}>{unconfigured}</p>
          ) : (
            <div className={CLASS.layers}>
              {layers.map((layer) => (
                <div key={layer.source} className={CLASS.layer}>
                  <span>↳ {layer.label}</span>
                  <Chip tone={layer.effective ? "alive" : "neutral"}>{layer.effective ? "effective" : "shadowed"}</Chip>
                </div>
              ))}
            </div>
          )}
          {/* Every action below takes Save's `busy` gate, not just the form:
              the write in flight may be a rename, and until it settles the
              sheet still shows the instance under its old name. An action
              fired in that window goes out against the name the write is
              moving away from - and the credential ones would recreate under
              it the orphan the rename just moved. */}
          {instance !== undefined && (
            <>
              <h3>Models</h3>
              <div className={CLASS.fullRow}>
                {/* Refresh is a read: the RPC deliberately skips
                    refuseWhenBroken, so it stays available while
                    providers.toml cannot be written. The rename gap is
                    different - it would read the row the selection names,
                    not the instance on screen, and cache another instance's
                    models under a name this sheet cannot yet trust. */}
                <Button
                  variant="quiet"
                  onClick={renameGap ? refuseDuringRename : onRefreshModels}
                  aria-disabled={modelsRefreshing}
                  disabled={busy}
                >
                  {modelsRefreshing ? "Refreshing live models…" : "Refresh live models"}
                </Button>
              </div>
              <div className={CLASS.actionRows}>
                {models.map((row) => (
                  <div key={row.id} className={CLASS.fullRow}>
                    <Switch
                      label={row.id}
                      checked={!row.disabled}
                      pending={pendingToggles?.has(`${name}/${row.id}`) ?? false}
                      disabled={busy || writesRefused}
                      onChange={(checked) => (renameGap ? refuseDuringRename() : onToggleModel(row.id, !checked))}
                    />
                  </div>
                ))}
              </div>
            </>
          )}
          <div className={CLASS.actionRows}>
            <div className={CLASS.fullRow}>
              <Button
                variant="quiet"
                onClick={renameGap ? refuseDuringRename : onTestCredentials}
                aria-disabled={testCredentialsPending}
                disabled={busy}
              >
                {testCredentialsPending ? "Testing credentials…" : "Test credentials"}
              </Button>
            </div>
            {safeTestResult && (
              <p className={CLASS.testResult} role="status">
                {safeTestResult.status}: {safeCredentialTestMessage(safeTestResult.status)}
              </p>
            )}
            {supportsApiKey && (
              <div className={CLASS.fullRow}>
                <Button variant="quiet" onClick={renameGap ? refuseDuringRename : onSetApiKey} disabled={busy}>
                  {instance.hasStoredFile ? "Replace key" : "Set key"}
                </Button>
              </div>
            )}
            {supportsCredentialJson && (
              <div className={CLASS.fullRow}>
                <Button variant="quiet" onClick={renameGap ? refuseDuringRename : onSetCredentialJson} disabled={busy}>
                  {instance.hasStoredFile ? "Replace credential JSON" : "Set credential JSON"}
                </Button>
              </div>
            )}
            {supportsOAuth && (
              <div className={CLASS.fullRow}>
                <Button variant="quiet" onClick={renameGap ? refuseDuringRename : onOAuthStart} disabled={busy}>
                  {instance.hasStoredOAuth ? "Refresh OAuth" : "Sign in…"}
                </Button>
              </div>
            )}
            {!instance.isDefault && (
              <div className={CLASS.fullRow}>
                <Button
                  variant="quiet"
                  onClick={renameGap ? refuseDuringRename : onSetDefault}
                  disabled={busy || writesRefused}
                >
                  ★ make default
                </Button>
              </div>
            )}
          </div>
          {showDangerZone && (
            <>
              <hr className={CLASS.divider} />
              <div className={CLASS.actionRows}>
                {showClearStoredKey && (
                  <div className={CLASS.fullRow}>
                    <Button
                      variant="dangerQuiet"
                      onClick={renameGap ? refuseDuringRename : onClearStoredKey}
                      disabled={busy}
                    >
                      {supportsCredentialJson ? "Clear stored credential JSON" : "Clear stored key"}
                    </Button>
                  </div>
                )}
                {showClear && (
                  <div className={CLASS.fullRow}>
                    <Button variant="dangerQuiet" onClick={renameGap ? refuseDuringRename : onClear} disabled={busy}>
                      Clear
                    </Button>
                  </div>
                )}
                {!fromEnvironment(instance) && (
                  <div className={CLASS.fullRow}>
                    <Button
                      variant="danger"
                      onClick={renameGap ? refuseDuringRename : onRemove}
                      disabled={busy || writesRefused}
                    >
                      Remove
                    </Button>
                  </div>
                )}
              </div>
            </>
          )}
        </>
      )}
    </Sheet>
  );
}
