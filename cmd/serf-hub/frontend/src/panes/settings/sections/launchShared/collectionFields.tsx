// collectionFields.tsx renders the 4 collection LaunchOption kinds (pathList/
// modelList/envMap/mcpServerList - Appendix B), each a CollectionEditor
// (widgets/collectioneditor) parameterized by its own add-value parsing/
// validation. CollectionEditor's own add-input is a single plain text field
// (see its own source), so envMap/mcpServerList parse a delimited string
// instead of the legacy's separate per-sub-field inputs - stays within
// CollectionEditor's real, already-built contract rather than forking it
// (out of this stream's manifest). Itemized in the wave-7 task-2 report's
// "Deliberate scope/parity decisions" list - two DIFFERENT tradeoffs, not
// one:
//   - envMap ("NAME=value"): a genuine UX step-down from the legacy's
//     separate name/value inputs - a value containing "=" still round-trips
//     correctly (split on the FIRST "=" only), but typing is less
//     ergonomic than two plain fields.
//   - mcpServerList ("name command args..."): expressiveness-equivalent,
//     not a step-down - the legacy's own 3 separate inputs (name/command/
//     args) are themselves just concatenated back into one command line by
//     the user's own shell-argument mental model; a single "name command
//     args..." field asks for the exact same information in one line
//     instead of three boxes.
//
// Also out of scope here (and in fields.tsx's own scalar path-kind
// rendering): the legacy engine's Constraint Validation API integration
// (validatePathInput/validateMCPCommandInput's setCustomValidity() +
// reportValidity() calls, which additionally surface the browser's own
// native validation-bubble UI alongside the custom inline error this port
// already shows). Not ported - the custom inline error is the only
// validation UI surfaced here.

import type { LaunchOption, MCPServerSpec, PathValidateResponse } from "../../../../protocol/types.gen";
import type { CollectionAddResult } from "../../../../widgets";
import { CollectionEditor, Switch } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./collectionFields.module.css";
import { schemaPathKind } from "./schema";

const CLASS = {
  section: requireClass(styles.section, "collectionFields.module.css", "section"),
  header: requireClass(styles.header, "collectionFields.module.css", "header"),
  label: requireClass(styles.label, "collectionFields.module.css", "label"),
  help: requireClass(styles.help, "collectionFields.module.css", "help"),
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
}

function StringListField({
  option,
  items,
  onChange,
  addPlaceholder,
  emptyMessage,
  validateAdd,
  explicitEmpty,
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

/** pathList kind: skillsDirs/pluginDirs/mcpConfigs. Every add is validated
 * server-side via serf/path/validate before being accepted, using the
 * server-canonicalized path when one comes back - matching the legacy's own
 * "blocks the add on failure, shows a field-level error" / "uses
 * valid.path if present else the raw trimmed input" behaviors. */
export function PathListField({ option, items, onChange, validatePath }: PathListFieldProps) {
  return (
    <StringListField
      option={option}
      items={items}
      onChange={onChange}
      addPlaceholder="/path/to/directory"
      emptyMessage={`No ${option.label.toLowerCase()} configured.`}
      validateAdd={async (trimmed) => {
        const result = await validatePath(trimmed, schemaPathKind(option.pathKind));
        if (!result.valid) return { ok: false, error: result.error || "invalid path" };
        return { ok: true, value: result.path || trimmed };
      }}
    />
  );
}

export interface ModelListFieldProps {
  option: LaunchOption; // kind: modelList
  items: string[];
  onChange: (items: string[]) => void;
  explicitEmpty: boolean;
  onExplicitEmptyChange: (checked: boolean) => void;
}

/** modelList kind: model_fallbacks is the only field today. Accepts a bare
 * "provider/model" string with no server validation (no RPC validates a
 * model id in isolation) - the searchable model picker (Appendix A) is out
 * of scope, see this file's sibling fields.tsx top comment. */
export function ModelListField({ option, items, onChange, explicitEmpty, onExplicitEmptyChange }: ModelListFieldProps) {
  return (
    <StringListField
      option={option}
      items={items}
      onChange={onChange}
      addPlaceholder="provider/model"
      emptyMessage={`No ${option.label.toLowerCase()} configured.`}
      validateAdd={async (trimmed) => ({ ok: true, value: trimmed })}
      explicitEmpty={{ checked: explicitEmpty, onChange: onExplicitEmptyChange, label: "No model fallbacks" }}
    />
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

/** envMap kind: `env`. Add syntax is "NAME=value", split on the FIRST '='
 * only so a value that itself contains '=' (e.g. a base64 blob) round-trips
 * intact - no validation on either sub-field beyond requiring a non-empty
 * NAME, matching the legacy's own envMap behavior exactly. */
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
        addPlaceholder="NAME=value"
        onAdd={handleAdd}
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
