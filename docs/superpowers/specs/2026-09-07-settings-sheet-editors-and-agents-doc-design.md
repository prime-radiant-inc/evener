# Settings: sheet editors and the personal AGENTS.md

Jesse asked for three things in the web UI's settings pane (2026-09-07): a way
to edit the user's personal AGENTS.md; a rethink of the provider "sidebar
edit" sheet, which could not rename an instance or edit much of anything;
and the same fix wherever that pattern is used. Brainstorm decisions, in
order: the personal file lives at `~/.config/evener/AGENTS.md` and is loaded
into every session; "plugin sources" means the Marketplaces list, and
marketplace editing covers rename and re-source; the edit UI keeps
row-tap-opens-sheet but the sheet becomes the editor; the AGENTS.md write has
no conflict precondition; protocol and surface are exposed on the provider
form; a marketplace's git ref stays unexposed.

Paths are relative to the repo root; frontend paths under
`cmd/evener-hub/frontend/src/`.

## 1. Personal AGENTS.md

### What exists today

Nothing loads a user-level instruction file. `agent.LoadProjectDocs`
(`agent/project_docs.go`) walks from the git root down to the working
directory only, picking up the profile's doc filenames
(`agent/provider/profile.go`: AGENTS.md plus CLAUDE.md, GEMINI.md, or
`.codex/instructions.md`). Its single caller is `agent/session_init.go`. The
config root `~/.config/evener` (`envvars/userdirs.ConfigRoot`) already holds
providers.toml, hub.toml, mcp.json, and the prompts, commands, plugins and
skills directories.

### Daemon: load it

`agent.LoadUserDoc(configRoot string) (ProjectDoc, bool)` reads
`<configRoot>/AGENTS.md`. Session init calls it with
`userdirs.DefaultConfigRoot()` (hub-spawned daemons resolve the same root
from their environment; nothing new is plumbed) and prepends the result to
`s.projectDocs`, so it renders first inside the existing
`prompts/sections/project-docs.md.tmpl` block, ahead of the repo's own docs.
Its `Path` is the tilde-collapsed display path `~/.config/evener/AGENTS.md`.
It applies to every provider profile. It shares `projectDocByteBudget` and
counts first: the repo docs get whatever is left, and truncation behaves
exactly as today. A missing or empty file changes nothing.

### Hub: read and write it

Two additive methods and one notification (protocol version stays
`evener-appwire-v4`; catalog entries in `appwire/protocol.go`, generated
outputs refreshed by `make generate`):

- `evener/settings/agentsDoc/get` — params `EmptyParams`; result
  `AgentsDocResponse{ path string; exists bool; content string }`. `path` is
  the absolute file path. A missing file is `exists=false, content=""`, not an
  error.
- `evener/settings/agentsDoc/set` — params `AgentsDocSetParams{ content
  string }`; result `AgentsDocResponse`. Writes `content` as-is (no trimming,
  no forced trailing newline) with temp-file-and-rename at mode 0o644, the
  same way `registry.WriteConfigFile` writes providers.toml, creating the
  config root if needed. No precondition: the last write wins (Jesse's call).
- `evener/settings/agentsDoc/changed` — broadcast after a successful set,
  carrying `AgentsDocResponse`, so other clients refresh.

The file path is `filepath.Join(cfg.LaunchConfigRoot, "AGENTS.md")`
(`hubcore.WebConfig.LaunchConfigRoot` is already the config root). Handlers
live in a new `cmd/evener-hub/app_rpc_agents_doc.go` beside the keybindings
and transcript-display handlers.

### Frontend: edit it

- A new settings section in the Agent setup cluster, after "Agents":
  `{ id: "agents-md", label: "AGENTS.md", cluster: "agents-models" }` in
  `panes/settings/sections.ts`, dispatched from `Settings.tsx`.
- `stores/agentsDoc.ts` follows the keybindings store shape: `fetch()`,
  `save(content)`, `loaded`/`loading`/`error`, and a subscription to the
  `changed` notification that replaces the loaded content.
- `panes/settings/sections/agentsDoc.tsx`: a one-line help sentence with the
  path in `<Code>`, a monospace `Textarea` (`minLines` 16, `autoGrow`), and
  a footer row with Save (primary, enabled only when the draft differs from
  the loaded content) and Revert. Save success toasts "Saved AGENTS.md"; a
  failure shows the error inline. An incoming `changed` notification while
  the draft is clean replaces the draft; while dirty it leaves the draft and
  shows a one-line note that the file changed elsewhere, with a "Load
  current" button.

## 2. Provider instances: the sheet is the editor

### What exists today

`panes/settings/sections/credentials/InstanceDetailSheet.tsx` is an
inspector: a button stack whose "Edit" opens `EditInstanceDialog`
(`instanceDialogs.tsx`), which edits Base URL only. `appwire.InstanceEditParams`
accepts base URL (with `clearBaseUrl`), protocol, surface and vars, with
"empty means unchanged". It cannot rename, and cannot change `api_key_env`
or `credential_headers`, both of which only `InstanceCreateParams` accepts.
`InstanceEntry` does not carry the authored `api_key_env` or
`credential_headers`.

### Wire changes (additive, `evener-appwire-v4`)

`InstanceEditParams` gains:

| field | meaning |
|---|---|
| `newName` | rename; empty means unchanged |
| `apiKeyEnv` / `clearApiKeyEnv` | set or drop the single authored `api_key_env` entry |
| `credentialHeader` / `clearCredentialHeader` | set (as `NAME=VALUE`, parsed by the existing `credentialHeaderFrom`) or drop `credential_headers` |
| `clearProtocol` / `clearSurface` | drop the authored override so the base's value applies again (the clear the existing doc comment ledgers) |
| `vars` with an empty value | deletes that authored variable (today `Edit` copies the empty string in; a blank var never meant anything, so the change is unambiguous) |

Each set/clear pair follows `baseUrl`/`clearBaseUrl`: never both meaningful
in one request. `InstanceEntry` gains `apiKeyEnv?: string` (the first
authored `api_key_env` entry) and `credentialHeader?: string` (the single
authored header rendered as `NAME=VALUE`). Both are variable names and
`$VAR` templates by construction (`registry.CheckCredentialHeaderValue`
refuses a literal), never secret values. They come from the authored layer
(`c.read()`), which `entryFor` does not read today; `List` reads the layer
once per call and passes the authored `registry.Provider` alongside each
instance.

### Hub: Edit

`hubInstancesController.Edit` extends its existing field-by-field apply with
the new pairs, then handles rename last, all under `c.mu`:

1. Refuse when `params.NewName` is set on an implicit instance
   (`InvalidParams`: "instance %q comes from the environment and cannot be
   renamed").
2. Validate with `registry.ValidInstanceName`; refuse `Conflict` when
   `c.reg.Get().Instance(newName)` exists (authored or implicit) or
   `l.Providers[newName]` exists.
3. Re-key `l.Providers[old]` to `newName`, set `p.ID = newName`, and set
   `l.Default = newName` when it named the old instance.
4. `writeLoadable`, then `c.reg.Reload()`; on reload failure restore the
   pre-edit file and return `InvalidParams` exactly as the current Edit does.
5. Move the stored key: `Get(old)`, `Set(new)`, `Clear(old)` on
   `c.auth.creds`. Move the OAuth record: `authopenai.LoadAuth(stateDir,
   old)`, `SaveAuth(stateDir, new)`, `DeleteAuth(stateDir, old)` when a
   record exists. Both are best-effort after the config write: a failure
   here is returned as a plain error naming what was left behind (the
   config is already renamed, so the instance list stays consistent with
   the file).

The existing `evener/auth/updated` broadcast follows a rename so other
clients drop their cached credential state for the old name.

### Frontend

`InstanceDetailSheet` becomes `InstanceSheet` (same file, renamed), a
`Sheet` with `size="wide"` on desktop and `side="bottom"` on mobile.
`EditInstanceDialog` and its tests are deleted. `ApiKeyDialog`,
`CredentialJsonDialog`, `ConnectProviderDialog`, and the OAuth dialogs are
unchanged. Body, top to bottom:

1. Header row as today: `StatusDot`, "★ default" and "from environment"
   chips.
2. The form, prefilled from the `InstanceEntry`:
   - Name (`Input`). Read-only with a "from environment" note on implicit
     instances. When dirty, an inline note under the field: "Launch config
     and past sessions that reference `<old>` keep the old name." No
     confirm dialog.
   - Base provider: a read-only meta row (`providerId`); re-basing is not
     supported by the edit RPC and is not added.
   - Base URL (`Input`). Emptying a field that had a value sends
     `clearBaseUrl`, with the existing "Resets the endpoint to the
     provider's default." note.
   - Protocol and Surface (`Select` each): options are the registry's four
     protocols (`openai-chat`, `openai-responses`, `anthropic`, `google`) and
     four surfaces (`openai`, `anthropic`, `google`, `generic`), plus an
     empty first option labelled "inherit from base". Choosing "inherit" on a
     field that had an authored value sends the matching clear flag.
   - Provider variables: one `Input` per entry of the base provider's `vars`
     (labelled by env var name, keyed by template name, exactly as the Add
     form does), prefilled from `instance.vars`; any authored var not in the
     template renders after them the same way.
   - API key environment variable (`Input`); emptying a set value sends
     `clearApiKeyEnv`.
   - Credential header (`Input`, placeholder `Authorization=Bearer $VAR`);
     emptying a set value sends `clearCredentialHeader`. The client refuses
     a value without `$` before any RPC, as the Add form does.
3. Sheet footer: Save (primary), enabled only when some field differs from
   the loaded instance and `writesRefused` is false. Save sends only the
   changed fields (existing "empty means unchanged" rule) and, on a rename,
   the section re-selects the instance under its new name so the sheet stays
   open on it. Success toasts "Saved <name>"; failure renders inline with a
   "Save failed" toast. Closing the sheet with unsaved edits discards them
   silently, matching the old editors.
4. Below the form, unchanged from today: the credential layers block, Test
   credentials, Set/Replace key, Set/Replace credential JSON, Sign in… /
   Refresh OAuth, ★ make default.
5. Danger zone, unchanged: Clear stored key, Clear, Remove, all
   ConfirmDialog-gated.

`AddInstanceDialog` gains the same Protocol and Surface selects
(`InstanceCreateParams` already accepts both), placed after Base URL.

## 3. Marketplaces: the sheet is the editor

### What exists today

`marketplacesPlugins/MarketplacesSection.tsx` renders rows with inline
Refresh and Remove buttons and an inline add form. There is no edit RPC.
`internal/plugins` keys `known_marketplaces.json` by name; a git-backed
marketplace is cloned to `<store>/marketplaces/<name>` (its
`InstallLocation`); `installed_plugins.json` is keyed `<plugin>@<marketplace>`
and each entry's `InstallPath` sits under `<store>/cache/<marketplace>/<plugin>/<sha>`.
Launch config names plugins by bare plugin name, so a marketplace rename
does not orphan it.

### Wire (additive)

`evener/marketplace/edit` — params `MarketplaceEditParams{ name string;
newName?: string; source?: MarketplaceSourceInput }`; result
`MarketplaceListResponse`. Empty `newName` and absent `source` mean
unchanged. Catalogued in `appwire/protocol.go`; the handler in `app_rpc.go`
mirrors `MarketplaceAdd` and, on success, broadcasts both
`evener/marketplace/updated` and `evener/plugin/updated` (installed plugin
keys can change).

### Manager: `EditMarketplace`

`(*plugins.Manager).EditMarketplace(ctx, name, newName string, src *Source)
(MarketplaceRef, error)`, under the store lock, in this order so the network
step precedes anything that moves on disk:

1. Load marketplaces; unknown `name` is `ErrMarketplaceNotFound`. A no-op
   call (same name, nil or equal source) returns the current ref.
2. If `src` is set and differs: fetch it into the shared staging dir with
   `fetchMarketplaceContainer` and `ParseCatalog` it, exactly as
   `AddMarketplace` does. Any failure here removes staging and returns
   before anything else changes.
3. If renaming: `validNameComponent`; a new `plugins.ErrMarketplaceExists`
   sentinel when `newName` is already registered, which the hub controller
   maps to `appwire.Conflict` (and `ErrMarketplaceNotFound` to
   `appwire.InvalidParams`). Rename `<marketplaces>/<old>` to
   `<marketplaces>/<new>` when the current source is not a directory; rename
   `<cache>/<old>` to `<cache>/<new>` when it exists. In the installed
   registry, re-key every `<plugin>@<old>` to `<plugin>@<new>` and rewrite
   each entry's `InstallPath` prefix from `<cache>/<old>/` to
   `<cache>/<new>/`.
4. Apply the source: a git-backed source swaps the staged clone into the
   (possibly renamed) install location with `swapInClone`; a directory
   source sets `InstallLocation` to the path and removes any old clone
   directory. Update `ref.Source` and `ref.LastUpdated`.
5. Save the installed registry, then the marketplaces file, both through
   `atomicWriteFile`.

A failure after step 3 renames the directories back before returning, and
never saves either file. A failure in step 5 after the registry saved but
before the marketplaces file saved is reported as an error naming the
inconsistency; `evener-doctor`'s plugin check already reports registry
entries that name no marketplace.

The frontend `extensionsStore` gains `editMarketplace(params)` and drops
the browse cache entry for the old name after a rename.

### Frontend

Marketplace rows become single tappable targets in the collection-page
idiom: name, a kind chip, the source meta line, a trailing chevron, one
full-width button. The add form is unchanged. A new
`marketplacesPlugins/MarketplaceSheet.tsx` (`size="wide"` desktop, bottom
sheet mobile; `marketplacesPlugins/index.tsx` owns `selectedMarketplace` the
same way it owns `selectedPlugin`, and switching segments clears both):

1. The form, prefilled from the `MarketplaceEntry`: Name (`Input`); Source
   kind (the same `RadioGroup` and three options the add form uses: Git URL,
   owner/repo, Local path; a `git-subdir` entry renders its URL under Git
   URL and is saved back unchanged unless the user edits it); the matching
   field for the kind (`Input` for URL and repo, `PathField` with the
   directory picker for a local path).
2. Meta table: install location and last updated, in mono.
3. Footer: Save, enabled only when dirty. When the source is dirty an
   inline note reads "Saving re-fetches the marketplace. Installed plugins
   are unaffected." Success toasts "Saved <name>"; on rename the page
   re-selects the new name so the sheet stays open.
4. Actions: Refresh (quiet), disabled while its RPC is in flight, keeping
   the existing expanded-node re-browse behavior.
5. Danger zone: Remove, ConfirmDialog-gated with the existing copy.

The sheet reads its entry from the store and closes when it vanishes.

## 4. Installed plugins and the design-system rule

`PluginDetailSheet` already edits its two binary states in place and opens
no editor dialog. It gets no functional change.

`docs/web-ui/design-system.md` §10's "Rows are single tappable targets;
actions live in a detail sheet" paragraph is rewritten: a detail sheet is the
item's **editor**, not an inspector. Editable facts render as form fields in
place, prefilled, with a dirty-gated Save in the sheet footer; renaming is
editing the name field. A separate dialog is reserved for write-only secret
entry (API key, credential JSON) and multi-step flows (OAuth). Binary state
stays a `Switch` row that applies immediately; the destructive action stays
ConfirmDialog-gated. `docs/web-ui/decisions.md` gets a 2026-09-07 entry
recording this and the reason (the provider sheet's "Edit" bounced to a
one-field dialog and could not rename).

## 5. Testing and gates

Test-driven throughout: each behavior below gets its failing test first.

Go:

- `agent/project_docs_test.go`: user doc prepended before repo docs; missing
  file is a no-op; the shared budget truncates the repo doc, not the user
  doc, when the user doc fits.
- `cmd/evener-hub/app_rpc_agents_doc_test.go`: get on a missing file; set
  creates the file (and the config root) at 0o644 and returns the new
  state; set broadcasts `changed`; a write failure returns an error and
  leaves the previous file intact.
- `cmd/evener-hub/app_instances_test.go`: rename re-keys providers.toml,
  repoints the default, moves the stored key and the OAuth record; rename
  of an implicit instance is refused; rename onto a taken name is
  `Conflict`; `apiKeyEnv`/`credentialHeader` set and clear; `clearProtocol`
  and `clearSurface`; the entry carries the authored `apiKeyEnv` and
  `credentialHeader`; a credential header without `$` is refused.
- `internal/plugins/marketplaces_test.go`: edit rename moves both
  directories and re-keys the registry and install paths; re-source swaps
  the staged clone and leaves installed plugins untouched; rename plus
  re-source; a fetch failure changes nothing on disk; a failure after the
  directory rename restores it; conflict; not found; no-op returns the
  current ref.
- `appwire`'s catalog test and `make generate` freshness
  (`types.gen.ts`, `docs/appwire-protocol.md`).

Frontend (vitest at the AppWire boundary with `FakeClient`):

- `stores/agentsDoc.test.ts` and `sections/agentsDoc.test.tsx`: load, edit,
  Save enabled only when dirty, Save sends the draft, Revert, `changed`
  while clean replaces, `changed` while dirty shows the note and Load
  current.
- `credentials/InstanceSheet.test.tsx`: prefill of every field; Save
  disabled until dirty and while `writesRefused`; the request carries only
  changed fields; each clear flag; the rename note; read-only name on an
  implicit instance; inline error on a failed save; re-select after rename;
  every existing sheet behavior test moves here.
- `credentials/instanceDialogs.test.tsx`: Add form protocol and surface.
- `marketplacesPlugins/MarketplaceSheet.test.tsx` and
  `MarketplacesSection.test.tsx`: rows are buttons that select; prefill;
  kind switch; Save payload for rename, re-source, and both; the re-fetch
  note; Refresh; Remove confirm; close-on-vanish; segment switch closes it.
- `docs/web-ui` claims about the sheet rule are covered by reading, not
  tests.

Gates before the PR: `npx biome check --write` on touched files, `make
test-web`, `make test-web-browser` on this Mac, `make lint`, `make vet`,
`make test`, plus a browser pass over the three sheets at desktop and
phone widths against a throwaway hub (the WebUI screenshot recipe), with
screenshots attached to the PR.

## 6. Delivery

Three independently mergeable slices, each its own PR against `main`
(stacked PRs get no CI here), in this order: the personal AGENTS.md (§1);
the provider sheet with its wire changes, the design-system rewrite and the
decisions entry (§2, §4); the marketplace sheet and edit RPC (§3). Each
slice carries its own tests and gate run.

## 7. Out of scope

Re-basing an instance onto a different provider; exposing a marketplace's
git ref or sha; a `git-subdir` option in the source picker; conflict
detection on AGENTS.md saves; any change to the installed-plugin sheet;
backward compatibility shims (the wire changes are additive within v4, so
none are needed).
