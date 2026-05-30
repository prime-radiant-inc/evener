# Provider Instances — Phase 2: unified CRUD UI (web + tui) Design

Status: **DRAFT** (2026-05-29). Final sub-project of PRI-1880, on top of Phases 1a
(behavior-tag), 1b (config instances), 1c (all-config + hub materialization), all
merged to local main. Implements §4.13 of the
[v7 design](2026-05-29-provider-type-instance-model-design.md). Design **approved by
Jesse 2026-05-29** (the mockups below).

## 1. Goal

One screen — in **both the web hub and `serf-tui`** — that manages provider
**instances**, replacing the duplicate read-only *Providers* + read-write
*Credentials* screens. Instances grouped by type; create / edit / remove /
set-default; per-instance credential management (API key + OAuth device-code).
Pickers display by instance name.

## 2. Approved UI

**Web** (replaces `templates/partials/credentials.html` + `settings/providers.html`):
instances grouped by type; each row shows name, `★default` marker, apiStyle/base_url,
the credential source-layers (reusing today's oauth>file>env shadowing), and actions
`[Set/Replace key] [Sign in…/Refresh OAuth] [Clear] [Edit] [Remove] [★ make default]`.
A per-type `[+ add instance]` opens an inline form: name, apiStyle (openai only),
base_url, credential (later / API key / OAuth). **TUI** (`credentials_panel.go`): the
same data as a grouped key-driven list — `↑↓` move, `enter` set key, `o` oauth, `c`
clear, `n` new, `e` edit, `x` remove, `*` default, `esc` close.

## 3. Data contract (`internal/appwire/types.go`)

A new **instance-list** response the UIs bind to (superseding `authList` for these
screens). One entry per instance:

```
InstanceEntry {
  name, type, apiStyle, baseURL string
  isDefault bool
  authModes []string            // from the type (apiKey / oauth / none)
  // credential status — same shape the current AuthStatus row uses:
  activeSource string           // file | env | oauth | absent | none
  hasStoredFile, hasStoredOAuth bool
  envVar, storedEmail string
}
InstanceListResponse { instances []InstanceEntry }   // sorted by type, then name
```

The credential fields are computed **per instance name** (file = `credentials.toml[name]`,
oauth = `auth/<name>.json`, env = the type's var — reusing 1c's `ResolveKey` precedence
and the existing layer logic), so the UI's source-layer rendering carries over unchanged.

## 4. RPC contract (extend the hub controller)

**Instance CRUD** (operate on the hub's `providers.toml` at `ProvidersConfigPath`):

- `InstanceList() InstanceListResponse` — load the config, join per-instance credential status.
- `InstanceCreate{type, name, apiStyle?, baseURL?}` — validate (see §6), append, write.
- `InstanceEdit{name, apiStyle?, baseURL?}` — **type is immutable** (changing it = remove+create); update fields, write.
- `InstanceRemove{name}` — drop the instance; if it was `default`, reassign to the first remaining by sorted name; also clear its credential (`credentials.toml[name]`) and OAuth (`auth/<name>.json`). Write.
- `InstanceSetDefault{name}` — set `default`, write.

All writes go through a **load → modify → `providerconfig.Marshal` → atomic write**
helper on `ProvidersConfigPath` (descriptors-only; never writes secrets). The
controller reloads the in-memory config after each write so subsequent reads + the
spawn path see the change without a hub restart.

**Re-keyed credential RPCs:** the existing `ApiKeySet` / `Logout` / `DeviceStart` /
`DevicePoll` / `LoginStart` / `LoginComplete` change their key argument from provider
**type** to instance **name**; they resolve the instance's **type** from the config to
gate auth modes (OAuth only for the `openai` tag) and to write `auth/<name>.json` /
`credentials.toml[name]`. (`auth/<name>.json` per-instance OAuth already exists from 1b.)

## 5. Frontend

- **Web:** rewrite `credentials.html` into the grouped instance screen + the add/edit
  forms + the new RPC calls in `assets/launchconfig.js`; delete the now-redundant
  read-only `settings/providers.html` (or repoint it). Reuse the existing
  `settings-collection` / `status-badge` / source-layer styles.
- **TUI:** rewrite `credentials_panel.go` to render grouped instances + the new
  keybindings, dispatching create/edit/remove/set-default messages (the existing
  set-key / oauth flows are reused, now keyed by instance name).
- **Pickers display by instance name:** `abbreviateModel` in `assets/spawn.js` and
  `cmd/serf-tui/model_display.go` — the model string is already `instanceName/model`
  (1b); ensure the display strips/labels by the configured instance set rather than the
  hardcoded type allowlist.

## 6. Edge cases / validation

- **Name:** unique, lowercased, no `/` (matches the loader, §6 of the 1c parent);
  reject duplicates and reserved-looking names at create.
- **apiStyle:** only valid for type `openai` (`responses` | `chat-completions`); the
  form hides it for other types; the RPC rejects it for non-openai.
- **Removing the default:** reassign default to the first remaining instance by sorted
  name; removing the **last** instance is allowed (the file becomes `default=""` with no
  instances → next hub start re-materializes from env — acceptable, documented).
- **OAuth applicability:** only instances whose type is `openai` show Sign-in; the RPC
  rejects OAuth ops on other types.
- **`none`-auth types (ollama):** no credential actions; show "no creds".
- **Concurrent edits:** the controller serializes writes (mutex) and reloads after each;
  last-write-wins within the single hub process.
- **A hand-edited file:** CRUD always reloads from disk before modifying, so manual
  edits aren't clobbered.

## 7. Testing strategy

- **CRUD RPCs:** create→list shows it; edit changes fields but not type; remove drops it
  + clears its creds/oauth + reassigns default; setDefault persists; all round-trip
  through `providerconfig.Load`; **no `api_key` ever written to providers.toml**.
- **Validation:** duplicate/upper/slash names rejected; apiStyle on non-openai rejected;
  OAuth op on non-openai rejected.
- **Credential re-keying:** `ApiKeySet("work", …)` writes `credentials.toml[work]`;
  OAuth for a custom openai instance writes `auth/work.json`; status reflects per-instance.
- **Web:** JSDOM/RPC round-trip for render + create + set-key + remove (mirror the
  existing credentials.html test approach if present).
- **TUI:** `credentials_panel` model test — grouped render, the new keybindings emit the
  right messages.
- **Pickers:** a custom instance appears in the picker labeled by instance name.
- Full suite green; **no `~/.serf` pollution** in tests (isolate `ProvidersConfigPath`).

## 8. Out of scope

- The fixed type registry stays code-defined (you add *instances*, not new *types*).
- No per-instance org/project/chatgpt-base descriptor fields yet (still env, per 1c §8).
- No remote/multi-user concerns beyond the single hub process.
