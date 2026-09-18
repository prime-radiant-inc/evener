// instanceEdit.ts: the provider sheet's form model - the draft the sheet
// edits, how it is seeded from an InstanceEntry, and how a dirty draft
// becomes the smallest evener/instance/edit request. Pure, so the diff rules
// (empty means unchanged on the wire, a clear flag for an emptied field,
// only the vars that changed) are pinned without rendering anything.
//
// protocol and baseUrl on the wire are the RESOLVED values - InstanceEntry
// carries no authored/inherited distinction for them - so the selects show
// what is in effect and "inherit from base" is the way back to the base's
// value (a clear the hub treats as a no-op when nothing was authored).
//
// vars do not resolve that way: InstanceEntry.vars carries only what this
// instance authored, never the base provider's curated defaults, which the
// registry applies when it resolves a URL. So a template variable gets its
// row from the template and not from the entry, blank until authored, and
// emptying one goes out as an empty value - the wire's delete - not a clear.
import type { InstanceEditParams, InstanceEntry, ProviderDescriptor } from "@evener/appwire-client";
import type { SelectOption } from "../../../../widgets";

export const PROTOCOL_OPTIONS: SelectOption[] = [
  { value: "", label: "inherit from base" },
  { value: "openai-chat", label: "openai-chat" },
  { value: "openai-responses", label: "openai-responses" },
  { value: "anthropic", label: "anthropic" },
  { value: "google", label: "google" },
];

export const SURFACE_OPTIONS: SelectOption[] = [
  { value: "", label: "inherit from base" },
  { value: "openai", label: "openai" },
  { value: "anthropic", label: "anthropic" },
  { value: "google", label: "google" },
  { value: "generic", label: "generic" },
];

export interface InstanceDraft {
  name: string;
  baseUrl: string;
  protocol: string;
  surface: string;
  vars: Record<string, string>;
  apiKeyEnv: string;
  credentialHeader: string;
}

/** The form's initial values for an instance. Every template variable of the
 * base provider gets a row (blank unless authored), keyed by template name
 * exactly as the Add form keys its inputs. */
export function draftFor(instance: InstanceEntry, template: ProviderDescriptor | undefined): InstanceDraft {
  const vars: Record<string, string> = {};
  for (const key of Object.keys(template?.vars ?? {})) vars[key] = "";
  for (const [key, value] of Object.entries(instance.vars ?? {})) vars[key] = value;
  return {
    name: instance.name,
    baseUrl: instance.baseUrl ?? "",
    protocol: instance.protocol,
    surface: instance.surface ?? "",
    vars,
    apiKeyEnv: instance.apiKeyEnv ?? "",
    credentialHeader: instance.credentialHeader ?? "",
  };
}

/** The variable rows to render: the template's, labelled by the env var
 * name the docs tell users to set (the Add form's own labelling), then any
 * authored var the template does not name, labelled by its key.
 *
 * Both runs sort by code point, not localeCompare: a placeholder name is an
 * identifier, so the row order must be the same everywhere rather than the
 * one the reader's locale collates. */
export function varRows(
  draft: InstanceDraft,
  template: ProviderDescriptor | undefined,
): { key: string; label: string }[] {
  const templateVars = template?.vars ?? {};
  const rows = Object.entries(templateVars)
    .sort(([a], [b]) => byCodePoint(a, b))
    .map(([key, env]) => ({ key, label: env }));
  for (const key of Object.keys(draft.vars).sort(byCodePoint)) {
    if (!(key in templateVars)) rows.push({ key, label: key });
  }
  return rows;
}

/** The order variable rows are shown in, on every surface that shows them: a
 * placeholder name is an identifier, so its place in the list belongs to the
 * name itself and not to the reader's locale. */
export function byCodePoint(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

/** The clearable entry fields and the wire `clear*` flag each goes out under
 * when a draft empties it. The flag and the field are different names
 * (`clearApiKeyEnv` clears `apiKeyEnv`), and the rename-identity comparison in
 * InstanceSheet has to see a clear as the field it changed, so this one table is
 * the pairing both sides read: instanceEditParams authors each flag forward, and
 * clearedField reverse-maps a flag back to its field. Adding a clearable field
 * here covers both. `trim` says whether the form compares the field's trimmed
 * value - the URLs and free-text fields do, the protocol/surface selects do not. */
export const CLEARABLE_FIELDS = [
  { field: "baseUrl", clearFlag: "clearBaseUrl", trim: true },
  { field: "protocol", clearFlag: "clearProtocol", trim: false },
  { field: "surface", clearFlag: "clearSurface", trim: false },
  { field: "apiKeyEnv", clearFlag: "clearApiKeyEnv", trim: true },
  { field: "credentialHeader", clearFlag: "clearCredentialHeader", trim: true },
] as const;

/** The entry field a wire `clear*` flag stands for, or the key itself when it
 * names no clear flag: the reverse of CLEARABLE_FIELDS, so a rename's identity
 * comparison sees a clear as the field it changed. */
export function clearedField(paramKey: string): string {
  const pair = CLEARABLE_FIELDS.find((entry) => entry.clearFlag === paramKey);
  return pair ? pair.field : paramKey;
}

/** The request that carries exactly the fields whose trimmed value differs
 * from `initial`'s trimmed value, or null when none does. Both sides are
 * trimmed so the diff holds for any draft, not only one `draftFor` built. */
export function instanceEditParams(initial: InstanceDraft, draft: InstanceDraft): InstanceEditParams | null {
  const params: InstanceEditParams = { name: initial.name };
  let changed = false;

  // An empty newName means UNCHANGED on the wire, so an emptied Name is not a
  // rename this request can carry: sending it would earn a success from an
  // edit that renamed nothing, and the sheet would report a save it did not get.
  const name = draft.name.trim();
  if (name !== "" && name !== initial.name.trim()) {
    params.newName = name;
    changed = true;
  }
  for (const { field, clearFlag, trim } of CLEARABLE_FIELDS) {
    const draftValue = trim ? draft[field].trim() : draft[field];
    const initialValue = trim ? initial[field].trim() : initial[field];
    if (draftValue === initialValue) continue;
    if (draftValue === "") params[clearFlag] = true;
    else params[field] = draftValue;
    changed = true;
  }
  const vars: Record<string, string> = {};
  for (const [key, value] of Object.entries(draft.vars)) {
    const trimmed = value.trim();
    if (trimmed !== (initial.vars[key] ?? "").trim()) vars[key] = trimmed;
  }
  if (Object.keys(vars).length > 0) {
    params.vars = vars;
    changed = true;
  }
  return changed ? params : null;
}
