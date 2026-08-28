// The advanced launch-config panel (floor §1.11): renders the schema-driven,
// per-launch options as design-system controls, collects them into
// launchOverrides via schema.ts's collectAdvancedOverrides, validates path-kind
// inputs live, and previews the fully resolved config. Collapsed by default and
// wire-free (the parent injects validatePath/resolveConfig/loadCatalog closures
// over the appwire client). The pure collect/precedence logic is fully in
// schema.ts.
//
// Every model-valued field here renders the SAME searchable ModelCatalog
// picker as the top-level Model field - the modelPicker kind (model,
// fastCheapModel) and the modelList kind (modelFallbacks) both. They used to
// be plain free-text boxes, which made a model id something you had to
// remember and type exactly. Every browsable path-valued field (the path and
// pathList kinds) renders the shared PathField the same way, for the same
// reason.
import { type ReactNode, useId, useState } from "react";
import type { LaunchConfigLayer, LaunchConfigResolved, LaunchOption, MCPServerSpec } from "../../protocol/types.gen";
import type { ModelCatalog as ModelCatalogEnvelope, PathFieldKind } from "../../widgets";
import { Button, CollectionEditor, FormRow, Input, ModelCatalog, PathField, RadioGroup, Select } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { asEnvEntries, asMcpList, asStringList, inheritedItems } from "../settings/sections/launchShared/inherited";
import { type PathValidation, validatePathListAdd } from "../settings/sections/launchShared/pathListAdd";
import { schemaPathKind } from "../settings/sections/launchShared/schema";
import styles from "./advancedOptions.module.css";
import { type AdvancedFieldValue, type AdvancedValues, collectAdvancedOverrides } from "./schema";

const BOOLEAN_DEFAULT = "(default)";

const CLASS = {
  root: requireClass(styles.root, "advancedOptions.module.css", "root"),
  fieldBlock: requireClass(styles.fieldBlock, "advancedOptions.module.css", "fieldBlock"),
  fieldLabel: requireClass(styles.fieldLabel, "advancedOptions.module.css", "fieldLabel"),
  fieldHelp: requireClass(styles.fieldHelp, "advancedOptions.module.css", "fieldHelp"),
  pickerAddRow: requireClass(styles.pickerAddRow, "advancedOptions.module.css", "pickerAddRow"),
  pickerAddField: requireClass(styles.pickerAddField, "advancedOptions.module.css", "pickerAddField"),
  toggle: requireClass(styles.toggle, "advancedOptions.module.css", "toggle"),
  panel: requireClass(styles.panel, "advancedOptions.module.css", "panel"),
  resolved: requireClass(styles.resolved, "advancedOptions.module.css", "resolved"),
  error: requireClass(styles.error, "advancedOptions.module.css", "error"),
};

export interface AdvancedOptionsProps {
  /** Already filtered to perLaunch evener options (schema.perLaunchEvenerOptions). */
  options: LaunchOption[];
  onOverridesChange: (overrides: LaunchConfigLayer) => void;
  /** evener/path/validate. Both the scalar path fields' live validation and the
   * pathList add rows go through it; `path` (the server-canonicalized spelling)
   * is used by an add when the caller's closure forwards it. */
  validatePath: (path: string, kind: string) => Promise<PathValidation>;
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
  validatePath,
  resolveConfig,
  loadCatalog,
  complete,
  children,
  resolvedDefaults,
}: AdvancedOptionsProps) {
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<AdvancedValues>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [resolved, setResolved] = useState<string | null>(null);
  const [resolveError, setResolveError] = useState<string | null>(null);
  const panelId = useId();

  function update(wireField: string, field: AdvancedFieldValue): void {
    const next = { ...values, [wireField]: field };
    setValues(next);
    onOverridesChange(collectAdvancedOverrides(options, next));
  }

  function updateScalar(opt: LaunchOption, value: string): void {
    update(opt.wireField, { value });
    if (opt.pathKind && value.trim() !== "") {
      validatePath(value, schemaPathKind(opt.pathKind)).then(
        (result) => {
          setErrors((prev) => ({ ...prev, [opt.wireField]: result.valid ? "" : (result.error ?? "invalid path") }));
          // Re-mark the stored value invalid so collect drops it (floor §1.11).
          update(opt.wireField, { value, invalid: !result.valid });
        },
        () => {
          // A failing validator never blocks (fail-open), matching preflight.
          setErrors((prev) => ({ ...prev, [opt.wireField]: "" }));
        },
      );
    } else if (opt.pathKind) {
      setErrors((prev) => ({ ...prev, [opt.wireField]: "" }));
    }
  }

  async function showResolved(): Promise<void> {
    setResolveError(null);
    try {
      const result = await resolveConfig(collectAdvancedOverrides(options, values));
      setResolved(JSON.stringify(result.effective, null, 2));
    } catch (err) {
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
        <div id={panelId} className={CLASS.panel}>
          {children}
          {options.map((opt) => (
            <Control
              key={opt.wireField}
              option={opt}
              value={values[opt.wireField]}
              error={errors[opt.wireField]}
              loadCatalog={loadCatalog}
              complete={complete}
              validatePath={validatePath}
              resolvedDefaults={resolvedDefaults}
              onScalar={(v) => updateScalar(opt, v)}
              onValue={(field) => update(opt.wireField, field)}
            />
          ))}
          <div>
            <Button variant="quiet" size="sm" onClick={() => void showResolved()}>
              Show resolved config
            </Button>
          </div>
          {resolveError && (
            <p className={CLASS.error} role="alert">
              Couldn't resolve: {resolveError}
            </p>
          )}
          {resolved !== null && (
            // biome-ignore lint/a11y/useSemanticElements: a labeled group is the accessible-name host for the <pre> code dump; <pre> itself takes no aria-label
            <div role="group" aria-label="Resolved config">
              <pre className={CLASS.resolved}>{resolved}</pre>
            </div>
          )}
        </div>
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
  resolvedDefaults?: LaunchConfigLayer;
  onScalar: (value: string) => void;
  onValue: (field: AdvancedFieldValue) => void;
}

/** The effective layer's raw value for this field, or undefined when no layer
 * sets it (or the resolve hasn't landed) - the source for every "<value>
 * (default)" label below. */
function resolvedValue(option: LaunchOption, resolvedDefaults: LaunchConfigLayer | undefined): unknown {
  if (!resolvedDefaults) return undefined;
  return (resolvedDefaults as Record<string, unknown>)[option.wireField];
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

/** The longest a resolved default runs in a placeholder before it is clipped:
 * long free-text values (a system prompt) must not turn a placeholder into a
 * wall of text. */
const DEFAULT_PLACEHOLDER_MAX = 60;

/** The placeholder naming the resolved default for the input-based controls
 * (integer, text, and the browsable path widget): "<value> (default)", with
 * long strings clipped. "" when no layer sets the field - the inputs stay
 * bare then, exactly as they were before the resolved-default labels. */
function inputDefaultPlaceholder(option: LaunchOption, resolvedDefaults: LaunchConfigLayer | undefined): string {
  const value = resolvedValue(option, resolvedDefaults);
  if (typeof value === "number" && Number.isFinite(value)) {
    // max_rounds -1 and 0 both mean unlimited: the flag's own "unlimited"
    // sentinel (fs.Int("max-rounds", -1, "... 0=unlimited")), and the agent's
    // applyDefaults converts 0→-1. Keyed on the wire field: the spawn panel's
    // option objects carry it verbatim.
    if (option.wireField === "maxRounds" && (value === -1 || value === 0)) return "unlimited (default)";
    return `${value} (default)`;
  }
  if (typeof value === "string" && value.trim() !== "") {
    const trimmed = value.trim();
    const shown =
      trimmed.length > DEFAULT_PLACEHOLDER_MAX ? `${trimmed.slice(0, DEFAULT_PLACEHOLDER_MAX - 1)}…` : trimmed;
    return `${shown} (default)`;
  }
  return "";
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
      // A composite widget, not a single labelable control, so the label is a
      // plain span (mirroring the spawn form's own Model field) - the picker's
      // inner combobox carries its own accessible name.
      return (
        <div className={CLASS.fieldBlock}>
          <span className={CLASS.fieldLabel}>{option.label}</span>
          <ModelCatalog
            value={current}
            onChange={onScalar}
            loadCatalog={loadCatalog}
            emptyLabel={stringDefaultLabel(option, resolvedDefaults)}
          />
          {option.description && <p className={CLASS.fieldHelp}>{option.description}</p>}
        </div>
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
  const labelId = useId();
  return (
    <section className={CLASS.fieldBlock} aria-labelledby={labelId}>
      <span id={labelId} className={CLASS.fieldLabel}>
        {option.label}
      </span>
      {children}
      {option.description && <p className={CLASS.fieldHelp}>{option.description}</p>}
    </section>
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
  resolvedDefaults,
  onValue,
}: {
  option: LaunchOption;
  items: string[];
  complete: (prefix: string, includeFiles: boolean) => Promise<string[]>;
  validatePath: (path: string, kind: string) => Promise<PathValidation>;
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
              <div className={CLASS.pickerAddRow}>
                <span className={CLASS.pickerAddField}>
                  <PathField
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
                </span>
                <Button type="submit" variant="quiet" disabled={value.trim() === "" || disabled}>
                  Add
                </Button>
              </div>
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
      renderAddField={({ value, onChange, disabled }) => (
        <div className={CLASS.pickerAddRow}>
          <span className={CLASS.pickerAddField}>
            <ModelCatalog value={value} onChange={onChange} loadCatalog={loadCatalog} />
          </span>
          <Button type="submit" variant="quiet" disabled={value.trim() === "" || disabled}>
            Add
          </Button>
        </div>
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
