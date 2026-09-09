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
import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { errorText } from "../../../../protocol/errors";
import type { AuthTestResponse, InstanceEntry } from "../../../../protocol/types.gen";
import { useIsMobile } from "../../../../shell/useIsMobile";
import { credentialsStore, useCredentialsStore } from "../../../../stores/credentials";
import { Button, Chip, FormRow, Input, Select, Sheet, StatusDot, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import {
  credentialLayers,
  keylessByDesign,
  safeCredentialTestMessage,
  safeCredentialTestResult,
  unconfiguredLabel,
} from "./credentialLabels";
import styles from "./InstanceSheet.module.css";
import {
  draftFor,
  type InstanceDraft,
  instanceEditParams,
  PROTOCOL_OPTIONS,
  SURFACE_OPTIONS,
  varRows,
} from "./instanceEdit";

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

const LITERAL_HEADER_ERROR = "Credential header must reference a $VARIABLE, never a literal secret.";
const EMPTY_NAME_ERROR = "Name cannot be empty.";
// The save landed, but its response was superseded, so nothing reseeded the
// form: the toast says what the sheet is showing - the user's own draft - and
// how to get the current state.
const STALE_SAVE_WARNING =
  "Saved, but the list changed underneath; your edits were kept — refresh to see the current state";

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
  // The instance a rename of this sheet's own went out for, held for the span
  // of the request: the old name leaves the store when the response lands, a
  // beat before the section re-selects the new one, so for that beat the
  // sheet's subject is in neither place. Keeping it here is what carries the
  // sheet across - an `open` that dips false unmounts the panel, replaying
  // its slide-in from off-screen and throwing focus out of the form - and it
  // is also the guard that keeps that vanish from closing the sheet.
  const [renamingFrom, setRenamingFrom] = useState<InstanceEntry | undefined>(undefined);
  // The name the section has the sheet on right now, readable from a save
  // still in flight: handleSave captured `name` from the render it ran in, and
  // the user is free to dismiss the sheet or pick another row before the
  // response lands. Compared against the instance the save went out for, this
  // is what says whether the answer is still this sheet's to act on.
  const shownName = useRef(name);

  const stored = name === null ? undefined : instances.find((i) => i.name === name);
  const instance = stored ?? renamingFrom;
  const template = instance === undefined ? undefined : availableProviders.find((p) => p.id === instance.providerId);

  function seed(inst: InstanceEntry): void {
    const seeded = draftFor(
      inst,
      credentialsStore.getState().availableProviders.find((p) => p.id === inst.providerId),
    );
    setInitial(seeded);
    setDraft(seeded);
    setFormError(null);
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
  // The section moved the selection to the new name: the held instance has
  // done its job, and holding it any longer would keep a ghost on screen.
  // biome-ignore lint/correctness/useExhaustiveDependencies: name is a deliberate trigger-only dep - the body only drops the held instance, but must re-run on every name change to release it
  useEffect(() => {
    setRenamingFrom(undefined);
  }, [name]);
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

  async function handleSave(): Promise<void> {
    // The action carries its own write gate rather than borrowing the Save
    // button's disabled state: the form submits too, and a refused or
    // in-flight write must not go out through that door either.
    if (busy || writesRefused) return;
    if (instance === undefined) return;
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
    setFormError(null);
    setBusy(true);
    if (params.newName !== undefined) setRenamingFrom(instance);
    try {
      // The store's verdict, not the response, says what landed: a refresh
      // that started after this save answers first, and the store discards
      // this response as superseded. Its listing is then a document nothing
      // holds, so a toast naming it, a reseed from it, or a steer onto a name
      // it alone reports would all show the user a state that is not there.
      const applied = await credentialsStore.getState().edit(params);
      // Except when the store's own list already holds the renamed instance:
      // that is this save, learned from the list instead of the response, so
      // it is a save like any other.
      const listedRename =
        params.newName !== undefined && credentialsStore.getState().instances.some((i) => i.name === params.newName)
          ? params.newName
          : undefined;
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
  const showDangerZone = instance !== undefined && (showClear || showClearStoredKey || !instance.implicit);
  const layers = instance === undefined ? [] : credentialLayers(instance);
  const unconfigured = instance === undefined ? null : unconfiguredLabel(instance);
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

  return (
    <Sheet
      open={open}
      onClose={onClose}
      title={instance?.name ?? ""}
      side={isMobile ? "bottom" : "right"}
      size="wide"
      footer={
        instance !== undefined && (
          <Button onClick={() => void handleSave()} disabled={!dirty || busy || writesRefused}>
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
            {instance.implicit && <Chip>from environment</Chip>}
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
              <FormRow
                label="Name"
                htmlFor={`${ids}-name`}
                help={
                  instance.implicit
                    ? "This instance comes from the environment and cannot be renamed."
                    : renaming
                      ? `Launch config and past sessions that reference "${instance.name}" keep the old name.`
                      : undefined
                }
              >
                <Input
                  id={`${ids}-name`}
                  value={draft.name}
                  onChange={(event) => update({ name: event.target.value })}
                  disabled={busy || instance.implicit}
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
                help="NAME=VALUE; the value must reference a $VARIABLE, never a literal secret."
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
          <div className={CLASS.actionRows}>
            <div className={CLASS.fullRow}>
              <Button variant="quiet" onClick={onTestCredentials} disabled={busy || testCredentialsPending}>
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
                <Button variant="quiet" onClick={onSetApiKey} disabled={busy}>
                  {instance.hasStoredFile ? "Replace key" : "Set key"}
                </Button>
              </div>
            )}
            {supportsCredentialJson && (
              <div className={CLASS.fullRow}>
                <Button variant="quiet" onClick={onSetCredentialJson} disabled={busy}>
                  {instance.hasStoredFile ? "Replace credential JSON" : "Set credential JSON"}
                </Button>
              </div>
            )}
            {supportsOAuth && (
              <div className={CLASS.fullRow}>
                <Button variant="quiet" onClick={onOAuthStart} disabled={busy}>
                  {instance.hasStoredOAuth ? "Refresh OAuth" : "Sign in…"}
                </Button>
              </div>
            )}
            {!instance.isDefault && (
              <div className={CLASS.fullRow}>
                <Button variant="quiet" onClick={onSetDefault} disabled={busy || writesRefused}>
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
                    <Button variant="dangerQuiet" onClick={onClearStoredKey} disabled={busy}>
                      {supportsCredentialJson ? "Clear stored credential JSON" : "Clear stored key"}
                    </Button>
                  </div>
                )}
                {showClear && (
                  <div className={CLASS.fullRow}>
                    <Button variant="dangerQuiet" onClick={onClear} disabled={busy}>
                      Clear
                    </Button>
                  </div>
                )}
                {!instance.implicit && (
                  <div className={CLASS.fullRow}>
                    <Button variant="danger" onClick={onRemove} disabled={busy || writesRefused}>
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
