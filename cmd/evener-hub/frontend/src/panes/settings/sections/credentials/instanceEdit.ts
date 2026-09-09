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
import type { InstanceEditParams, InstanceEntry, ProviderDescriptor } from "../../../../protocol/types.gen";
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
  const baseUrl = draft.baseUrl.trim();
  if (baseUrl !== initial.baseUrl.trim()) {
    if (baseUrl === "") params.clearBaseUrl = true;
    else params.baseUrl = baseUrl;
    changed = true;
  }
  if (draft.protocol !== initial.protocol) {
    if (draft.protocol === "") params.clearProtocol = true;
    else params.protocol = draft.protocol;
    changed = true;
  }
  if (draft.surface !== initial.surface) {
    if (draft.surface === "") params.clearSurface = true;
    else params.surface = draft.surface;
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
  const apiKeyEnv = draft.apiKeyEnv.trim();
  if (apiKeyEnv !== initial.apiKeyEnv.trim()) {
    if (apiKeyEnv === "") params.clearApiKeyEnv = true;
    else params.apiKeyEnv = apiKeyEnv;
    changed = true;
  }
  const credentialHeader = draft.credentialHeader.trim();
  if (credentialHeader !== initial.credentialHeader.trim()) {
    if (credentialHeader === "") params.clearCredentialHeader = true;
    else params.credentialHeader = credentialHeader;
    changed = true;
  }
  return changed ? params : null;
}
