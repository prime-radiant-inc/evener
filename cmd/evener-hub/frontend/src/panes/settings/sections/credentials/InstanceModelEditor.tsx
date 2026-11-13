// InstanceModelEditor: the in-sheet drill-in subview inside InstanceSheet for
// inspecting and editing one model's overrides ([providers.<name>.models."<id>"])
// or creating a custom model row.

import type { InstanceEntry, InstanceModelDetailResponse, InstanceModelEditParams } from "@evener/appwire-client";
import {
  CONNECTION_REPLACED_ERROR,
  effortLabel,
  errorText,
  friendlyErrorMessage,
  isEndpointConflict,
} from "@evener/appwire-client";
import { type KeyboardEvent, type MutableRefObject, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { credentialsStore, isStaleListingRefusal } from "../../../../stores/credentials";
import {
  Button,
  Disclosure,
  FormRow,
  InlineNotice,
  Input,
  SegmentedControl,
  Select,
  Skeleton,
  Switch,
  useFocusRehome,
  useToasts,
} from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import type { FormError } from "./formError";
import styles from "./InstanceSheet.module.css";
import {
  ALL_EFFORT_LEVELS,
  aliasOfPlaceholder,
  compactionThresholdPlaceholder,
  defaultEffortOptions,
  hasAuthoredOverrides,
  hasModelValidationErrors,
  hasRoutingOverrides,
  isModelDraftDirty,
  type ModelDraft,
  modelEditParams,
  modelProtocolOptions,
  modelSurfaceOptions,
  reasoningOptions,
  seedModelDraft,
  thinkingFormatOptions,
  thinkingShapeOptions,
  toggleEffortValue,
  toggleInputModality,
  tokenLimitPlaceholder,
  toolCallingOptions,
  validateModelDraft,
  wireIdPlaceholder,
} from "./instanceModelEdit";
import { confirmListingState } from "./reconcileListing";

const CLASS = {
  subviewRoot: requireClass(styles.subviewRoot, "InstanceSheet.module.css", "subviewRoot"),
  subviewNav: requireClass(styles.subviewNav, "InstanceSheet.module.css", "subviewNav"),
  form: requireClass(styles.form, "InstanceSheet.module.css", "form"),
  fieldInline: requireClass(styles.fieldInline, "InstanceSheet.module.css", "fieldInline"),
  fieldGrow: requireClass(styles.fieldGrow, "InstanceSheet.module.css", "fieldGrow"),
  effortGroup: requireClass(styles.effortGroup, "InstanceSheet.module.css", "effortGroup"),
  modalityGroup: requireClass(styles.modalityGroup, "InstanceSheet.module.css", "modalityGroup"),
  routingFields: requireClass(styles.routingFields, "InstanceSheet.module.css", "routingFields"),
  divider: requireClass(styles.divider, "InstanceSheet.module.css", "divider"),
  fullRow: requireClass(styles.fullRow, "InstanceSheet.module.css", "fullRow"),
};

const CHANGED_INSTANCE_MODEL_ERROR =
  "This instance was replaced under the same name; reload the model editor before saving.";
const ENDPOINT_CHANGED_MODEL_ERROR =
  "This instance changed to a different endpoint since the model editor was opened. Review its destination and save again.";
const RENAME_PENDING_MODEL_ERROR =
  "This rename is still being confirmed; wait for the renamed instance to appear before saving.";
const STALE_MODEL_SAVE_WARNING =
  "Saved, but the list changed underneath; your edits were kept — refresh to see the current state";

type ModelFormErrorReason =
  | "load-failed"
  | "rename-pending"
  | "instance-replaced"
  | "connection-replaced"
  | "endpoint-changed"
  | "save-failed";

export interface InstanceModelEditorProps {
  instance: InstanceEntry;
  mode: "edit" | "create";
  model?: string;
  writesRefused: boolean;
  renameGap: boolean;
  pendingToggles?: ReadonlySet<string>;
  saveRef: MutableRefObject<(() => Promise<void>) | null>;
  onStateChange: (state: { busy: boolean; canSave: boolean }) => void;
  onBack: () => void;
  onToggleModel: (model: string, disabled: boolean) => void;
  onRefuseDuringRename: () => void;
  onModelSaved: (landed: InstanceEntry | undefined, createdModelId?: string) => void;
  onRequestRemoveCustom: (model: string, expectedEndpointFingerprint?: string) => void;
  draftIdentity: (entry: InstanceEntry) => string;
  draftIdentityWithoutEndpoint: (entry: InstanceEntry) => string;
}

interface ResettableInputRowProps {
  id: string;
  label: string;
  value: string;
  initialValue: string;
  placeholder: string;
  error?: string;
  disabled: boolean;
  onChange: (value: string) => void;
  onReset: () => void;
}

function ResettableInputRow({
  id,
  label,
  value,
  initialValue,
  placeholder,
  error,
  disabled,
  onChange,
  onReset,
}: ResettableInputRowProps) {
  return (
    <FormRow label={label} htmlFor={id} error={error}>
      <div className={CLASS.fieldInline}>
        <div className={CLASS.fieldGrow}>
          <Input
            id={id}
            value={value}
            placeholder={placeholder}
            disabled={disabled}
            aria-describedby={error ? `${id}-error` : undefined}
            onChange={(event) => onChange(event.target.value)}
          />
        </div>
        {initialValue !== "" && value !== "" && (
          <Button variant="link" disabled={disabled} onClick={onReset}>
            Reset
          </Button>
        )}
      </div>
    </FormRow>
  );
}

export function InstanceModelEditor({
  instance,
  mode,
  model = "",
  writesRefused,
  renameGap,
  pendingToggles,
  saveRef,
  onStateChange,
  onBack,
  onToggleModel,
  onRefuseDuringRename,
  onModelSaved,
  onRequestRemoveCustom,
  draftIdentity,
  draftIdentityWithoutEndpoint,
}: InstanceModelEditorProps) {
  const ids = useId();
  const toast = useToasts();
  const subviewRootRef = useRef<HTMLDivElement>(null);
  const backButtonRef = useRef<HTMLButtonElement>(null);
  const mountedMode = useRef(mode);
  const viewTokenRef = useRef(0);
  const shownInstanceName = useRef(instance.name);
  const seededIdentity = useRef<string | null>(draftIdentity(instance));
  const seededFingerprint = useRef<string | undefined>(instance.endpointFingerprint);
  useFocusRehome(subviewRootRef);

  const [detail, setDetail] = useState<InstanceModelDetailResponse | null>(null);
  const [loading, setLoading] = useState(mode === "edit");
  const [initial, setInitial] = useState<ModelDraft>(() => seedModelDraft(null));
  const [draft, setDraft] = useState<ModelDraft>(() => seedModelDraft(null));
  const [routingOpen, setRoutingOpen] = useState(mode === "create");
  const [busy, setBusy] = useState(false);
  const [submitAttempted, setSubmitAttempted] = useState(false);
  const [formError, setFormError] = useState<FormError<ModelFormErrorReason> | null>(null);

  const modelIdFieldId = `${ids}-model-id`;

  useLayoutEffect(() => {
    shownInstanceName.current = instance.name;
  }, [instance.name]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: viewTokenRef advances on every target view transition and on unmount
  useLayoutEffect(() => {
    viewTokenRef.current += 1;
    return () => {
      viewTokenRef.current += 1;
    };
  }, [instance.name, mode, model]);

  useLayoutEffect(() => {
    return () => {
      saveRef.current = null;
    };
  }, [saveRef]);

  useLayoutEffect(() => {
    if (mountedMode.current === "create") {
      const modelInput = subviewRootRef.current?.ownerDocument.getElementById(modelIdFieldId);
      if (modelInput && "focus" in modelInput && typeof modelInput.focus === "function") {
        modelInput.focus();
        return;
      }
    }
    backButtonRef.current?.focus();
  }, [modelIdFieldId]);

  function loadModelForInstance(targetInstance: InstanceEntry, options: { clearError: boolean }): () => void {
    if (options.clearError) {
      setFormError(null);
    }
    if (mode === "create") {
      setLoading(false);
      setDetail(null);
      const blank = seedModelDraft(null);
      setInitial(blank);
      setDraft(blank);
      setRoutingOpen(true);
      seededIdentity.current = draftIdentity(targetInstance);
      seededFingerprint.current = targetInstance.endpointFingerprint;
      return () => {};
    }
    let cancelled = false;
    const requestedIdentity = draftIdentity(targetInstance);
    const requestedFingerprint = targetInstance.endpointFingerprint;
    const tokenAtReload = viewTokenRef.current;
    const isStale = () => cancelled || viewTokenRef.current !== tokenAtReload;
    setLoading(true);
    void credentialsStore
      .getState()
      .getModel({ name: targetInstance.name, model })
      .then((loaded) => {
        if (isStale()) return;
        setDetail(loaded);
        const seeded = seedModelDraft(loaded);
        setInitial(seeded);
        setDraft(seeded);
        setRoutingOpen(hasRoutingOverrides(loaded.authored));
        seededIdentity.current = requestedIdentity;
        seededFingerprint.current = requestedFingerprint;
        setLoading(false);
      })
      .catch((err: unknown) => {
        if (isStale()) return;
        setLoading(false);
        setFormError({ reason: "load-failed", message: friendlyErrorMessage(err) });
      });
    return () => {
      cancelled = true;
    };
  }

  // biome-ignore lint/correctness/useExhaustiveDependencies: fetch only when editing a model not already loaded in detail
  useEffect(() => {
    if (mode === "edit" && detail !== null && detail.name === instance.name && detail.model === model) {
      setRoutingOpen(hasRoutingOverrides(detail.authored));
      setLoading(false);
      return;
    }
    return loadModelForInstance(instance, { clearError: true });
  }, [instance.name, mode, model]);

  const errors = validateModelDraft(draft, detail, instance.models ?? [], mode, initial);
  const dirty = isModelDraftDirty(initial, draft, mode);
  const canSave = !loading && (mode === "create" || detail !== null) && dirty && !hasModelValidationErrors(errors);

  useLayoutEffect(() => {
    onStateChange({ busy, canSave });
  }, [busy, canSave, onStateChange]);

  function update(patch: Partial<ModelDraft>): void {
    setDraft((current) => ({ ...current, ...patch }));
  }

  function applySavedDetail(nextDetail: InstanceModelDetailResponse): void {
    setDetail(nextDetail);
    const seeded = seedModelDraft(nextDetail);
    setInitial(seeded);
    setDraft(seeded);
    setSubmitAttempted(false);
    if (mode === "create") {
      setRoutingOpen(hasRoutingOverrides(nextDetail.authored));
    } else if (hasRoutingOverrides(nextDetail.authored)) {
      setRoutingOpen(true);
    }
  }

  function reseedForReplacement(targetInstance: InstanceEntry): void {
    setFormError({ reason: "instance-replaced", message: CHANGED_INSTANCE_MODEL_ERROR });
    setSubmitAttempted(false);
    seededIdentity.current = null;
    seededFingerprint.current = undefined;
    loadModelForInstance(targetInstance, { clearError: false });
  }

  async function handleEndpointConflict(tokenAtStart: number): Promise<void> {
    if (viewTokenRef.current === tokenAtStart) {
      setFormError({ reason: "endpoint-changed", message: ENDPOINT_CHANGED_MODEL_ERROR });
    }
    toast.push("error", ENDPOINT_CHANGED_MODEL_ERROR);
    const listed = await confirmListingState(() => true);
    if (!listed || viewTokenRef.current !== tokenAtStart) return;
    const refreshed = credentialsStore.getState().instances.find((i) => i.name === instance.name);
    if (refreshed === undefined) return;
    if (draftIdentityWithoutEndpoint(refreshed) !== draftIdentityWithoutEndpoint(instance)) return;
    seededIdentity.current = draftIdentity(refreshed);
    seededFingerprint.current = refreshed.endpointFingerprint;
  }

  async function runModelEdit(
    params: InstanceModelEditParams,
    options: { successToast: string; failurePrefix: string; createdModelId?: string },
  ): Promise<void> {
    const tokenAtStart = viewTokenRef.current;
    setFormError(null);
    setBusy(true);
    try {
      const result = await credentialsStore.getState().editModel(params);
      if (!result.applied) {
        if (viewTokenRef.current === tokenAtStart) {
          const current = credentialsStore.getState().instances.find((i) => i.name === instance.name);
          if (current === undefined || draftIdentity(current) !== seededIdentity.current) {
            seededIdentity.current = null;
          }
        }
        toast.push("warning", STALE_MODEL_SAVE_WARNING);
        return;
      }
      toast.push("success", options.successToast);
      if (shownInstanceName.current !== instance.name) return;
      const refreshed = credentialsStore.getState().instances.find((i) => i.name === instance.name);
      if (viewTokenRef.current !== tokenAtStart) {
        onModelSaved(refreshed, undefined);
        return;
      }
      if (refreshed !== undefined) {
        seededIdentity.current = draftIdentity(refreshed);
        seededFingerprint.current = refreshed.endpointFingerprint;
      }
      if (result.detail !== null) {
        applySavedDetail(result.detail);
      }
      onModelSaved(refreshed, options.createdModelId);
    } catch (err) {
      if (isStaleListingRefusal(err)) {
        void credentialsStore
          .getState()
          .fetch()
          .catch(() => {});
        if (viewTokenRef.current === tokenAtStart) {
          setFormError({ reason: "connection-replaced", message: CONNECTION_REPLACED_ERROR });
        }
        toast.push("warning", CONNECTION_REPLACED_ERROR);
        return;
      }
      if (isEndpointConflict(err)) {
        await handleEndpointConflict(tokenAtStart);
        return;
      }
      const message = errorText(err);
      if (viewTokenRef.current === tokenAtStart) {
        setFormError({ reason: "save-failed", message });
      }
      toast.push("error", `${options.failurePrefix}: ${message}`);
    } finally {
      if (viewTokenRef.current === tokenAtStart) {
        setBusy(false);
      }
    }
  }

  async function handleSave(): Promise<void> {
    if (busy || writesRefused || loading) return;
    setSubmitAttempted(true);
    if (renameGap) {
      setFormError({ reason: "rename-pending", message: RENAME_PENDING_MODEL_ERROR });
      return;
    }
    if (draftIdentity(instance) !== seededIdentity.current) {
      reseedForReplacement(instance);
      return;
    }
    if (hasModelValidationErrors(errors)) return;
    const params = modelEditParams(instance.name, initial, draft, mode, seededFingerprint.current);
    if (params === null) return;
    await runModelEdit(params, {
      successToast: `Saved ${params.model}`,
      failurePrefix: "Save failed",
      createdModelId: mode === "create" ? params.model : undefined,
    });
  }

  async function handleResetAll(): Promise<void> {
    if (busy || writesRefused || loading || detail === null) return;
    if (renameGap) {
      onRefuseDuringRename();
      return;
    }
    if (draftIdentity(instance) !== seededIdentity.current) {
      reseedForReplacement(instance);
      return;
    }
    await runModelEdit(
      {
        name: instance.name,
        model: detail.model,
        resetAll: true,
        ...(seededFingerprint.current ? { expectedEndpointFingerprint: seededFingerprint.current } : {}),
      },
      {
        successToast: `Reset overrides for ${detail.model}`,
        failurePrefix: "Reset failed",
      },
    );
  }

  saveRef.current = handleSave;

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>): void {
    if (event.key === "Escape" && !event.defaultPrevented) {
      event.preventDefault();
      onBack();
    }
  }

  const activeModelId = mode === "edit" ? (detail?.model ?? model) : draft.model.trim();
  const listedModel = instance.models?.find((m) => m.id === activeModelId);
  const isDisabled = listedModel !== undefined ? Boolean(listedModel.disabled) : (detail?.disabled ?? false);
  const activeEffortValues = draft.effortValues ?? detail?.effective.effortValues ?? [];
  const activeModalities = draft.inputModalities ?? detail?.effective.inputModalities ?? ["text"];
  const showModelError = mode === "create" && (draft.model !== "" || submitAttempted) ? errors.model : undefined;
  const compactionThresholdId = `${ids}-compaction-threshold`;
  const contextWindowId = `${ids}-context-window`;
  const maxOutputTokensId = `${ids}-max-output-tokens`;
  const defaultEffortId = `${ids}-default-effort`;
  const toolsId = `${ids}-tools`;
  const protocolId = `${ids}-model-protocol`;
  const surfaceId = `${ids}-model-surface`;
  const thinkingFormatId = `${ids}-thinking-format`;
  const thinkingShapeId = `${ids}-thinking-shape`;
  const wireIdFieldId = `${ids}-wire-id`;
  const aliasOfFieldId = `${ids}-alias-of`;

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: subview container intercepts Escape to return to InstanceSheet overview without dismissing the parent Sheet
    <div ref={subviewRootRef} className={CLASS.subviewRoot} onKeyDown={handleKeyDown}>
      <div className={CLASS.subviewNav}>
        <Button ref={backButtonRef} variant="link" onClick={onBack}>
          {`← ${instance.name}`}
        </Button>
        {mode === "edit" && detail !== null && hasAuthoredOverrides(detail.authored) && (
          <Button variant="secondary" size="xs" disabled={busy || writesRefused} onClick={() => void handleResetAll()}>
            Reset all overrides
          </Button>
        )}
      </div>

      <h3>{mode === "create" ? "Add custom model" : activeModelId}</h3>

      {loading && <Skeleton />}

      {!loading && (mode === "create" || detail !== null) && (
        <form
          className={CLASS.form}
          aria-label={mode === "create" ? "Add custom model" : `Configure ${activeModelId}`}
          onSubmit={(event) => {
            event.preventDefault();
            void handleSave();
          }}
        >
          {detail?.warnings && detail.warnings.length > 0 && (
            <InlineNotice tone="neutral" live="off" data-reason="model-warnings">
              {detail.warnings.join("; ")}
            </InlineNotice>
          )}

          {mode === "create" && (
            <FormRow label="Model ID" htmlFor={modelIdFieldId} error={showModelError}>
              <Input
                id={modelIdFieldId}
                value={draft.model}
                onChange={(event) => update({ model: event.target.value })}
                disabled={busy}
                required
                aria-describedby={showModelError ? `${modelIdFieldId}-error` : undefined}
              />
            </FormRow>
          )}

          {mode === "edit" && detail !== null && (
            <Switch
              label="Enabled"
              checked={!isDisabled}
              pending={pendingToggles?.has(`${instance.name}/${detail.model}`) ?? false}
              disabled={busy || writesRefused}
              description={
                detail.disabledLockstepTarget
                  ? `Toggles with ${detail.disabledLockstepTarget} (alias target).`
                  : undefined
              }
              onChange={(checked) => (renameGap ? onRefuseDuringRename() : onToggleModel(detail.model, !checked))}
            />
          )}

          <ResettableInputRow
            id={compactionThresholdId}
            label="Auto-compact threshold"
            value={draft.compactionThreshold}
            initialValue={initial.compactionThreshold}
            placeholder={compactionThresholdPlaceholder(detail, draft.contextWindow)}
            error={errors.compactionThreshold}
            disabled={busy}
            onChange={(compactionThreshold) => update({ compactionThreshold })}
            onReset={() => update({ compactionThreshold: "" })}
          />

          <ResettableInputRow
            id={contextWindowId}
            label="Context window"
            value={draft.contextWindow}
            initialValue={initial.contextWindow}
            placeholder={tokenLimitPlaceholder(detail?.effective.contextWindow, detail?.provenance.ContextWindow)}
            error={errors.contextWindow}
            disabled={busy}
            onChange={(contextWindow) => update({ contextWindow })}
            onReset={() => update({ contextWindow: "" })}
          />

          <ResettableInputRow
            id={maxOutputTokensId}
            label="Max output tokens"
            value={draft.maxOutputTokens}
            initialValue={initial.maxOutputTokens}
            placeholder={tokenLimitPlaceholder(detail?.effective.maxOutputTokens, detail?.provenance.MaxOutputTokens)}
            error={errors.maxOutputTokens}
            disabled={busy}
            onChange={(maxOutputTokens) => update({ maxOutputTokens })}
            onReset={() => update({ maxOutputTokens: "" })}
          />

          <FormRow label="Reasoning">
            <SegmentedControl
              label="Reasoning"
              hideLabel
              value={draft.reasoning === null ? "" : draft.reasoning ? "true" : "false"}
              options={reasoningOptions(detail)}
              onChange={(value) => update({ reasoning: value === "" ? null : value === "true" })}
              disabled={busy}
            />
          </FormRow>

          <FormRow label="Default effort" htmlFor={defaultEffortId}>
            <Select
              id={defaultEffortId}
              value={draft.defaultEffort}
              options={defaultEffortOptions(detail)}
              onChange={(event) => update({ defaultEffort: event.target.value })}
              disabled={busy}
            />
          </FormRow>

          <FormRow label="Allowed effort levels" error={errors.effortValues}>
            <div className={CLASS.effortGroup}>
              {ALL_EFFORT_LEVELS.map((level) => {
                const selected = activeEffortValues.includes(level);
                return (
                  <Button
                    key={level}
                    size="xs"
                    variant={selected ? "primary" : "secondary"}
                    aria-pressed={selected}
                    disabled={busy}
                    onClick={() =>
                      update({
                        effortValues: toggleEffortValue(draft.effortValues, detail?.effective.effortValues, level),
                      })
                    }
                  >
                    {effortLabel(level, [...ALL_EFFORT_LEVELS])}
                  </Button>
                );
              })}
              {draft.effortValues !== null && (
                <Button variant="link" disabled={busy} onClick={() => update({ effortValues: null })}>
                  Reset
                </Button>
              )}
            </div>
          </FormRow>

          <FormRow label="Input modalities" error={errors.inputModalities}>
            <div className={CLASS.modalityGroup}>
              <Switch
                label="Image"
                checked={activeModalities.includes("image")}
                disabled={busy}
                onChange={(checked) =>
                  update({
                    inputModalities: toggleInputModality(
                      draft.inputModalities,
                      detail?.effective.inputModalities,
                      "image",
                      checked,
                    ),
                  })
                }
              />
              <Switch
                label="PDF"
                checked={activeModalities.includes("pdf")}
                disabled={busy}
                onChange={(checked) =>
                  update({
                    inputModalities: toggleInputModality(
                      draft.inputModalities,
                      detail?.effective.inputModalities,
                      "pdf",
                      checked,
                    ),
                  })
                }
              />
              {draft.inputModalities !== null && (
                <Button variant="link" disabled={busy} onClick={() => update({ inputModalities: null })}>
                  Reset
                </Button>
              )}
            </div>
          </FormRow>

          <FormRow label="Tool calling" htmlFor={toolsId}>
            <Select
              id={toolsId}
              value={draft.tools === null ? "" : draft.tools ? "true" : "false"}
              options={toolCallingOptions(detail)}
              onChange={(event) => update({ tools: event.target.value === "" ? null : event.target.value === "true" })}
              disabled={busy}
            />
          </FormRow>

          <Disclosure open={routingOpen} onOpenChange={setRoutingOpen} summary="Routing & wire overrides">
            <div className={CLASS.routingFields}>
              <FormRow label="Protocol" htmlFor={protocolId}>
                <Select
                  id={protocolId}
                  value={draft.protocol}
                  options={modelProtocolOptions(detail)}
                  onChange={(event) => update({ protocol: event.target.value })}
                  disabled={busy}
                />
              </FormRow>

              <FormRow label="Surface" htmlFor={surfaceId}>
                <Select
                  id={surfaceId}
                  value={draft.surface}
                  options={modelSurfaceOptions(detail)}
                  onChange={(event) => update({ surface: event.target.value })}
                  disabled={busy}
                />
              </FormRow>

              <FormRow label="Thinking format" htmlFor={thinkingFormatId}>
                <Select
                  id={thinkingFormatId}
                  value={draft.thinkingFormat}
                  options={thinkingFormatOptions(detail)}
                  onChange={(event) => update({ thinkingFormat: event.target.value })}
                  disabled={busy}
                />
              </FormRow>

              <FormRow label="Thinking shape" htmlFor={thinkingShapeId}>
                <Select
                  id={thinkingShapeId}
                  value={draft.thinkingShape}
                  options={thinkingShapeOptions(detail)}
                  onChange={(event) => update({ thinkingShape: event.target.value })}
                  disabled={busy}
                />
              </FormRow>

              <ResettableInputRow
                id={wireIdFieldId}
                label="Wire model ID"
                value={draft.wireId}
                initialValue={initial.wireId}
                placeholder={wireIdPlaceholder(detail)}
                disabled={busy}
                onChange={(wireId) => update({ wireId })}
                onReset={() => update({ wireId: "" })}
              />

              <ResettableInputRow
                id={aliasOfFieldId}
                label="Alias of"
                value={draft.aliasOf}
                initialValue={initial.aliasOf}
                placeholder={aliasOfPlaceholder(detail)}
                disabled={busy}
                onChange={(aliasOf) => update({ aliasOf })}
                onReset={() => update({ aliasOf: "" })}
              />
            </div>
          </Disclosure>
        </form>
      )}

      {formError !== null && (
        <InlineNotice tone="danger" data-reason={formError.reason}>
          {formError.message}
        </InlineNotice>
      )}

      {mode === "edit" && detail?.authoredOnly && (
        <>
          <hr className={CLASS.divider} />
          <div className={CLASS.fullRow}>
            <Button
              variant="dangerQuiet"
              disabled={busy || writesRefused}
              onClick={() =>
                renameGap ? onRefuseDuringRename() : onRequestRemoveCustom(detail.model, seededFingerprint.current)
              }
            >
              Remove custom model
            </Button>
          </div>
        </>
      )}
    </div>
  );
}
