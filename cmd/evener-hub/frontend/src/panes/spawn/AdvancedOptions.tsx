// The advanced launch-config panel (floor §1.11): renders the schema-driven,
// per-launch options as design-system controls, collects them into
// launchOverrides via the package's collectAdvancedOverrides, validates path-kind
// inputs live, and previews the fully resolved config. Collapsed by default and
// wire-free (the parent injects validatePath/resolveConfig/loadCatalog closures
// over the appwire client). The pure collect/precedence logic is fully in
// the package's spawnSchema module.
//
// Every model-valued field here renders the SAME searchable ModelCatalog
// picker as the top-level Model field - the modelPicker kind (model,
// fastCheapModel) and the modelList kind (modelFallbacks) both. They used to
// be plain free-text boxes, which made a model id something you had to
// remember and type exactly. Every browsable path-valued field (the path and
// pathList kinds) renders the shared PathField the same way, for the same
// reason.

import {
  type AdvancedFieldValue,
  type AdvancedValues,
  asEnvEntries,
  asMcpList,
  asStringList,
  collectAdvancedOverrides,
  inheritedItems,
  inputDefaultPlaceholder,
  type LaunchConfigLayer,
  type LaunchConfigResolved,
  type LaunchOption,
  type MCPServerSpec,
  type PathValidation,
  resolvedValue,
  schemaPathKind,
  validatePathListAdd,
} from "@evener/appwire-client";
import { type Dispatch, type ReactNode, type SetStateAction, useId, useRef, useState } from "react";
import {
  Button,
  Card,
  CodeBlock,
  CollectionEditor,
  FormRow,
  InlineNotice,
  Input,
  ModelCatalog,
  type ModelCatalog as ModelCatalogEnvelope,
  PathField,
  type PathFieldKind,
  RadioGroup,
  Select,
} from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./advancedOptions.module.css";

const BOOLEAN_DEFAULT = "(default)";

const CLASS = {
  root: requireClass(styles.root, "advancedOptions.module.css", "root"),
  toggle: requireClass(styles.toggle, "advancedOptions.module.css", "toggle"),
  panel: requireClass(styles.panel, "advancedOptions.module.css", "panel"),
};

export interface AdvancedOptionsProps {
  /** Already filtered to perLaunch evener options (schema.perLaunchEvenerOptions). */
  options: LaunchOption[];
  onOverridesChange: (overrides: LaunchConfigLayer) => void;
  /** Optional draft-owned raw fields, including incomplete/invalid edits. */
  values?: AdvancedValues;
  onValuesChange?: (values: AdvancedValues) => void;
  /** Reads the originating draft after an asynchronous validation or remount. */
  readValues?: () => AdvancedValues;
  /** Optional draft-owned path-validation messages, keyed by wireField and
   * mirroring `values`/`onValuesChange`. A draft that owns them keeps its error
   * visible across an inactive spell or a remount; callers without a durable
   * draft fall back to component-local state. */
  errors?: Record<string, string>;
  onErrorsChange?: Dispatch<SetStateAction<Record<string, string>>>;
  /** Stable identity of the draft these values belong to. Async validation and
   * resolve results are dropped when it changes, but a same-draft remount -
   * which recreates readValues' own identity - must NOT drop them. Defaults to
   * readValues when absent, preserving callback-identity behavior for callers
   * that do not own a durable draft object. */
  draftId?: unknown;
  /** evener/path/validate. Both the scalar path fields' live validation and the
   * pathList add rows go through it; `path` (the server-canonicalized spelling)
   * is used by an add when the caller's closure forwards it. */
  validatePath: (path: string, kind: string) => Promise<PathValidation>;
  createDirectory: (path: string) => Promise<void>;
  resolveConfig: (overrides: LaunchConfigLayer) => Promise<LaunchConfigResolved>;
  /** Loads the model catalog for this panel's model-valued fields, scoped the
   * same way the top-level Model field is (harness + cwd). */
  loadCatalog: () => Promise<ModelCatalogEnvelope>;
  /** Path completions for this panel's browsable path fields (evener/paths/
   * complete). Each field's PathField derives includeFiles from its own kind. */
  complete: (prefix: string, includeFiles: boolean) => Promise<string[]>;
  /** Rendered first inside the expanded panel, ahead of the schema controls
   * (9ct0: hosts the Access-mode field moved in from the top-level bar). */
  children?: ReactNode;
  /** The effective layer of the pane's own launch/resolve for the current
   * cwd (undefined until it lands or after it fails): a control whose unset
   * state reads "(default)" prepends its entry here - "On (default)",
   * "high (default)", "openai/gpt-5 (default)" - so the empty marker names
   * what a session started now would actually inherit. */
  resolvedDefaults?: LaunchConfigLayer;
}

export function AdvancedOptions({
  options,
  onOverridesChange,
  values: draftValues,
  onValuesChange,
  readValues,
  errors: draftErrors,
  onErrorsChange,
  draftId,
  validatePath,
  createDirectory,
  resolveConfig,
  loadCatalog,
  complete,
  children,
  resolvedDefaults,
}: AdvancedOptionsProps) {
  const [open, setOpen] = useState(false);
  const [localValues, setLocalValues] = useState<AdvancedValues>({});
  const values = draftValues ?? localValues;
  const setValues = onValuesChange ?? setLocalValues;
  const valuesRef = useRef(values);
  valuesRef.current = values;
  // A draft's identity is durable (the draft object itself); readValues is a
  // callback the parent recreates whenever the draft changes - and, for the
  // same draft, whenever it remounts. Key ownership on the durable identity so
  // an in-flight result for the SAME draft is not dropped on remount, while a
  // different draft still resets feedback.
  const ownerKey = draftId !== undefined ? draftId : readValues;
  const activeOwner = useRef(ownerKey);
  activeOwner.current = ownerKey;
  // Validation messages are transient feedback for a caller that has no durable
  // draft to attach them to. When the draft owns them (Spawn passes a map), the
  // async result below writes to the ORIGINATING draft even while a different
  // draft is active - exactly as the `invalid` flag it explains already does.
  const [localErrors, setLocalErrors] = useState<Record<string, string>>({});
  const errors = draftErrors ?? localErrors;
  const setErrors = onErrorsChange ?? setLocalErrors;
  const draftOwnedErrors = onErrorsChange !== undefined;
  const [resolved, setResolved] = useState<string | null>(null);
  const [resolveError, setResolveError] = useState<string | null>(null);
  // useState(ownerKey) would treat a function ownerKey (readValues) as a lazy
  // initializer and CALL it, so the identity is carried inside a wrapper object.
  const [owner, setOwner] = useState({ key: ownerKey });
  // The draft identity identifies the project. Reset only transient feedback,
  // before rendering children; keep the disclosure and picker controls mounted.
  if (owner.key !== ownerKey) {
    setOwner({ key: ownerKey });
    // Only component-local feedback resets on a draft change: a draft-owned
    // error map travels with its draft, so the newly active draft shows its own.
    setLocalErrors({});
    setResolved(null);
    setResolveError(null);
  }
  const panelId = useId();

  function currentValues(): AdvancedValues {
    return readValues ? readValues() : valuesRef.current;
  }

  function update(wireField: string, field: AdvancedFieldValue): void {
    const next = { ...currentValues(), [wireField]: field };
    if (!readValues) valuesRef.current = next;
    setValues(next);
    onOverridesChange(collectAdvancedOverrides(options, next));
  }

  function updateScalar(opt: LaunchOption, value: string): void {
    const field = { value };
    update(opt.wireField, field);
    if (opt.pathKind && value.trim() !== "") {
      validatePath(value, schemaPathKind(opt.pathKind)).then(
        (result) => {
          // A later edit owns this field now, even if it returned to the same
          // text. Other fields may have changed too: update merges live state.
          if (currentValues()[opt.wireField] !== field) return;
          if (draftOwnedErrors || activeOwner.current === ownerKey) {
            setErrors((prev) => ({ ...prev, [opt.wireField]: result.valid ? "" : (result.error ?? "invalid path") }));
          }
          // Re-mark the stored value invalid so collect drops it (floor §1.11).
          update(opt.wireField, { value, invalid: !result.valid });
        },
        () => {
          // A failing validator never blocks (fail-open), matching preflight.
          if (currentValues()[opt.wireField] === field && (draftOwnedErrors || activeOwner.current === ownerKey)) {
            setErrors((prev) => ({ ...prev, [opt.wireField]: "" }));
          }
        },
      );
    } else if (opt.pathKind) {
      setErrors((prev) => ({ ...prev, [opt.wireField]: "" }));
    }
  }

  async function showResolved(): Promise<void> {
    setResolveError(null);
    try {
      // Resolve from the CURRENT draft values (readValues), not the render
      // snapshot: an edit merged by a just-settled async validation would
      // otherwise be missing from the preview.
      const result = await resolveConfig(collectAdvancedOverrides(options, currentValues()));
      if (activeOwner.current !== ownerKey) return;
      setResolved(JSON.stringify(result.effective, null, 2));
    } catch (err) {
      if (activeOwner.current !== ownerKey) return;
      setResolveError(err instanceof Error ? err.message : String(err));
    }
  }

  function toggle(): void {
    setOpen((prev) => !prev);
  }

  return (
    <section className={CLASS.root}>
      <button type="button" className={CLASS.toggle} aria-expanded={open} aria-controls={panelId} onClick={toggle}>
        Advanced options
      </button>
      {open && (
        <Card variant="outlined">
          <div id={panelId} className={CLASS.panel}>
            {children}
            {options.map((opt) => (
              <div key={opt.wireField} data-launch-field={opt.wireField}>
                <Control
                  option={opt}
                  value={values[opt.wireField]}
                  error={errors[opt.wireField]}
                  loadCatalog={loadCatalog}
                  complete={complete}
                  validatePath={validatePath}
                  createDirectory={createDirectory}
                  resolvedDefaults={resolvedDefaults}
                  onScalar={(v) => updateScalar(opt, v)}
                  onValue={(field) => update(opt.wireField, field)}
                />
              </div>
            ))}
            <div>
              <Button variant="quiet" size="sm" onClick={() => void showResolved()}>
                Show resolved config
              </Button>
            </div>
            {resolveError && <InlineNotice tone="danger">Couldn't resolve: {resolveError}</InlineNotice>}
            {resolved !== null && (
              // biome-ignore lint/a11y/useSemanticElements: a labeled group is the accessible-name host for the CodeBlock dump, which takes no aria-label of its own
              <div role="group" aria-label="Resolved config">
                <CodeBlock text={resolved} language="json" fold={false} />
              </div>
            )}
          </div>
        </Card>
      )}
    </section>
  );
}

interface ControlProps {
  option: LaunchOption;
  value: AdvancedFieldValue | undefined;
  error?: string;
  loadCatalog: () => Promise<ModelCatalogEnvelope>;
  complete: (prefix: string, includeFiles: boolean) => Promise<string[]>;
  /** Gates a pathList add (the scalar path kinds validate through onScalar). */
  validatePath: (path: string, kind: string) => Promise<PathValidation>;
  createDirectory: (path: string) => Promise<void>;
  resolvedDefaults?: LaunchConfigLayer;
  onScalar: (value: string) => void;
  onValue: (field: AdvancedFieldValue) => void;
}

/** The "<value> (default)" label for an unset string-valued control, or plain
 * "(default)" when the effective layer doesn't set the field. Falls back to
 * the schema's builtinDefaultLabel for a field whose default is dynamic
 * (fast_cheap_model → "primary model") — the resolve can't compute the
 * actual value, so the label names the answer in prose instead. */
function stringDefaultLabel(option: LaunchOption, resolvedDefaults: LaunchConfigLayer | undefined): string {
  const value = resolvedValue(option, resolvedDefaults);
  if (typeof value === "string" && value !== "") return `${value} (default)`;
  if (option.builtinDefaultLabel) return `${option.builtinDefaultLabel} (default)`;
  return "(default)";
}

/** The "<On|Off> (default)" label for an unset boolean control - the panel's
 * own On/Off wording, matching the set values' display labels. */
function booleanDefaultLabel(option: LaunchOption, resolvedDefaults: LaunchConfigLayer | undefined): string {
  const value = resolvedValue(option, resolvedDefaults);
  if (value === true) return "On (default)";
  if (value === false) return "Off (default)";
  return "(default)";
}

/** The schema's browsable path kinds, mapped onto the widget's. A "command"
 * pathKind names an executable to resolve on PATH and "" names no path at all,
 * so neither is browsable - those stay plain text boxes. */
function pathFieldKind(pathKind: string | undefined): PathFieldKind | null {
  switch (pathKind) {
    case "dir":
    case "file":
    case "outputFile":
      return pathKind;
    default:
      return null;
  }
}

function Control({
  option,
  value,
  error,
  loadCatalog,
  complete,
  validatePath,
  createDirectory,
  resolvedDefaults,
  onScalar,
  onValue,
}: ControlProps) {
  const controlId = useId();
  const current = typeof value?.value === "string" ? value.value : "";

  /** The plain text box: the text kind, plus any path field whose pathKind
   * isn't browsable. Path-kind fields carry live validation surfaced through
   * FormRow's error slot. */
  function textRow() {
    return (
      <FormRow label={option.label} htmlFor={controlId} help={option.description} error={error || undefined}>
        <Input
          id={controlId}
          value={current}
          onChange={(e) => onScalar(e.target.value)}
          placeholder={inputDefaultPlaceholder(option, resolvedDefaults) || undefined}
        />
      </FormRow>
    );
  }

  switch (option.kind) {
    case "boolean":
      return (
        <FormRow label={option.label} htmlFor={controlId} help={option.description}>
          <Select
            id={controlId}
            value={current === "" ? BOOLEAN_DEFAULT : current}
            onChange={(e) => onScalar(e.target.value === BOOLEAN_DEFAULT ? "" : e.target.value)}
            options={[
              { value: BOOLEAN_DEFAULT, label: booleanDefaultLabel(option, resolvedDefaults) },
              { value: "true", label: "On" },
              { value: "false", label: "Off" },
            ]}
          />
        </FormRow>
      );
    case "radio":
      return (
        <RadioGroup
          label={option.label}
          value={current}
          onChange={onScalar}
          options={(option.choices ?? []).map((c) => ({ value: c.value, label: c.label, disabled: c.disabled }))}
        />
      );
    case "select":
      return (
        <FormRow label={option.label} htmlFor={controlId} help={option.description}>
          <Select
            id={controlId}
            value={current}
            onChange={(e) => onScalar(e.target.value)}
            options={[
              // The panel owns the one empty option: the schema ships its own
              // value:"" choice for some fields (reasoning_effort,
              // context_strategy), which is dropped here rather than rendered
              // as a second, indistinguishable "(default)".
              { value: "", label: stringDefaultLabel(option, resolvedDefaults) },
              ...(option.choices ?? [])
                .filter((c) => (c.value ?? "") !== "")
                .map((c) => ({ value: c.value, label: c.label })),
            ]}
          />
        </FormRow>
      );
    case "integer":
      return (
        <FormRow label={option.label} htmlFor={controlId} help={option.description}>
          <Input
            id={controlId}
            type="number"
            value={current}
            onChange={(e) => onScalar(e.target.value)}
            placeholder={inputDefaultPlaceholder(option, resolvedDefaults) || undefined}
          />
        </FormRow>
      );
    case "path": {
      // A path field browses; only a non-browsable pathKind (a command name, or
      // none at all) stays a plain text box.
      const pathKind = pathFieldKind(option.pathKind);
      if (pathKind === null) return textRow();
      // PathField's trigger is a <button>, which FormRow's own <label htmlFor>
      // labels natively - so unlike the ModelCatalog rows this keeps the FormRow
      // shell, and with it the error slot carrying the submit-time
      // evener/path/validate failure.
      return (
        <FormRow label={option.label} htmlFor={controlId} help={option.description} error={error || undefined}>
          <PathField
            ariaLabel={option.label}
            directory={{ validatePath, createDirectory }}
            id={controlId}
            value={current}
            onChange={onScalar}
            kind={pathKind}
            complete={complete}
            // The trigger's empty face names the resolved default path the way
            // the selects' empty options do; undefined keeps its built-in
            // "(default)" when no layer sets the field.
            placeholder={inputDefaultPlaceholder(option, resolvedDefaults) || undefined}
          />
        </FormRow>
      );
    }
    case "modelPicker":
      // A composite widget, not a single labelable control, so the row is a
      // FormRow group rather than a <label htmlFor> - the picker's inner
      // combobox carries its own accessible name.
      return (
        <FormRow label={option.label} help={option.description || undefined}>
          <ModelCatalog
            value={current}
            onChange={onScalar}
            loadCatalog={loadCatalog}
            emptyLabel={stringDefaultLabel(option, resolvedDefaults)}
          />
        </FormRow>
      );
    case "modelList":
      return (
        <CollectionSection option={option}>
          <ModelListControl
            option={option}
            items={Array.isArray(value?.value) ? (value.value as string[]) : []}
            loadCatalog={loadCatalog}
            resolvedDefaults={resolvedDefaults}
            onValue={onValue}
          />
        </CollectionSection>
      );
    case "pathList":
      return (
        <CollectionSection option={option}>
          <PathListControl
            option={option}
            items={Array.isArray(value?.value) ? (value.value as string[]) : []}
            complete={complete}
            validatePath={validatePath}
            createDirectory={createDirectory}
            resolvedDefaults={resolvedDefaults}
            onValue={onValue}
          />
        </CollectionSection>
      );
    case "envMap":
      return (
        <CollectionSection option={option}>
          <EnvControl
            option={option}
            value={isRecord(value?.value) ? value.value : {}}
            resolvedDefaults={resolvedDefaults}
            onValue={onValue}
          />
        </CollectionSection>
      );
    case "mcpServerList":
      return (
        <CollectionSection option={option}>
          <McpControl
            option={option}
            value={isMcpList(value?.value) ? value.value : []}
            resolvedDefaults={resolvedDefaults}
            onValue={onValue}
          />
        </CollectionSection>
      );
    default:
      return textRow();
  }
}

function CollectionSection({ option, children }: { option: LaunchOption; children: ReactNode }) {
  return (
    <FormRow label={option.label} help={option.description || undefined}>
      {children}
    </FormRow>
  );
}

function isRecord(v: unknown): v is Record<string, string> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function isMcpList(v: unknown): v is MCPServerSpec[] {
  return Array.isArray(v) && v.every((item) => typeof item === "object" && item !== null && "name" in item);
}

/**
 * skillsDirs/pluginDirs/mcpConfigs: adds come from the same browse widget every
 * other path field uses rather than a hand-typed path. The picked path lands in
 * CollectionEditor's own draft, which the Add button submits - and the submit
 * goes through the SAME shared validatePathListAdd decision the settings-side
 * pathList field uses (dedupe, then evener/path/validate). These are the same
 * wire fields reaching the same daemon from either surface, so a path added
 * here is gated exactly the way one added in Settings is.
 */
function PathListControl({
  option,
  items,
  complete,
  validatePath,
  createDirectory,
  resolvedDefaults,
  onValue,
}: {
  option: LaunchOption;
  items: string[];
  complete: (prefix: string, includeFiles: boolean) => Promise<string[]>;
  validatePath: (path: string, kind: string) => Promise<PathValidation>;
  createDirectory: (path: string) => Promise<void>;
  resolvedDefaults: LaunchConfigLayer | undefined;
  onValue: (field: AdvancedFieldValue) => void;
}) {
  const pathKind = pathFieldKind(option.pathKind);
  return (
    <CollectionEditor<string>
      label={option.label}
      items={items}
      inheritedItems={inheritedItems(resolvedValue(option, resolvedDefaults), items, (item) => item, asStringList)}
      getKey={(item) => item}
      renderItem={(item) => item}
      removeLabel={(item) => `Remove ${item}`}
      onRemove={(item) => onValue({ value: items.filter((i) => i !== item) })}
      emptyMessage="None."
      addPlaceholder={option.description ?? "Add an entry"}
      onAdd={async (entry) => {
        const outcome = await validatePathListAdd(option, items, entry, validatePath);
        if (!outcome.ok) return { ok: false, error: outcome.error };
        onValue({ value: [...items, outcome.value] });
        return { ok: true };
      }}
      // A non-browsable pathKind keeps the built-in plain-text add field.
      renderAddField={
        pathKind === null
          ? undefined
          : ({ value, onChange, disabled }) => (
              <PathField
                directory={{ validatePath, createDirectory }}
                value={value}
                onChange={onChange}
                kind={pathKind}
                complete={complete}
                disabled={disabled}
                // The empty-state face, standing in for the trigger's
                // default "(default)": an add row has no default.
                placeholder="Add a path"
                // Names the trigger after the list it feeds. CollectionEditor
                // renders no label of its own in renderAddField mode, and all
                // three pathList options sit in one panel, so the placeholder
                // alone would name three indistinguishable buttons - the same
                // reason collectionFields.tsx's PathAddField passes it.
                ariaLabel={option.label}
              />
            )
      }
    />
  );
}

/**
 * modelFallbacks (and any future modelList field): the ordered fallback list,
 * with adds coming from the same searchable picker as every other model field
 * rather than a hand-typed "provider/model". The picked id lands in
 * CollectionEditor's own draft, which the Add button submits.
 */
function ModelListControl({
  option,
  items,
  loadCatalog,
  resolvedDefaults,
  onValue,
}: {
  option: LaunchOption;
  items: string[];
  loadCatalog: () => Promise<ModelCatalogEnvelope>;
  resolvedDefaults: LaunchConfigLayer | undefined;
  onValue: (field: AdvancedFieldValue) => void;
}) {
  return (
    <CollectionEditor<string>
      label={option.label}
      items={items}
      inheritedItems={inheritedItems(resolvedValue(option, resolvedDefaults), items, (item) => item, asStringList)}
      getKey={(item) => item}
      renderItem={(item) => item}
      removeLabel={(item) => `Remove ${item}`}
      onRemove={(item) => onValue({ value: items.filter((i) => i !== item) })}
      emptyMessage="None."
      onAdd={(entry) => {
        if (items.includes(entry)) return { ok: false, error: "Already added." };
        onValue({ value: [...items, entry] });
        return { ok: true };
      }}
      renderAddField={({ value, onChange }) => (
        <ModelCatalog value={value} onChange={onChange} loadCatalog={loadCatalog} />
      )}
    />
  );
}

function EnvControl({
  option,
  value,
  resolvedDefaults,
  onValue,
}: {
  option: LaunchOption;
  value: Record<string, string>;
  resolvedDefaults: LaunchConfigLayer | undefined;
  onValue: (field: AdvancedFieldValue) => void;
}) {
  const entries = Object.entries(value);
  return (
    <CollectionEditor<[string, string]>
      label={option.label}
      items={entries}
      inheritedItems={inheritedItems(resolvedValue(option, resolvedDefaults), entries, ([name]) => name, asEnvEntries)}
      getKey={([name]) => name}
      renderItem={([name, val]) => `${name}=${val}`}
      removeLabel={([name]) => `Remove ${name}`}
      onRemove={([name]) => {
        const next = { ...value };
        delete next[name];
        onValue({ value: next });
      }}
      emptyMessage="None."
      addPlaceholder="NAME=value"
      onAdd={(entry) => {
        const eq = entry.indexOf("=");
        if (eq <= 0) return { ok: false, error: "Use NAME=value." };
        const name = entry.slice(0, eq).trim();
        onValue({ value: { ...value, [name]: entry.slice(eq + 1) } });
        return { ok: true };
      }}
    />
  );
}

function McpControl({
  option,
  value,
  resolvedDefaults,
  onValue,
}: {
  option: LaunchOption;
  value: MCPServerSpec[];
  resolvedDefaults: LaunchConfigLayer | undefined;
  onValue: (field: AdvancedFieldValue) => void;
}) {
  return (
    <CollectionEditor<MCPServerSpec>
      label={option.label}
      items={value}
      inheritedItems={inheritedItems(resolvedValue(option, resolvedDefaults), value, (spec) => spec.name, asMcpList)}
      getKey={(spec) => spec.name}
      renderItem={(spec) => `${spec.name}: ${spec.command} ${(spec.args ?? []).join(" ")}`.trim()}
      removeLabel={(spec) => `Remove ${spec.name}`}
      onRemove={(spec) => onValue({ value: value.filter((s) => s.name !== spec.name) })}
      emptyMessage="None."
      addPlaceholder="name=command arg1 arg2"
      onAdd={(entry) => {
        const eq = entry.indexOf("=");
        if (eq <= 0) return { ok: false, error: "Use name=command args." };
        const name = entry.slice(0, eq).trim();
        const parts = entry
          .slice(eq + 1)
          .trim()
          .split(/\s+/);
        const command = parts[0] ?? "";
        if (command === "") return { ok: false, error: "A command is required." };
        if (value.some((s) => s.name === name)) return { ok: false, error: "Already added." };
        onValue({ value: [...value, { name, command, args: parts.slice(1) }] });
        return { ok: true };
      }}
    />
  );
}
