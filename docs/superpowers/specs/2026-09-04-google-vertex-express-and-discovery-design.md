# Google Vertex: express-mode API key, live discovery, stored credentials

## Problem

Evener already speaks to Gemini on Vertex AI (`google-vertex`, registry spec
§9.4): `gcp-adc` auth, base URL built from `GOOGLE_VERTEX_PROJECT` and
`GOOGLE_VERTEX_LOCATION`, wire captures, an ADC-gated live test. Three things
stop it from being usable from the web hub by someone holding a Google Cloud
API key and a project full of credits:

1. **The hub offers no way to enter a credential for Vertex.** `google-vertex`
   is `gcp-adc`-only, so its `authModes` is `["adc"]` and the instance sheet
   renders no key field. ADC means "run `gcloud auth application-default
   login` on the hub host", which a browser user cannot do.
2. **Vertex has no live model discovery.** The `vertex-gemini` transport
   preset pins `models_endpoint = "-"`, so the hub's picker shows only the
   embedded catalog rows. The catalog is six days stale: `gemini-3.8-flash`
   is GA and resolves today only with a `model not in catalog` warning.
3. **Google's new `AQ.`-prefixed Cloud API keys reach Vertex, not the Gemini
   Developer API.** Such a key hits `aiplatform.googleapis.com` in express
   mode; against `generativelanguage.googleapis.com` it fails with
   `API_KEY_SERVICE_BLOCKED`. Evener's `google` provider only knows the
   latter host.

## Goals

- A hub user can add a Vertex instance and paste a credential into it — a
  Google Cloud API key, or a service-account / application-default JSON —
  without setting environment variables on the hub host.
- ADC and stored-credential Vertex instances list the Gemini models the
  project can actually see, live, in the hub picker and `launch-check`.
- `gemini-3.8-flash` is a real catalog row and the default for `google`,
  `google-vertex`, and the new express-mode provider.
- Every new path is pinned by deterministic tests; wire behaviour by opt-in
  live tests, per `docs/developing-evener/testing.md`.

## Non-goals

- **"Sign in with Google" (user OAuth in the hub).** Feasible (browser + PKCE
  + loopback, which `auth/openai` already implements for Codex) but it needs
  a registered OAuth client with a consent screen, and Google's device flow
  cannot request `cloud-platform` at all. Deferred to its own spec.
- Claude on Vertex through the express-mode key: `:rawPredict` returns 404
  for API-key callers. The express provider is Gemini-only by construction.
- Live discovery for express-mode instances. `ModelGardenService.
  ListPublisherModels` rejects API keys by policy (verified below); express
  instances stay catalog-only and the docs say so.
- Changing the registry's "auth is a transport property" rule (spec §4.3).
  Two Vertex auth paths are two provider rows, as Azure/Bedrock/Codex are.

## Verified facts (2026-09-04, project `jesse-coding-agents`)

Every claim below was tested with `curl` on the day of writing; the plan must
not re-derive them from memory.

| Fact | Evidence |
|---|---|
| Express-mode `generateContent` works on `v1` and `v1beta1` with an `AQ.` key via `x-goog-api-key` | `POST https://aiplatform.googleapis.com/v1/publishers/google/models/gemini-3.8-flash:generateContent` → 200 |
| The express key also works on the project-scoped path | `…/v1/projects/{P}/locations/global/publishers/google/models/gemini-3.8-flash:generateContent` → 200 |
| Google Search grounding works on the express key | `tools: [{google_search: {}}]` → 200 with `groundingMetadata` |
| `gemini-3.8-flash` is `global`-only today | regional `us-central1` → 404 `Publisher model … not found`; `global` → 200 |
| API keys cannot list models | `GET …/v1beta1/publishers/google/models` with `x-goog-api-key`, `?key=`, `x-goog-user-project`, single-model `GET`, regional host: all 401 `CREDENTIALS_MISSING`, `method: google.cloud.aiplatform.v1beta1.ModelGardenService.ListPublisherModels`, "API keys are not supported by this API" |
| The express-mode REST reference lists exactly `countTokens`, `generateContent`, `streamGenerateContent` | https://docs.cloud.google.com/vertex-ai/generative-ai/docs/start/express-mode/vertex-ai-express-mode-api-reference |
| Vertex's OpenAI-compatible surface has no `/models` route | `…/endpoints/openapi/models` → HTML 404 with OAuth and with the key |
| OAuth listing works once a quota project is named | `GET https://aiplatform.googleapis.com/v1beta1/publishers/google/models` with user ADC → 403 "requires a quota project"; add `x-goog-user-project: jesse-coding-agents` → 200, 27 `publisherModels`, no `nextPageToken` at `pageSize=200` |
| The listing carries **no capability data** | entry keys: `name`, `versionId`, `launchStage` (`GA` / `PUBLIC_PREVIEW` / `EXPERIMENTAL`), `publisherModelTemplate`, `supportedActions` (console links only), `openSourceCategory` |
| The listing path is not project-scoped and lives on `v1beta1` | `v1/publishers/google/models` → HTML 404 even with OAuth; `v1beta1/projects/{P}/locations/global/publishers/google/models` → 404 |
| `generateContent` with the project in the path does **not** need the quota-project header | ADC `POST …/v1/projects/{P}/locations/global/publishers/google/models/gemini-3.8-flash:generateContent` → 200 without it |
| Upstream models.dev has the new rows | `scripts/ops/refresh-model-catalog.sh --check` adds `google/gemini-3.8-flash`, `google-vertex/gemini-3.8-flash`, `google-vertex/claude-fable-5-1@default` |
| Evener's existing `google-vertex` path works end-to-end with ADC + the two variables (§5 flow 3) | after `gcloud auth application-default login`: `GOOGLE_VERTEX_PROJECT=jesse-coding-agents GOOGLE_VERTEX_LOCATION=global go run ./cmd/llmcall --provider google-vertex --model gemini-3.8-flash "…"` → `OK`; `providers list` shows `google-vertex` and `google-vertex-anthropic` with source `adc` and the `global` base URL |
| A fresh gcloud ADC file is `authorized_user` JSON with **no** `quota_project_id` | keys: `account`, `client_id`, `client_secret`, `refresh_token`, `type`, `universe_domain` — so §2.2's header is what makes listing work, and §4 accepts this file's contents verbatim |
| The quota header was live-verified with `authorized_user` credentials only (ADC login and the same JSON stored) | listing + generation pins on 2026-09-04; no service-account key was available, so R6 limits the header to user credentials |

Today's 27-id listing, for the discovery fixture (§2.4):

```
gemini-1.5-pro-002, gemini-2.5-flash-preview-04-17, gemini-2.5-pro-exp-03-25,
gemini-2.5-pro, gemini-2.5-flash, gemini-2.5-flash-lite, gemini-2.5-pro-tts,
gemini-2.5-flash-tts, gemini-live-2.5-flash-native-audio, gemini-3-flash-preview,
gemini-3.1-flash-image-preview, gemini-3.1-pro-preview, gemini-3.5-flash,
gemini-3.1-flash-image, gemini-3-pro-image, gemini-embedding-2,
gemini-3.1-flash-lite, gemini-3.1-flash-lite-image, gemini-3.5-transcribe-live-preview,
gemini-3.5-transcribe-preview, gemini-3.5-flash-lite, gemini-3.6-flash,
gemini-3.5-live-translate-preview, gemini-omni-1.1-flash-preview, gemini-3.7-flash,
gemini-3.8-flash, spicy-mayo
```

`launchStage` per id at capture time: `EXPERIMENTAL` only for
`gemini-2.5-pro-exp-03-25`; `PUBLIC_PREVIEW` for `gemini-2.5-flash-preview-04-17`,
`gemini-3-flash-preview`, `gemini-3.1-flash-image-preview`, `gemini-3.1-pro-preview`,
`gemini-3.5-transcribe-live-preview`, `gemini-3.5-transcribe-preview`,
`gemini-3.5-live-translate-preview`, `gemini-omni-1.1-flash-preview`; `GA` for
everything else, `spicy-mayo` included. The plan captures the raw JSON
response as the fixture file; this list is what it must contain.

## 1. `google-vertex-express` — the API-key provider

A curated implicit provider in `llm/registry/data/providers_overlay.toml`.
It is built on `google`, not `google-vertex`: that yields the `google`
protocol and surface and no Claude rows; google's catalog also carries
Gemma, Lyria, and Deep Research rows, which the express endpoint does not
serve (verified 2026-09-05: 404 for all three with the express key), so
`inherit_models_matching = ["gemini-*"]` narrows the inherited rows to
Gemini. It also yields the `{BASE_URL}` variable pattern every other
implicit provider uses, so the hub's add dialog shows it the same optional
base-URL override it shows for `google`.

```toml
[providers."google-vertex-express"]
implicit = true
name = "Google Vertex (API key)"
base = "google"
inherit_models = true
inherit_models_matching = ["gemini-*"]
api_key_env = ["GOOGLE_VERTEX_API_KEY"]
transport = "vertex-gemini"
auth = "header"
auth_header = "x-goog-api-key"
models_endpoint = "-"                 # API keys cannot list (see Verified facts); §2.3 enables it on the preset
vars = { "BASE_URL" = "https://aiplatform.googleapis.com/v1" }
vars_env = { "BASE_URL" = "GOOGLE_VERTEX_EXPRESS_BASE_URL" }
default_model = "gemini-3.8-flash"
cheap_model = "gemini-2.5-flash-lite"
web_search = true
```

How the layers combine (all existing behaviour, `llm/registry/load.go`):

- `curatedRecord` inherits `google`'s merged record — models, transport,
  `APIKeyEnv` — then `fold`s this block. `fold` expands the `vertex-gemini`
  preset and overlays the row's own fields, so `auth`/`auth_header` replace
  the preset's `gcp-adc`, `endpoint`, `stream_endpoint`, and
  `count_tokens_endpoint = "-"` come from the preset, and the row's own
  `models_endpoint = "-"` overrides the preset's listing path (§2.3).
  `api_key_env` **replaces** the inherited list (`fold`: `if src.APIKeyEnv !=
  nil`), so `GEMINI_API_KEY` / `GOOGLE_API_KEY` never make this instance
  exist. `vars` / `vars_env` merge key-wise, so `BASE_URL` is overridden and
  no other variable leaks in.
- `inherit_models_matching` (overlay key, applied by `keepInheritedRows`
  right after `inherit`, before any of the provider's own layers fold in)
  drops every inherited row whose id does not match `gemini-*`; rows the
  provider brings itself — an upstream entry or its own overlay/user rows —
  are unaffected.
- `v1` rather than `v1beta1`, to match `google-vertex`'s base URL. Both work
  (verified).
- `web_search = true` is explicit because the first-party gate
  (`gateWebSearch` / `firstPartyEndpoint`) compares against the curated
  record's own transport; for a curated row that is itself, so the gate
  passes either way — the explicit value documents the verified capability.
- Existence follows spec §5.1: the implicit instance exists when
  `GOOGLE_VERTEX_API_KEY` is set or the credentials store has an entry under
  `google-vertex-express` (or a custom instance name based on it).

Also in this section:

- `default_order`: insert `"google-vertex-express"` after `"google-vertex"`.
- `envvars/envvars.go`: `GoogleVertexAPIKey` (`GOOGLE_VERTEX_API_KEY`, Secret,
  Public — "Google Cloud API key for Vertex AI express mode; the
  `google-vertex-express` instance") and `GoogleVertexExpressBaseURL`
  (`GOOGLE_VERTEX_EXPRESS_BASE_URL`, Public).
- Docs: `docs/llm-providers.md` instance table row and a paragraph under
  "Cloud transports" explaining express mode, the `AQ.` key, and that
  discovery is catalog-only; `docs/developing-evener/environment.md` rows;
  README provider list.

**Hub behaviour, no code change:** the add dialog lists the provider (with
a `GOOGLE_VERTEX_EXPRESS_BASE_URL` input it renders for every `vars_env`
name — optional; the dialog submits it under the template key `BASE_URL`,
so a typed value takes effect (fixed 2026-09-05, roborev round 1)); after
creation the instance's `authModes` is `["apiKey"]`, so the sheet offers
**Set API key**, which stores the key in `credentials.toml` under the
instance name and the registry reload makes the instance live. This is the
exact flow the user described as missing.

## 2. Live discovery for `google-vertex` (ADC and stored-credential instances)

### 2.1 The resolved project is already on `Resolved.Transport.Vars`

No registry change. `buildTransport` (`resolve.go`) already scans every
transport template for `{NAME}` placeholders and records the value each one
resolved to on `Transport.Vars` — the existing
`testdata/golden/google-vertex-opus-5.json` shows `"vars":
{"GOOGLE_VERTEX_LOCATION": "global", "GOOGLE_VERTEX_PROJECT": "my-project"}`.
The authenticator and the listing code read `res.Transport.Vars`. (An
earlier draft of this spec added a separate `Resolved.Vars`; it was
redundant.) The host for the listing URL comes from `res.Transport.BaseURL`.

### 2.2 Quota project on ADC requests

`tokenauth.GCPADC.Apply` additionally sets `x-goog-user-project` to
`res.Transport.Vars["GOOGLE_VERTEX_PROJECT"]` when that key is present and non-empty.
Required for the listing call under user credentials (verified); harmless on
project-scoped `generateContent`; a no-op for instances without the variable.
The header is not credential material.

**Amended 2026-09-04 (ruling R6, final review):** the header is sent only
when the credential is a user credential (`"type": "authorized_user"` in the
ADC file or the stored JSON). Service-account and other credential types are
attributed to their own project and Google requires
`serviceusage.services.use` on any project named by this header, so sending
it for a least-privilege service account would turn working requests into
403s. The service-account path was not live-verified on this branch; with R6
it behaves exactly as before the branch (no header).

### 2.3 `models_endpoint` for the Vertex preset

`[transports."vertex-gemini"]` changes `models_endpoint` from `"-"` to
`"/publishers/google/models"`. For a Vertex transport this path is
**host-relative on `v1beta1`**, not base-URL-relative: the listing is not
project-scoped and does not exist on `v1` (verified). `google-vertex-express`
keeps `"-"` through its own `models_endpoint` override, because API keys
cannot call it.

### 2.4 `google.Protocol.ListModels`

When `res.Transport.HostRule == registry.HostRuleVertexLocation` (retained on
the resolved transport; a semantic discriminator, not URL sniffing), the
listing URL is `scheme://host` of `res.Transport.BaseURL` + `/v1beta1` +
`ModelsEndpoint` + `?pageSize=200`, and the response is decoded as
`{"publisherModels": [{"name": "publishers/google/models/<id>",
"launchStage": "..."}]}`. Pagination is followed via `nextPageToken` if
present (none today; cheap to honour).

Filter — a heuristic, because the listing carries no capability data:

- keep ids beginning `gemini-`;
- keep `launchStage` in {`GA`, `PUBLIC_PREVIEW`};
- drop ids containing any of `tts`, `embedding`, `image`, `live`,
  `transcribe`, `translate`, `omni`, `audio`.

Applied to today's fixture the filter must yield exactly these 13 ids, in
listing order — the fixture test's expected value:

```
gemini-1.5-pro-002, gemini-2.5-flash-preview-04-17, gemini-2.5-pro,
gemini-2.5-flash, gemini-2.5-flash-lite, gemini-3-flash-preview,
gemini-3.1-pro-preview, gemini-3.5-flash, gemini-3.1-flash-lite,
gemini-3.5-flash-lite, gemini-3.6-flash, gemini-3.7-flash, gemini-3.8-flash
```

Rows are `registry.Model{ID}` with no caps;
the registry's catalog union (`ModelIDs`) supplies metadata for known ids
and the picker shows unknown new ids as bare rows — that is the point of
live discovery. The denylist is a package-level slice with a comment naming
this spec; the fixture test (§7) is the regression guard when Google adds
modalities.

The non-Vertex path (generativelanguage `models/` + `supportedGenerationMethods`)
is unchanged.

### 2.5 Hub

No code change. `fetchLiveModels` already iterates instances and calls
`client.Models`; `hubModelList` unions catalog and live ids; `launch-check
--models` follows the same client. A listing failure is skipped per instance
today and stays that way.

## 3. Catalog refresh

- `make refresh-model-catalog` (never hand-edit the snapshot). Expect the
  additions listed under Verified facts plus ~30 unrelated rows.
- `default_model = "gemini-3.8-flash"` on `[providers.google]` and
  `[providers."google-vertex"]`.
- Regenerate `llm/registry/testdata/golden` with `go test ./llm/registry/
  -update` and review the diff; the refresh script's converter tests and
  overlay report run as part of the target.

## 4. Stored credential for `gcp-adc` instances

The hub can hold a Google credential JSON for a Vertex instance so the hub
host needs neither `gcloud` nor environment variables. Two JSON shapes are
accepted, because `google.CredentialsFromJSON` accepts both:
a **service-account key** and an **`authorized_user`** file (the contents
of a workstation's `application_default_credentials.json`). The second lets a
laptop's ADC be pasted into a remote hub; it needs the quota-project header
from §2.2, which is why that section precedes this one.

*(Amended 2026-09-05, roborev round 1: only `service_account` and
`authorized_user` are accepted; `external_account` configurations can name
local files or executables as credential sources and are refused at
validation and at first request. Round 6: the fields each type needs to mint
a token — `client_email` and `private_key`, or `client_id`, `client_secret`
and `refresh_token` — must be present and non-empty; Google's parser does not
check them and would fail only at the first request. Round 11: a
service-account `private_key` must parse offline as Google's signer will
parse it (PEM or PKCS#8/PKCS#1, RSA). Round 15: a `token_uri`, when present,
must be one of Google's own token endpoints
(`https://oauth2.googleapis.com/token`, or the legacy
`https://accounts.google.com/o/oauth2/token`), because the library sends the
refresh token or the signed assertion to whatever endpoint the file names; a
top-level `installed`/`web` block is refused because the library then returns
an OAuth client configuration with no token source; and these fields are
read through tagged decoding, case-insensitively, exactly as the library
reads them.)*

### 4.1 Storage

The value lives in the existing credentials store (`internal/credentials`,
`credentials.toml`) under the instance name — the same slot an API key uses.
No new file, no new store. The store already writes TOML strings safely;
the JSON is a single string value.

### 4.2 Registry

`credential()`'s `AuthGCPADC` branch becomes:

1. store entry under the instance name → `Credential{Value: json, Source:
   "store"}`;
2. else `adcAvailable(env)` → `Credential{Source: "adc"}` (unchanged);
3. else `none(...)` with the existing remedy text, extended to mention the
   hub's stored-credential option.

`Resolved.Credential` is already `json:"-"`, so the JSON never reaches
transcripts or the wire log. Spec §5.1's existence rule gains "or a
credentials-store entry under the instance name". The `store` source label
already exists in the hub's `credentialLabels.ts`.

### 4.3 Authenticator

`tokenauth.GCPADC.tokenSource` caches one token source per instance, rebuilt
when the credential's identity changes — its source (`adc`, `none`, `store`),
or the stored JSON's digest; when `res.Credential.Source ==
"store"` it builds the token source with `google.CredentialsFromJSON(ctx,
[]byte(value), cloudPlatformScope)` instead of `FindDefaultCredentials`.
Everything else (`ReuseTokenSource`, the bearer header, §2.2's quota
header) is shared.
Malformed JSON surfaces as the existing `llm.ConfigurationError` shape,
naming the instance and "stored credential".

### 4.4 Hub

- `authModesFor(registry.AuthGCPADC)` → `["adc", "credentialJson"]`.
- New RPC `evener/auth/credentialJson/set` (`AuthCredentialJsonSetParams
  {Provider, Value}` → `AuthStatusResponse`): refuses unless the instance's
  auth is `gcp-adc`; validates with `google.CredentialsFromJSON` before
  storing (a paste error is reported at set time, not at first request);
  stores via the credentials store; reloads the registry; broadcasts
  `evener/auth/updated`. Clearing reuses `evener/auth/apiKey/clear`, which
  already targets the store layer only.
- `ApiKeySet` refuses `gcp-adc` instances the way it refuses Codex ones,
  pointing at the credential-JSON action — a bare key in that slot is one
  the authenticator would reject.
- Frontend: `InstanceDetailSheet` offers **Set Google credential JSON**
  (help text: a service-account key or an `application_default_credentials.json`)
  when `authModes` includes `credentialJson` (a textarea dialog beside the
  API-key dialog in `instanceDialogs.tsx`); the layer/label copy for a
  `store` source on a `gcp-adc` instance reads "stored credential", not
  "stored key". `keylessByDesign` is unaffected (`credentialRequired` stays
  true for `gcp-adc`).
- `validateProviderCredentials` at spawn needs no change: `CredentialSource
  != "none"` already passes a stored credential.

### 4.5 CLI

None. The `evener providers` commands are untouched; the docs note that the
same value can be placed in `credentials.toml` by hand.

### 4.6 TUI (amended 2026-09-06)

Originally out of scope: the credentials panel had no multi-line input, so
Enter on a `credentialJson` instance showed a notice naming the web hub as
the place to paste (roborev round 9 of PR #879). That notice is gone. Enter
now opens a paste prompt and stores the document through the same
`evener/auth/credentialJson/set` the hub calls.

A terminal delivers a bracketed paste as one key message carrying every
rune, newlines included, so a pretty-printed JSON document arrives whole and
its newlines never reach the submit branch. Measured against bubbletea's own
reader, that holds at every size tried, up to 100k runes.

Without bracketing, the document arrives as ordinary keys, and the outcome
turns on which byte the terminal sends for a line break: LF maps to
`KeyCtrlJ`, CR to `KeyEnter`. The field keeps both LF and space (it dropped
them before), so an LF paste still accumulates whole. A CR paste submits at
each line and cannot work, which is one of two reasons for the second way in:
`f` on the instance asks for the path to the file holding the document, read
on the machine the user typed it on, not the hub's, with path completion.

The other reason is that the two inputs cannot be told apart. Reading them
through one prompt meant guessing whether a value was a path or the
credential — by shape, or by whether the terminal marked it as a paste — and
a terminal that marks no pastes leaves no signal to guess from. So the paste
prompt echoes nothing at all: it renders a character count, whatever it is
given. The file prompt shows its path, which is not secret, on any platform's
path syntax.
Control bytes are stripped from what is rendered, since clipboard content
reaches the view. A value that is neither a document nor a readable path is
reported by its reason alone: the error line is rendered and outlives the
panel, so it repeats none of what was submitted. For the same reason
`CheckCredentialJSON` bounds the two values its refusals quote back (an
unsupported `type`, a foreign `token_uri`) to a short prefix.

The file is read inside the command rather than while the key is handled, so
a slow filesystem cannot hold up the interface. It is still opened without
blocking, so a path naming a pipe cannot leave that command waiting forever
either; what the open descriptor actually is decides whether it is read — a
regular file, nothing else — and the read is bounded. Checking the path and
then opening it by name again would leave a window for it to become
something else in between.

## 5. End-to-end flows the hub must support after this change

1. **Express key.** Settings → Credentials → Add provider instance → base
   "Google Vertex (API key)" → name it → create → Set API key → paste `AQ.…`
   → launch a session on `google-vertex-express/gemini-3.8-flash`. Picker
   shows catalog rows only.
2. **Stored credential + live discovery.** Add provider instance → base
   "Google Vertex" → fill `GOOGLE_VERTEX_PROJECT`, `GOOGLE_VERTEX_LOCATION =
   global` → create → Set service-account JSON → paste → picker shows the
   live Gemini list for that project; launch on `gemini-3.8-flash`.
3. **ADC on the host (unchanged).** `gcloud auth application-default login`
   on the hub host → same as 2 without the paste; discovery now works.

## 6. Documentation

- `docs/llm-providers.md`: instance table rows; the Vertex paragraph under
  "Cloud transports" gains express mode, the stored-credential source, the
  quota-project header, and the discovery filter and its limits.
- `docs/llm-provider-config-and-launch.md` "Credentials store": the
  `gcp-adc` sentence changes from "needs no credentials-store entry" to
  "may hold a credential JSON".
- `docs/developing-evener/environment.md`: the two new variables.
- `docs/superpowers/specs/2026-08-28-provider-registry-design.md` §5.1 and
  §9.4: amend in place, dated.
- README provider list mentions `google-vertex-express`.

## 7. Testing

Deterministic (`make test`, no credentials, no network):

- `llm/registry`: golden for `google-vertex-express` resolution (auth,
  header, base URL, endpoints, `APIKeyEnv`, default model, no Claude rows);
  a test that `GEMINI_API_KEY` alone does not create the express instance
  and `GOOGLE_VERTEX_API_KEY` alone does; the `gcp-adc` store-first credential order; §5.1
  existence with a store entry and no ADC file.
- `llm/providers/wirecapture`: goldens for express `generateContent` and
  stream (`x-goog-api-key`, `v1` publisher path, no project segment).
- `llm/providers/google`: listing decode + filter against the 27-id fixture
  captured above; the URL built for a Vertex transport (`v1beta1`,
  host-relative); the non-Vertex path unchanged.
- `llm/providers/tokenauth`: `x-goog-user-project` set from
  `Transport.Vars` and absent without it; stored-JSON token source chosen for `Source == "store"`
  (seam: `FindCredentials`/a `CredentialsFromJSON` seam); one source per
  instance, rebuilt when the credential's identity changes — a stored
  credential replaced (digest), a stored credential arriving for an
  ADC-backed instance, or an ADC-backed instance re-resolved with no
  credential (source), which must error rather than reuse.
- `envvars`: the two new variables registered.
- `cmd/evener-hub`: `authModesFor` for `gcp-adc`; `credentialJson/set`
  validation, refusal for non-`gcp-adc` instances, store write, reload;
  `ApiKeySet` refusal for `gcp-adc`.
- Frontend (`make test-web`): `instanceDialogs.test.tsx` case for the
  express base (renders the base-URL input, no project/location inputs);
  `InstanceDetailSheet` shows the JSON action for `credentialJson` mode.
- `cmd/evener/models_live_test.go`'s offline shape test gains the express
  row (base URL, endpoints, header) beside `TestVertexAnthropicRequestShape`.

Live, opt-in (`EVENER_LIVE_TESTS=1` + `-live-config`, skip otherwise):

- `TestLiveVertexExpressOneRequest`: one `generateContent` on
  `google-vertex-express/gemini-3.8-flash`; needs `GOOGLE_VERTEX_API_KEY`.
- `TestLiveVertexListModels`: ADC instance lists ≥ 1 `gemini-` id and the
  configured `default_model` is among them; needs ADC + project/location.
- `TestLiveVertexStoredCredentialOneRequest`: same request through a
  credentials-store JSON entry; skips unless the live config's store has
  one.

Manual, before merge: flows 1 and 2 of §5 through a real `evener hub`,
recorded per `docs/developing-evener/agentic-testing.md`.

## 8. Notes and follow-ups

- ~~Observed, out of scope: the hub add dialog keys instance `vars` by the
  **environment** variable name (`instanceDialogs.tsx`, `setVars(...[varName])`)
  while the registry looks variables up by **template** name. Identity-mapped
  providers (`google-vertex`) work; a provider whose names differ (`google`:
  `BASE_URL` ↔ `GOOGLE_BASE_URL`) cannot take a typed override. Not touched
  here; recorded for a separate fix.~~ **Resolved 2026-09-05, roborev round
  1:** `ProviderDescriptor.Vars` (additive, roborev round 5) is a template-name →
  env-var-name map and the dialog keys `vars` (and its rendered inputs' ids)
  by the template name while still labeling by the env name; `VarsEnv` stays
  the sorted env-var-name list because the TUI decodes
  `evener/instance/list` through the shared types and `ProtocolVersion` is
  compared exactly, so no v3 wire shape may change.
- **Resolved 2026-09-06, roborev round 19 (follow-up PR):** the add form
  rendered a `GOOGLE_APPLICATION_CREDENTIALS` input for `google-vertex`
  because models.dev's `env` list names it beside the project and location
  and every entry became a `VarsEnv` input; instance `vars` only feed
  template placeholders, so the value was persisted and ignored. The hub now
  builds the descriptor from `Registry.TemplateVarsEnv`, the `vars_env`
  entries a URL template reads or a host rule consumes.
- User OAuth ("Sign in with Google") is the natural next spec if per-user
  credentials or remote hubs without pasteable JSON become a requirement.
