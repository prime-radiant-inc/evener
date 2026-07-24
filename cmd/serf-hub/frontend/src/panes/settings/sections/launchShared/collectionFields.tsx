// collectionFields.tsx renders the 4 collection LaunchOption kinds (pathList/
// modelList/envMap/mcpServerList - Appendix B), each a CollectionEditor
// (widgets/collectioneditor) parameterized by its own add-value parsing/
// validation. pathList/modelList use CollectionEditor's own default plain-
// text add field; envMap/mcpServerList each need more than one sub-value per
// row, so they use CollectionEditor's renderAddField slot instead - two
// DIFFERENT shapes, not one:
//   - envMap: a real structured NAME/value pair (EnvAddFields below) - two
//     boxes composed into CollectionEditor's own single `draft` string via
//     one code-owned "=" join point (never a user-typed delimiter), so a
//     value containing "=" round-trips with no parsing ambiguity at all.
//   - mcpServerList ("name command args..."): still one delimited field, not
//     structured - the legacy's own 3 separate inputs (name/command/args)
//     are themselves just concatenated back into one command line by the
//     user's own shell-argument mental model, so a single field asks for the
//     exact same information in one line instead of three boxes; this is
//     expressiveness-equivalent, not a step-down, so restructuring it into
//     boxes the way envMap's own fields were restructured would add surface
//     without fixing anything.
//
// Also out of scope here (and in fields.tsx's own scalar path-kind
// rendering): the legacy engine's Constraint Validation API integration
// (validatePathInput/validateMCPCommandInput's setCustomValidity() +
// reportValidity() calls, which additionally surface the browser's own
// native validation-bubble UI alongside the custom inline error this port
// already shows). Not ported - the custom inline error is the only
// validation UI surfaced here.

import { type ReactNode, useId } from "react";
import type { LaunchOption, MCPServerSpec, PathValidateResponse } from "../../../../protocol/types.gen";
import { extensionsStore } from "../../../../stores/extensions";
import type { CollectionAddResult, PathFieldKind } from "../../../../widgets";
import { Button, CollectionEditor, Input, ModelCatalog, PathField, Switch } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { fetchModelCatalog } from "../../../../widgets/modelCatalog/catalogClient";
import styles from "./collectionFields.module.css";
import { schemaPathKind } from "./schema";

const CLASS = {
  section: requireClass(styles.section, "collectionFields.module.css", "section"),
  header: requireClass(styles.header, "collectionFields.module.css", "header"),
  label: requireClass(styles.label, "collectionFields.module.css", "label"),
  help: requireClass(styles.help, "collectionFields.module.css", "help"),
  envRow: requireClass(styles.envRow, "collectionFields.module.css", "envRow"),
  envField: requireClass(styles.envField, "collectionFields.module.css", "envField"),
  modelAddRow: requireClass(styles.modelAddRow, "collectionFields.module.css", "modelAddRow"),
  modelAddField: requireClass(styles.modelAddField, "collectionFields.module.css", "modelAddField"),
  pathAddRow: requireClass(styles.pathAddRow, "collectionFields.module.css", "pathAddRow"),
  pathAddField: requireClass(styles.pathAddField, "collectionFields.module.css", "pathAddField"),
  visuallyHidden: requireClass(styles.visuallyHidden, "collectionFields.module.css", "visuallyHidden"),
};

function SectionHeader({ label, help }: { label: string; help?: string }) {
  return (
    <div className={CLASS.header}>
      <span className={CLASS.label}>{label}</span>
      {help && <p className={CLASS.help}>{help}</p>}
    </div>
  );
}

// --- pathList / modelList: both a plain string[] behind CollectionEditor ---

interface StringListFieldProps {
  option: LaunchOption;
  items: string[];
  onChange: (items: string[]) => void;
  addPlaceholder: string;
  emptyMessage: string;
  validateAdd: (trimmed: string) => Promise<{ ok: true; value: string } | { ok: false; error: string }>;
  explicitEmpty?: { checked: boolean; onChange: (checked: boolean) => void; label: string };
  /** Replaces CollectionEditor's plain-text add field (modelList uses the
   * searchable model picker, so a model id is never hand-typed). */
  renderAddField?: (props: { value: string; onChange: (value: string) => void; disabled: boolean }) => ReactNode;
}

function StringListField({
  option,
  items,
  onChange,
  addPlaceholder,
  emptyMessage,
  validateAdd,
  explicitEmpty,
  renderAddField,
}: StringListFieldProps) {
  async function handleAdd(trimmed: string): Promise<CollectionAddResult> {
    const outcome = await validateAdd(trimmed);
    if (!outcome.ok) return { ok: false, error: outcome.error };
    onChange([...items, outcome.value]);
    if (explicitEmpty?.checked) explicitEmpty.onChange(false);
    return { ok: true };
  }

  return (
    <div className={CLASS.section}>
      <SectionHeader label={option.label} help={option.description} />
      <CollectionEditor<string>
        label={option.label}
        items={items}
        getKey={(item) => item}
        renderItem={(item) => item}
        removeLabel={(item) => `Remove ${item}`}
        onRemove={(item) => onChange(items.filter((i) => i !== item))}
        emptyMessage={emptyMessage}
        addPlaceholder={addPlaceholder}
        onAdd={handleAdd}
        renderAddField={renderAddField}
      />
      {explicitEmpty && (
        <Switch checked={explicitEmpty.checked} onChange={explicitEmpty.onChange} label={explicitEmpty.label} />
      )}
    </div>
  );
}

export interface PathListFieldProps {
  option: LaunchOption; // kind: pathList
  items: string[];
  onChange: (items: string[]) => void;
  validatePath: (path: string, kind: string) => Promise<PathValidateResponse>;
}

/** The pathList add field's empty-state text - the picker's closed trigger
 * shows it in place of a value, so it's the field's whole visible content when
 * empty and has to name what the field holds: mcpConfigs is a list of .json
 * files, the two dir lists are directories. */
function pathAddPlaceholder(kind: PathFieldKind): string {
  return kind === "dir" ? "/path/to/directory" : "/path/to/file";
}

/** The browse kind for a pathList option. Every pathList field in the schema
 * names a real filesystem path - skillsDirs/pluginDirs are directories,
 * mcpConfigs is a file - so anything else falls back to browsing directories,
 * which is the only kind that lists nothing a list of paths can't hold. */
function pathFieldKind(pathKind: string | undefined): PathFieldKind {
  if (pathKind === "file" || pathKind === "outputFile") return pathKind;
  return "dir";
}

/** pathList kind: skillsDirs/pluginDirs/mcpConfigs. Adds come from the shared
 * path picker (PathAddField below), and every add is still validated
 * server-side via serf/path/validate before being accepted, using the
 * server-canonicalized path when one comes back - matching the legacy's own
 * "blocks the add on failure, shows a field-level error" / "uses
 * valid.path if present else the raw trimmed input" behaviors. */
export function PathListField({ option, items, onChange, validatePath }: PathListFieldProps) {
  const kind = pathFieldKind(option.pathKind);
  const placeholder = pathAddPlaceholder(kind);
  return (
    <StringListField
      option={option}
      items={items}
      onChange={onChange}
      addPlaceholder={placeholder}
      emptyMessage={`No ${option.label.toLowerCase()} configured.`}
      validateAdd={async (trimmed) => {
        const result = await validatePath(trimmed, schemaPathKind(option.pathKind));
        if (!result.valid) return { ok: false, error: result.error || "invalid path" };
        return { ok: true, value: result.path || trimmed };
      }}
      renderAddField={({ value, onChange: setDraft, disabled }) => (
        <div className={CLASS.pathAddRow}>
          <PathAddField
            value={value}
            onChange={setDraft}
            kind={kind}
            placeholder={placeholder}
            ariaLabel={option.label}
            disabled={disabled}
          />
        </div>
      )}
    />
  );
}

/**
 * The pathList add row: the shared path picker plus CollectionEditor's own
 * submit button. `complete` is the extensions store's serf/paths/complete
 * call, imported directly the way ModelAddField imports its own catalog
 * loader rather than threading a prop through StringListField - and no
 * `listRecents`, since a skills- or config-file list has no meaningful
 * "recent" of its own (that belongs to the spawn working directory alone).
 *
 * The browsed path lands in CollectionEditor's `draft`; the Add button submits
 * it, which is where serf/path/validate still gates it (the picker's own panel
 * is portaled outside this <form>, so Enter inside the picker picks a path
 * rather than submitting the row - asserted by collectionFields.test.tsx's
 * "Enter on a directory row descends without submitting the add row").
 *
 * `ariaLabel` carries the option's own label into the trigger's accessible
 * name: CollectionEditor skips its own visually-hidden label wrapper in
 * renderAddField mode (naming is the caller's job), and all three pathList
 * fields sit in the same "Resources" group, so without it their triggers would
 * all be named by the same placeholder text.
 */
function PathAddField({
  value,
  onChange,
  kind,
  placeholder,
  ariaLabel,
  disabled,
}: {
  value: string;
  onChange: (value: string) => void;
  kind: PathFieldKind;
  placeholder: string;
  ariaLabel: string;
  disabled: boolean;
}) {
  return (
    <>
      <span className={CLASS.pathAddField}>
        <PathField
          value={value}
          onChange={onChange}
          kind={kind}
          complete={(prefix, includeFiles) => extensionsStore.getState().completePaths(prefix, includeFiles)}
          placeholder={placeholder}
          ariaLabel={ariaLabel}
          disabled={disabled}
        />
      </span>
      <Button type="submit" variant="quiet" disabled={value.trim() === "" || disabled}>
        Add
      </Button>
    </>
  );
}

export interface ModelListFieldProps {
  option: LaunchOption; // kind: modelList
  items: string[];
  onChange: (items: string[]) => void;
  explicitEmpty: boolean;
  onExplicitEmptyChange: (checked: boolean) => void;
}

/**
 * modelList kind: model_fallbacks is the only field today. Adds come from the
 * SAME searchable ModelCatalog picker every other model field uses, rather
 * than a hand-typed "provider/model" string - picking writes the qualified id
 * into CollectionEditor's own draft and submits it, so nothing about the
 * add/validate path changes. A model id still needs no server validation (no
 * RPC validates one in isolation), but now it can only be a real catalog
 * entry rather than an unverifiable typo.
 */
export function ModelListField({ option, items, onChange, explicitEmpty, onExplicitEmptyChange }: ModelListFieldProps) {
  return (
    <StringListField
      option={option}
      items={items}
      onChange={onChange}
      addPlaceholder="provider/model"
      emptyMessage={`No ${option.label.toLowerCase()} configured.`}
      validateAdd={async (trimmed) => {
        if (items.includes(trimmed)) return { ok: false, error: "Already added." };
        return { ok: true, value: trimmed };
      }}
      explicitEmpty={{ checked: explicitEmpty, onChange: onExplicitEmptyChange, label: "No model fallbacks" }}
      renderAddField={({ value, onChange: setDraft, disabled }) => (
        <div className={CLASS.modelAddRow}>
          <ModelAddField value={value} onChange={setDraft} disabled={disabled} />
        </div>
      )}
    />
  );
}

/**
 * The modelList add row: the shared model picker plus CollectionEditor's own
 * submit button. The picker is unscoped (/api/models with no harness/cwd) -
 * a launch-config fallback list isn't scoped to one live spawn - matching the
 * scalar modelPicker field in this section's sibling fields.tsx.
 *
 * The picked id lands in CollectionEditor's `draft`; the Add button submits it
 * (the picker's own panel is portaled outside this <form>, so Enter inside the
 * picker picks a model rather than submitting the row).
 */
function ModelAddField({
  value,
  onChange,
  disabled,
}: {
  value: string;
  onChange: (value: string) => void;
  disabled: boolean;
}) {
  return (
    <>
      <span className={CLASS.modelAddField}>
        <ModelCatalog value={value} onChange={onChange} loadCatalog={() => fetchModelCatalog()} />
      </span>
      <Button type="submit" variant="quiet" disabled={value.trim() === "" || disabled}>
        Add
      </Button>
    </>
  );
}

// --- envMap: name/value pairs, "NAME=value" add syntax ---------------------

export interface EnvMapFieldProps {
  option: LaunchOption; // kind: envMap
  value: Record<string, string>;
  onChange: (value: Record<string, string>) => void;
}

interface EnvEntry {
  name: string;
  value: string;
}

/**
 * The envMap add row: a real structured NAME/value pair (CollectionEditor's
 * own renderAddField slot), composed into CollectionEditor's single `draft`
 * string via one code-owned "=" join point that's never user-typed - so
 * handleAdd below can keep parsing "NAME=value" completely unchanged, and a
 * value containing "=" (e.g. a base64 blob) round-trips with no ambiguity
 * at all. Typing "=" into the NAME field itself is stripped rather than
 * composed through: an env var name can never contain one, so there's
 * nothing legitimate to preserve, and stripping keeps the FIRST "=" in the
 * composed string always exactly the name/value boundary. Both boxes are
 * named for assistive tech via a visually-hidden label (the placeholder
 * already conveys it sighted), matching widgets/collectioneditor's own
 * default-field labeling technique.
 */
function EnvAddFields({
  value,
  onChange,
  disabled,
}: {
  value: string;
  onChange: (value: string) => void;
  disabled: boolean;
}) {
  const nameId = useId();
  const valueId = useId();
  const eq = value.indexOf("=");
  const name = eq === -1 ? value : value.slice(0, eq);
  const envValue = eq === -1 ? "" : value.slice(eq + 1);

  return (
    <>
      <span className={CLASS.envField}>
        <label className={CLASS.visuallyHidden} htmlFor={nameId}>
          Variable name
        </label>
        <Input
          id={nameId}
          value={name}
          onChange={(e) => onChange(`${e.target.value.replace(/=/g, "")}=${envValue}`)}
          placeholder="NAME"
          disabled={disabled}
        />
      </span>
      <span className={CLASS.envField}>
        <label className={CLASS.visuallyHidden} htmlFor={valueId}>
          Variable value
        </label>
        <Input
          id={valueId}
          value={envValue}
          onChange={(e) => onChange(`${name}=${e.target.value}`)}
          placeholder="value"
          disabled={disabled}
        />
      </span>
      <Button type="submit" variant="quiet" disabled={value.trim() === "" || disabled}>
        Add
      </Button>
    </>
  );
}

/** envMap kind: `env`. Add syntax is still "NAME=value" under the hood
 * (handleAdd), split on the FIRST '=' only so a value that itself contains
 * '=' round-trips intact - no validation on either sub-field beyond
 * requiring a non-empty NAME, matching the legacy's own envMap behavior
 * exactly. EnvAddFields above is what composes that string now, from two
 * real boxes instead of asking the user to type the '=' themselves. */
export function EnvMapField({ option, value, onChange }: EnvMapFieldProps) {
  const entries: EnvEntry[] = Object.entries(value).map(([name, v]) => ({ name, value: v }));

  async function handleAdd(trimmed: string): Promise<CollectionAddResult> {
    const eq = trimmed.indexOf("=");
    if (eq <= 0) return { ok: false, error: "Use NAME=value." };
    const name = trimmed.slice(0, eq).trim();
    const envValue = trimmed.slice(eq + 1);
    if (!name) return { ok: false, error: "Use NAME=value." };
    onChange({ ...value, [name]: envValue });
    return { ok: true };
  }

  return (
    <div className={CLASS.section}>
      <SectionHeader label={option.label} help={option.description} />
      <CollectionEditor<EnvEntry>
        label={option.label}
        items={entries}
        getKey={(entry) => entry.name}
        renderItem={(entry) => `${entry.name}=${entry.value}`}
        removeLabel={(entry) => `Remove ${entry.name}=${entry.value}`}
        onRemove={(entry) => {
          const next = { ...value };
          delete next[entry.name];
          onChange(next);
        }}
        emptyMessage="No environment variables configured."
        onAdd={handleAdd}
        renderAddField={({ value: draft, onChange: setDraft, disabled }) => (
          <div className={CLASS.envRow}>
            <EnvAddFields value={draft} onChange={setDraft} disabled={disabled} />
          </div>
        )}
      />
    </div>
  );
}

// --- mcpServerList: name/command/args triples, "name command args..." ------

export interface McpServerListFieldProps {
  option: LaunchOption; // kind: mcpServerList
  items: MCPServerSpec[];
  onChange: (items: MCPServerSpec[]) => void;
  validateCommand: (command: string) => Promise<PathValidateResponse>;
}

function specKey(spec: MCPServerSpec): string {
  return `${spec.name}|${spec.command}|${(spec.args ?? []).join(" ")}`;
}

function renderSpec(spec: MCPServerSpec): string {
  const args = spec.args && spec.args.length > 0 ? ` ${spec.args.join(" ")}` : "";
  return `${spec.name} → ${spec.command}${args}`;
}

/** mcpServerList kind: `mcps`. Add syntax is "name command [args...]"
 * (whitespace-split); the command token is validated via serf/path/validate
 * kind="command" before the row is accepted, matching the legacy's own
 * validateMCPCommandInput. */
export function McpServerListField({ option, items, onChange, validateCommand }: McpServerListFieldProps) {
  async function handleAdd(trimmed: string): Promise<CollectionAddResult> {
    const tokens = trimmed.split(/\s+/).filter(Boolean);
    const [name, command, ...args] = tokens;
    if (!name || !command) return { ok: false, error: "Use: name command [args...]" };
    const result = await validateCommand(command);
    if (!result.valid) return { ok: false, error: result.error || "invalid command" };
    onChange([...items, { name, command: result.path || command, args }]);
    return { ok: true };
  }

  return (
    <div className={CLASS.section}>
      <SectionHeader label={option.label} help={option.description} />
      <CollectionEditor<MCPServerSpec>
        label={option.label}
        items={items}
        getKey={specKey}
        renderItem={renderSpec}
        removeLabel={(spec) => `Remove ${renderSpec(spec)}`}
        onRemove={(spec) => onChange(items.filter((i) => specKey(i) !== specKey(spec)))}
        emptyMessage="No MCP servers configured."
        addPlaceholder="name command args..."
        onAdd={handleAdd}
      />
    </div>
  );
}
