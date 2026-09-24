// LaunchConfigForm is the top-level rendering half of the LaunchConfigControls
// port (Appendix B): given an already-loaded schema/current layer, it owns
// the in-memory LaunchFormState, dispatches each supported option to its
// field renderer (fields.tsx / collectionFields.tsx), and runs the
// validate -> collect -> save sequence + the shared "Saved at {time}" /
// "Error: {message}" status-line behavior (self-clears after 5s unless it's
// an Error) used identically by both launchServer.tsx and project.tsx.
//
// Deliberately does NOT own: the initial schema()/getLayer() load sequence
// (2-state vs 3-state loading contract differs between the 2 pages - see
// launchServer.tsx/project.tsx), or the diagnostics panel (launchServer-only,
// rendered from resolve()/setLayer()'s own returned diagnostics, which this
// component exposes via onSaved rather than rendering itself).
//
// It DOES own the host boundary: `current` and `onSave` both describe the host
// selected now, and this form is handed a new host - or a re-registered one -
// without being unmounted, so the draft is reseeded whenever the per-host store
// INSTANCE behind it changes (see `draftOwner` and the seeding below).

import {
  asEnvObjects,
  asMcpList,
  asStringList,
  buildFormState,
  collectConfig,
  globalDefaultHint,
  groupOptions,
  inactivePromptDependent,
  inheritedItems,
  isCollectionKind,
  isPromptCompositeWireField,
  type LaunchConfigLayer,
  type LaunchConfigLayerName,
  type LaunchConfigResolved,
  type LaunchFormState,
  type LaunchOption,
  type MCPServerSpec,
  matchesEnvCredentialError,
  optionSupportsLayer,
  type PathValidateResponse,
  PROMPT_COMPOSITE_SPECS,
  PROMPT_DEPENDENT_WIRE_FIELDS,
  schemaPathKind,
} from "@evener/appwire-client";
import { useEffect, useMemo, useRef, useState } from "react";
import { Button, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { EnvMapField, McpServerListField, ModelListField, PathListField } from "./collectionFields";
import { type LaunchFormPaths, PromptCompositeField, ScalarField } from "./fields";
import styles from "./LaunchConfigForm.module.css";

const CLASS = {
  root: requireClass(styles.root, "LaunchConfigForm.module.css", "root"),
  group: requireClass(styles.group, "LaunchConfigForm.module.css", "group"),
  groupHeader: requireClass(styles.groupHeader, "LaunchConfigForm.module.css", "groupHeader"),
  field: requireClass(styles.field, "LaunchConfigForm.module.css", "field"),
  actions: requireClass(styles.actions, "LaunchConfigForm.module.css", "actions"),
  status: requireClass(styles.status, "LaunchConfigForm.module.css", "status"),
};

const STATUS_CLEAR_MS = 5000;

export interface LaunchConfigFormProps {
  options: LaunchOption[];
  layer: LaunchConfigLayerName;
  current: LaunchConfigLayer;
  /** The (separately-fetched) global layer, project-layer callers only -
   * drives the inline "default: {value}" hints. */
  globalDefaults?: LaunchConfigLayer;
  /** The effective layer of the caller's launch/resolve for this cwd
   * (undefined until it lands or after it fails): unset fields whose empty
   * marker is the generic one prepend their entry here - "high (default)",
   * "true (use global default)". */
  resolvedDefaults?: LaunchConfigLayer;
  successToast: string;
  validatePath: (path: string, kind: string) => Promise<PathValidateResponse>;
  /** The browse-assisted path fields' helpers for the host whose layer is being
   * edited (component 07b). A host-scoped pane passes the store bound to its
   * selected host; omitted = the controller's own helpers (today's behavior). */
  paths?: LaunchFormPaths;
  /** The host whose own model catalog the modelPicker/modelList fields offer
   * (component 07b). Omitted = the local hub, so a direct render is today's. */
  host?: string;
  /** The per-host store instance the parent resolved for this host. The form's
   * draft BELONGS to that instance, not to the host's name: a host removed and
   * re-added under the same name is a different registration, whose instance
   * (stores/launchConfig.ts's hostEntry) carries a different `current`,
   * `onSave` and `paths`, so a draft typed for the old registration is reseeded
   * rather than written to the new one. A caller with no instance to hand over
   * (a direct render, a test) keeps the host name as the owner. */
  draftOwner?: object;
  onSave: (config: LaunchConfigLayer) => Promise<LaunchConfigResolved>;
  onSaved?: (resolved: LaunchConfigResolved) => void;
}

function formatSavedAt(): string {
  return `Saved at ${new Date().toLocaleTimeString()}`;
}

/** Reads the effective layer's value for a field, or undefined when no layer
 * sets it (or the resolve hasn't landed). */
function resolvedValue(option: LaunchOption, resolvedDefaults: LaunchConfigLayer | undefined): unknown {
  if (!resolvedDefaults) return undefined;
  return (resolvedDefaults as Record<string, unknown>)[option.wireField];
}

export function LaunchConfigForm({
  options,
  layer,
  current,
  globalDefaults,
  resolvedDefaults,
  successToast,
  validatePath,
  paths,
  host,
  draftOwner,
  onSave,
  onSaved,
}: LaunchConfigFormProps) {
  const supportedOptions = useMemo(() => options.filter((opt) => optionSupportsLayer(opt, layer)), [options, layer]);
  const [state, setState] = useState<LaunchFormState>(() => buildFormState(supportedOptions, current));
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});

  // The draft belongs to ONE per-host store instance. A host-scoped parent
  // hands this form a different instance - with it a different `current`, a
  // different `onSave` and a different `paths` - while it stays mounted (the
  // frame above a real pane remounts the body on a switch, and this form's own
  // contract has to hold on its own too), and a re-registration under the same
  // NAME does the same: stores/launchConfig.ts builds a new instance for the new
  // registration. A draft seeded once would then be shown as the new
  // registration's settings and submitted to it by Save.
  //
  // Reseeded DURING render rather than from an effect, which is what makes the
  // commit that carries the new owner the first one that renders it: an effect
  // lands after that commit is painted, leaving a frame in which the old
  // registration's values stand under the new one. Reset on the OWNER and not on
  // `current`: a same-host re-read (a refetch, a reconnect) brings a
  // referentially new `current` from the SAME instance and must not throw away
  // what is being typed. The host NAME alone is not the owner either - it is
  // unchanged across a re-registration - so a caller that has the instance hands
  // it over, and one that does not keeps today's name-keyed behavior.
  const owner: object | string | undefined = draftOwner ?? host;
  const [seededOwner, setSeededOwner] = useState<object | string | undefined>(owner);
  const [status, setStatusText] = useState("");
  const [busy, setBusy] = useState(false);
  const clearTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const toast = useToasts();
  if (seededOwner !== owner) {
    setSeededOwner(owner);
    setState(buildFormState(supportedOptions, current));
    setFieldErrors({});
    setStatusText("");
  }

  // The draft swap above drops the status line, so the self-clear timer it armed
  // has nothing left to clear - and this is where that teardown lives, not in
  // the render-phase reseed. A render is not a place for a side effect: React
  // double-invokes it under StrictMode and may discard a pass outright under
  // concurrent rendering, so a render-phase clearTimeout can cancel a timer for
  // a reseed no commit ever carried out. An effect's cleanup also runs on the
  // edge a render can never reach - the form going away - which is what keeps a
  // save's timer from outliving the form. It runs exactly once per owner change.
  // biome-ignore lint/correctness/useExhaustiveDependencies: owner is the trigger, not a read - the cleanup it gates clears a timer armed by the owner before it
  useEffect(() => () => clearTimeout(clearTimerRef.current), [owner]);

  function setStatus(text: string): void {
    clearTimeout(clearTimerRef.current);
    setStatusText(text);
    if (text && !text.startsWith("Error:")) {
      clearTimerRef.current = setTimeout(() => {
        setStatusText((prev) => (prev === text ? "" : prev));
      }, STATUS_CLEAR_MS);
    }
  }

  function updateScalar(wireField: string, value: string): void {
    setState((s) => ({ ...s, scalars: { ...s.scalars, [wireField]: value } }));
  }
  function updateList(wireField: string, values: string[]): void {
    setState((s) => ({ ...s, lists: { ...s.lists, [wireField]: values } }));
  }
  function updateExplicitEmpty(wireField: string, checked: boolean): void {
    setState((s) => ({ ...s, explicitEmpty: { ...s.explicitEmpty, [wireField]: checked } }));
  }
  function updateEnvMap(wireField: string, value: Record<string, string>): void {
    setState((s) => ({ ...s, envMaps: { ...s.envMaps, [wireField]: value } }));
  }
  function updateMcpList(wireField: string, items: MCPServerSpec[]): void {
    setState((s) => ({ ...s, mcpLists: { ...s.mcpLists, [wireField]: items } }));
  }

  // validateScalarPathFields mirrors assets/launchconfig.js's validate()
  // item (1): every path-kind scalar input not currently inactive due to
  // prompt-mode. pathList/mcpServerList entries are validated once, at
  // add-time, in their own field components instead of re-validated here -
  // a deliberate scope simplification from the legacy's own defensive
  // re-validation pass (see this task's own report).
  async function validateScalarPathFields(): Promise<boolean> {
    const modes = {
      systemPromptMode: state.scalars.systemPromptMode,
      systemPromptAppendMode: state.scalars.systemPromptAppendMode,
    };
    const errors: Record<string, string> = {};
    for (const opt of supportedOptions) {
      if (opt.kind !== "path" || !opt.pathKind) continue;
      if (inactivePromptDependent(opt.wireField, modes)) continue;
      const raw = (state.scalars[opt.wireField] ?? "").trim();
      if (!raw) continue;
      const result = await validatePath(raw, schemaPathKind(opt.pathKind));
      if (!result.valid) errors[opt.wireField] = result.error || "invalid path";
    }
    setFieldErrors(errors);
    return Object.keys(errors).length === 0;
  }

  async function handleSubmit(): Promise<void> {
    if (busy) return;
    const valid = await validateScalarPathFields();
    if (!valid) return; // matches the legacy's own "returns early, no save attempted" - no status-line change
    setBusy(true);
    try {
      const resolved = await onSave(collectConfig(supportedOptions, state));
      setStatus(formatSavedAt());
      toast.push("success", successToast);
      onSaved?.(resolved);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      if (matchesEnvCredentialError(message) && supportedOptions.some((o) => o.wireField === "env")) {
        setFieldErrors((e) => ({ ...e, env: message }));
      }
      setStatus(`Error: ${message}`);
      toast.push("error", "Save failed");
    } finally {
      setBusy(false);
    }
  }

  function renderOption(opt: LaunchOption) {
    if (isPromptCompositeWireField(opt.wireField)) {
      const spec = PROMPT_COMPOSITE_SPECS[opt.wireField];
      if (!spec) return null;
      return (
        <PromptCompositeField
          option={opt}
          layer={layer}
          modeValue={state.scalars[opt.wireField] ?? ""}
          fileValue={state.scalars[spec.fileWire] ?? ""}
          textValue={state.scalars[spec.textWire] ?? ""}
          onModeChange={(v) => updateScalar(opt.wireField, v)}
          onFileChange={(v) => updateScalar(spec.fileWire, v)}
          onTextChange={(v) => updateScalar(spec.textWire, v)}
          fileGlobalDefaultHint={globalDefaultHint(spec.fileWire, layer, globalDefaults)}
          textGlobalDefaultHint={globalDefaultHint(spec.textWire, layer, globalDefaults)}
          fileError={fieldErrors[spec.fileWire]}
          paths={paths}
        />
      );
    }

    if (isCollectionKind(opt.kind)) {
      const effective = resolvedValue(opt, resolvedDefaults);
      switch (opt.kind) {
        case "pathList":
          return (
            <PathListField
              option={opt}
              items={state.lists[opt.wireField] ?? []}
              onChange={(v) => updateList(opt.wireField, v)}
              validatePath={validatePath}
              paths={paths}
              inheritedItems={inheritedItems(effective, state.lists[opt.wireField] ?? [], (s) => s, asStringList)}
            />
          );
        case "modelList":
          return (
            <ModelListField
              option={opt}
              host={host}
              items={state.lists[opt.wireField] ?? []}
              onChange={(v) => updateList(opt.wireField, v)}
              explicitEmpty={state.explicitEmpty[opt.wireField] ?? false}
              onExplicitEmptyChange={(checked) => updateExplicitEmpty(opt.wireField, checked)}
              inheritedItems={inheritedItems(effective, state.lists[opt.wireField] ?? [], (s) => s, asStringList)}
            />
          );
        case "envMap": {
          const localEnv = state.envMaps[opt.wireField] ?? {};
          const localEntries = Object.entries(localEnv).map(([name, value]) => ({ name, value }));
          return (
            <EnvMapField
              option={opt}
              value={localEnv}
              onChange={(v) => updateEnvMap(opt.wireField, v)}
              inheritedItems={inheritedItems(effective, localEntries, (e) => e.name, asEnvObjects)}
            />
          );
        }
        default: // mcpServerList
          return (
            <McpServerListField
              option={opt}
              items={state.mcpLists[opt.wireField] ?? []}
              onChange={(v) => updateMcpList(opt.wireField, v)}
              validateCommand={(command) => validatePath(command, "command")}
              inheritedItems={inheritedItems(effective, state.mcpLists[opt.wireField] ?? [], (s) => s.name, asMcpList)}
            />
          );
      }
    }

    return (
      <ScalarField
        option={opt}
        layer={layer}
        value={state.scalars[opt.wireField] ?? ""}
        onChange={(v) => updateScalar(opt.wireField, v)}
        globalDefaultHint={globalDefaultHint(opt.wireField, layer, globalDefaults)}
        error={fieldErrors[opt.wireField]}
        resolvedDefaults={resolvedDefaults}
        paths={paths}
        host={host}
      />
    );
  }

  const renderable = supportedOptions.filter((opt) => !PROMPT_DEPENDENT_WIRE_FIELDS.has(opt.wireField));
  const groups = groupOptions(renderable);

  return (
    // A plain div, not a <form>: every pathList/modelList/envMap/
    // mcpServerList field renders its own CollectionEditor, which is
    // itself a <form> (so Enter-to-add works on its own add-row) - nesting
    // this component's Save action inside a SECOND, outer <form> is invalid
    // HTML (a real bug this task's own tests caught via React's dev-mode
    // warning) and risks a stray Enter keypress in one collection's add
    // field bubbling into a submit of the whole page. Save is therefore a
    // plain button click, not a form submission.
    //
    // Unremarked side effect of that fix, worth being explicit about: since
    // this root is no longer a <form> at all, pressing Enter in a plain
    // scalar text/integer field (Agent, Base URL, a system-prompt path, ...)
    // no longer submits the whole page the way a native <form> would - the
    // legacy engine got that for free from the browser; this port doesn't
    // reintroduce it. The Save button remains reachable by keyboard
    // (Tab-then-Enter/Space on the button itself) - only the "Enter from
    // any field submits" shortcut is gone.
    <div className={CLASS.root}>
      {groups.map((group, index) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: group.group repeats when the legacy header-per-change rule re-opens the same name non-contiguously (see groupOptions) - the segment's position is the only stable identity.
        <div key={`${group.group}-${index}`} className={CLASS.group}>
          <div className={CLASS.groupHeader}>{group.group}</div>
          {group.options.map((opt) => (
            <div key={opt.field} className={CLASS.field}>
              {renderOption(opt)}
            </div>
          ))}
        </div>
      ))}
      <div className={CLASS.actions}>
        <Button type="button" onClick={() => void handleSubmit()} disabled={busy}>
          Save launch defaults
        </Button>
        <p className={CLASS.status} aria-live="polite">
          {status}
        </p>
      </div>
    </div>
  );
}
