# Independent panel and revisions

Reviewed 2026-09-10. Initial designs are preserved in commit `5ccfbd8ce`.
The panel examined the initial designs, not the revisions below. These are
agent heuristic critiques, not observed user-study results.

## Rankings

| Lens | First to last | Recommendation |
|---|---|---|
| First-run comprehension and copy | C, A, D, E, B, F | C for onboarding; retain A’s compact form and D’s recognizable model names |
| Technical truth and maintainability | A, F, B, C, E, D | A as the shared connector; F for Settings; preserve the full editor |
| Accessibility and interaction | A, C, E, D, F, B | A generally; C for onboarding after shared interaction repairs |

**Original synthesis, before the clarified first-run priority:** A is the strongest general-purpose starting point. C deserves a
side-by-side first-run trial. B and F remain Settings alternatives; D and E
explore recognition by desired model or existing access. All six remain available.

The first-run reviewer favored a visible beginning and finish. The technical
reviewer favored reuse of existing provider identities and auth boundaries. The
accessibility reviewer found fewer distractions in the compact flow. These are
complementary priorities, not a unanimous vote for one layout.

## First-run findings and disposition

| Finding | Revision |
|---|---|
| E’s API-key route unexpectedly opened ChatGPT sign-in | Preserve API-key intent when OpenAI is selected; ChatGPT remains a separate choice |
| B hid result headings and explanations | Remove the blanket heading-hiding rule |
| Cancel and Change provider had the same destination | Cancel exits and discards the draft; Change provider returns to provider choices; saved setup offers Done |
| Error recovery looked like starting over | Retain the masked in-memory draft and nonsecret settings; explain that saved access remains; correct model-specific rejection advice |
| Prerequisites arrived late or disappeared on mobile | Explain “one provider” in the main picker, including mobile; define API key beside its acquisition steps; clarify Claude API billing and link current ChatGPT requirements |
| Instructions named the wrong ChatGPT button | Split API-key and ChatGPT instructions and distinguish Azure from Vertex prerequisites |

## Technical findings and disposition

| Finding | Revision and boundary |
|---|---|
| Missing save and check outcomes | Add missing-credential, configuration, save-failure, partial-save, and saved-but-unchecked states. Partial-save copy requires re-reading host status before retrying |
| Pasted key might not be the active credential; endpoint changes are sensitive | Add destination/source review for custom endpoints, overrides, and host references. Explain credential precedence. “No new credential” makes no promise to disable host access |
| OpenAI API and Codex identities were conflated | Use `openai-codex` identity and endpoint for ChatGPT, `openai` for API keys; include expired-code and browser-redirect mockup states |
| Expert editing and alternate auth could be lost | Preserve an explicit full-editor handoff listing surface, template variables, override resets, rename, and credential-management controls. Add a host-access alternative and optional remote Ollama key |
| Vertex instructions were too broad and JSON was exposed | Name supported host ADC files and JSON types; conceal pasted JSON by default with explicit reveal |
| Finish implied durable verification or default changes | Return to the invoking task with the connection/model choice; retain unverified language on unsupported continuation; explicitly leave the global default unchanged and scope checks to this setup |

The full-editor screen documents the handoff to existing functionality. It does
not rebuild the expert editor. Destination/source resolution, partial-save status
refresh, OAuth, and model lists remain simulations. Production work would need to
read actual host state and handle every write boundary; these mockups implement
none of that integration.

Source checks for these constraints:

- `cmd/evener-hub/app_auth.go:453-482,552-645,760-767`: credential-save errors, Codex identity, OAuth routes.
- `cmd/evener-hub/app_credentials.go:111-144`: connection-check result classification.
- `llm/registry/instances.go:71-102,429-478`: host ADC discovery, endpoint inheritance boundary, active credential precedence.
- `llm/registry/credential_json.go:13-35`: accepted JSON types and required fields.
- `llm/registry/data/providers_overlay.toml:198-210,573-618`: Codex defaults and compatible/local presets.
- `appwire/types.go:2102-2109,2812-2866,2874-2877`: selection, full-editor controls, check response boundary.
- [OpenAI Codex authentication](https://developers.openai.com/codex/auth), fetched 2026-09-10: maintained account/auth guidance. Evener’s own Codex transport remains OAuth-only even where other Codex clients support API keys.

## Accessibility findings and disposition

| Finding | Revision |
|---|---|
| Invalid advanced URL was hidden in a collapsed disclosure | Open the containing disclosure on native invalid events; the browser can reveal and focus the invalid field |
| B’s hidden result headings also broke focus | Keep result headings visible and move focus to the rendered heading |
| Rendering discarded key and configuration drafts | Retain drafts in memory, isolated by provider and access method; clear on discard/reset/completion, with no browser storage |
| F lost its search and mobile return position | Preserve the filter and restore focus to the selected provider in view |
| Input boundaries had insufficient contrast | Increase form-control boundary contrast; verify at least 3:1 against the fill and surrounding panel |
| E’s Change provider led back to method selection | Return to actual provider choices |

The reviewer tested desktop Chrome and 390px viewport emulation, keyboard
activation and focus, form validity, search, recovery, and computed contrast.
No physical-device or screen-reader testing was performed. The initial defect
probe passed its evidence assertions; those assertions established the defects,
not accessibility compliance.

## Revision verification

`verify.mjs` exercises real rendered prototype interactions in Chrome, with
external requests blocked and browser errors collected. The panel regressions
were added before the corresponding repairs and failed against the initial
behavior. Passing results and refreshed screenshots are recorded with the final
study artifacts. The verifier covers six desktop/mobile flows, requiredness,
advanced disclosure, outcomes, recovery, cancellation, method intent, concealed
JSON, editor access, endpoint review, keyboard focus, and boundary contrast.

This verifies the design artifact only. It does not prove production auth,
persistence, catalog discovery, screen-reader compliance, or real-user usability.

### Final independent recheck

The accessibility reviewer independently retraced the original findings and the
revised cancellation, OpenAI-method, and Ollama-destination paths. It confirmed
the original repairs and found one additional error: changing Ollama’s URL with
an empty optional key still claimed a new credential was supplied. The display
now checks whether that key was entered. The reusable verifier covers both the
empty-key and supplied-key cases.

The parent reran the reviewer’s unchanged Chrome probe after this correction:
**41/41 checks passed**, with no external requests, non-GET requests, or console
errors. [The independent results](screenshots/panel-verification.json) and
[the final reusable-verifier results](screenshots/verification.json) are retained.
The complete gallery and representative desktop/mobile credential screens were
also visually inspected. Production behavior remains unchanged.

## First-run priority correction, 2026-09-11

Jesse clarified that the primary audience is new users with no providers set up.
The original synthesis gave repeat setup and provider management too much weight.
The recommendation is now **C’s guided onboarding with the compact credential
step shared with A**. A remains the leaner comparison; the earlier panel rankings
above are preserved as historical findings, not recast as a new vote.

All six alternatives now explicitly start with no connected providers. B and F
describe available choices rather than existing connections. Their other rows
are alternatives, not a list the newcomer must configure. Existing-host access
remains reachable behind disclosures. All successful connection paths continue
to the first-session composer instead of returning to provider settings.

The verifier includes an empty-start and first-session-handoff check for every
option. Each new check failed before this revision and passed after it. The
credential, failure-recovery, accessibility, and no-provider-traffic checks remain
in place. These are still design-only simulations.

The first-run reviewer independently exercised all six Anthropic demo-key flows
at desktop size and C again at 390px. It found no new blockers: the empty state,
available-choice framing, acquisition instructions, single required key,
collapsed advanced settings, and first-session handoff all passed. It ranked C
ahead of A for novice orientation. All observed requests were local GETs; no
provider request was attempted. [The focused results](screenshots/first-run-review.json)
are retained. This verifies the ordinary demo success journey, not every auth
method, actual novice usability, or a submitted production session.
