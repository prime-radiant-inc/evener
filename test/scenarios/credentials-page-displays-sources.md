# credentials-page-displays-sources: /credentials shows correct effective source per provider

**What this covers**: regression baseline + hub-side OAuth priority
(`f824379`). The credentials page is the user's primary view onto
"which provider is configured and how". Validates env-only providers
show as env, OAuth-signed-in providers show as oauth with email, env-
shadows-file dual-state shows both layers with effective/shadowed
badges.

## Pre-state

- Hub running.
- Browser authed.
- OAuth signed in for OpenAI (`./serf openai status` shows
  `source=oauth`). This is the typical dev state after running
  through `auth-device-autodetect.md`.
- **This card deliberately runs against Jesse's real `$HOME`** — the
  documented OAuth-footgun exception in `docs/agentic-testing.md`'s Setup
  checklist, since OAuth state lives under the normal user state home
  and a fresh isolated `$HOME` would never have it. That means it reads
  (and, per Sharp edges below, temporarily edits) Jesse's real
  `~/.serf/credentials.toml`. Back it up before touching it and restore
  it in Cleanup:
  ```bash
  cp ~/.serf/credentials.toml ~/.serf/credentials.toml.bak-$(date +%s)
  ```

## Steps

1. Open `/credentials` in a browser tab.
2. Read the row content for each provider.
3. Confirm OpenAI shows `Configured via OAuth — <email>` (NOT
   "Configured via environment variable", even if `$OPENAI_API_KEY`
   is set — that's `f824379`).
4. Confirm at least one env-only provider (anthropic, google) shows
   `Configured via environment variable`.
5. For an env+file dual provider (set up by intentionally placing
   a stored file alongside the env var — see Sharp edges), confirm
   BOTH layers render with explicit `effective` and `shadowed`
   badges.
6. Click `Set API key` on any provider. Confirm an inline editor
   opens with a password input. Click `Cancel` (don't actually save).
7. For OpenAI, click `Refresh OAuth`. Confirm a redirect URL is
   opened (or the popup blocker hint appears) — DO NOT complete the
   flow unless you want to re-auth.

## Expected

- The page renders without errors. No `[ERROR]` banner in nav.
- OpenAI row: `Configured via OAuth — jesse@primeradiant.com` (or
  the test account's email). Action buttons: `Set API key`,
  `Refresh OAuth`, `Clear`.
- Env-only rows: `Configured via environment variable`. Action
  buttons: `Set API key` only (no Clear; the env var isn't owned).
- Unconfigured rows (kimi, glm, etc): `Not configured`. Action
  button: `Set API key`.
- `ollama` row: `No credentials required`. No action buttons.
- Falsification: OpenAI shows env-source while a valid stored OAuth
  record exists; or any provider shows wrong source label.

## Cleanup

- If you opened a "Set API key" editor, cancel out so subsequent
  scenarios see a clean page.
- If you set up the dual-layer case in Sharp edges, restore the
  backup made in Pre-state over the real file (`mv
  ~/.serf/credentials.toml.bak-<ts> ~/.serf/credentials.toml`) rather
  than hand-editing the test entry back out — don't leave Jesse's real
  credentials store on a diff you can't fully account for.

## Sharp edges

- The dual-layer (env + file) display path is only exercised when
  a provider has BOTH a stored file entry AND an env var. To set one up
  **without clobbering whatever Jesse's real `credentials.toml` already
  holds** (a bare `>` truncates the file — never do that here): back up
  the file (see Pre-state), then merge in a test entry with a TOML-aware
  edit rather than overwriting the whole file, e.g.
  `python3 -c "import tomllib,tomli_w,os; p=os.path.expanduser('~/.serf/credentials.toml'); d=tomllib.load(open(p,'rb')); d.setdefault('providers',{})['kimi']={'api_key':'test'}; tomli_w.dump(d, open(p,'wb'))"`
  (`tomli_w` is not stdlib — `pip install tomli-w` first if missing; mode
  stays `0600` — `chmod 600` afterward if the tool didn't preserve
  it), then set `KIMI_API_KEY=other-test`. Reload page. Should see both
  layers. Restore from the backup in Cleanup — don't leave the `test` key
  in Jesse's real credentials store.
- Per `credentials.toml` is mode 0600; the UI never displays stored
  values. Verifying the file's perms is a separate scenario.
- The `Set API key` editor's password input has `autocomplete=off`
  so browser password managers don't auto-fill. If you see them
  trying, that's a regression.
- "Refresh OAuth" for OpenAI restarts the OAuth flow. On a headless
  Linux host you'll get the device-code variant — confirm in CLI
  via `./serf openai status` afterward.
