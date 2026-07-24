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
// remember and type exactly.
import { type ReactNode, useId, useState } from "react";
import type { LaunchConfigLayer, LaunchConfigResolved, LaunchOption, MCPServerSpec } from "../../protocol/types.gen";
import type { ModelCatalog as ModelCatalogEnvelope } from "../../widgets";
import { Button, CollectionEditor, FormRow, Input, ModelCatalog, RadioGroup, Select } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./advancedOptions.module.css";
import { type AdvancedFieldValue, type AdvancedValues, collectAdvancedOverrides } from "./schema";

const BOOLEAN_DEFAULT = "(default)";

const CLASS = {
  root: requireClass(styles.root, "advancedOptions.module.css", "root"),
  modelBlock: requireClass(styles.modelBlock, "advancedOptions.module.css", "modelBlock"),
  modelLabel: requireClass(styles.modelLabel, "advancedOptions.module.css", "modelLabel"),
  modelHelp: requireClass(styles.modelHelp, "advancedOptions.module.css", "modelHelp"),
  modelAddRow: requireClass(styles.modelAddRow, "advancedOptions.module.css", "modelAddRow"),
  modelAddField: requireClass(styles.modelAddField, "advancedOptions.module.css", "modelAddField"),
  toggle: requireClass(styles.toggle, "advancedOptions.module.css", "toggle"),
  panel: requireClass(styles.panel, "advancedOptions.module.css", "panel"),
  resolved: requireClass(styles.resolved, "advancedOptions.module.css", "resolved"),
  error: requireClass(styles.error, "advancedOptions.module.css", "error"),
};

export interface AdvancedOptionsProps {
  /** Already filtered to perLaunch serf options (schema.perLaunchSerfOptions). */
  options: LaunchOption[];
  onOverridesChange: (overrides: LaunchConfigLayer) => void;
  validatePath: (path: string, kind: string) => Promise<{ valid: boolean; error?: string }>;
  resolveConfig: (overrides: LaunchConfigLayer) => Promise<LaunchConfigResolved>;
  /** Loads the model catalog for this panel's model-valued fields, scoped the
   * same way the top-level Model field is (harness + cwd). */
  loadCatalog: () => Promise<ModelCatalogEnvelope>;
  /** Rendered first inside the expanded panel, ahead of the schema controls
   * (9ct0: hosts the Access-mode field moved in from the top-level bar). */
  children?: ReactNode;
}

export function AdvancedOptions({
  options,
  onOverridesChange,
  validatePath,
  resolveConfig,
  loadCatalog,
  children,
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
      validatePath(value, opt.pathKind).then(
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
  onScalar: (value: string) => void;
  onValue: (field: AdvancedFieldValue) => void;
}

function Control({ option, value, error, loadCatalog, onScalar, onValue }: ControlProps) {
  const controlId = useId();
  const current = typeof value?.value === "string" ? value.value : "";

  switch (option.kind) {
    case "boolean":
      return (
        <FormRow label={option.label} htmlFor={controlId} help={option.description}>
          <Select
            id={controlId}
            value={current === "" ? BOOLEAN_DEFAULT : current}
            onChange={(e) => onScalar(e.target.value === BOOLEAN_DEFAULT ? "" : e.target.value)}
            options={[
              { value: BOOLEAN_DEFAULT, label: "(default)" },
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
              { value: "", label: "(default)" },
              ...(option.choices ?? []).map((c) => ({ value: c.value, label: c.label })),
            ]}
          />
        </FormRow>
      );
    case "integer":
      return (
        <FormRow label={option.label} htmlFor={controlId} help={option.description}>
          <Input id={controlId} type="number" value={current} onChange={(e) => onScalar(e.target.value)} />
        </FormRow>
      );
    case "modelPicker":
      // A composite widget, not a single labelable control, so the label is a
      // plain span (mirroring the spawn form's own Model field) - the picker's
      // inner combobox carries its own accessible name.
      return (
        <div className={CLASS.modelBlock}>
          <span className={CLASS.modelLabel}>{option.label}</span>
          <ModelCatalog value={current} onChange={onScalar} loadCatalog={loadCatalog} />
          {option.description && <p className={CLASS.modelHelp}>{option.description}</p>}
        </div>
      );
    case "modelList":
      return (
        <ModelListControl
          option={option}
          items={Array.isArray(value?.value) ? (value.value as string[]) : []}
          loadCatalog={loadCatalog}
          onValue={onValue}
        />
      );
    case "pathList":
      return (
        <ListControl
          option={option}
          items={Array.isArray(value?.value) ? (value.value as string[]) : []}
          onValue={onValue}
        />
      );
    case "envMap":
      return <EnvControl option={option} value={isRecord(value?.value) ? value.value : {}} onValue={onValue} />;
    case "mcpServerList":
      return <McpControl option={option} value={isMcpList(value?.value) ? value.value : []} onValue={onValue} />;
    default:
      // text - path-kind fields carry live validation surfaced through
      // FormRow's error slot.
      return (
        <FormRow label={option.label} htmlFor={controlId} help={option.description} error={error || undefined}>
          <Input id={controlId} value={current} onChange={(e) => onScalar(e.target.value)} />
        </FormRow>
      );
  }
}

function isRecord(v: unknown): v is Record<string, string> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function isMcpList(v: unknown): v is MCPServerSpec[] {
  return Array.isArray(v) && v.every((item) => typeof item === "object" && item !== null && "name" in item);
}

function ListControl({
  option,
  items,
  onValue,
}: {
  option: LaunchOption;
  items: string[];
  onValue: (field: AdvancedFieldValue) => void;
}) {
  return (
    <CollectionEditor<string>
      label={option.label}
      items={items}
      getKey={(item) => item}
      renderItem={(item) => item}
      removeLabel={(item) => `Remove ${item}`}
      onRemove={(item) => onValue({ value: items.filter((i) => i !== item) })}
      emptyMessage="None."
      addPlaceholder={option.description ?? "Add an entry"}
      onAdd={(entry) => {
        if (items.includes(entry)) return { ok: false, error: "Already added." };
        onValue({ value: [...items, entry] });
        return { ok: true };
      }}
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
  onValue,
}: {
  option: LaunchOption;
  items: string[];
  loadCatalog: () => Promise<ModelCatalogEnvelope>;
  onValue: (field: AdvancedFieldValue) => void;
}) {
  return (
    <CollectionEditor<string>
      label={option.label}
      items={items}
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
        <div className={CLASS.modelAddRow}>
          <span className={CLASS.modelAddField}>
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
  onValue,
}: {
  option: LaunchOption;
  value: Record<string, string>;
  onValue: (field: AdvancedFieldValue) => void;
}) {
  const entries = Object.entries(value);
  return (
    <CollectionEditor<[string, string]>
      label={option.label}
      items={entries}
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
  onValue,
}: {
  option: LaunchOption;
  value: MCPServerSpec[];
  onValue: (field: AdvancedFieldValue) => void;
}) {
  return (
    <CollectionEditor<MCPServerSpec>
      label={option.label}
      items={value}
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
