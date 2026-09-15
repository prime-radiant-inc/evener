# Source study and design constraints

10 September 2026. Baseline `2664cc881`. Two independent agents audited the backend/terminal paths and frontend/documentation paths. Their reports were inspected by the parent. This is source analysis, not a live credential walkthrough.

## What is confusing today

### The first connection starts at the wrong boundary

`docs/llm-provider-config-and-launch.md:268–271` says the credentials pane lists every curated implicit provider even before credentials exist. In the actual frontend path:

1. `cmd/evener-hub/app_instances.go:57–64` builds the display list from `Registry.Instances()`.
2. `llm/registry/instances.go:498–506` omits credential-requiring implicit providers with no credential source.
3. `cmd/evener-hub/frontend/src/stores/credentials.ts:97–104` uses that returned list unchanged.
4. `ConnectProviderDialog.tsx:224–255` renders those instances. The full provider catalog goes to the Add dialog instead (`194–195`).

The existing independent test `TestInstances_PseudoProviderNeedsBaseURL` asserts that its clean fixture exposes only Ollama (`llm/registry/instances_test.go:52–54`). A separate auth-status surface already enumerates implicit providers for credential entry (`cmd/evener-hub/app_auth.go:420–423`). The design should use a discoverable provider catalog without broadening the launch-ready instance set or weakening its credential filter.

### The current first-key path

New session → Connect provider → Add provider instance → select Base provider and invent Name → Create → find the new instance → Set API key → paste → Save → return to list → Test connection → choose a model → Start.

Sources: `Spawn.tsx:1193–1220,1373–1382`; `ConnectProviderDialog.tsx:151–170,187–195,284–318`; `instanceDialogs.tsx:84–115,126–219,271–286` under `cmd/evener-hub/frontend/src/panes/`.

Settings adds another detour: credential actions replace the editor sheet, and completing them closes back to the list. The user must reopen the row to test (`CredentialsSection.tsx:198–214,259–261,293–324`). The session model chooser has no connect-another-provider action (`session/chrome/ModelSwitchTrigger.tsx:167–175`; `widgets/modelCatalog/index.tsx:250–367`).

### Optionality is unclear even when labeled

The Add form displays Name, Base URL, Protocol, Surface, template variables, API key environment variable, and Credential header together (`instanceDialogs.tsx:126–219`). It never takes the actual API key. A novice can reasonably mistake the environment-variable input for the secret itself. Name/base validation is explained only after submit. The sheet puts this configuration form above its credential actions (`InstanceSheet.tsx:340–487`).

The linked document opens with stores, subprocesses, timeout tuning, and configuration precedence. It lacks a web walkthrough. `docs/llm-providers.md` is also a technical reference, introducing Protocol, Transport, Provider, Model, and Surface before setup. The redesign needs a short user how-to separate from both references.

## Required information by route

| Route | Main flow | Hidden unless requested |
|---|---|---|
| Anthropic, OpenAI API, Google Gemini, OpenRouter | Provider and API key | Connection name, endpoint, protocol/surface overrides, environment/header references |
| ChatGPT / Codex | OpenAI sign-in approval; device flow or redirect fallback | Account-specific troubleshooting |
| Ollama | Reachable server URL with local default; installed model after connection | Remote authentication and custom overrides |
| Azure | Resource name and API key unless already resolved on host | Other inherited settings |
| Vertex | Project ID, location, and host ADC or accepted credential JSON | Optional endpoint overrides |
| Custom/gateway | Endpoint, supported API format, authentication choice and required credentials | Connection naming, header/environment overrides, protocol capability details |

Defaults and auth facts: `llm/registry/data/providers_overlay.toml:91–99,152–161,198–213,269–272,292–347,573–618`. Vertex host derives from location (`load.go:992–1009`); project and location requirements are independently asserted in `load_test.go:743–745`.

Other supported variants must remain reachable in a production all-providers catalog, including Bedrock bearer-token + region, Vertex Anthropic, Vertex Express, and provider-specific coding plans. The prototype catalog deliberately samples routes; it does not redefine supported providers.

## Keep promises aligned with the backend

- **Saving is not checking.** Key storage and instance creation are separate RPCs. `ApiKeySet` saves, reloads, and returns status (`app_auth.go:453–482`). Empty stored-key submission currently cancels silently; the proposed form instead validates visibly.
- **The existing check lists models.** `app_credentials.go:107–144` calls `client.Models`, not generation. A successful result verifies list access; it cannot prove that a selected model can generate. CLI `providers probe` is different: it sends `ping` completion requests (`cmd/evener/providers.go:313,353–365`).
- **Unsupported checks are legitimate.** Catalog-only providers can be usable. Vertex Anthropic and Vertex Express have no live model-list endpoint (`providers_overlay.toml:18–23,331–347`). Offer a labeled unverified continuation rather than stranding the user.
- **No durable verification status exists today.** The test response has provider, status, and message only (`appwire/types.go:2102–2109`). Any persisted “checked just now” status needs explicit design and invalidation; initial implementation can show the immediate result only.
- **OpenAI has two connections.** API-key `openai` and OAuth `openai-codex` must stay distinct under any single-brand presentation (`openai_login.go:20–24`). No Anthropic subscription OAuth exists here (`app_auth.go:760–762`).
- **ADC is specific.** Evener reads credential files and accepted stored JSON. It does not probe the metadata server. External-account JSON types are refused (`instances.go:71–73`; `credential_json.go:13–35`).
- **Use active credentials honestly.** Resolution can shadow a newly saved key. The UI must distinguish the saved source from the active source and never send credentials inherited from one endpoint to an unrelated endpoint (`instances.go:85–90,378–380,454–478`).
- **Preserve editing power.** Existing protocol, surface, name, endpoint, variables, and header/environment reference controls remain available through advanced configuration. Clear-override semantics differ from empty-means-unchanged (`appwire/types.go:2832–2839`). Do not replace the full editor with a reduced form.
- **Partial writes need a recovery state.** If settings save but the key save fails, show the saved connection with “access details needed.” Do not claim an atomic connect operation the API does not offer.

## Terminal equivalents

CLI currently supports `providers list [--check]`, `probe`, and `add <name> --base X`; keys are intentionally excluded from command-line arguments (`cmd/evener/providers.go:23–30`). TUI create uses base → name → protocol → base URL and does not expose all web create fields (`credentials_panel.go:299–350`). TUI has API-key, credential-JSON, and OAuth actions but no equivalent simplified first-provider wizard.

A selected design should carry the same provider-first and required-only sequence into TUI. Keep CLI reference flags documented; a future interactive CLI connector is a separate scope decision. The six visual alternatives in this study focus on web navigation, with shared cross-client semantics.

## Working assumptions and how to challenge them

**Provider-first is the default recommendation.** A beginner usually recognizes “Anthropic” or “OpenAI” before “credential header.” The strongest alternative is model-first: someone may know only “Claude.” That is why D is a real candidate, with provider/billing choice made explicit. Observed novice tests showing better task completion for D would change the recommendation.

**The popular shortlist is editorial.** Anthropic, OpenAI, Google Gemini, and OpenRouter are proposed recognizable entry points. No usage analytics were consulted. Review with actual onboarding data before fixing the order permanently.

**The supplied feedback is one user report.** Agent reviewers can expose problems and compare tradeoffs; they cannot establish measured conversion improvements or replace novice usability sessions.

## External instructional sources

Fetched 2026-09-10:
- [Claude API overview](https://platform.claude.com/docs/en/api/overview): account prerequisites and the [API-key page](https://platform.claude.com/settings/keys).
- [Gemini API keys](https://ai.google.dev/gemini-api/docs/api-key): the [AI Studio key page](https://aistudio.google.com/apikey) and project-scoped access/billing.
- [The user-cited document](https://github.com/prime-radiant-inc/evener/blob/main/docs/llm-provider-config-and-launch.md): fetched before evaluating its user instructions.

Other outbound console links in the mockup are illustrative acquisition destinations and were not authenticated. Final product links should come from maintained provider metadata and be checked during implementation.
